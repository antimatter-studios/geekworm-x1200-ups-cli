// Command x1200 reports the state of a Geekworm X1200 UPS HAT.
//
// It reads only sysfs, so it needs no privileges and does not contend with the kernel drivers that
// own the I2C addresses. See internal/ups for why that matters.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/build"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/coulomb"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/estimate"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/gpio"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/history"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/pmic"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/service"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/sysfs"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/ups"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/x1200"
)

// options is what the flags parsed to, kept separate from the parsing so that run can be exercised
// without a process, a clock or a real filesystem.
type options struct {
	root     string
	json     bool
	watch    time.Duration
	history  string
	window   time.Duration
	gpio     bool
	chip     string
	capacity float64
	maxGap   time.Duration
	archive  string
	// trustCurrent is the operator asserting that they know what the INA219 measures. The tool
	// cannot establish it, so it cannot default to true.
	trustCurrent bool
}

// unverifiedCurrent is why the charge totals are withheld.
//
// Stated in the output rather than left to the README, because the figures it suppresses are ones a
// reader would otherwise expect to see, and their absence needs a reason attached.
const unverifiedCurrent = "charge and energy need the current's circuit identified; on this board the INA219 does not track the Pi's load (525mA idle, 267mA at full load), so integrating it would name a quantity nobody can. pass --trust-current if you have established what it measures"

// Defaults for the sample store.
//
// One store, under /var/lib, which is where the Filesystem Hierarchy Standard puts state a program
// keeps across reboots. There were briefly two — a volatile rate window and a persistent archive —
// and the split did not survive contact with the question "why". The estimator wants the recent
// tail and the integrator wants everything, which is two views of one file, not two files.
//
// Appending is what makes /var/lib affordable. A whole-file rewrite costs tens of kilobytes per
// sample and this Pi has already destroyed one SD card that way; an append costs about forty-five
// bytes, and the rewrite happens only when the file passes maxArchiveBytes.
const (
	defaultStore = "/var/lib/x1200/samples"
	// defaultWindow is how much history the rate estimate looks at, not how much is kept.
	defaultWindow = 2 * time.Hour
	// keepWindow is how much is retained when the file is finally pruned. Long, because capacity is
	// only measurable across a real discharge and those are rare.
	keepWindow      = 30 * 24 * time.Hour
	maxSamples      = 20000
	maxArchiveBytes = 1 << 20
)

// main is wiring and nothing else: every decision it makes is in parse or run, both of which are
// tested. What is left here is the impure edges — the real filesystem, the real output stream, the
// real clock, and the exit status.
func main() {
	// Subcommands are dispatched before flags so that `x1200 version` works on a machine with no
	// hardware and no /sys at all — the first question asked of a binary in the wrong place is what
	// it is, and that answer must never depend on finding a UPS.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		if err := command(os.Args[1], os.Args[2:], os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		return
	}

	opts, done, err := parse(os.Args[1:], os.Stderr)
	if err != nil {
		os.Exit(2)
	}
	if done {
		return
	}
	store := history.Store{}
	if opts.history != "" {
		store = history.Archive(opts.history).Store
	}
	if err := run(sysfs.OS(opts.root), os.Stdout, time.Sleep, time.Now, store, gpio.OS{}, opts); err != nil {
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
	fs.StringVar(&opts.history, "store", defaultStore, "sample store; empty disables recording and the figures derived from it")
	fs.DurationVar(&opts.window, "window", defaultWindow, "how much history to keep and estimate from")
	fs.BoolVar(&opts.gpio, "gpio", true, "read mains presence and charging state from GPIO")
	fs.StringVar(&opts.chip, "chip", "", "gpiochip to use; empty picks the header controller")
	// Declared, never inferred: the hardware cannot know what cells are fitted. Also frequently
	// wrong — 18650 chemistry caps around 3500 mAh a cell, and anything sold as 5000+ is commonly
	// overstated two- or threefold — so a runtime derived from it says so wherever it is reported.
	fs.Float64Var(&opts.capacity, "capacity-mah", 0, "declared pack capacity, for a runtime estimate; 0 omits it")
	// Must be at least the sampling interval or every interval is refused and the charge figures
	// silently vanish. The systemd unit passes its own timer interval for exactly that reason.
	fs.DurationVar(&opts.maxGap, "max-gap", coulomb.MaxGap, "longest gap between samples that may be integrated across")
	fs.BoolVar(&opts.trustCurrent, "trust-current", false, "report charge and energy totals; requires knowing what the INA219 measures")

	if err := fs.Parse(args); err != nil {
		return opts, false, err
	}
	if *showVersion {
		// To stdout, not stderr: a version is output, not a diagnostic, and piping it into
		// something is a reasonable thing to want.
		fmt.Fprintln(os.Stdout, build.Current().String())
		return opts, true, nil
	}
	return opts, false, nil
}

