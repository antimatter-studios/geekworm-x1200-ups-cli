// Package history keeps a short record of past readings so that a rate of change can be measured.
//
// A single reading cannot answer "how long left". The gauge reports a percentage and a voltage and
// nothing about time, so the only way to a duration is to watch the number move. That means state
// between invocations, which is the first thing in this program that is not a pure function of the
// machine's current condition.
//
// The store lives under /var/lib, which is where the Filesystem Hierarchy Standard puts state a
// program keeps across reboots. That is what this is: a record of how the pack has actually behaved,
// which is worth more the longer it runs and is the only way to measure real capacity.
//
// It is written by appending, and that is not a detail. Rewriting the file on every sample costs its
// full size each time — at a few thousand samples, tens of kilobytes — and this Pi has already
// destroyed one SD card through write amplification. Appending one line costs about forty-five
// bytes, and the full rewrite happens only when the file has grown past a cap.
//
// Surviving a reboot used to be an argument against persistence, on the grounds that a rate measured
// before a power cycle describes a different machine. It is handled instead: a gap longer than the
// integrator's tolerance ends a run, so samples on the far side of an outage are not averaged
// against samples on this side of it.
//
// Format is one sample per line, space separated: unix seconds, percent, volts, and optionally
// amps and watts. Line-oriented because appending is then a concatenation rather than a
// parse-modify-serialise cycle, and because a human debugging this at 2am can read it with cat.
//
// The last two fields are optional so that a store written by an older version still parses. A
// format change that silently discards accumulated history would be worst precisely when the
// history matters — during an outage, when nobody wants to start counting again from zero.
package history

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sample is one observation of the pack.
//
// Both numbers are kept, not just the percentage, because they fail in different ways. The
// percentage is what the question is asked in, but on this gauge it moves in whole steps and can sit
// still for minutes at a time — an observed run held 80% across several samples while the voltage
// fell steadily throughout. A rate derived only from percentage would read as zero during those
// plateaus and then jump. Voltage is the finer signal and is what the gauge measures underneath.
type Sample struct {
	At       time.Time
	Percent  int
	VoltageV float64
	// CurrentA and WattsW are what the INA219 measured, and they are the whole reason this store
	// grew past the gauge. The percentage is a model: during an observed unplug it fell to 85% under
	// load and sprang back to 96% within a minute of the load coming off, which is cell sag being
	// read as depletion rather than charge moving anywhere. Current is measured, so integrating it
	// gives charge actually delivered.
	//
	// Zero is indistinguishable from absent here, which is acceptable: a genuine zero current means
	// nothing is being drawn, and integrating nothing over any interval contributes nothing either
	// way. That is not true of the percentage, which is why that one is not optional.
	CurrentA float64
	WattsW   float64
}

// Store is the two capabilities this package needs, injected in the same shape as sysfs.FS so that
// the whole of the logic can be tested without touching a disk.
type Store struct {
	// Read returns the file's contents, and whether it existed.
	Read func() (string, bool)
	// Write replaces the file's contents.
	Write func(string) error
}

// Appendable is a Store that can add one line without rewriting the file.
//
// This exists for SD card wear, and the arithmetic is why. Rewriting the whole store on every
// sample costs its full size per write: at 2000 samples that is around 60 kB, and a five-minute
// timer makes 288 writes a day, so about 17 MB daily and 6 GB a year of write amplification. This
// Pi has already destroyed one SD card. Appending one line costs about 45 bytes, some 300 times
// less, and the full rewrite then happens only when the file actually needs pruning.
type Appendable struct {
	Store
	// Add appends one sample without reading or rewriting what is already there.
	Add func(Sample) error
	// Size reports the stored byte count, so a caller can decide when a rewrite is due without
	// parsing the whole file.
	Size func() (int64, bool)
}

// Archive returns an append-only store for the long-lived record under /var/lib.
//
// Distinct from File not by accident: the two stores have different lifetimes and different failure
// modes. The rate window is volatile and small and correctly lost on reboot; the archive is meant to
// accumulate across reboots, which is exactly what makes its write pattern worth caring about.
func Archive(path string) Appendable {
	return Appendable{
		Store: File(path),
		Add: func(s Sample) error {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			// O_APPEND makes the write atomic for sizes below the pipe buffer, so two samplers
			// racing cannot interleave halves of a line. They should not race — the timer is
			// serialised — but a person running the command by hand while the timer fires is not a
			// situation that should corrupt anything.
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = f.WriteString(Render([]Sample{s}))
			return err
		},
		Size: func() (int64, bool) {
			info, err := os.Stat(path)
			if err != nil {
				return 0, false
			}
			return info.Size(), true
		},
	}
}

