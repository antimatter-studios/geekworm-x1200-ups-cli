package ups

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/estimate"
)

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }
func b(v bool) *bool       { return &v }

func sample() *Reading {
	return &Reading{
		Battery: &Battery{Name: "battery", Percent: i(95), VoltageV: f(4.1525), Status: "Unknown", Present: b(true)},
		Power:   &Power{Name: "hwmon6", Chip: "ina219", BusV: f(5.06), ShuntMV: f(3), CurrentA: f(0.267), WattsW: f(1.34), ShuntOhms: f(0.01)},
	}
}

func TestText(t *testing.T) {
	got := Text(sample())
	// Every value carries its own key: the old layout put them in columns and left the reader to
	// guess, which failed worst on the gauge's status.
	for _, want := range []string{
		"battery", "charge:", "95%", "voltage:", "4.152 V", "state:",
		"power", "bus:", "5.06 V", "current:", "0.267 A", "draw:", "1.34 W",
		"shunt:", "10.000 mOhm", "drop 3 mV", "ina219",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Text missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("Text did not end with a newline")
	}
}

// Values line up in one column across every group, so the eye follows a single edge.
func TestTextAlignsValuesAcrossGroups(t *testing.T) {
	got := Text(sample())
	col := -1
	for _, line := range strings.Split(got, "\n") {
		idx := strings.Index(line, ":")
		if !strings.HasPrefix(line, "  ") || idx < 0 {
			continue
		}
		// Position of the value, not of the colon: step past the colon, then over the padding.
		rest := line[idx+1:]
		at := idx + 1 + len(rest) - len(strings.TrimLeft(rest, " "))
		if col == -1 {
			col = at
			continue
		}
		if at != col {
			t.Errorf("value column %d != %d in %q", at, col, line)
		}
	}
	if col == -1 {
		t.Fatal("no key/value rows found")
	}
}

func TestTextNil(t *testing.T) {
	if got := Text(nil); got != "" {
		t.Errorf("Text(nil) = %q; want empty", got)
	}
	if got := Text(&Reading{}); got != "" {
		t.Errorf("Text(empty) = %q; want empty", got)
	}
}

func TestBatteryRows(t *testing.T) {
	if got := batteryRows(nil, nil); got != nil {
		t.Errorf("batteryRows(nil) = %v", got)
	}
	// A gauge that cannot report a percentage must not render as 0%: an unreadable gauge and a flat
	// pack are opposite situations and only one is an emergency.
	got := find(batteryRows(&Battery{Name: "battery", Status: "Unknown"}, nil), "charge")
	if strings.Contains(got, "0%") {
		t.Errorf("absent percentage rendered as %q", got)
	}
	if got := find(batteryRows(&Battery{Percent: i(0), Status: "Discharging"}, nil), "charge"); got != "0%" {
		t.Errorf("zero percent rendered as %q; want 0%%", got)
	}
	if got := find(batteryRows(&Battery{Percent: i(50), Status: "Unknown", Present: b(false)}, nil), "pack"); got != "NOT FITTED" {
		t.Errorf("absent pack not flagged: %q", got)
	}
	if got := find(batteryRows(&Battery{Percent: i(50), Status: "Unknown", Present: b(true)}, nil), "pack"); got != "" {
		t.Errorf("present pack wrongly flagged: %q", got)
	}
}

// The gauge reads "unknown" forever on this hardware, so a bare "unknown" is the least useful thing
// the tool could print. With no inference available it must at least explain itself.
func TestBatteryStateExplainsAnUnknownGauge(t *testing.T) {
	got := find(batteryRows(&Battery{Percent: i(80), Status: "Unknown"}, nil), "state")
	if !strings.HasPrefix(got, "unknown") {
		t.Fatalf("state = %q, want it to start with unknown", got)
	}
	if !strings.Contains(got, "cannot tell") {
		t.Errorf("state = %q, want an explanation of why", got)
	}
}

