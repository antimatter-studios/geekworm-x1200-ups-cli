//go:build linux

package gpio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

// biasFlag turns a Bias into its line flag.
func biasFlag(b Bias) uint64 {
	switch b {
	case BiasPullUp:
		return flagBiasPullUp
	case BiasPullDown:
		return flagBiasPullDown
	case BiasDisabled:
		return flagBiasDisabled
	default:
		return 0
	}
}

// OS is a Port backed by real character devices.
type OS struct{}

// ioctl is the single unsafe call in the package, kept to one place so that everything above it is
// ordinary Go that can be reasoned about without thinking about pointers.
func ioctl(fd uintptr, request uint32, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(request), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// request leases one line and returns the file holding the lease.
//
// The lease lives on the returned file, not on the chip: closing it releases the line. That is why
// the caller closes rather than this function — and why a crash cannot leave a line claimed, since
// the kernel drops the lease when the process's descriptors go.
func request(chip string, offset uint32, flags uint64) (*os.File, error) {
	cf, err := os.OpenFile(chip, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", chip, err)
	}
	defer cf.Close()

	req := lineRequest{NumLines: 1}
	req.Offsets[0] = offset
	req.Config.Flags = flags
	copy(req.Consumer[:], Consumer)

	if err := ioctl(cf.Fd(), reqGetLine(), unsafe.Pointer(&req)); err != nil {
		return nil, fmt.Errorf("request line %d on %s: %w", offset, chip, err)
	}
	if req.FD < 0 {
		return nil, fmt.Errorf("request line %d on %s: kernel returned no descriptor", offset, chip)
	}
	return os.NewFile(uintptr(req.FD), fmt.Sprintf("%s:%d", chip, offset)), nil
}

// Read returns the level of a line.
func (OS) Read(chip string, offset uint32, bias Bias) (bool, error) {
	line, err := request(chip, offset, flagInput|biasFlag(bias))
	if err != nil {
		return false, err
	}
	defer line.Close()

	vals := lineValues{Mask: 1}
	if err := ioctl(line.Fd(), reqGetValues(), unsafe.Pointer(&vals)); err != nil {
		return false, fmt.Errorf("read line %d on %s: %w", offset, chip, err)
	}
	return vals.Bits&1 == 1, nil
}

// Write drives a line.
//
// The line is released as soon as this returns, which for a level that must persist — the charging
// control, say — means the pin reverts to whatever the hardware does with an unclaimed line. That
// is a real limitation and the reason a persistent level belongs in config.txt's `gpio=` directive
// rather than here. This exists for the momentary case and for testing what a pin does.
func (OS) Write(chip string, offset uint32, value bool) error {
	line, err := request(chip, offset, flagOutput)
	if err != nil {
		return err
	}
	defer line.Close()

	vals := lineValues{Mask: 1}
	if value {
		vals.Bits = 1
	}
	if err := ioctl(line.Fd(), reqSetValues(), unsafe.Pointer(&vals)); err != nil {
		return fmt.Errorf("write line %d on %s: %w", offset, chip, err)
	}
	return nil
}

// Chips lists the GPIO character devices, header controller first.
//
// Ordering is the useful part. A Pi 5 presents several chips and only one of them carries the 40-pin
// header: the RP1 southbridge, labelled pinctrl-rp1. Older Pis label theirs pinctrl-bcm2835. Picking
// the lowest-numbered device instead would land on the wrong controller on a Pi 5 and read a line
// that exists but is not the one anybody meant.
func (OS) Chips() ([]string, error) {
	matches, err := filepath.Glob("/dev/gpiochip*")
	if err != nil || len(matches) == 0 {
		return nil, ErrNoChip
	}
	sort.Strings(matches)

	type scored struct {
		path  string
		rank  int
		lines uint32
	}
	var chips []scored
	for _, path := range matches {
		label, lines, err := describe(path)
		if err != nil {
			continue
		}
		rank := 2
		switch {
		case strings.Contains(label, "rp1"):
			rank = 0
		case strings.Contains(label, "bcm"):
			rank = 1
		}
		chips = append(chips, scored{path, rank, lines})
	}
	if len(chips) == 0 {
		return nil, ErrNoChip
	}
	sort.SliceStable(chips, func(i, j int) bool { return chips[i].rank < chips[j].rank })

	out := make([]string, 0, len(chips))
	for _, c := range chips {
		out = append(out, c.path)
	}
	return out, nil
}

// describe reads a chip's label and line count.
func describe(path string) (string, uint32, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		// Fall back to read-only: listing chips should work for a user who cannot drive them.
		if f, err = os.Open(path); err != nil {
			return "", 0, err
		}
	}
	defer f.Close()

	var info chipInfo
	if err := ioctl(f.Fd(), reqChipInfo(), unsafe.Pointer(&info)); err != nil {
		return "", 0, err
	}
	return strings.ToLower(nullTerminated(info.Label[:])), info.Lines, nil
}

// nullTerminated trims a fixed C string to its content.
func nullTerminated(b []byte) string {
	if i := indexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

// compile-time check that OS satisfies the interface.
var _ Port = OS{}

// unused keeps errors imported when build tags exclude other uses.
var _ = errors.New
