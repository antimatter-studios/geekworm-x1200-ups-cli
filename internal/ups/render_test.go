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
	if got := batteryRows(nil, nil, nil); got != nil {
		t.Errorf("batteryRows(nil) = %v", got)
	}
	// A gauge that cannot report a percentage must not render as 0%: an unreadable gauge and a flat
	// pack are opposite situations and only one is an emergency.
	got := find(batteryRows(&Battery{Name: "battery", Status: "Unknown"}, nil, nil), "charge")
	if strings.Contains(got, "0%") {
		t.Errorf("absent percentage rendered as %q", got)
	}
	if got := find(batteryRows(&Battery{Percent: i(0), Status: "Discharging"}, nil, nil), "charge"); got != "0%" {
		t.Errorf("zero percent rendered as %q; want 0%%", got)
	}
	if got := find(batteryRows(&Battery{Percent: i(50), Status: "Unknown", Present: b(false)}, nil, nil), "pack"); got != "NOT FITTED" {
		t.Errorf("absent pack not flagged: %q", got)
	}
	if got := find(batteryRows(&Battery{Percent: i(50), Status: "Unknown", Present: b(true)}, nil, nil), "pack"); got != "" {
		t.Errorf("present pack wrongly flagged: %q", got)
	}
}

// The gauge reads "unknown" forever on this hardware, so a bare "unknown" is the least useful thing
// the tool could print. With no inference available it must at least explain itself.
func TestBatteryStateExplainsAnUnknownGauge(t *testing.T) {
	got := find(batteryRows(&Battery{Percent: i(80), Status: "Unknown"}, nil, nil), "state")
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
	got := find(batteryRows(&Battery{Percent: i(80), Status: "Unknown"}, e, nil), "state")
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
	got := find(batteryRows(&Battery{Percent: i(80), Status: "Unknown"}, e, nil), "state")
	if !strings.HasPrefix(got, "unknown") {
		t.Errorf("state = %q; steady must not be read as a direction", got)
	}
}

// A real status from the kernel is passed through untouched: the explanation is only for the
// permanent "unknown" this hardware produces.
func TestBatteryStatePassesARealStatusThrough(t *testing.T) {
	got := find(batteryRows(&Battery{Percent: i(80), Status: "Discharging"}, nil, nil), "state")
	if got != "discharging" {
		t.Errorf("state = %q, want the kernel's own word unadorned", got)
	}
}

