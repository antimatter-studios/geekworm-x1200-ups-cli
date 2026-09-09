package calibrate

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// synth builds observations from a known resistance, so a fit can be checked against the truth.
//
// The drop is what a real shunt would produce: current times resistance. The PMIC figure is derived
// from the same current, which is what makes this a test of the arithmetic rather than of the
// hardware.
func synth(ohms float64, busV float64, currents ...float64) []Point {
	var out []Point
	for _, i := range currents {
		out = append(out, Point{
			ShuntMV:   i * ohms * 1000,
			BusV:      busV,
			PMICWatts: i * busV,
		})
	}
	return out
}

func TestFitRecoversAKnownResistance(t *testing.T) {
	got, err := Fit(synth(0.005, 5.05, 0.3, 0.5, 0.8, 1.2))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.OhmsFromSlope-0.005) > 1e-6 {
		t.Errorf("slope = %.6f ohm, want 0.005", got.OhmsFromSlope)
	}
	if math.Abs(got.OhmsFromMean-0.005) > 1e-6 {
		t.Errorf("mean = %.6f ohm, want 0.005", got.OhmsFromMean)
	}
	if got.Note != "" {
		t.Errorf("note = %q, want none for a clean fit", got.Note)
	}
}

// A single point is not a calibration: two instruments can agree once by accident.
func TestFitRefusesTooFewPoints(t *testing.T) {
	for _, n := range []int{0, 1, 2} {
		currents := make([]float64, n)
		for i := range currents {
			currents[i] = 0.5
		}
		_, err := Fit(synth(0.005, 5.05, currents...))
		var few ErrTooFewPoints
		if !errors.As(err, &few) {
			t.Errorf("%d points: err = %v, want ErrTooFewPoints", n, err)
		}
	}
	if err := (ErrTooFewPoints{Got: 1}).Error(); !strings.Contains(err, "accident") {
		t.Errorf("error text should say why one point is not enough: %q", err)
	}
}

// The slope's advantage over the mean, and the reason it is the headline figure: a fixed offset —
// the HAT's own quiescent draw, say — shifts every point equally, which the intercept absorbs and
// an average does not.
func TestSlopeIsUnaffectedByAFixedOffset(t *testing.T) {
	clean := synth(0.005, 5.05, 0.3, 0.5, 0.8, 1.2)
	offset := append([]Point(nil), clean...)
	for i := range offset {
		offset[i].ShuntMV += 0.4 // a constant extra drop, present at every load
	}

	fit, err := Fit(offset)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(fit.OhmsFromSlope-0.005) > 2e-4 {
		t.Errorf("slope = %.6f with an offset present, want about 0.005", fit.OhmsFromSlope)
	}
	// The mean is dragged by the same offset, which is why both are reported: their disagreement is
	// itself the evidence that an offset exists.
	if math.Abs(fit.OhmsFromMean-0.005) < 2e-4 {
		t.Error("the mean was expected to be dragged by the offset; if it is not, the test is not testing anything")
	}
}

// A calibration taken entirely at idle constrains the slope barely at all, however tidy it looks.
func TestFitWarnsWhenEveryPointIsAtTheSameLoad(t *testing.T) {
	got, err := Fit(synth(0.005, 5.05, 0.50, 0.51, 0.52, 0.50))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Note, "same load") {
		t.Errorf("note = %q, want it to say the load range is too narrow", got.Note)
	}
}

// Instruments that disagree across the range must say so rather than average into a tidy lie.
func TestFitWarnsWhenTheInstrumentsDisagree(t *testing.T) {
	points := synth(0.005, 5.05, 0.3, 0.6, 0.9, 1.2)
	points[1].ShuntMV *= 1.6 // one badly inconsistent reading
	points[3].ShuntMV *= 0.6

	got, err := Fit(points)
	if err != nil {
		t.Fatal(err)
	}
	if got.SpreadPercent < 25 {
		t.Fatalf("spread = %.1f%%, expected the injected disagreement to show", got.SpreadPercent)
	}
	if got.Note == "" {
		t.Error("wide disagreement carried no caveat")
	}
}

