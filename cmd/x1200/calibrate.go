package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/calibrate"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/pmic"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/sysfs"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/ups"
)

// calibrateCommand fits the shunt resistance against the Pi's own power sensors.
//
// It prints and does not apply, which is the whole shape of it. Writing the value into sysfs would
// be a state change on a machine described declaratively elsewhere, and a tool that reconfigures the
// machine behind that description is how a system stops matching what it says it is.
func calibrateCommand(args []string, out, errOut io.Writer, run pmic.Runner, sleep func(time.Duration)) error {
	fs := flag.NewFlagSet("x1200 calibrate", flag.ContinueOnError)
	fs.SetOutput(errOut)
	root := fs.String("root", "/sys", "sysfs root")
	samples := fs.Int("samples", 12, "how many paired observations to take")
	interval := fs.Duration("interval", 2*time.Second, "wait between observations")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *samples < 3 {
		fmt.Fprintln(errOut, "x1200: --samples must be at least 3; two instruments can agree once by accident")
		return fmt.Errorf("samples too low")
	}

	fmt.Fprintf(out, "Sampling the INA219 against the Pi's PMIC, %d times %s apart.\n", *samples, *interval)
	fmt.Fprintln(out, "Vary the load while this runs — `yes > /dev/null` on a few cores, then stop them.")
	fmt.Fprintln(out, "A calibration taken entirely at idle constrains the answer barely at all.")
	fmt.Fprintln(out)

	fsys := sysfs.OS(*root)
	points := make([]calibrate.Point, 0, *samples)
	for i := 0; i < *samples; i++ {
		if i > 0 {
			sleep(*interval)
		}
		reading, err := ups.Read(fsys)
		if err != nil {
			return err
		}
		if reading.Power == nil || reading.Power.BusV == nil {
			return fmt.Errorf("no INA219 reading; is the ina2xx driver bound? run `x1200 doctor`")
		}
		// Never in0_input here. It is quantised to whole millivolts, and the whole signal is only a
		// few millivolts wide, so fitting a slope to it fits the rounding. See Power.PreciseShuntMV.
		dropMV, ok := reading.Power.PreciseShuntMV()
		if !ok {
			return fmt.Errorf("cannot derive the shunt drop: need both curr1_input and shunt_resistor; run `x1200 doctor`")
		}
		got, err := pmic.Read(run)
		if err != nil {
			return err
		}
		p := calibrate.Point{ShuntMV: dropMV, BusV: *reading.Power.BusV, PMICWatts: got.Watts}
		points = append(points, p)
		// Three decimals, because the point of deriving the drop from the current register is that
		// it carries 10 uV resolution — printing it rounded to the millivolt would hide the fix.
		fmt.Fprintf(out, "  %2d/%d  drop %6.3f mV   rail %.3f V   pmic %5.3f W\n",
			i+1, *samples, p.ShuntMV, p.BusV, p.PMICWatts)
	}

	fit, err := calibrate.Fit(points)
	if err != nil {
		return err
	}

	nearest, offBy := calibrate.Nearest(fit.OhmsFromSlope)
	fmt.Fprintf(out, `
fitted shunt resistance
  from slope:    %.4f mOhm   <- prefer this; a fixed offset cannot bias it
  from mean:     %.4f mOhm
  nearest part:  %.4f mOhm   (%.1f%% away)
  points:        %d over %.3f A of load range
  spread:        %.1f%%
`, fit.OhmsFromSlope*1000, fit.OhmsFromMean*1000, nearest*1000, offBy, fit.Points, fit.CurrentRangeA, fit.SpreadPercent)

	if fit.Note != "" {
		fmt.Fprintf(out, "\n  caveat: %s\n", fit.Note)
	}
	fmt.Fprintf(out, "\n  bias:   %s\n", calibrate.EfficiencyNote)

	if !calibrate.Plausible(fit.OhmsFromSlope) {
		fmt.Fprintf(out, `
This is not a plausible sense resistance, so something is wrong with the
measurement rather than surprising about the board. Nothing to apply.
`)
		return fmt.Errorf("implausible fit: %.6f ohm", fit.OhmsFromSlope)
	}

	// Printed, never applied. The operator pastes this wherever their machine is described.
	fmt.Fprintf(out, `
Nothing has been changed. To use it, either declare it where this machine is
described, or set it directly:

  echo %.0f | sudo tee /sys/bus/i2c/devices/1-0040/hwmon/hwmon*/shunt_resistor

That path takes microohms. It does not survive a reboot on its own, which is
why it belongs in whatever describes the machine rather than in a shell.
`, nearest*1e6)
	return nil
}