func TestPowerRows(t *testing.T) {
	if got := powerRows(nil); got != nil {
		t.Errorf("powerRows(nil) = %v", got)
	}
	// The fixture carries the driver's default shunt, so it also gets the calibration warning.
	rows := powerRows(sample().Power)
	if len(rows) != 5 {
		t.Fatalf("powerRows = %d; want bus, current, draw, shunt, UNCALIBRATED", len(rows))
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

// GPIO6 is measured where the inference is guessed, so it wins. This is the whole point of reading
// the pin: an inference needs minutes of history and cannot tell a full pack on mains from one on
// battery that has not yet lost a percent.
func TestBatteryStatePrefersMeasuredSupplyOverInference(t *testing.T) {
	rate := -20.0
	discharging := &estimate.Estimate{State: estimate.Discharging, PercentPerHour: &rate}

	onMains := find(batteryRows(&Battery{Percent: i(80), Status: "Unknown"}, discharging, &Supply{OnMains: true, Line: 6}), "state")
	if !strings.Contains(onMains, "mains") {
		t.Errorf("state = %q; the measured pin must win over the inference", onMains)
	}
	if strings.Contains(onMains, "inferred") {
		t.Errorf("state = %q; a measured reading must not be labelled inferred", onMains)
	}

	onBattery := find(batteryRows(&Battery{Percent: i(80), Status: "Unknown"}, nil, &Supply{OnMains: false, Line: 6}), "state")
	if !strings.Contains(onBattery, "battery") {
		t.Errorf("state = %q; want it to say the mains is gone", onBattery)
	}
}

func TestSupplyRows(t *testing.T) {
	if got := supplyRows(nil, nil); len(got) != 0 {
		t.Errorf("supplyRows(nil, nil) = %v; want nothing to render", got)
	}

	lost := supplyRows(&Supply{OnMains: false, Line: 6, Caveat: "pull-up"}, nil)
	if src := find(lost, "source"); !strings.Contains(src, "MAINS LOST") || !strings.Contains(src, "GPIO6") {
		t.Errorf("source = %q; want the loss called out and the pin named", src)
	}
	// The caveat qualifies a positive reading only: a stuck-high pin reads as healthy, which says
	// nothing about a reading that is already low.
	if find(lost, "caveat") != "" {
		t.Error("caveat shown while on battery, where it does not apply")
	}
	if c := find(supplyRows(&Supply{OnMains: true, Line: 6, Caveat: "pull-up"}, nil), "caveat"); c == "" {
		t.Error("no caveat shown on mains, where a stuck pin would look identical")
	}

	// Charging is active-low on this board, so the rendering must not leak the raw level.
	on := find(supplyRows(nil, &Charging{Enabled: true, Line: 16}), "charging")
	if !strings.Contains(on, "enabled") || !strings.Contains(on, "GPIO16") {
		t.Errorf("charging = %q", on)
	}
	if off := find(supplyRows(nil, &Charging{Enabled: false, Line: 16}), "charging"); !strings.Contains(off, "disabled") {
		t.Errorf("charging = %q", off)
	}
}

// The shunt warning is the difference between a reader believing the wattage and knowing it is
// roughly half what it should be. Every current, power, mAh and Wh figure scales by this constant.
func TestPowerRowsWarnsWhenTheShuntIsTheDriverDefault(t *testing.T) {
	warned := find(powerRows(sample().Power), "UNCALIBRATED")
	if warned == "" {
		t.Fatal("no warning at the ina2xx default of 0.01 ohm")
	}
	if !strings.Contains(warned, "calibrate") {
		t.Errorf("warning = %q; want it to name the way out", warned)
	}

	// A calibrated shunt must not warn, or the warning becomes noise a reader learns to skip.
	calibrated := *sample().Power
	calibrated.ShuntOhms = f(0.005)
	if got := find(powerRows(&calibrated), "UNCALIBRATED"); got != "" {
		t.Errorf("warned about a calibrated shunt: %q", got)
	}

	// No shunt reading at all is not the same as an uncalibrated one, and must not warn either.
	unknown := *sample().Power
	unknown.ShuntOhms = nil
	if got := find(powerRows(&unknown), "UNCALIBRATED"); got != "" {
		t.Errorf("warned with no shunt value to judge: %q", got)
	}
}

// Charge measured and charge modelled must never look like the same kind of claim: the gauge was
// observed swinging eleven points in sixty seconds with no charge movement behind it.
func TestDeliveredRowsAreSeparateFromTheGauge(t *testing.T) {
	if got := deliveredRows(nil); got != nil {
		t.Errorf("deliveredRows(nil) = %v", got)
	}
	d := &Delivered{MilliampHours: 123.4, WattHours: 0.62, MeanCurrentA: 0.52, CoveredS: 600, SpanS: 600}
	rows := deliveredRows(d)
	if find(rows, "charge") != "123.4 mAh" {
		t.Errorf("charge = %q", find(rows, "charge"))
	}
	if find(rows, "energy") == "" || find(rows, "mean current") == "" {
		t.Errorf("rows = %+v", rows)
	}
	// Both times always, so a reader can see whether the total covers the period it appears to.
	over := find(rows, "measured over")
	if !strings.Contains(over, "elapsed") {
		t.Errorf("measured over = %q; want covered and elapsed together", over)
	}
}

// A runtime from a declared capacity must say it is declared. The label on a cell is not a
// measurement, and 18650s sold at 5000 mAh are commonly overstated two- or threefold.
func TestDeliveredRuntimeSaysItRestsOnADeclaration(t *testing.T) {
	runtime, capacity := 7200.0, 3000.0
	rows := deliveredRows(&Delivered{
		MilliampHours: 100, MeanCurrentA: 0.5, CoveredS: 600, SpanS: 600,
		RuntimeS: &runtime, CapacityMAh: &capacity,
	})
	got := find(rows, "runtime")
	if got == "" {
		t.Fatal("no runtime rendered when one was computed")
	}
	if !strings.Contains(got, "declared") || !strings.Contains(got, "NOT measured") {
		t.Errorf("runtime = %q; want it to say plainly that it rests on a declared capacity", got)
	}
	if !strings.Contains(got, "3000") {
		t.Errorf("runtime = %q; want the capacity it used echoed", got)
	}

	// With no declared capacity there is no runtime to show, and inventing one would be worse.
	bare := deliveredRows(&Delivered{MilliampHours: 100, MeanCurrentA: 0.5, CoveredS: 600, SpanS: 600})
	if find(bare, "runtime") != "" {
		t.Error("produced a runtime with no declared capacity")
	}
}

// Integrating a current only means something if you know which circuit it flows through, and on this
// board that is not established: the INA219 read 525 mA idle and 267 mA at full load, and was flat
// across a mains transition. A confident mAh figure from an unidentified signal is worse than none,
// because it looks like a measurement.
func TestDeliveredWithholdsTotalsWhileTheCurrentIsUnidentified(t *testing.T) {
	implied, runtime, capacity := 2600.0, 7200.0, 6000.0
	d := &Delivered{
		MilliampHours: 123.4, WattHours: 0.62, MeanCurrentA: 0.52, CoveredS: 600, SpanS: 600,
		ImpliedCapacityMAh: &implied, RuntimeS: &runtime, CapacityMAh: &capacity,
		Unverified: "the circuit is not established",
	}
	rows := deliveredRows(d)

	for _, withheld := range []string{"charge", "energy", "implied capacity", "runtime"} {
		if got := find(rows, withheld); got != "" {
			t.Errorf("%s = %q; must be withheld while the signal is unidentified", withheld, got)
		}
	}
	// The mean and the coverage are facts about the signal rather than interpretations of it, so
	// they stand: withholding them would hide the evidence that something is being measured at all.
	if find(rows, "mean current") == "" {
		t.Error("the mean current was withheld; it is a fact about the signal, not an interpretation")
	}
	if find(rows, "measured over") == "" {
		t.Error("coverage was withheld")
	}
	// The absence needs its reason attached, since a reader expects these figures to be present.
	if find(rows, "WITHHELD") == "" {
		t.Error("totals vanished with no explanation")
	}
}

// Once an operator asserts they know what the signal is, everything reports as before.
func TestDeliveredReportsEverythingOnceTrusted(t *testing.T) {
	implied := 2600.0
	d := &Delivered{
		MilliampHours: 123.4, WattHours: 0.62, MeanCurrentA: 0.52, CoveredS: 600, SpanS: 600,
		ImpliedCapacityMAh: &implied,
	}
	rows := deliveredRows(d)
	for _, want := range []string{"charge", "energy", "implied capacity"} {
		if find(rows, want) == "" {
			t.Errorf("%s missing with no Unverified set", want)
		}
	}
	if find(rows, "WITHHELD") != "" {
		t.Error("a trusted reading still carried a withholding notice")
	}
}