// run reads and renders, repeatedly if asked.
//
// sleep is a parameter rather than a call to time.Sleep so that a test of the watch loop takes no
// time and needs no goroutine.
func run(fs sysfs.FS, out io.Writer, sleep func(time.Duration), now func() time.Time, store history.Store, port gpio.Port, opts options) error {
	for {
		text, err := once(fs, now, store, port, opts)
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
func once(fs sysfs.FS, now func() time.Time, store history.Store, port gpio.Port, opts options) (string, error) {
	reading, err := ups.Read(fs)
	if err != nil {
		return "", err
	}
	// GPIO first: the estimator wants to know whether the pack is discharging, and a measured
	// answer from the mains pin is better than the inference it would otherwise fall back on.
	if opts.gpio {
		supply(reading, port, opts.chip)
	}
	samples := track(reading, now, store, opts.window)
	// Two views of one store: the rate estimate wants only the recent tail, because an hour-old
	// sample describes a load that may no longer exist. The integrator below wants everything, since
	// charge delivered over a long period is the whole point of keeping a long period.
	reading.Estimate = estimateFrom(reading, history.Recent(samples, now(), window(opts)))
	reading.Delivered = deliveredFrom(samples, opts.capacity, opts.maxGap, opts.trustCurrent)
	if opts.trustCurrent {
		implyCapacity(reading)
	}
	corroborate(reading)
	if opts.json {
		return ups.JSON(reading)
	}
	return ups.Text(reading), nil
}

// track records this reading and returns the accumulated samples.
//
// Returns nil when there is nothing to record against — no store configured, or a gauge that cannot
// report a percentage — because a derived figure with no basis should be absent rather than empty.
func track(reading *ups.Reading, now func() time.Time, store history.Store, window time.Duration) []history.Sample {
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
	// The INA219 readings are what makes integration possible, so they are recorded even though the
	// gauge is what the sample is keyed on.
	if reading.Power != nil {
		if reading.Power.CurrentA != nil {
			sample.CurrentA = *reading.Power.CurrentA
		}
		if reading.Power.WattsW != nil {
			sample.WattsW = *reading.Power.WattsW
		}
	}

	samples, err := history.Append(store, sample, keepWindow, maxSamples)
	if err != nil {
		return nil
	}
	return samples
}

// window returns the rate window, guarding a zero the same way track does.
func window(opts options) time.Duration {
	if opts.window <= 0 {
		return defaultWindow
	}
	return opts.window
}

// estimateFrom derives the percentage-based estimate.
func estimateFrom(reading *ups.Reading, samples []history.Sample) *estimate.Estimate {
	if len(samples) == 0 {
		return nil
	}
	// Tell the estimator what the mains pin measured, where it was readable. It uses this only to
	// sharpen its wording: a pack that is definitely on battery but has not yet dropped a whole
	// percent is not "steady", it is early, and the two deserve different words.
	onBattery := reading.Supply != nil && !reading.Supply.OnMains
	est := estimate.From(samples, onBattery)
	return &est
}

// deliveredFrom integrates the measured current into charge.
//
// Absent rather than zero when there is too little to say. A charge total of 0.0 mAh reads as "no
// current flowed", which is a claim about the hardware; the absence of the group reads as "not
// enough measurement yet", which is a claim about the observation. Only the second is true early on.
func deliveredFrom(samples []history.Sample, capacityMAh float64, maxGap time.Duration, trustCurrent bool) *ups.Delivered {
	if len(samples) < 2 {
		return nil
	}
	charge := coulomb.IntegrateWithin(samples, maxGap)
	note, usable := coulomb.Confidence(charge)
	if !usable {
		return nil
	}

	out := &ups.Delivered{
		MilliampHours: charge.MilliampHours,
		WattHours:     charge.WattHours,
		MeanCurrentA:  charge.MeanCurrentA,
		CoveredS:      charge.CoveredS,
		SpanS:         charge.SpanS,
		Note:          note,
	}
	if !trustCurrent {
		out.Unverified = unverifiedCurrent
		return out
	}
	if runtime, ok := coulomb.Runtime(charge, capacityMAh); ok {
		secs := runtime.Seconds()
		out.RuntimeS = &secs
		out.CapacityMAh = &capacityMAh
	}
	return out
}

// supply attaches the mains and charging state read from GPIO.
//
// Silent on failure, and that is deliberate: the overwhelmingly common reason these fail is that the
// pin has never been configured as an input, which is a setup step rather than a fault, and printing
// an error on every invocation until somebody runs `x1200 setup` would train them to ignore it. The
// absence of the supply group is itself the signal, and `x1200 doctor` is where the explanation
// belongs.
//
// The failure that would matter — a pin that reads the wrong way — cannot be detected here at all,
// which is why the caveat on Supply exists.
func supply(reading *ups.Reading, port gpio.Port, chip string) {
	if port == nil {
		return
	}
	if chip == "" {
		chips, err := port.Chips()
		if err != nil || len(chips) == 0 {
			return
		}
		chip = chips[0]
	}
	if onMains, err := x1200.Mains(port, chip); err == nil {
		reading.Supply = &ups.Supply{
			OnMains: onMains,
			Line:    x1200.PLDLine,
			Caveat:  "read with a pull-up, so a disconnected pin also reads as mains",
		}
	}
	if enabled, err := x1200.Charging(port, chip); err == nil {
		reading.Charging = &ups.Charging{Enabled: enabled, Line: x1200.ChargeLine}
	}
}

// command dispatches a subcommand.
//
// Kept deliberately small. The tool's job is to print a reading, and subcommands are for the things
// that are not that: identifying the binary, and later setting the machine up and checking it over.
// An unknown one lists what exists rather than only complaining, because a person who guessed wrong
// wants the answer more than the correction.
func command(name string, args []string, out, errOut io.Writer) error {
	switch name {
	case "version":
		return versionCommand(args, out, errOut)
	case "systemd":
		return systemdCommand(args, out, errOut)
	case "record":
		return recordCommand(args, out, errOut)
	case "calibrate":
		return calibrateCommand(args, out, errOut, pmic.Exec, time.Sleep)
	case "doctor":
		return doctorCommand(args, out, errOut, gpio.OS{}, pmic.Exec)
	default:
		fmt.Fprintf(errOut, "x1200: unknown command %q\n\ncommands:\n"+
			"  version   what this binary is\n"+
			"  systemd   print the service and timer units\n"+
			"  record    take one sample and append it to the store\n"+
			"  calibrate fit the shunt resistance against the Pi's own sensors\n"+
			"  doctor    check the prerequisites are in place\n"+
			"\nrun `x1200 -h` for flags\n", name)
		return fmt.Errorf("unknown command %q", name)
	}
}

// versionCommand prints what this binary is, in full or as JSON.
func versionCommand(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("x1200 version", flag.ContinueOnError)
	fs.SetOutput(errOut)
	asJSON := fs.Bool("json", false, "emit JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return err
	}

	info := build.Current()
	if *asJSON {
		encoded, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}
	_, err := io.WriteString(out, info.Details())
	return err
}

// corroborate cross-checks the mains pin against the pack.
//
// The mains line is read with a pull-up, so "mains present" is the level a floating pin produces: a
// HAT not seated, a pogo-pin contact gone intermittent, or the board removed entirely all read as
// healthy mains. The detector's failure mode is to report that nothing is wrong.
//
// A falling percentage is the opposite kind of evidence. It is a measurement rather than an absence,
// it comes from a different chip on a different bus, and it was the only thing that told the truth
// during an observed outage where the pin said nothing at all. So when the two disagree — pin says
// mains, gauge says the charge is going down — the disagreement is reported rather than resolved.
//
// Deliberately not resolved in favour of either. Overriding the pin would be a guess about which
// sensor is broken, and the honest output is that they contradict each other and one of them needs
// looking at. Anything automatic built on this must treat a suspect reading as "assume the worst".
func corroborate(reading *ups.Reading) {
	if reading.Supply == nil || !reading.Supply.OnMains {
		return
	}
	if reading.Estimate == nil || reading.Estimate.State != estimate.Discharging {
		return
	}
	reading.Supply.Suspect = fmt.Sprintf(
		"GPIO%d reads mains but the pack is draining; a floating pin also reads mains, so check the HAT is seated",
		reading.Supply.Line)
}

// implyCapacity works the pack's real capacity backwards from two independent measurements.
//
// The gauge says how fast the percentage is falling; the INA219 says how much current is flowing.
// Together those imply a capacity: if the percentage will reach zero in four hours while half an amp
// flows, the pack holds about two amp-hours from where it is now, and scaling by the present state
// of charge gives the full figure.
//
// This is worth having because a declared capacity cannot be checked any other way without running
// a pack flat, and declared capacities are unreliable in a known direction — 18650 chemistry caps
// around 3500 mAh a cell, so anything sold as 5000 mAh is overstated. Where the implied figure comes
// out well below the declared one, the declared one is the suspect.
//
// It inherits the shunt error in full, so it is only as good as the calibration. That is not a
// reason to withhold it: the shunt is reported alongside, and a figure that is wrong by a known
// factor still settles whether a claim is out by two- or threefold.
func implyCapacity(reading *ups.Reading) {
	d, e := reading.Delivered, reading.Estimate
	if d == nil || e == nil || e.TimeToEmpty == nil || d.MeanCurrentA <= 0 {
		return
	}
	if reading.Battery == nil || reading.Battery.Percent == nil {
		return
	}
	percent := float64(*reading.Battery.Percent)
	if percent <= 0 {
		return
	}
	// Charge left from here, then scaled up to a full pack by the present state of charge.
	remainingMAh := d.MeanCurrentA * 1000 * e.TimeToEmpty.Hours()
	full := remainingMAh / (percent / 100)
	d.ImpliedCapacityMAh = &full
}

// systemdCommand prints the unit files.
//
// Printed rather than installed. Writing to /etc and enabling a timer are decisions about what the
// machine does, and this machine is described by Pulumi — a tool that configures it directly is how
// a system stops matching its own description. So this emits text and something else decides.
//
// It exists because the program is no longer only a binary. The Debian package installs these units;
// a tarball download installs nothing but the binary, and this is how that user gets the same files
// rather than a worse copy from the README.
func systemdCommand(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("x1200 systemd", flag.ContinueOnError)
	fs.SetOutput(errOut)
	binary := fs.String("binary", service.DefaultBinary, "path to the installed binary, used in ExecStart")
	only := fs.String("only", "", "print just one unit: service or timer")
	if err := fs.Parse(args); err != nil {
		return err
	}

	files := service.Files(*binary)
	switch *only {
	case "service":
		_, err := io.WriteString(out, files["x1200.service"])
		return err
	case "timer":
		_, err := io.WriteString(out, files["x1200.timer"])
		return err
	case "":
	default:
		fmt.Fprintf(errOut, "x1200: --only takes service or timer, not %q\n", *only)
		return fmt.Errorf("bad --only %q", *only)
	}

	// Both, with filename headers, so the output can be read by a person and split by a script.
	for _, name := range service.Names() {
		fmt.Fprintf(out, "# ==> %s <==\n%s\n", name, files[name])
	}
	_, err := io.WriteString(out, service.Instructions())
	return err
}

// recordCommand takes one sample and appends it to the archive.
//
// Separate from the default read for one reason: it appends rather than rewriting. The rate window
// under /tmp is small and rewritten whole, which is fine in a tmpfs. The archive lives on the SD
// card, and rewriting a 60 kB file every five minutes is 17 MB a day of write amplification on a
// machine that has already destroyed one card. Appending a line costs about 45 bytes.
func recordCommand(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("x1200 record", flag.ContinueOnError)
	fs.SetOutput(errOut)
	root := fs.String("root", "/sys", "sysfs root")
	archive := fs.String("archive", defaultStore, "file to append the sample to")
	window := fs.Duration("window", keepWindow, "how much history to keep when the store is pruned")
	maxBytes := fs.Int64("max-bytes", maxArchiveBytes, "rewrite and prune the store once it exceeds this size")
	quiet := fs.Bool("quiet", false, "print nothing on success, for a systemd timer")
	// Accepted and ignored: the unit passes it so that one ExecStart line serves both this and the
	// reporting path, and rejecting it would make the unit fail for a flag that does no harm here.
	_ = fs.Duration("max-gap", coulomb.MaxGap, "ignored by record; accepted so the unit can pass one flag set")
	if err := fs.Parse(args); err != nil {
		return err
	}

	reading, err := ups.Read(sysfs.OS(*root))
	if err != nil {
		return err
	}
	if reading.Battery == nil || reading.Battery.Percent == nil {
		return fmt.Errorf("no battery percentage to record")
	}

	sample := history.Sample{At: time.Now(), Percent: *reading.Battery.Percent}
	if reading.Battery.VoltageV != nil {
		sample.VoltageV = *reading.Battery.VoltageV
	}
	if reading.Power != nil {
		if reading.Power.CurrentA != nil {
			sample.CurrentA = *reading.Power.CurrentA
		}
		if reading.Power.WattsW != nil {
			sample.WattsW = *reading.Power.WattsW
		}
	}

	rewrote, err := history.Record(history.Archive(*archive), sample, *window, maxSamples, *maxBytes)
	if err != nil {
		return err
	}
	if *quiet {
		return nil
	}
	if rewrote {
		fmt.Fprintf(out, "recorded %d%% %.3fV %.3fA (archive pruned)\n", sample.Percent, sample.VoltageV, sample.CurrentA)
		return nil
	}
	fmt.Fprintf(out, "recorded %d%% %.3fV %.3fA\n", sample.Percent, sample.VoltageV, sample.CurrentA)
	return nil
}
