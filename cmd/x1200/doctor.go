package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/gpio"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/pmic"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/sysfs"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/ups"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/x1200"
)

// check is one prerequisite and what to do when it is missing.
type check struct {
	name string
	ok   bool
	// detail is what was found, whether or not it was what was wanted.
	detail string
	// fix is what to do about it, and is only shown when the check failed. Every failing check must
	// have one: a diagnostic that reports a problem without naming the remedy has moved the work
	// rather than done it.
	fix string
	// fatal marks a check whose failure stops the tool working at all, as against one that costs a
	// feature. The distinction is what turns a list into a priority.
	fatal bool
	// known marks a limitation of the hardware rather than a misconfiguration.
	//
	// A third severity exists because FAIL carries a specific meaning — you have set this up wrongly
	// and here is the correction — and a known limitation is a different claim. Putting the two under
	// one label dilutes the checks somebody can actually act on: the shunt being uncalibrated is a
	// repair, while the INA219's circuit being undocumented is not, and a reader who cannot tell
	// them apart learns to skim both.
	known bool
}

// doctorCommand reports whether the prerequisites are in place.
//
// This exists because the failure modes are silent by design. An unreadable GPIO means the supply
// group is simply absent; an unbound driver means the battery is absent; an uncalibrated shunt means
// every current figure is wrong by a constant factor with nothing to indicate it. Each of those is
// the right behaviour for the reporting path — inventing a value would be worse — but it leaves the
// operator with no way to tell "this is fine" from "this has never worked".
func doctorCommand(args []string, out, errOut io.Writer, port gpio.Port, run pmic.Runner) error {
	fs := flag.NewFlagSet("x1200 doctor", flag.ContinueOnError)
	fs.SetOutput(errOut)
	root := fs.String("root", "/sys", "sysfs root")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fsys := sysfs.OS(*root)
	var checks []check

	reading, readErr := ups.Read(fsys)

	// Not fatal, and worth being clear why: this tool reads sysfs, so it needs the drivers bound and
	// not the device node. /dev/i2c-1 is what i2cdetect and i2cget use, which makes it a diagnostic
	// convenience rather than a prerequisite — and a check that reports a working system as broken
	// teaches an operator to ignore the whole report.
	if _, err := os.Stat("/dev/i2c-1"); err == nil {
		checks = append(checks, check{name: "i2c device", ok: true, detail: "/dev/i2c-1 present, so i2cdetect works"})
	} else if readErr == nil && reading.Battery != nil {
		checks = append(checks, check{name: "i2c device", ok: true,
			detail: "/dev/i2c-1 absent, but the drivers are bound — this tool does not need it"})
	} else {
		checks = append(checks, check{
			name: "i2c device", detail: "/dev/i2c-1 missing and no driver bound either",
			fix: "needs BOTH `dtparam=i2c_arm=on` in /boot/firmware/config.txt AND the i2c-dev module.\n" +
				"        With only the first, a reboot gives a working controller and no device file, which looks\n" +
				"        exactly like the reboot not having happened.",
		})
	}

	if readErr == nil && reading.Battery != nil {
		checks = append(checks, check{name: "fuel gauge", ok: true,
			detail: fmt.Sprintf("%s bound, reading %s", reading.Battery.Name, percentOf(reading.Battery))})
	} else {
		checks = append(checks, check{
			name: "fuel gauge", detail: "no battery power_supply found", fatal: true,
			fix: "I2C has no enumeration, so a driver cannot discover the chip. Assert it:\n" +
				"        echo max17040 0x36 | sudo tee /sys/bus/i2c/devices/i2c-1/new_device",
		})
	}

	if readErr == nil && reading.Power != nil {
		checks = append(checks, check{name: "power monitor", ok: true,
			detail: fmt.Sprintf("%s bound", reading.Power.Chip)})

		if reading.Power.ShuntUncalibrated() {
			checks = append(checks, check{
				name:   "shunt resistance",
				detail: "10.000 mOhm — the ina2xx default, provably wrong on this board",
				fix: "every current, power, mAh and Wh figure is scaled by this, likely ~2x low.\n" +
					"        run `x1200 calibrate` to fit it against the Pi's own sensors.",
			})
		} else if reading.Power.ShuntOhms != nil {
			checks = append(checks, check{name: "shunt resistance", ok: true,
				detail: fmt.Sprintf("%.3f mOhm, not the driver default", *reading.Power.ShuntOhms*1000)})
		}
	} else {
		checks = append(checks, check{
			name: "power monitor", detail: "no hwmon publishing bus voltage, current and power",
			fix: "assert the chip as above:\n" +
				"        echo ina219 0x40 | sudo tee /sys/bus/i2c/devices/i2c-1/new_device\n" +
				"        without it there is no current measurement, so no charge, energy or measured capacity.",
		})
	}

	// Mains detection, which is the feature people most expect to work and most often has not been
	// enabled, because the pin needs configuring before it has a readable level at all.
	chip := ""
	if port != nil {
		if chips, err := port.Chips(); err == nil && len(chips) > 0 {
			chip = chips[0]
		}
	}
	switch {
	case chip == "":
		checks = append(checks, check{
			name: "gpio", detail: "no gpiochip found",
			fix: "mains detection needs the GPIO character device. On a Pi this is /dev/gpiochip*; without it, on-mains-or-battery is unavailable.",
		})
	default:
		if onMains, err := x1200.Mains(port, chip); err == nil {
			state := "on battery"
			if onMains {
				state = "on mains"
			}
			checks = append(checks, check{name: "mains detection", ok: true,
				detail: fmt.Sprintf("GPIO%d readable on %s, %s", x1200.PLDLine, chip, state)})
		} else {
			checks = append(checks, check{
				name:   "mains detection",
				detail: fmt.Sprintf("GPIO%d not readable", x1200.PLDLine),
				fix: "add `gpio=6=ip,pu` to /boot/firmware/config.txt and reboot. An unconfigured line has no\n" +
					"        level at all — `pinctrl get 6` shows `--` — so no amount of polling will catch an edge.",
			})
		}
	}

	checks = append(checks, shadowedUnits(fileExists)...)

	// A known limitation, not a misconfiguration, and reported for a reason an operator can act on:
	// stop looking for the charge figures, and do not reach for --trust-current casually. Measured on
	// hardware, the INA219 read 525 mA idle and 267 mA at full load, was flat across a mains
	// transition, and decays to a fixed floor of about 267 regardless of what the machine is doing.
	// Geekworm documents neither the chip nor its shunt and publish no schematic.
	if readErr == nil && reading.Power != nil {
		checks = append(checks, check{
			name: "current source", known: true,
			detail: "the INA219 does not track the Pi's load, so its circuit is unidentified",
			fix: "charge, energy and implied capacity are withheld because of this — that is deliberate,\n" +
				"        not a fault. Do not pass --trust-current until you have established what the\n" +
				"        current measures; a confident mAh figure from an unidentified signal is worse\n" +
				"        than none, because it looks like a measurement.",
		})
	}

	// Only needed by calibration, so its absence is not a fault on a machine that is not a Pi.
	if got, err := pmic.Read(run); err == nil {
		checks = append(checks, check{name: "pi power sensors", ok: true,
			detail: fmt.Sprintf("%d rails, %.2f W total", got.Rails, got.Watts)})
	} else {
		checks = append(checks, check{
			name: "pi power sensors", detail: "vcgencmd pmic_read_adc unavailable",
			fix: "only `x1200 calibrate` needs this. It is a Raspberry Pi firmware interface, so it is expected to be missing elsewhere.",
		})
	}

	width := 0
	for _, c := range checks {
		if n := len(c.name); n > width {
			width = n
		}
	}

	var failed, fatal, known int
	for _, c := range checks {
		var mark string
		switch {
		case c.ok:
			mark = " ok "
		case c.known:
			// Neither passing nor broken: true, unfixable, and worth knowing.
			known++
			mark = "known"
		case c.fatal:
			failed++
			fatal++
			mark = "STOP"
		default:
			failed++
			mark = "FAIL"
		}
		fmt.Fprintf(out, "[%-5s] %-*s  %s\n", mark, width, c.name, c.detail)
		if !c.ok && c.fix != "" {
			fmt.Fprint(out, indent(c.fix, width))
		}
	}

	fmt.Fprintln(out)
	// Known limitations are counted apart from failures throughout, so that "nothing is misconfigured"
	// stays sayable on a machine that still has an undocumented sensor on it.
	suffix := ""
	if known > 0 {
		word := "limitation"
		if known > 1 {
			word += "s"
		}
		suffix = fmt.Sprintf(" %d known %s, which no configuration will change.", known, word)
	}
	switch {
	case fatal > 0:
		fmt.Fprintf(out, "%d of %d checks failed, %d of them fatal: the tool cannot report anything useful yet.%s\n", failed, len(checks), fatal, suffix)
		return fmt.Errorf("%d fatal prerequisite(s) missing", fatal)
	case failed > 0:
		fmt.Fprintf(out, "%d of %d checks failed. The tool works, but some readings are missing or unscaled.%s\n", failed, len(checks), suffix)
		return nil
	default:
		fmt.Fprintf(out, "Nothing is misconfigured.%s\n", suffix)
		return nil
	}
}

