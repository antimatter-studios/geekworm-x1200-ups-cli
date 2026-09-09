// Package coulomb integrates measured current into charge actually delivered.
//
// This exists because the fuel gauge cannot be believed in the short term, and that is a measured
// finding rather than a suspicion. Across one observed unplug the pack read:
//
//	4.168 V  97%   on mains
//	4.061 V  85%   on battery, about five minutes in
//	4.204 V  96%   back on mains, sixty seconds later
//
// Eleven percent "recovered" in a minute. Nothing charges that fast: a multi-thousand-mAh pack at
// the fraction of an amp this board draws would take hours. What happened is that the cell sagged
// under load, the MAX17040's voltage model read the sag as depletion, and it sprang back when the
// load came off. Real charge moved was well under one percent.
//
// So the percentage is trustworthy at rest, unreliable for minutes after any load change, and
// worthless as a short-term rate. The INA219 measures current instead of modelling it, and
// integrating current over time gives charge — the quantity the gauge is only guessing at.
//
// Everything here is pure. The caller supplies the samples.
package coulomb

import (
	"time"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/history"
)

// MaxGap is the longest interval between two samples that may be integrated across.
//
// This is the whole difficulty of integrating a series nobody promised to sample regularly. The tool
// runs on demand: twice a second under `--watch 500ms`, or once when somebody types it and then not
// again for an hour. Trapezoidal integration assumes the current between two samples resembles the
// current at their ends, and across an hour-long gap that assumption is not merely approximate, it
// is fabrication — one reading of 0.5 A an hour before another would contribute 500 mAh that nobody
// measured.
//
// Two minutes is chosen to comfortably cover a `--watch 60s` loop while refusing anything that looks
// like the tool simply not having been run.
const MaxGap = 2 * time.Minute

// Charge is what was measured to have flowed.
type Charge struct {
	// MilliampHours and WattHours are the integrals over the covered intervals.
	MilliampHours float64 `json:"mah"`
	WattHours     float64 `json:"wh"`
	// Covered is the time actually integrated: the sum of the gaps that were short enough to trust.
	Covered  time.Duration `json:"-"`
	CoveredS float64       `json:"covered_s"`
	// Span is wall-clock time from the first sample to the last.
	Span  time.Duration `json:"-"`
	SpanS float64       `json:"span_s"`
	// Skipped counts intervals dropped for exceeding MaxGap.
	//
	// Reported rather than hidden because it is the difference between "0.4 Ah went through the pack
	// over two hours" and "0.4 Ah went through it during the eleven minutes anybody was watching".
	// A reader who cannot see the coverage cannot tell which they are looking at.
	Skipped int `json:"skipped_intervals"`
	// MeanCurrentA is the average over the covered time, which is the honest denominator: dividing by
	// the wall-clock span would understate it by whatever fraction was not observed.
	MeanCurrentA float64 `json:"mean_current_a"`
}

// Integrate accumulates charge over the samples.
//
// Trapezoidal rather than rectangular. Current on this board tracks CPU load and therefore ramps
// rather than stepping, so averaging each interval's endpoints is both more accurate and no more
// code than taking one end and ignoring the other.
func Integrate(samples []history.Sample) Charge {
	var c Charge
	if len(samples) < 2 {
		return c
	}
	c.Span = samples[len(samples)-1].At.Sub(samples[0].At)
	c.SpanS = c.Span.Seconds()

	for i := 1; i < len(samples); i++ {
		prev, cur := samples[i-1], samples[i]
		gap := cur.At.Sub(prev.At)
		if gap <= 0 {
			// Duplicate or out-of-order timestamps contribute nothing rather than a negative.
			continue
		}
		if gap > MaxGap {
			c.Skipped++
			continue
		}
		hours := gap.Hours()
		c.MilliampHours += (prev.CurrentA + cur.CurrentA) / 2 * hours * 1000
		c.WattHours += (prev.WattsW + cur.WattsW) / 2 * hours
		c.Covered += gap
	}
	c.CoveredS = c.Covered.Seconds()
	if c.Covered > 0 {
		c.MeanCurrentA = c.MilliampHours / 1000 / c.Covered.Hours()
	}
	return c
}

// Runtime estimates how long a declared capacity would last at the measured mean current.
//
// capacityMAh is declared by the operator and never inferred, because the hardware cannot know what
// cells are fitted. It is also frequently wrong in a specific direction: 18650 chemistry caps out
// around 3500 mAh per cell, and cells sold as 5000 mAh or more are commonly overstated by a factor
// of two or three. So a runtime derived from a declared figure inherits that error in full, and
// whatever reports it must say that it rests on a declaration rather than a measurement.
//
// ok is false when there is nothing to compute from, which is a different answer from zero.
func Runtime(c Charge, capacityMAh float64) (time.Duration, bool) {
	if capacityMAh <= 0 || c.MeanCurrentA <= 0 || c.Covered <= 0 {
		return 0, false
	}
	hours := capacityMAh / (c.MeanCurrentA * 1000)
	return time.Duration(hours * float64(time.Hour)), true
}

// Confidence reports whether the integral rests on enough observation to be worth quoting.
//
// Two independent ways to be unusable, and they need different words. Too little covered time means
// the numbers are real but tiny — a few seconds of watching says nothing about a pack's behaviour.
// Poor coverage means the tool was not running for most of the span, so the total describes the
// minutes somebody happened to be looking rather than the period it appears to cover.
func Confidence(c Charge) (note string, usable bool) {
	const minCovered = 60 * time.Second
	if c.Covered < minCovered {
		return "less than a minute of measured current; nothing to conclude yet", false
	}
	if c.Skipped > 0 {
		ratio := c.Covered.Seconds() / c.Span.Seconds()
		if ratio < 0.5 {
			return "covers well under half the elapsed time; the tool was not running for most of it", true
		}
		return "some intervals were too far apart to integrate and were skipped", true
	}
	return "", true
}
