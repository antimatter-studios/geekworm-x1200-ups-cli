package gpio

// This file describes the Linux GPIO character-device ABI. It carries no build tag on purpose.
//
// The layouts and request numbers are facts about Linux, but stating them does not require Linux to
// compile — and if they lived in the linux-only file their tests could only run on the target. That
// is the wrong way round: these are exactly the values that cannot be checked by running the program
// on hardware this project cannot reach, because a wrong size or a wrong request number surfaces as
// a bare EINVAL with nothing to say which field was wrong.
//
// Keeping them here means `go test` on a laptop proves the arithmetic.

// Layout constants from linux/gpio.h. These are ABI: they cannot be changed to suit Go, and every
// one of them is checked by a test, because a wrong size produces EINVAL on hardware rather than a
// compile error here.
const (
	maxNameSize   = 32
	linesMax      = 64
	numAttrsMax   = 10
	sizeofRequest = 592 // struct gpio_v2_line_request
	sizeofValues  = 16  // struct gpio_v2_line_values
	sizeofChipInf = 68  // struct gpiochip_info
)

// Line flags, from enum gpio_v2_line_flag.
const (
	flagInput        uint64 = 1 << 2
	flagOutput       uint64 = 1 << 3
	flagBiasPullUp   uint64 = 1 << 8
	flagBiasPullDown uint64 = 1 << 9
	flagBiasDisabled uint64 = 1 << 10
)

// ioctl direction bits.
const (
	iocWrite = 1
	iocRead  = 2
)

// ioc composes an ioctl request number the way the kernel's _IOC macro does: direction, size, type
// and number packed into 32 bits. Written out rather than hard-coded so that the sizes above are the
// single source of truth and a struct that drifts is caught by the size tests instead of by EINVAL.
func ioc(dir, typ, nr, size uint32) uint32 {
	return dir<<30 | size<<16 | typ<<8 | nr
}

const iocTypeGPIO = 0xB4

func reqGetLine() uint32   { return ioc(iocRead|iocWrite, iocTypeGPIO, 0x07, sizeofRequest) }
func reqGetValues() uint32 { return ioc(iocRead|iocWrite, iocTypeGPIO, 0x0E, sizeofValues) }
func reqSetValues() uint32 { return ioc(iocRead|iocWrite, iocTypeGPIO, 0x0F, sizeofValues) }
func reqChipInfo() uint32  { return ioc(iocRead, iocTypeGPIO, 0x01, sizeofChipInf) }

// struct gpio_v2_line_attribute
type lineAttribute struct {
	ID      uint32
	Padding uint32
	Value   uint64 // union of flags, values, debounce_period_us
}

// struct gpio_v2_line_config_attribute
type lineConfigAttribute struct {
	Attr lineAttribute
	Mask uint64
}

// struct gpio_v2_line_config
type lineConfig struct {
	Flags    uint64
	NumAttrs uint32
	Padding  [5]uint32
	Attrs    [numAttrsMax]lineConfigAttribute
}

// struct gpio_v2_line_request
type lineRequest struct {
	Offsets         [linesMax]uint32
	Consumer        [maxNameSize]byte
	Config          lineConfig
	NumLines        uint32
	EventBufferSize uint32
	Padding         [5]uint32
	FD              int32
}

// struct gpio_v2_line_values
type lineValues struct {
	Bits uint64
	Mask uint64
}

// struct gpiochip_info
type chipInfo struct {
	Name  [maxNameSize]byte
	Label [maxNameSize]byte
	Lines uint32
}
