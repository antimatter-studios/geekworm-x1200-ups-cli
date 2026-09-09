// Package calibrate fits the shunt resistance from a second, independent power measurement.
//
// The shunt is the one constant that silently scales everything. Current, power, mAh and Wh are all
// derived from a voltage drop divided by this resistance, so a wrong value is wrong everywhere by
// the same factor and nothing in the output contradicts it. The ina2xx driver defaults to 10 mOhm,
// and on this board that is not merely unverified but provably wrong: it implies 1.34 W entering a
// board whose own PMIC reports 2.73 W delivered to rails downstream of it, and input cannot be less
// than what it feeds.
//
// The Pi's PMIC is the independent measurement. It reports per-rail voltages and currents through
// firmware, entirely separately from the I2C bus, so the two can be compared: the INA219 says how
// much voltage is dropped across an unknown resistance, the PMIC says how much power is being
// consumed downstream of it, and the ratio gives the resistance.
//
// # Why this reports rather than applies
//
// Writing the fitted value into sysfs would be a state change on a machine described declaratively
// elsewhere, and a tool that reconfigures the machine behind that description is how a system stops
// matching what it says it is. So this prints something an operator can paste, and something else
// decides.
//
// # The systematic error, stated rather than hidden
//
// The PMIC measures power delivered to rails *downstream* of the shunt, so input power is that
// figure divided by the conversion efficiency, which is below one. Treating the PMIC total as input
// power therefore biases the fitted resistance high, by exactly the reciprocal of the efficiency —
// perhaps ten percent for a switching converter of this class.
//
// That bias cannot be removed without knowing the efficiency, which nothing here can measure. What
// it can do is fit across several load levels rather than one: the slope of drop against power is
// insensitive to any fixed offset, such as the HAT's own quiescent consumption, even though it
// remains sensitive to the scaling. And it can report the spread, so an operator can see whether the
// points lie on a line at all.
//
// A single point is not a calibration. Two sensors agreeing once can agree by accident.
package calibrate

import (
	"fmt"
	"math"
	"sort"
)

// Point is one simultaneous observation of both instruments.
type Point struct {
	// ShuntMV is the INA219's measured drop across the unknown resistance, in millivolts. This is
	// the only quantity the INA219 truly measures; its current and power are derived from it.
	ShuntMV float64
	// BusV is the rail voltage the shunt feeds.
	BusV float64
	// PMICWatts is the Pi's own total across its rails, from a source that shares no hardware with
	// the INA219 — which is what makes the comparison worth anything.
	PMICWatts float64
}

// currentA is the current the PMIC observation implies, given the rail voltage.
//
// Using the bus voltage rather than a nominal 5 V matters under load: the rail was measured sagging
// from 5.072 V to 4.952 V between idle and four busy cores, and treating that as constant would put
// the implied current out by the same couple of percent that the fit is trying to resolve.
func (p Point) currentA() float64 {
	if p.BusV <= 0 {
		return 0
	}
	return p.PMICWatts / p.BusV
}

// Result is the outcome of a calibration.
type Result struct {
	// OhmsFromSlope is the headline figure: the resistance implied by how the drop grows with
	// current across the sampled load range. Preferred over averaging per-point values because it
	// is unaffected by any constant offset, such as the board's own quiescent draw.
	OhmsFromSlope float64
	// OhmsFromMean is the resistance implied by treating each point independently and averaging.
	// Reported alongside because agreement between the two is evidence the model fits, and
	// disagreement is evidence of an offset the slope has absorbed and the mean has not.
	OhmsFromMean float64
	// Points is how many observations went in. Fewer than three is not a calibration.
	Points int
	// SpreadPercent is the relative spread of the per-point resistances: the range divided by the
	// mean. It is the honest confidence signal — two instruments that agree at every load level
	// produce a tight spread, and one that disagrees produces a wide one.
	SpreadPercent float64
	// CurrentRangeA is the span of implied current the fit covers. A calibration taken entirely at
	// idle constrains the slope barely at all, however many points it contains.
	CurrentRangeA float64
	// Note qualifies the fit; empty when nothing needs saying.
	Note string
}

// ErrTooFewPoints is returned when there is not enough to fit.
type ErrTooFewPoints struct{ Got int }

func (e ErrTooFewPoints) Error() string {
	return fmt.Sprintf("calibrate: %d points is not a calibration; two sensors can agree once by accident, so at least 3 are needed across different load levels", e.Got)
}

// EfficiencyNote is the systematic bias, in words, for printing beside any fitted value.
const EfficiencyNote = "the PMIC measures power downstream of the shunt, so input power is higher by the converter's efficiency; this fit is therefore biased high by roughly that factor (perhaps 5-10%)"