// A drop that does not grow with load is not a resistor, and no figure should be offered for it.
func TestFitRejectsANonPositiveSlope(t *testing.T) {
	points := []Point{
		{ShuntMV: 5, BusV: 5.05, PMICWatts: 1.0},
		{ShuntMV: 3, BusV: 5.05, PMICWatts: 3.0},
		{ShuntMV: 1, BusV: 5.05, PMICWatts: 5.0},
	}
	got, err := Fit(points)
	if err != nil {
		t.Fatal(err)
	}
	if got.OhmsFromSlope > 0 {
		t.Fatalf("slope = %v, expected non-positive from an inverted relationship", got.OhmsFromSlope)
	}
	if !strings.Contains(got.Note, "should not be used") {
		t.Errorf("note = %q, want it to refuse the fit outright", got.Note)
	}
}

// Zero and negative readings constrain nothing and would divide by zero.
func TestFitDiscardsUnusablePoints(t *testing.T) {
	points := append(synth(0.005, 5.05, 0.3, 0.5, 0.8),
		Point{ShuntMV: 0, BusV: 5.05, PMICWatts: 2},
		Point{ShuntMV: 3, BusV: 0, PMICWatts: 2},
		Point{ShuntMV: 3, BusV: 5.05, PMICWatts: 0},
	)
	got, err := Fit(points)
	if err != nil {
		t.Fatal(err)
	}
	if got.Points != 3 {
		t.Errorf("points = %d, want the 3 usable ones", got.Points)
	}
}

// The rail sags under load — measured from 5.072 V to 4.952 V between idle and four busy cores — so
// the implied current must use the measured bus voltage rather than a nominal 5 V.
func TestImpliedCurrentUsesTheMeasuredRail(t *testing.T) {
	sagging := Point{ShuntMV: 5, BusV: 4.952, PMICWatts: 2.73}
	nominal := Point{ShuntMV: 5, BusV: 5.0, PMICWatts: 2.73}
	if sagging.currentA() <= nominal.currentA() {
		t.Error("a sagging rail must imply more current for the same power, not less")
	}
	if got := (Point{BusV: 0, PMICWatts: 2}).currentA(); got != 0 {
		t.Errorf("currentA with no rail voltage = %v, want 0 rather than a division by zero", got)
	}
}

// A 5 A board cannot carry a large sense resistor: 0.1 ohm at 5 A drops half a volt and burns 2.5 W.
func TestPlausible(t *testing.T) {
	for _, ohms := range []float64{0.001, 0.005, 0.01, 0.02} {
		if !Plausible(ohms) {
			t.Errorf("%v judged implausible; it is a normal sense resistor", ohms)
		}
	}
	for _, ohms := range []float64{0, -0.005, 0.1, 1, 1e-9} {
		if Plausible(ohms) {
			t.Errorf("%v judged plausible; it is a measurement failure", ohms)
		}
	}
}

// A fit landing near a manufactured value is evidence the calibration worked, since the true value
// is a real part with a printed tolerance rather than an arbitrary number.
func TestNearestPreferredValue(t *testing.T) {
	value, off := Nearest(0.0052)
	if value != 0.005 {
		t.Errorf("nearest to 0.0052 = %v, want 0.005", value)
	}
	if off > 5 {
		t.Errorf("off by %.1f%%, want a small figure for a near miss", off)
	}
	if value, _ := Nearest(0.0098); value != 0.01 {
		t.Errorf("nearest to 0.0098 = %v, want 0.01", value)
	}
}

// The systematic bias must be stated wherever a value is printed, because it cannot be removed.
func TestEfficiencyNoteNamesTheDirection(t *testing.T) {
	if !strings.Contains(EfficiencyNote, "biased high") {
		t.Errorf("EfficiencyNote does not say which way the bias runs: %q", EfficiencyNote)
	}
}