// Printing "unknown" beside an estimate that says "discharging" contradicts itself. Where the
// samples imply a direction, use it — labelled as inferred, because it is a weaker claim than a
// hardware signal and must not be dressed up as one.
func TestBatteryStateUsesTheInferredDirection(t *testing.T) {
	rate := -20.0
	e := &estimate.Estimate{State: estimate.Discharging, PercentPerHour: &rate}
	got := find(batteryRows(&Battery{Percent: i(80), Status: "Unknown"}, e), "state")
	if !strings.HasPrefix(got, "discharging") {
		t.Fatalf("state = %q, want discharging", got)
	}
	if !strings.Contains(got, "inferred") {
		t.Errorf("state = %q, want it labelled as inferred rather than measured", got)
	}
}

// Steady is not evidence of either. A full pack on mains and a pack on battery that has not yet lost
// a whole percent look identical, so neither may be claimed.
func TestBatteryStateWillNotGuessFromSteady(t *testing.T) {
	e := &estimate.Estimate{State: estimate.Steady}
	got := find(batteryRows(&Battery{Percent: i(80), Status: "Unknown"}, e), "state")
	if !strings.HasPrefix(got, "unknown") {
		t.Errorf("state = %q; steady must not be read as a direction", got)
	}
}

// A real status from the kernel is passed through untouched: the explanation is only for the
// permanent "unknown" this hardware produces.
func TestBatteryStatePassesARealStatusThrough(t *testing.T) {
	got := find(batteryRows(&Battery{Percent: i(80), Status: "Discharging"}, nil), "state")
	if got != "discharging" {
		t.Errorf("state = %q, want the kernel's own word unadorned", got)
	}
}

func TestPowerRows(t *testing.T) {
	if got := powerRows(nil); got != nil {
		t.Errorf("powerRows(nil) = %v", got)
	}
	rows := powerRows(sample().Power)
	if len(rows) != 4 {
		t.Fatalf("powerRows = %d; want bus, current, draw, shunt", len(rows))
	}
	// The shunt scales current and draw, so it is shown with the drop it was derived from.
	shunt := find(rows, "shunt")
	if !strings.Contains(shunt, "drop 3 mV") || !strings.Contains(shunt, "ina219") {
		t.Errorf("shunt = %q; want the measured drop and the chip", shunt)
	}
	// With no shunt known there is nothing to footnote and no row to show.
	bare := powerRows(&Power{Name: "hwmon6", BusV: f(5), CurrentA: f(1), WattsW: f(5)})
	if find(bare, "shunt") != "" {
		t.Errorf("bare powerRows invented a shunt: %v", bare)
	}
}

func TestEstimateRows(t *testing.T) {
	if got := estimateRows(nil); got != nil {
		t.Errorf("estimateRows(nil) = %v", got)
	}
	// An absent estimate still prints, because silence is indistinguishable from the feature not
	// existing, whereas the note tells a reader to leave it running.
	rows := estimateRows(&estimate.Estimate{State: estimate.Unknown, Note: "not enough history yet"})
	if find(rows, "note") == "" {
		t.Error("an estimate with no duration must still say why")
	}
	if find(rows, "remaining") != "" {
		t.Error("no duration should be rendered when there is none")
	}
}

// find returns the value of a row by key, or "" when absent.
func find(rows []row, key string) string {
	for _, r := range rows {
		if r.key == key {
			return r.value
		}
	}
	return ""
}

func TestJSON(t *testing.T) {
	out, err := JSON(sample())
	if err != nil {
		t.Fatal(err)
	}
	var back Reading
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("JSON did not round-trip: %v", err)
	}
	if back.Battery == nil || back.Battery.Percent == nil || *back.Battery.Percent != 95 {
		t.Errorf("round-trip lost the percentage: %+v", back.Battery)
	}
}

func TestJSONOmitsAbsentValuesRatherThanZeroingThem(t *testing.T) {
	// The distinction this program exists to preserve, at the output boundary. A consumer seeing no
	// "percent" key knows the gauge cannot report one; "percent": 0 would mean the pack is flat.
	out, err := JSON(&Reading{Battery: &Battery{Name: "battery", Status: "Unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "percent") {
		t.Errorf("absent percentage emitted a key:\n%s", out)
	}
	if !strings.Contains(out, `"status"`) {
		t.Errorf("status should always be present:\n%s", out)
	}
}

func TestJSONNil(t *testing.T) {
	if _, err := JSON(nil); !errors.Is(err, ErrNoDevices) {
		t.Errorf("JSON(nil) err = %v; want ErrNoDevices", err)
	}
}