// Fit fits the resistance from the observations.
func Fit(points []Point) (Result, error) {
	usable := make([]Point, 0, len(points))
	for _, p := range points {
		// A point with no drop or no power constrains nothing and would divide by zero.
		if p.ShuntMV > 0 && p.PMICWatts > 0 && p.BusV > 0 {
			usable = append(usable, p)
		}
	}
	if len(usable) < 3 {
		return Result{}, ErrTooFewPoints{Got: len(usable)}
	}

	// Per-point resistances: R = V_drop / I.
	ohms := make([]float64, 0, len(usable))
	currents := make([]float64, 0, len(usable))
	for _, p := range usable {
		i := p.currentA()
		if i <= 0 {
			continue
		}
		ohms = append(ohms, (p.ShuntMV/1000)/i)
		currents = append(currents, i)
	}
	if len(ohms) < 3 {
		return Result{}, ErrTooFewPoints{Got: len(ohms)}
	}

	out := Result{Points: len(ohms)}

	var sum float64
	for _, o := range ohms {
		sum += o
	}
	out.OhmsFromMean = sum / float64(len(ohms))

	sorted := append([]float64(nil), ohms...)
	sort.Float64s(sorted)
	if out.OhmsFromMean > 0 {
		out.SpreadPercent = (sorted[len(sorted)-1] - sorted[0]) / out.OhmsFromMean * 100
	}

	// Least squares slope of drop (volts) against current (amps), forced through no particular
	// intercept: the intercept absorbs any fixed offset, which is precisely what makes the slope the
	// better estimator of the resistance itself.
	var sx, sy, sxx, sxy float64
	n := float64(len(ohms))
	for i := range ohms {
		x := currents[i]
		y := usable[i].ShuntMV / 1000
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	denom := n*sxx - sx*sx
	if denom != 0 {
		out.OhmsFromSlope = (n*sxy - sx*sy) / denom
	} else {
		// Every point at the same current: the slope is undefined and the mean is all there is.
		out.OhmsFromSlope = out.OhmsFromMean
	}

	sortedCurrents := append([]float64(nil), currents...)
	sort.Float64s(sortedCurrents)
	out.CurrentRangeA = sortedCurrents[len(sortedCurrents)-1] - sortedCurrents[0]

	out.Note = judge(out)
	return out, nil
}

// judge produces the caveat that belongs with a fit.
//
// Ordered by severity, and only one is shown: a reader who is told three things at once acts on
// none of them. A negative or absurd slope is a broken measurement and outranks everything; too
// narrow a load range makes the slope meaningless however tidy it looks; a wide spread means the
// instruments disagree.
func judge(f Result) string {
	switch {
	case f.OhmsFromSlope <= 0:
		return "the fitted slope is not positive, which means the drop did not grow with load; the measurements do not describe a resistor and the fit should not be used"
	case f.CurrentRangeA < 0.1:
		return "every sample was taken at nearly the same load, so the slope is barely constrained; repeat with the CPU busy as well as idle"
	case f.SpreadPercent > 25:
		return "the two instruments disagree by more than a quarter across the range; treat this as indicative only"
	case f.SpreadPercent > 10:
		return "some disagreement between the instruments; the slope is the figure to prefer"
	default:
		return ""
	}
}

// Plausible reports whether a fitted resistance is a value a real board might carry.
//
// Sense resistors come in a narrow range of preferred values, and a 5 A board cannot use a large
// one: 0.1 Ohm at 5 A would drop half a volt and dissipate two and a half watts, which is both a
// broken rail and a fire risk. Anything outside a decade either side of the plausible band is a
// measurement failure rather than a surprising board.
func Plausible(ohms float64) bool {
	const min, max = 0.0005, 0.05
	return ohms >= min && ohms <= max
}

// Nearest returns the closest preferred sense-resistor value, and how far off the fit was.
//
// Offered because the true value is a manufactured part with a printed tolerance, not an arbitrary
// real number: a fit landing near a standard value is evidence the calibration worked, and the
// standard value is the better thing to configure. Reported rather than substituted, though — an
// operator who wanted the raw figure should not have it silently rounded.
func Nearest(ohms float64) (value, offByPercent float64) {
	preferred := []float64{0.001, 0.002, 0.0025, 0.003, 0.005, 0.01, 0.015, 0.02, 0.025, 0.05}
	best, bestErr := 0.0, math.Inf(1)
	for _, p := range preferred {
		if e := math.Abs(p-ohms) / p; e < bestErr {
			best, bestErr = p, e
		}
	}
	return best, bestErr * 100
}
