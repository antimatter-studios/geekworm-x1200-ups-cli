// Package sysfs reads the small text files the kernel exposes under /sys.
//
// The filesystem is passed in rather than reached for. Every function here is pure given its
// arguments: the two that actually touch a disk are the closures inside OS, and nothing else in
// this program performs I/O at all. That is what lets the whole of the logic be tested against an
// in-memory map, on a machine with no Raspberry Pi and no UPS attached.
//
// Everything is tolerant of missing files, because a driver publishes only the attributes its chip
// supports. The MAX17040 has no current_now and no charge_full, and treating those as errors would
// report a broken battery rather than a battery that measures fewer things.
//
// Missing and zero stay distinguishable throughout. A battery reporting 0% and a battery that
// cannot report a percentage are different situations, and only one of them is an emergency.
package sysfs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FS is the two capabilities this program needs from a filesystem.
//
// A struct of functions rather than an interface: there is exactly one real implementation and one
// test implementation, and a test one is a map literal instead of a type declaration.
type FS struct {
	// Read returns a file's trimmed contents, and whether it was present and non-empty.
	Read func(path string) (string, bool)
	// Glob returns the paths matching a shell pattern.
	Glob func(pattern string) []string
}

// OS returns an FS backed by the real filesystem, rooted at root.
//
// This is the only impure constructor in the program, and the root is a parameter so that pointing
// the whole thing at a directory of captured files is a matter of passing a different string.
func OS(root string) FS {
	if root == "" {
		root = "/sys"
	}
	return FS{
		Read: func(path string) (string, bool) {
			raw, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				return "", false
			}
			// Empty counts as absent deliberately: the kernel publishes some attributes as an
			// empty file when the chip has nothing to say — this hardware's battery exposes a
			// `temp` that reads as nothing at all — and an empty string is not a reading.
			s := strings.TrimSpace(string(raw))
			return s, s != ""
		},
		Glob: func(pattern string) []string {
			matches, err := filepath.Glob(filepath.Join(root, pattern))
			if err != nil {
				// Glob's only error is a malformed pattern, which is a bug in this program rather
				// than a state the machine can be in.
				return nil
			}
			return relative(root, matches)
		},
	}
}

// relative strips the root back off a set of matches, so that callers only ever handle the paths
// they asked about and the root stays an implementation detail of OS.
func relative(root string, matches []string) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, err := filepath.Rel(root, m)
		if err != nil {
			out = append(out, m)
			continue
		}
		out = append(out, rel)
	}
	return out
}

// Map returns an FS backed by a map from path to contents. Used by tests, and by nothing else.
func Map(files map[string]string) FS {
	return FS{
		Read: func(path string) (string, bool) {
			s, ok := files[path]
			s = strings.TrimSpace(s)
			return s, ok && s != ""
		},
		Glob: func(pattern string) []string {
			var out []string
			for path := range files {
				dir := filepath.Dir(path)
				if ok, _ := filepath.Match(pattern, dir); ok && !contains(out, dir) {
					out = append(out, dir)
				}
				if ok, _ := filepath.Match(pattern, path); ok && !contains(out, path) {
					out = append(out, path)
				}
			}
			sortStrings(out)
			return out
		},
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// sortStrings keeps Map's Glob deterministic. Map iteration order in Go is deliberately random, and
// a test that passes only when the runtime happens to visit hwmon0 before hwmon1 is worse than no
// test: it fails once a month for reasons nobody can reproduce.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Int reads an integer attribute.
func Int(fs FS, path string) (int64, bool) {
	s, ok := fs.Read(path)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Scaled reads an integer attribute and divides it, which is what every caller wants.
//
// sysfs reports in whole small units so it never has to print a decimal point: microvolts,
// milliamps, microwatts. Doing the conversion here keeps the divisors in one place beside a comment
// naming the unit, instead of scattering unexplained constants through the callers.
func Scaled(fs FS, path string, divisor float64) (float64, bool) {
	n, ok := Int(fs, path)
	if !ok || divisor == 0 {
		return 0, false
	}
	return float64(n) / divisor, true
}

// IntPtr and the others return nil for an absent attribute, so that a struct can be built in a
// single literal without any field being assigned afterwards.
func IntPtr(fs FS, path string) *int {
	n, ok := Int(fs, path)
	if !ok {
		return nil
	}
	v := int(n)
	return &v
}

// ScaledPtr is Scaled, as a pointer.
func ScaledPtr(fs FS, path string, divisor float64) *float64 {
	v, ok := Scaled(fs, path, divisor)
	if !ok {
		return nil
	}
	return &v
}

// BoolPtr reads an attribute that is 1 or 0.
func BoolPtr(fs FS, path string) *bool {
	n, ok := Int(fs, path)
	if !ok {
		return nil
	}
	v := n == 1
	return &v
}

// TextOr reads an attribute, falling back to a default when it is absent.
func TextOr(fs FS, path, fallback string) string {
	if s, ok := fs.Read(path); ok {
		return s
	}
	return fallback
}
