//go:build !linux

package gpio

// OS is a Port that cannot work here.
//
// The program cross-compiles for darwin so it can be built and tested on a laptop, and the honest
// answer on a machine with no GPIO character device is an error. Returning false would be
// indistinguishable from a line that is genuinely low, which for a mains-detection signal means
// silently reporting a power cut that is not happening — or worse, the reverse.
type OS struct{}

// Read always fails.
func (OS) Read(string, uint32, Bias) (bool, error) { return false, ErrUnsupported }

// Write always fails.
func (OS) Write(string, uint32, bool) error { return ErrUnsupported }

// Chips always fails.
func (OS) Chips() ([]string, error) { return nil, ErrUnsupported }

var _ Port = OS{}
