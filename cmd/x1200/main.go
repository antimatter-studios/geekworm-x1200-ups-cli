// Command x1200 reports the state of a Geekworm X1200 UPS HAT.
//
// It reads only sysfs, so it needs no privileges and does not contend with the kernel drivers that
// own the I2C addresses. See internal/ups for why that matters.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/estimate"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/history"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/sysfs"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/ups"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

// options is what the flags parsed to, kept separate from the parsing so that run can be exercised
// without a process, a clock or a real filesystem.
type options struct {
	root    string
	json    bool
	watch   time.Duration
	history string
	window  time.Duration
}

// Defaults for the sample store.
//
// The path is under /tmp because that is a tmpfs on every distribution this runs on, and both
// properties of a tmpfs are wanted. Writes never reach the SD card, which matters on a Pi that has
// already lost one card to write amplification; and the file disappears on reboot, which is correct
// rather than unfortunate — a discharge rate measured before a power cycle describes a machine in a
// different state, and carrying it across a reboot would produce a confident answer from samples on
// the wrong side of the event.
const (
	defaultHistory = "/tmp/x1200-history"
	defaultWindow  = 2 * time.Hour
	maxSamples     = 2000
)

// main is wiring and nothing else: every decision it makes is in parse or run, both of which are
// tested. What is left here is the impure edges — the real filesystem, the real output stream, the
// real clock, and the exit status.
func main() {
	opts, done, err := parse(os.Args[1:], os.Stderr)
	if err != nil {
		os.Exit(2)
	}
	if done {
		return
	}
	store := history.Store{}
	if opts.history != "" {
		store = history.File(opts.history)
	}
	if err := run(sysfs.OS(opts.root), os.Stdout, time.Sleep, time.Now, store, opts); err != nil {
		fmt.Fprintln(os.Stderr, "x1200:", err)
		os.Exit(1)
	}
}

// parse turns arguments into options.
//
// done reports that the work is finished and nothing should be read — currently only -version,
// which has to print before any attempt to find hardware, so that a binary on the wrong machine can
// still say what it is.
func parse(args []string, errOut io.Writer) (opts options, done bool, err error) {
	fs := flag.NewFlagSet("x1200", flag.ContinueOnError)
	fs.SetOutput(errOut)
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.StringVar(&opts.root, "root", "/sys", "sysfs root, for testing against captured files")
	fs.BoolVar(&opts.json, "json", false, "emit JSON instead of text")
	fs.DurationVar(&opts.watch, "watch", 0, "repeat at this interval, e.g. 2s; 0 reads once")
	fs.StringVar(&opts.history, "history", defaultHistory, "sample store used to estimate time remaining; empty disables it")
	fs.DurationVar(&opts.window, "window", defaultWindow, "how much history to keep and estimate from")

	if err := fs.Parse(args); err != nil {
		return opts, false, err
	}
	if *showVersion {
		fmt.Fprintln(errOut, version)
		return opts, true, nil
	}
	return opts, false, nil
}

// run reads and renders, repeatedly if asked.
//
// sleep is a parameter rather than a call to time.Sleep so that a test of the watch loop takes no
// time and needs no goroutine.
func run(fs sysfs.FS, out io.Writer, sleep func(time.Duration), now func() time.Time, store history.Store, opts options) error {
	for {
		text, err := once(fs, now, store, opts)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(out, text); err != nil {
			return err
		}
		if opts.watch <= 0 {
			return nil
		}
		sleep(opts.watch)
	}
}

// once produces exactly one rendering, recording a sample on the way through.
//
// A failure to record is not a failure to report. The store lives in a tmpfs that a reader may have
// made read-only, or pointed somewhere unwritable, and none of that is a reason to withhold the
// battery percentage — which is the number that matters when the power has just gone. The estimate
// is simply absent, and its own Note says why nothing can be said.
func once(fs sysfs.FS, now func() time.Time, store history.Store, opts options) (string, error) {
	reading, err := ups.Read(fs)
	if err != nil {
		return "", err
	}
	reading.Estimate = track(reading, now, store, opts.window)
	if opts.json {
		return ups.JSON(reading)
	}
	return ups.Text(reading), nil
}

// track records this reading and returns what the accumulated samples imply.
//
// Returns nil when there is nothing to record against — no store configured, or a gauge that cannot
// report a percentage — because an estimate with no basis should be absent rather than empty.
func track(reading *ups.Reading, now func() time.Time, store history.Store, window time.Duration) *estimate.Estimate {
	if store.Read == nil || store.Write == nil {
		return nil
	}
	if reading.Battery == nil || reading.Battery.Percent == nil {
		return nil
	}
	// A zero window would prune every sample older than this instant, leaving a history of one and
	// an estimate that can never form — silently, and looking exactly like the feature being broken.
	// The flag default covers the real path; this covers every other caller.
	if window <= 0 {
		window = defaultWindow
	}

	sample := history.Sample{At: now(), Percent: *reading.Battery.Percent}
	if reading.Battery.VoltageV != nil {
		sample.VoltageV = *reading.Battery.VoltageV
	}

	samples, err := history.Append(store, sample, window, maxSamples)
	if err != nil {
		// Report the reading anyway; say why the estimate is missing rather than swallowing it.
		return &estimate.Estimate{State: estimate.Unknown, Note: "history unavailable: " + err.Error()}
	}
	// discharging is not yet known from any authoritative source — the mains-detection GPIO is the
	// next thing to build — so the estimator is told nothing and infers direction from the samples.
	est := estimate.From(samples, false)
	return &est
}
