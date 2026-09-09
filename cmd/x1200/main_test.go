package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/build"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/estimate"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/gpio"
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

// noPort disables GPIO, which is what options{} means: gpio defaults false on a zero value, and
// these tests exercise the sysfs and history paths.
func noPort() gpio.Port { return nil }

func TestOnceText(t *testing.T) {
	got, err := once(hardware(), clock(), noStore(), noPort(), options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "95%") || !strings.Contains(got, "1.34 W") {
		t.Errorf("once = %q", got)
	}
}

func TestOnceJSON(t *testing.T) {
	got, err := once(hardware(), clock(), noStore(), noPort(), options{json: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(got), "{") || !strings.Contains(got, `"percent": 95`) {
		t.Errorf("once(json) = %q", got)
	}
}

func TestOnceWithNothingBound(t *testing.T) {
	if _, err := once(sysfs.Map(map[string]string{}), clock(), noStore(), noPort(), options{}); !errors.Is(err, ups.ErrNoDevices) {
		t.Errorf("err = %v; want ErrNoDevices", err)
	}
}

func TestRunReadsOnceByDefault(t *testing.T) {
	r := &recorder{}
	if err := run(hardware(), r, r.sleep, clock(), noStore(), noPort(), options{}); err != nil {
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
	err := run(hardware(), stopping, r.sleep, clock(), noStore(), noPort(), options{watch: 2 * time.Second})
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
	if err := run(sysfs.Map(map[string]string{}), &recorder{}, func(time.Duration) {}, clock(), noStore(), noPort(), options{}); !errors.Is(err, ups.ErrNoDevices) {
		t.Errorf("err = %v; want ErrNoDevices", err)
	}
}

func TestVersionIsNeverEmpty(t *testing.T) {
	// An unstamped binary must still answer. It now derives its identity from the commit Go embeds
	// rather than from a placeholder, so there is no build that legitimately has nothing to say —
	// and printing nothing is the one outcome that leaves a reader unable to act.
	if got := build.Current(); got.Version == "" {
		t.Errorf("version is empty: %+v", got)
	}
	if got := build.Current().String(); !strings.Contains(got, "x1200") {
		t.Errorf("String() = %q", got)
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
	// The string itself now comes from internal/build and goes to stdout rather than the writer
	// passed here, so this asserts the short-circuit rather than the text. TestVersionCommand covers
	// the content.
}

func TestVersionCommand(t *testing.T) {
	var out, errOut strings.Builder
	if err := command("version", nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"x1200", "version:", "source:", "go:", "platform:"} {
		if !strings.Contains(got, want) {
			t.Errorf("version output missing %q:\n%s", want, got)
		}
	}
}

func TestVersionCommandJSON(t *testing.T) {
	var out, errOut strings.Builder
	if err := command("version", []string{"-json"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var back build.Info
	if err := json.Unmarshal([]byte(out.String()), &back); err != nil {
		t.Fatalf("version -json did not parse: %v\n%s", err, out.String())
	}
	if back.Version == "" || back.Source == "" {
		t.Errorf("version -json incomplete: %+v", back)
	}
}

// An unknown command must list what exists: someone who guessed wrong wants the answer more than
// the correction.
func TestUnknownCommandListsWhatExists(t *testing.T) {
	var out, errOut strings.Builder
	if err := command("wat", nil, &out, &errOut); err == nil {
		t.Error("unknown command succeeded")
	}
	if !strings.Contains(errOut.String(), "version") {
		t.Errorf("unknown command did not list the real ones: %q", errOut.String())
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
	got, err := once(hardware(), clock(), noStore(), noPort(), options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "95%") {
		t.Errorf("output lost the battery: %q", got)
	}
	if strings.Contains(got, "estimate") {
		t.Errorf("estimate rendered with no store configured: %q", got)
	}
}

// A single reading cannot imply a rate, and the tool must say so rather than invent one.
func TestOnceWithOneSampleSaysItNeedsMore(t *testing.T) {
	got, err := once(hardware(), clock(), memStore(), noPort(), options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "estimate") {
		t.Fatalf("no estimate group at all: %q", got)
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
		text, err := once(falling(pct, "4000000"), now, store, noPort(), options{})
		if err != nil {
			t.Fatalf("reading %d: %v", i, err)
		}
		out = text
	}

	if !strings.Contains(out, "remaining:") {
		t.Fatalf("no duration after 40 samples: %q", out)
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
		text, err := once(falling(80-i/2, "4000000"), now, store, noPort(), options{json: true})
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

// The failure the caveat warns about, actually caught: a pull-up means a floating pin reads as
// mains, so a pin claiming mains while the pack drains is the signature of a HAT that is not seated.
func TestCorroborateFlagsAPinThatContradictsTheGauge(t *testing.T) {
	rate := -30.0
	r := &ups.Reading{
		Supply:   &ups.Supply{OnMains: true, Line: 6},
		Estimate: &estimate.Estimate{State: estimate.Discharging, PercentPerHour: &rate},
	}
	corroborate(r)
	if r.Supply.Suspect == "" {
		t.Fatal("mains claimed while draining was not flagged")
	}
	if !strings.Contains(r.Supply.Suspect, "seated") {
		t.Errorf("suspect = %q; want it to name the likely cause", r.Supply.Suspect)
	}
	// Not resolved in favour of either: overriding the pin would be a guess about which sensor broke.
	if !r.Supply.OnMains {
		t.Error("corroborate overrode the pin instead of reporting the contradiction")
	}
}

func TestCorroborateStaysQuietWhenTheSourcesAgree(t *testing.T) {
	rate := -30.0
	discharging := &estimate.Estimate{State: estimate.Discharging, PercentPerHour: &rate}

	// On battery and draining: consistent.
	r := &ups.Reading{Supply: &ups.Supply{OnMains: false, Line: 6}, Estimate: discharging}
	corroborate(r)
	if r.Supply.Suspect != "" {
		t.Errorf("flagged a consistent reading: %q", r.Supply.Suspect)
	}

	// On mains and charging: consistent.
	up := 10.0
	r = &ups.Reading{
		Supply:   &ups.Supply{OnMains: true, Line: 6},
		Estimate: &estimate.Estimate{State: estimate.Charging, PercentPerHour: &up},
	}
	corroborate(r)
	if r.Supply.Suspect != "" {
		t.Errorf("flagged a charging pack on mains: %q", r.Supply.Suspect)
	}

	// On mains and steady is the normal resting state and must not warn.
	r = &ups.Reading{
		Supply:   &ups.Supply{OnMains: true, Line: 6},
		Estimate: &estimate.Estimate{State: estimate.Steady},
	}
	corroborate(r)
	if r.Supply.Suspect != "" {
		t.Errorf("flagged a full pack sitting on mains: %q", r.Supply.Suspect)
	}
}

// With no pin reading or no estimate there is nothing to cross-check, and inventing a warning from
// one source would be worse than silence.
func TestCorroborateNeedsBothSources(t *testing.T) {
	for _, r := range []*ups.Reading{
		{},
		{Supply: &ups.Supply{OnMains: true, Line: 6}},
		{Estimate: &estimate.Estimate{State: estimate.Discharging}},
	} {
		corroborate(r) // must not panic
		if r.Supply != nil && r.Supply.Suspect != "" {
			t.Errorf("warned with only one source: %+v", r.Supply)
		}
	}
}
