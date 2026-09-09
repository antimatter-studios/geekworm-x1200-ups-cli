package coulomb

import (
	"math"
	"testing"
	"time"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/history"
)

var base = time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)

func at(secs int, amps, watts float64) history.Sample {
	return history.Sample{
		At:       base.Add(time.Duration(secs) * time.Second),
		Percent:  90,
		VoltageV: 4.0,
		CurrentA: amps,
		WattsW:   watts,
	}
}

func close(t *testing.T, label string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.4f, want %.4f (±%.4f)", label, got, want, tol)
	}
}

// One amp for one hour is one amp-hour, which is the arithmetic everything else here rests on.
func TestIntegrateAConstantCurrent(t *testing.T) {
	var samples []history.Sample
	for i := 0; i <= 60; i++ {
		samples = append(samples, at(i*60, 1.0, 5.0)) // one sample a minute for an hour
	}
	got := Integrate(samples)

	close(t, "mAh", got.MilliampHours, 1000, 1)
	close(t, "Wh", got.WattHours, 5, 0.01)
	close(t, "mean current", got.MeanCurrentA, 1.0, 0.001)
	if got.Skipped != 0 {
		t.Errorf("skipped %d intervals in a regular series", got.Skipped)
	}
	if got.Covered != time.Hour {
		t.Errorf("covered = %v, want 1h", got.Covered)
	}
}

// Trapezoidal, not rectangular: current on this board ramps with CPU load rather than stepping, so
// a ramp from 0 to 2 A over an hour must average 1 A rather than picking an endpoint.
func TestIntegrateARamp(t *testing.T) {
	var samples []history.Sample
	for i := 0; i <= 60; i++ {
		amps := 2.0 * float64(i) / 60
		samples = append(samples, at(i*60, amps, amps*5))
	}
	close(t, "mAh over a 0→2A ramp", Integrate(samples).MilliampHours, 1000, 5)
}

// The failure this package's MaxGap exists to prevent. One reading an hour after another would
// otherwise contribute charge nobody measured — the tool runs on demand, so gaps are normal.
func TestIntegrateRefusesToSpanALongGap(t *testing.T) {
	samples := []history.Sample{
		at(0, 0.5, 2.5),
		at(60, 0.5, 2.5),
		at(3660, 0.5, 2.5), // an hour later
		at(3720, 0.5, 2.5),
	}
	got := Integrate(samples)

	if got.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", got.Skipped)
	}
	// Two one-minute intervals at half an amp: about 16.7 mAh. Spanning the hour would give ~510.
	close(t, "mAh", got.MilliampHours, 16.7, 1)
	if got.Covered != 2*time.Minute {
		t.Errorf("covered = %v, want 2m", got.Covered)
	}
	// The span still reports the full wall clock, so the difference between the two is visible.
	if got.Span < time.Hour {
		t.Errorf("span = %v, want the full elapsed time", got.Span)
	}
}

// The mean must divide by observed time, not elapsed time: dividing by the span would understate it
// by whatever fraction nobody was watching.
func TestMeanCurrentUsesCoveredTimeNotSpan(t *testing.T) {
	samples := []history.Sample{
		at(0, 1.0, 5.0),
		at(60, 1.0, 5.0),
		at(7260, 1.0, 5.0), // two hours later
		at(7320, 1.0, 5.0),
	}
	close(t, "mean current", Integrate(samples).MeanCurrentA, 1.0, 0.01)
}

func TestIntegrateNeedsTwoSamples(t *testing.T) {
	if got := Integrate(nil); got.MilliampHours != 0 || got.Covered != 0 {
		t.Errorf("nil = %+v, want zero", got)
	}
	if got := Integrate([]history.Sample{at(0, 1, 5)}); got.MilliampHours != 0 {
		t.Error("a single sample produced charge")
	}
}

// A clock that went backwards must not subtract charge.
func TestIntegrateIgnoresNonAdvancingTime(t *testing.T) {
	samples := []history.Sample{at(0, 1, 5), at(0, 1, 5), at(60, 1, 5)}
	got := Integrate(samples)
	if got.MilliampHours < 0 {
		t.Errorf("mAh = %.4f, want no negative contribution", got.MilliampHours)
	}
	close(t, "mAh", got.MilliampHours, 16.7, 1)
}

// Runtime rests on a declared capacity, so it must refuse rather than invent when it has none.
func TestRuntimeNeedsADeclaredCapacity(t *testing.T) {
	c := Integrate([]history.Sample{at(0, 1.0, 5.0), at(60, 1.0, 5.0)})

	if _, ok := Runtime(c, 0); ok {
		t.Error("produced a runtime with no declared capacity")
	}
	if _, ok := Runtime(c, -1); ok {
		t.Error("produced a runtime from a negative capacity")
	}
	// 3000 mAh at 1 A is three hours.
	got, ok := Runtime(c, 3000)
	if !ok {
		t.Fatal("no runtime from a valid capacity")
	}
	close(t, "runtime hours", got.Hours(), 3, 0.01)
}

func TestRuntimeNeedsAMeasuredCurrent(t *testing.T) {
	idle := Integrate([]history.Sample{at(0, 0, 0), at(60, 0, 0)})
	if _, ok := Runtime(idle, 3000); ok {
		t.Error("produced a runtime from zero current, which would be infinite")
	}
}

// Two independent ways to be unusable, needing different words: a real but tiny sample, versus a
// total that looks like it covers hours but describes minutes.
func TestConfidence(t *testing.T) {
	brief := Integrate([]history.Sample{at(0, 1, 5), at(10, 1, 5)})
	note, usable := Confidence(brief)
	if usable || note == "" {
		t.Errorf("ten seconds judged usable: %q, %v", note, usable)
	}

	var solid []history.Sample
	for i := 0; i <= 30; i++ {
		solid = append(solid, at(i*60, 1, 5))
	}
	if note, usable := Confidence(Integrate(solid)); !usable || note != "" {
		t.Errorf("half an hour of regular samples: %q, %v; want usable and unqualified", note, usable)
	}

	// Usable but badly covered: the numbers are real, the period they appear to cover is not.
	patchy := []history.Sample{
		at(0, 1, 5), at(60, 1, 5), at(120, 1, 5),
		at(7200, 1, 5), at(7260, 1, 5),
	}
	note, usable = Confidence(Integrate(patchy))
	if !usable {
		t.Error("patchy coverage judged unusable; the figures are still real")
	}
	if note == "" {
		t.Error("patchy coverage carried no caveat")
	}
}
