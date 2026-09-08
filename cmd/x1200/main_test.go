package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/sysfs"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/ups"
)

func hardware() sysfs.FS {
	return sysfs.Map(map[string]string{
		"class/power_supply/battery/type":        "Battery",
		"class/power_supply/battery/capacity":    "95",
		"class/power_supply/battery/voltage_now": "4152500",
		"class/power_supply/battery/status":      "Unknown",
		"class/hwmon/hwmon6/name":                "ina219",
		"class/hwmon/hwmon6/in1_input":           "5060",
		"class/hwmon/hwmon6/curr1_input":         "267",
		"class/hwmon/hwmon6/power1_input":        "1340000",
	})
}

// recorder captures output without a file, and counts waits without spending any time.
type recorder struct {
	written strings.Builder
	waits   []time.Duration
}

func (r *recorder) Write(p []byte) (int, error) { return r.written.Write(p) }
func (r *recorder) sleep(d time.Duration)       { r.waits = append(r.waits, d) }

func TestOnceText(t *testing.T) {
	got, err := once(hardware(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "95%") || !strings.Contains(got, "1.34 W") {
		t.Errorf("once = %q", got)
	}
}

func TestOnceJSON(t *testing.T) {
	got, err := once(hardware(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(got), "{") || !strings.Contains(got, `"percent": 95`) {
		t.Errorf("once(json) = %q", got)
	}
}

func TestOnceWithNothingBound(t *testing.T) {
	if _, err := once(sysfs.Map(map[string]string{}), false); !errors.Is(err, ups.ErrNoDevices) {
		t.Errorf("err = %v; want ErrNoDevices", err)
	}
}

func TestRunReadsOnceByDefault(t *testing.T) {
	r := &recorder{}
	if err := run(hardware(), r, r.sleep, options{}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(r.written.String(), "battery"); n != 1 {
		t.Errorf("wrote %d readings; want 1", n)
	}
	if len(r.waits) != 0 {
		t.Errorf("slept %v; want not at all", r.waits)
	}
}

func TestRunWatchRepeatsAtTheGivenInterval(t *testing.T) {
	r := &recorder{}
	// The loop is endless by design, so the test stops it from the outside: the third wait returns
	// an error through the writer, which is the only way out that does not involve a real clock.
	stopping := &stopAfter{inner: r, limit: 3}
	err := run(hardware(), stopping, r.sleep, options{watch: 2 * time.Second})
	if err == nil {
		t.Fatal("watch loop did not stop")
	}
	if len(r.waits) != 2 {
		t.Errorf("waits = %v; want two, between three readings", r.waits)
	}
	for _, d := range r.waits {
		if d != 2*time.Second {
			t.Errorf("wait = %v; want the interval that was asked for", d)
		}
	}
}

// stopAfter fails the nth write, giving the watch loop a way to terminate in a test.
type stopAfter struct {
	inner *recorder
	limit int
	count int
}

var errStop = errors.New("stop")

func (s *stopAfter) Write(p []byte) (int, error) {
	s.count++
	if s.count >= s.limit {
		return 0, errStop
	}
	return s.inner.Write(p)
}

func TestRunPropagatesReadErrors(t *testing.T) {
	if err := run(sysfs.Map(map[string]string{}), &recorder{}, func(time.Duration) {}, options{}); !errors.Is(err, ups.ErrNoDevices) {
		t.Errorf("err = %v; want ErrNoDevices", err)
	}
}

func TestVersionDefault(t *testing.T) {
	// Stamped at build time; an unstamped binary must still answer rather than print nothing.
	if version == "" {
		t.Error("version is empty")
	}
}

func TestParseDefaults(t *testing.T) {
	opts, done, err := parse(nil, &strings.Builder{})
	if err != nil || done {
		t.Fatalf("parse = %v, %v, %v", opts, done, err)
	}
	if opts.root != "/sys" || opts.json || opts.watch != 0 {
		t.Errorf("defaults = %+v", opts)
	}
}

func TestParseFlags(t *testing.T) {
	opts, done, err := parse([]string{"-json", "-root", "/tmp/fake", "-watch", "5s"}, &strings.Builder{})
	if err != nil || done {
		t.Fatalf("parse = %v, %v, %v", opts, done, err)
	}
	if !opts.json || opts.root != "/tmp/fake" || opts.watch != 5*time.Second {
		t.Errorf("parsed = %+v", opts)
	}
}

func TestParseVersionStopsBeforeReadingHardware(t *testing.T) {
	// -version has to answer on a machine with no UPS, so it must short-circuit rather than fall
	// through to a read that would fail.
	var out strings.Builder
	_, done, err := parse([]string{"-version"}, &out)
	if err != nil || !done {
		t.Fatalf("parse(-version) = %v, %v", done, err)
	}
	if !strings.Contains(out.String(), version) {
		t.Errorf("version not printed: %q", out.String())
	}
}

func TestParseRejectsUnknownFlags(t *testing.T) {
	if _, _, err := parse([]string{"-nonsense"}, &strings.Builder{}); err == nil {
		t.Error("unknown flag accepted")
	}
}