// File returns a Store backed by a real file.
//
// Write goes via a temporary file and a rename so that a reader never observes a half-written
// history. The rename is atomic within a directory, which is why the temporary is created beside
// the target rather than in some other tmpdir.
func File(path string) Store {
	return Store{
		Read: func() (string, bool) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return "", false
			}
			return string(raw), true
		},
		Write: func(content string) error {
			dir := filepath.Dir(path)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
			if err != nil {
				return err
			}
			name := tmp.Name()
			if _, err := tmp.WriteString(content); err != nil {
				tmp.Close()
				os.Remove(name)
				return err
			}
			if err := tmp.Close(); err != nil {
				os.Remove(name)
				return err
			}
			return os.Rename(name, path)
		},
	}
}

// Memory returns a Store backed by a string. Used by tests, and by nothing else.
func Memory(initial string) (Store, *string) {
	content := initial
	present := initial != ""
	return Store{
		Read: func() (string, bool) { return content, present },
		Write: func(s string) error {
			content = s
			present = true
			return nil
		},
	}, &content
}

// Parse turns stored text into samples, discarding anything it cannot read.
//
// Tolerant by design. A truncated final line is the expected consequence of a power cut during a
// write, which is precisely the event this program exists to observe — refusing to start because the
// last line is malformed would lose the whole history at the moment it becomes interesting.
func Parse(text string) []Sample {
	var out []Sample
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		// Three fields is the original format and still valid; five carries the INA219 readings.
		if len(fields) != 3 && len(fields) != 5 {
			continue
		}
		secs, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		pct, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		volts, err := strconv.ParseFloat(fields[2], 64)
		if err != nil {
			continue
		}
		sample := Sample{At: time.Unix(secs, 0).UTC(), Percent: pct, VoltageV: volts}
		if len(fields) == 5 {
			// A malformed tail is dropped rather than failing the line: the timestamp and percentage
			// are still usable, and half a sample beats none.
			if amps, err := strconv.ParseFloat(fields[3], 64); err == nil {
				sample.CurrentA = amps
			}
			if watts, err := strconv.ParseFloat(fields[4], 64); err == nil {
				sample.WattsW = watts
			}
		}
		out = append(out, sample)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Render turns samples back into storable text.
func Render(samples []Sample) string {
	var b strings.Builder
	for _, s := range samples {
		fmt.Fprintf(&b, "%d %d %.4f %.4f %.4f\n", s.At.Unix(), s.Percent, s.VoltageV, s.CurrentA, s.WattsW)
	}
	return b.String()
}

// Prune drops samples older than window, and caps the total kept.
//
// Both limits matter and they bound different things. The window bounds *relevance* — an hour-old
// sample describes a load that may no longer exist. The count bounds the file, which otherwise grows
// without limit under `--watch 1s` and turns a diagnostic aid into a disk problem.
// Recent returns the tail of samples inside a window, without touching the file.
//
// The estimator wants the last couple of hours; the integrator wants everything. Those are two views
// of one store rather than a reason to keep two stores, and filtering in memory is far cheaper than
// the writes a second file would cost.
func Recent(samples []Sample, now time.Time, window time.Duration) []Sample {
	cutoff := now.Add(-window)
	for i, s := range samples {
		if !s.At.Before(cutoff) {
			return samples[i:]
		}
	}
	return nil
}

func Prune(samples []Sample, now time.Time, window time.Duration, max int) []Sample {
	cutoff := now.Add(-window)
	out := make([]Sample, 0, len(samples)+1)
	for _, s := range samples {
		if s.At.Before(cutoff) {
			continue
		}
		out = append(out, s)
	}
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

// Record appends a sample to an archive, rewriting only when the file has grown past maxBytes.
//
// The rewrite is the expensive operation and it is the one this defers. Between rewrites each sample
// costs a single short append; when the file finally exceeds the cap it is parsed, pruned and written
// once. Returns whether a rewrite happened, which is worth knowing when reasoning about wear.
func Record(store Appendable, s Sample, window time.Duration, max int, maxBytes int64) (rewrote bool, err error) {
	if size, ok := store.Size(); ok && size > maxBytes {
		var existing []Sample
		if text, ok := store.Read(); ok {
			existing = Parse(text)
		}
		kept := Prune(append(existing, s), s.At, window, max)
		return true, store.Write(Render(kept))
	}
	return false, store.Add(s)
}

// Append adds a sample, prunes, and writes the result back.
//
// Returns the samples as stored, so a caller can estimate from exactly what was persisted rather
// than from a slightly different in-memory view.
func Append(store Store, s Sample, window time.Duration, max int) ([]Sample, error) {
	var existing []Sample
	if text, ok := store.Read(); ok {
		existing = Parse(text)
	}
	// A clock that has gone backwards — an NTP step, which a Pi with no RTC does routinely on boot —
	// would otherwise leave samples that sort before ones already recorded and make every interval
	// negative. Dropping the future is cheaper than trying to reconcile it.
	kept := make([]Sample, 0, len(existing)+1)
	for _, e := range existing {
		if e.At.After(s.At) {
			continue
		}
		kept = append(kept, e)
	}
	kept = append(kept, s)
	kept = Prune(kept, s.At, window, max)
	return kept, store.Write(Render(kept))
}
