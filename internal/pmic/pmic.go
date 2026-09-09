// Package pmic reads the Raspberry Pi's own power measurements.
//
// This is the second, independent instrument the shunt calibration needs. The Pi 5's DA9091 reports
// per-rail voltages and currents, and it shares no hardware with the INA219 on the HAT — different
// chip, different bus, different driver — which is exactly what makes comparing them worth
// anything. Two readings from the same sensor agreeing proves nothing.
//
// It is reached through vcgencmd, and that is a deliberate exception to a rule this program
// otherwise keeps. Everything else here works on any Linux machine: sysfs for the gauge, the GPIO
// character device for the pins, no vendor tooling anywhere. The PMIC has no sysfs interface at all
// — it lives behind the VideoCore firmware mailbox — so there is no way to read it without either
// vcgencmd or reimplementing the mailbox protocol against an undocumented interface.
//
// The exception is contained: only calibration uses it, calibration is inherently a Raspberry Pi
// operation, and its absence is reported as a missing prerequisite rather than as a crash. Nothing
// in the normal reporting path depends on this package.
package pmic

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Reading is the total the PMIC reports.
type Reading struct {
	// Watts is the sum across every rail, which is power delivered *downstream* of the HAT's shunt.
	// It is therefore a lower bound on what enters the board: the difference is conversion loss.
	Watts float64
	// EXT5V is the input rail voltage as the PMIC sees it, useful as a sanity check against the
	// INA219's own bus voltage — two instruments measuring the same rail should broadly agree.
	EXT5V float64
	// Rails is how many voltage/current pairs were summed, so a caller can tell a real total from a
	// firmware that answered with almost nothing.
	Rails int
}

// ErrUnavailable is returned when the PMIC cannot be read.
//
// Distinct from a parse failure because the remedy differs: this means the machine is not a
// Raspberry Pi, or vcgencmd is not installed, and no amount of retrying changes it.
type ErrUnavailable struct{ Err error }

func (e ErrUnavailable) Error() string {
	return "pmic: cannot read the Pi's own power sensors (vcgencmd pmic_read_adc): " + e.Err.Error()
}

func (e ErrUnavailable) Unwrap() error { return e.Err }

// Runner runs a command and returns its output, injected so the parsing can be tested without a Pi.
type Runner func(name string, args ...string) ([]byte, error)

// Exec is the real runner.
func Exec(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// Read returns the PMIC total.
func Read(run Runner) (Reading, error) {
	out, err := run("vcgencmd", "pmic_read_adc")
	if err != nil {
		return Reading{}, ErrUnavailable{Err: err}
	}
	return Parse(string(out))
}

// Parse sums the rails from vcgencmd's output.
//
// The format pairs a voltage and a current per rail, distinguished only by a suffix on the name:
//
//	3V7_WL_SW_A current(0)=0.05605000A
//	3V7_WL_SW_V volt(1)=3.69824000V
//	EXT5V_V volt(24)=5.08691000V
//
// So a rail's power is the product of two lines that must be matched by name. Summing currents and
// voltages separately would be meaningless — they belong to different rails at different voltages —
// which is why this pairs them up rather than accumulating as it goes.
func Parse(text string) (Reading, error) {
	amps := map[string]float64{}
	volts := map[string]float64{}

	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		name, value, ok := field(scanner.Text())
		if !ok {
			continue
		}
		switch {
		case strings.HasSuffix(name, "_A"):
			amps[strings.TrimSuffix(name, "_A")] = value
		case strings.HasSuffix(name, "_V"):
			volts[strings.TrimSuffix(name, "_V")] = value
		}
	}

	var r Reading
	r.EXT5V = volts["EXT5V"]
	for rail, a := range amps {
		v, ok := volts[rail]
		if !ok {
			// A current with no matching voltage cannot be turned into power, and guessing 5 V for it
			// would inflate the total by an unknown amount. Dropped, and counted by omission.
			continue
		}
		r.Watts += a * v
		r.Rails++
	}
	if r.Rails == 0 {
		return r, fmt.Errorf("pmic: no rails found in vcgencmd output; the format may have changed")
	}
	return r, nil
}

// field pulls the rail name and numeric value out of one line.
func field(line string) (name string, value float64, ok bool) {
	line = strings.TrimSpace(line)
	space := strings.Index(line, " ")
	eq := strings.Index(line, "=")
	if space <= 0 || eq <= space {
		return "", 0, false
	}
	name = line[:space]

	// Trailing unit character: A for amps, V for volts. Trimming by suffix rather than by index
	// keeps this working if the firmware ever pads the number differently.
	raw := strings.TrimSpace(line[eq+1:])
	raw = strings.TrimSuffix(strings.TrimSuffix(raw, "A"), "V")
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return "", 0, false
	}
	return name, v, true
}