// percentOf renders a gauge reading, distinguishing an unreadable gauge from a flat pack.
func percentOf(b *ups.Battery) string {
	if b.Percent == nil {
		return "no percentage"
	}
	return fmt.Sprintf("%d%%", *b.Percent)
}

// unitPaths are the two places this program's units can end up, in systemd's own precedence order.
//
// A package installs into /lib; a machine's own configuration goes in /etc. systemd prefers /etc,
// which is the whole point of the split and also the trap below.
var unitPaths = []struct{ etc, lib string }{
	{"/etc/systemd/system/x1200.service", "/lib/systemd/system/x1200.service"},
	{"/etc/systemd/system/x1200.timer", "/lib/systemd/system/x1200.timer"},
}

// shadowedUnits reports a packaged unit that has been silently overridden.
//
// This is the failure worth catching because it is invisible. /etc wins over /lib, so a unit written
// into /etc replaces the packaged one permanently — while dpkg goes on owning its copy, package
// upgrades go on replacing it, and none of that has any effect. Ship a corrected unit in a new
// release and the machine keeps running the stale override, with nothing anywhere to say why.
//
// It is worse than two resources racing for one path, which at least announces itself by being
// non-deterministic. This is deterministic and quiet.
//
// The remedy is a drop-in rather than a replacement. /etc/systemd/system/x1200.service.d/*.conf
// composes with the packaged unit instead of hiding it, so an override survives upgrades and an
// upgrade survives the override.
func shadowedUnits(exists func(string) bool) []check {
	var out []check
	for _, u := range unitPaths {
		if !exists(u.etc) || !exists(u.lib) {
			continue
		}
		name := u.etc[strings.LastIndex(u.etc, "/")+1:]
		out = append(out, check{
			name:   "unit shadowing",
			detail: fmt.Sprintf("%s exists in BOTH /etc and /lib; the /etc copy wins and the packaged one is inert", name),
			fix: "systemd prefers /etc over /lib, so package upgrades to " + u.lib + " will have no effect\n" +
				"        while " + u.etc + " exists. Either delete the /etc copy and let the package own the\n" +
				"        unit, or replace it with a drop-in that composes instead of hiding:\n" +
				"          /etc/systemd/system/" + name + ".d/override.conf",
		})
	}
	return out
}

// indent renders a fix under its check, wrapping continuation lines to line up with the first.
//
// Computed rather than baked into each fix string, which is what broke when the severity marker
// changed width: an indentation constant embedded in prose is a constant nobody remembers to revisit.
func indent(fix string, width int) string {
	// "[known] " plus the name column, plus two spaces, is where the detail begins.
	lead := strings.Repeat(" ", len("[known] ")+width+2)
	lines := strings.Split(fix, "\n")
	var b strings.Builder
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if i == 0 {
			fmt.Fprintf(&b, "%s→ %s\n", lead, line)
			continue
		}
		fmt.Fprintf(&b, "%s  %s\n", lead, line)
	}
	return b.String()
}

// fileExists is the real predicate, separated so the shadowing logic can be tested without writing
// into /etc on the machine running the tests.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
