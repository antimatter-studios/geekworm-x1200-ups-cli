package ups

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
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
	for _, want := range []string{"battery", "95%", "4.152 V", "unknown", "power", "5.06 V", "0.267 A", "1.34 W", "shunt 10.000 mOhm", "drop 3 mV", "ina219"} {
		if !strings.Contains(got, want) {
			t.Errorf("Text missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("Text did not end with a newline")
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

func TestBatteryLine(t *testing.T) {
	if got := batteryLine(nil); got != "" {
		t.Errorf("batteryLine(nil) = %q", got)
	}
	// A gauge that cannot report a percentage must not render as 0%.
	got := batteryLine(&Battery{Name: "battery", Status: "Unknown"})
	if strings.Contains(got, "0%") || !strings.Contains(got, "?%") {
		t.Errorf("absent percentage rendered as %q", got)
	}
	// A flat pack must render as 0%, not as unknown.
	if got := batteryLine(&Battery{Percent: i(0), Status: "Discharging"}); !strings.Contains(got, "0%") {
		t.Errorf("zero percent rendered as %q", got)
	}
	if got := batteryLine(&Battery{Percent: i(50), Status: "Unknown", Present: b(false)}); !strings.Contains(got, "NO PACK FITTED") {
		t.Errorf("absent pack not flagged: %q", got)
	}
	if got := batteryLine(&Battery{Percent: i(50), Status: "Unknown", Present: b(true)}); strings.Contains(got, "NO PACK") {
		t.Errorf("present pack wrongly flagged: %q", got)
	}
}

func TestPowerLines(t *testing.T) {
	if got := powerLines(nil); got != nil {
		t.Errorf("powerLines(nil) = %v", got)
	}
	lines := powerLines(sample().Power)
	if len(lines) != 2 {
		t.Fatalf("powerLines = %d lines; want 2 (rail, then the shunt it came from)", len(lines))
	}
	// With no shunt detail there is nothing to footnote, so the second line must not appear empty.
	bare := powerLines(&Power{Name: "hwmon6", BusV: f(5), CurrentA: f(1), WattsW: f(5)})
	if len(bare) != 1 {
		t.Errorf("bare powerLines = %v; want a single line", bare)
	}
}

func TestLabel(t *testing.T) {
	if got := label("battery"); len(got) != 10 {
		t.Errorf("label(%q) = %q, width %d; want 10", "battery", got, len(got))
	}
	if got := label(""); len(got) != 10 {
		t.Errorf("label(\"\") width %d; want 10", len(got))
	}
	// Longer than the column still gets a separator rather than running into the value.
	if got := label("aVeryLongLabel"); !strings.HasSuffix(got, " ") {
		t.Errorf("label(long) = %q; want a trailing space", got)
	}
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
