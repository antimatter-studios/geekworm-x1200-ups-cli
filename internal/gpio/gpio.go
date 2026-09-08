// Package gpio reads and writes GPIO lines through the kernel's character device.
//
// Three approaches were available and two are wrong for this program.
//
// Shelling out to `pinctrl` is what Geekworm's own scripts do, and it is what a Raspberry Pi user
// would reach for first. It is rejected because `pinctrl` ships with Raspberry Pi OS and nothing
// else, and this binary has to work on any distribution on its own. A tool whose central feature
// silently depends on a vendor utility being installed is not standalone.
//
// The sysfs interface (/sys/class/gpio/export) is simpler still and is deprecated: it is compiled
// out by default on modern kernels, is racy between export and use, and has no way to express a bias
// resistor — which this hardware needs, because the mains-detection line floats.
//
// So: the /dev/gpiochipN character device, the interface the kernel actually wants used, driven by
// ioctl. That means struct layouts and request numbers matching linux/gpio.h exactly, which is the
// cost. The benefit is no dependency, no external binary, correct bias handling, and a lease the
// kernel releases when the process exits, so a crash cannot leave a line claimed.
//
// The layouts and the arithmetic that produces the request numbers are unit tested, because a
// mistake in either produces EINVAL at runtime on hardware this cannot be tested against.
package gpio

import "errors"

// Bias is the internal pull applied while a line is read.
//
// It matters here rather than being a detail. The X1200 signals mains presence on a line that is
// otherwise unconfigured and floating, and a floating input reads as noise. Which pull to choose is
// a decision about failure: with a pull-up a disconnected line reads high, and high means "mains
// present" on this board, so the detector's failure mode is to report that everything is fine. On a
// HAT that connects through pogo pins — the exact contact that goes intermittent — that is worth
// knowing about rather than discovering during an outage.
type Bias int

const (
	// BiasNone leaves the line as the hardware left it.
	BiasNone Bias = iota
	// BiasPullUp reads high when nothing drives the line.
	BiasPullUp
	// BiasPullDown reads low when nothing drives the line.
	BiasPullDown
	// BiasDisabled explicitly removes any pull.
	BiasDisabled
)

// ErrUnsupported is returned on platforms with no GPIO character device.
//
// The program cross-compiles for darwin so that it can be built and tested on a laptop, and the
// honest answer there is that the operation cannot be performed — not a zero, which would be
// indistinguishable from a line that is genuinely low.
var ErrUnsupported = errors.New("gpio: not supported on this platform")

// ErrNoChip is returned when no suitable gpiochip could be found.
var ErrNoChip = errors.New("gpio: no gpiochip found")

// Consumer is the name this program claims lines under. It appears in `gpioinfo` and in
// `pinctrl get`, so a person wondering what has taken a line can see who to blame.
const Consumer = "x1200"

// Reader reads one line.
type Reader interface {
	// Read returns the level of a line on a chip, requesting it with the given bias.
	Read(chip string, offset uint32, bias Bias) (bool, error)
}

// Writer drives one line.
type Writer interface {
	// Write sets a line to a level, leaving it driven until the process exits.
	Write(chip string, offset uint32, value bool) error
}

// Port is the whole capability, injected wherever GPIO is needed so that callers stay testable.
type Port interface {
	Reader
	Writer
	// Chips lists the character devices present, most likely first.
	Chips() ([]string, error)
}

// Fake is a Port backed by a map, for tests and for anything that needs to run without hardware.
type Fake struct {
	// Levels is keyed by offset. Missing offsets read as an error, which is what an unrequestable
	// line does on real hardware.
	Levels map[uint32]bool
	// Err, when set, is returned by every operation.
	Err error
	// Written records what Write was asked to do, in order.
	Written []struct {
		Offset uint32
		Value  bool
	}
}

// Read returns a recorded level.
func (f *Fake) Read(_ string, offset uint32, _ Bias) (bool, error) {
	if f.Err != nil {
		return false, f.Err
	}
	v, ok := f.Levels[offset]
	if !ok {
		return false, errors.New("gpio: line not available")
	}
	return v, nil
}

// Write records a change and applies it to the levels, so a read afterwards sees it.
func (f *Fake) Write(_ string, offset uint32, value bool) error {
	if f.Err != nil {
		return f.Err
	}
	if f.Levels == nil {
		f.Levels = map[uint32]bool{}
	}
	f.Levels[offset] = value
	f.Written = append(f.Written, struct {
		Offset uint32
		Value  bool
	}{offset, value})
	return nil
}

// Chips returns one fake chip.
func (f *Fake) Chips() ([]string, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return []string{"/dev/gpiochip0"}, nil
}
