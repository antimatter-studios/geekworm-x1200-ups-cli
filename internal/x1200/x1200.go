// Package x1200 holds what is specific to the Geekworm X1200 board: which GPIO lines it uses and
// what their levels mean.
//
// Everything else in this program is deliberately generic — the reader finds devices by shape, so an
// INA226 or a different HAT works unchanged. This package is where that stops, because a pin number
// and a polarity are facts about one board and cannot be inferred from anything the kernel exposes.
//
// Both polarities here are counter-intuitive and both are quoted from the vendor, because getting
// either backwards produces a confident, inverted answer that no other measurement contradicts.
package x1200

import (
	"errors"
	"fmt"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/gpio"
)

// Pin assignments, from https://wiki.geekworm.com/X1200_Hardware.
//
//	PIN 3  / GPIO2  - I2C SDA
//	PIN 5  / GPIO3  - I2C SCL
//	PIN 31 / GPIO6  - Power loss detection
//	PIN 36 / GPIO16 - Battery charging control
const (
	// PLDLine carries mains presence. Physical pin 31.
	PLDLine uint32 = 6
	// ChargeLine enables and disables charging. Physical pin 36.
	ChargeLine uint32 = 16
)

// PLDBias is the pull applied when reading mains presence.
//
// Pull-up, which is what the line already sits with, and it is worth being explicit about the
// consequence. Mains-present reads high, so an unconnected or intermittent line also reads high:
// the detector's failure mode is to report that everything is fine. On a HAT that connects through
// pogo pins — precisely the contact that goes intermittent — a stuck "on mains" must be treated as
// a possible fault rather than as proof, and anything built on this needs to know that.
const PLDBias = gpio.BiasPullUp

// ErrNoPin is returned when the line cannot be read at all.
//
// Usually this means the pin has never been configured as an input. An unconfigured line has no
// readable level — `pinctrl get 6` shows "--" in the level column — and no amount of retrying will
// produce one. Persisting `gpio=6=ip,pu` in config.txt is what fixes it.
var ErrNoPin = errors.New("x1200: mains-detection line is not readable; is gpio=6=ip,pu set in config.txt?")

// Mains reports whether the adapter is supplying power.
//
// Quoted from the vendor's hardware table: "AC power loss & power adapter failture detection,
// Low-power supply failed, High-power supply OK". So high means mains, low means running on the
// pack. This is the only authoritative source for that distinction on this board — the fuel gauge
// cannot tell direction, and the INA219 sits downstream of the changeover so it measures the same
// load either way.
func Mains(port gpio.Reader, chip string) (bool, error) {
	high, err := port.Read(chip, PLDLine, PLDBias)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrNoPin, err)
	}
	return high, nil
}

// Charging reports whether battery charging is currently enabled.
//
// Note the inversion, which is the trap on this board: the vendor's own commands are
// `pinctrl set 16 op dh` to DISABLE charging and `pinctrl set 16 op dl` to ENABLE it. High is off.
// The related X728 works the other way round, so anyone reasoning from that board — or from the
// intuition that high means on — gets it exactly backwards and disables charging while believing
// they enabled it.
func Charging(port gpio.Reader, chip string) (bool, error) {
	high, err := port.Read(chip, ChargeLine, gpio.BiasNone)
	if err != nil {
		return false, fmt.Errorf("x1200: cannot read charging control on GPIO%d: %w", ChargeLine, err)
	}
	return !high, nil
}

// SetCharging enables or disables charging.
//
// The level written is the inverse of the argument, for the reason given on Charging.
//
// This does not persist. The kernel releases a line when the process that claimed it exits, so the
// pin reverts as soon as this program returns. A level that must survive belongs in config.txt's
// `gpio=` directive — see Persisted. This exists for the momentary case: proving which way round the
// pin is, or disabling charging for as long as a supervising process runs.
func SetCharging(port gpio.Writer, chip string, enabled bool) error {
	if err := port.Write(chip, ChargeLine, !enabled); err != nil {
		return fmt.Errorf("x1200: cannot set charging on GPIO%d: %w", ChargeLine, err)
	}
	return nil
}

// Persisted returns the config.txt directives that make these pins usable across a reboot.
//
// `gpio=` is applied by the firmware at boot, which is the only mechanism that survives without a
// daemon holding the line open. The charging control is deliberately absent: leaving it undriven
// lets the board charge normally, and writing a level here would silently impose a policy on every
// boot that nobody reading config.txt later would connect to charging behaviour.
func Persisted() []string {
	return []string{fmt.Sprintf("gpio=%d=ip,pu", PLDLine)}
}
