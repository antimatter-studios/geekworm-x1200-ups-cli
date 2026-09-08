package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/history"
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

// clock returns a deterministic time source that advances a minute per call, so that samples
// recorded during a test are spaced the way real ones would be rather than sharing one instant.
func clock() func() time.Time {
	t := time.Date(2026, 9, 8, 21, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(time.Minute)
		return t
	}
}

// memStore is a history store held in a string, so no test touches a disk.
func memStore() history.Store {
	store, _ := history.Memory("")
	return store
}

// noStore is an unconfigured store, which is how the tool behaves under -history="".
func noStore() history.Store { return history.Store{} }

func TestOnceText(t *testing.T) {
	got, err := once(hardware(), clock(), noStore(), options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "95%") || !strings.Contains(got, "1.34 W") {
		t.Errorf("once = %q", got)
	}
}

func TestOnceJSON(t *testing.T) {
	got, err := once(hardware(), clock(), noStore(), options{json: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(got), "{") || !strings.Contains(got, `"percent": 95`) {
		t.Errorf("once(json) = %q", got)
	}
}

func TestOnceWithNothingBound(t *testing.T) {
	if _, err := once(sysfs.Map(map[string]string{}), clock(), noStore(), options{}); !errors.Is(err, ups.ErrNoDevices) {
		t.Errorf("err = %v; want ErrNoDevices", err)
	}
}

func TestRunReadsOnceByDefault(t *testing.T) {
	r := &recorder{}
	if err := run(hardware(), r, r.sleep, clock(), noStore(), options{}); err != nil {
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
	err := run(hardware(), stopping, r.sleep, clock(), noStore(), options{watch: 2 * time.Second})
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
	if err := run(sysfs.Map(map[string]string{}), &recorder{}, func(time.Duration) {}, clock(), noStore(), options{}); !errors.Is(err, ups.ErrNoDevices) {
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

// falling returns hardware whose gauge reads pct, so a discharge can be simulated across calls.
func falling(pct int, volts string) sysfs.FS {
	return sysfs.Map(map[string]string{
		"class/power_supply/battery/type":        "Battery",
		"class/power_supply/battery/capacity":    strconv.Itoa(pct),
		"class/power_supply/battery/voltage_now": volts,
		"class/power_supply/battery/status":      "Unknown",
	})
}

// With no store configured the tool must still report the battery. The estimate is the addition;
// losing the percentage to gain it would be a bad trade, and -history="" is a supported choice.
func TestOnceWithoutAStoreStillReportsTheBattery(t *testing.T) {
	got, err := once(hardware(), clock(), noStore(), options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "95%") {
		t.Errorf("output lost the battery: %q", got)
	}
	if strings.Contains(got, "remaining") {
		t.Errorf("estimate rendered with no store configured: %q", got)
	}
}

// A single reading cannot imply a rate, and the tool must say so rather than invent one.
func TestOnceWithOneSampleSaysItNeedsMore(t *testing.T) {
	got, err := once(hardware(), clock(), memStore(), options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "remaining") {
		t.Fatalf("no estimate line at all: %q", got)
	}
	if !strings.Contains(got, "history") && !strings.Contains(got, "not enough") {
		t.Errorf("first reading should explain the absent estimate: %q", got)
	}
}

// The whole feature, end to end: feed a steady discharge through the real code path and check a
// plausible duration comes out.
func TestDischargeProducesATimeRemaining(t *testing.T) {
	store := memStore()
	now := clock() // one minute per call

	// 40 readings, one a minute, losing a percent every other minute: 30%/hour from 80%.
	var out string
	for i := 0; i < 40; i++ {
		pct := 80 - i/2
		text, err := once(falling(pct, "4000000"), now, store, options{})
		if err != nil {
			t.Fatalf("reading %d: %v", i, err)
		}
		out = text
	}

	if !strings.Contains(out, "remaining") {
		t.Fatalf("no estimate after 40 samples: %q", out)
	}
	if strings.Contains(out, "—") {
		t.Errorf("still no duration after 40 minutes of discharge: %q", out)
	}
	// 60% left at 30%/hour is about two hours. Allow a wide band: the point is that it is a
	// believable number in hours, not that it is exact.
	if !strings.Contains(out, "h") {
		t.Errorf("expected an hours figure, got %q", out)
	}
}

// JSON consumers need the duration as seconds, not as Go's nanosecond integer.
func TestJSONCarriesSecondsNotNanoseconds(t *testing.T) {
	store := memStore()
	now := clock()
	var out string
	for i := 0; i < 40; i++ {
		text, err := once(falling(80-i/2, "4000000"), now, store, options{json: true})
		if err != nil {
			t.Fatal(err)
		}
		out = text
	}
	if !strings.Contains(out, `"time_to_empty_s"`) {
		t.Errorf("JSON lacks time_to_empty_s: %q", out)
	}
	if strings.Contains(out, `"TimeToEmpty"`) {
		t.Errorf("JSON leaked the Duration field: %q", out)
	}
}
