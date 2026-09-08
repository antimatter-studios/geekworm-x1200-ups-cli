package gpio

import (
	"testing"
	"unsafe"
)

// The sizes the kernel expects, from linux/gpio.h on a 64-bit build.
//
// These are the highest-value tests in the package. Everything else here can be exercised by running
// the program; these cannot, because this project has no access to the Raspberry Pi it targets. A
// struct that is one field or one padding word out produces EINVAL from the ioctl with nothing to
// say which field was wrong, on a machine the author cannot attach a debugger to.
func TestStructSizesMatchTheKernelABI(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"gpio_v2_line_attribute", unsafe.Sizeof(lineAttribute{}), 16},
		{"gpio_v2_line_config_attribute", unsafe.Sizeof(lineConfigAttribute{}), 24},
		{"gpio_v2_line_config", unsafe.Sizeof(lineConfig{}), 272},
		{"gpio_v2_line_request", unsafe.Sizeof(lineRequest{}), sizeofRequest},
		{"gpio_v2_line_values", unsafe.Sizeof(lineValues{}), sizeofValues},
		{"gpiochip_info", unsafe.Sizeof(chipInfo{}), sizeofChipInf},
	} {
		if c.got != c.want {
			t.Errorf("sizeof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// Field offsets matter as much as the total: two mistakes that cancel out in the size would still
// put every value in the wrong place.
func TestStructOffsetsMatchTheKernelABI(t *testing.T) {
	var req lineRequest
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"offsets", unsafe.Offsetof(req.Offsets), 0},
		{"consumer", unsafe.Offsetof(req.Consumer), 256},
		{"config", unsafe.Offsetof(req.Config), 288},
		{"num_lines", unsafe.Offsetof(req.NumLines), 560},
		{"event_buffer_size", unsafe.Offsetof(req.EventBufferSize), 564},
		{"fd", unsafe.Offsetof(req.FD), 588},
	} {
		if c.got != c.want {
			t.Errorf("offsetof(gpio_v2_line_request.%s) = %d, want %d", c.name, c.got, c.want)
		}
	}

	var cfg lineConfig
	if got := unsafe.Offsetof(cfg.Attrs); got != 32 {
		t.Errorf("offsetof(gpio_v2_line_config.attrs) = %d, want 32", got)
	}
}

// The request numbers as the kernel's _IOC macro produces them. Hard-coded here deliberately: the
// point is to check the arithmetic in ioc against known-good constants, so recomputing them the same
// way would prove nothing.
func TestIoctlRequestNumbers(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uint32
		want uint32
	}{
		{"GPIO_V2_GET_LINE_IOCTL", reqGetLine(), 0xC250B407},
		{"GPIO_V2_LINE_GET_VALUES_IOCTL", reqGetValues(), 0xC010B40E},
		{"GPIO_V2_LINE_SET_VALUES_IOCTL", reqSetValues(), 0xC010B40F},
		{"GPIO_GET_CHIPINFO_IOCTL", reqChipInfo(), 0x8044B401},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// Bias flags are the reason this uses the character device rather than sysfs, so the values must be
// right: a wrong bit could request an edge event or open-drain instead of a pull.
func TestLineFlagValues(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uint64
		want uint64
	}{
		{"INPUT", flagInput, 1 << 2},
		{"OUTPUT", flagOutput, 1 << 3},
		{"BIAS_PULL_UP", flagBiasPullUp, 1 << 8},
		{"BIAS_PULL_DOWN", flagBiasPullDown, 1 << 9},
		{"BIAS_DISABLED", flagBiasDisabled, 1 << 10},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

func TestFakePortRecordsWrites(t *testing.T) {
	f := &Fake{Levels: map[uint32]bool{6: true}}

	got, err := f.Read("/dev/gpiochip0", 6, BiasPullUp)
	if err != nil || !got {
		t.Fatalf("Read = %v, %v; want true, nil", got, err)
	}
	if _, err := f.Read("/dev/gpiochip0", 99, BiasPullUp); err == nil {
		t.Error("reading an unavailable line should fail, not return false")
	}
	if err := f.Write("/dev/gpiochip0", 16, false); err != nil {
		t.Fatal(err)
	}
	if len(f.Written) != 1 || f.Written[0].Offset != 16 || f.Written[0].Value {
		t.Errorf("Written = %+v", f.Written)
	}
	// A write is observable by a subsequent read, as it would be on hardware.
	if got, _ := f.Read("/dev/gpiochip0", 16, BiasNone); got {
		t.Error("write did not take effect")
	}
}
