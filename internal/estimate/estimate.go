// Package estimate turns a series of battery samples into a time remaining.
//
// The whole difficulty is that the obvious method is wrong. Taking the first and last sample and
// drawing a line between them produces an answer that is confidently, badly incorrect for the first
// few minutes of every discharge — which is exactly when somebody is looking at it.
//
// This was measured rather than assumed. On an observed mains failure the pack read:
//
//	21:22  94%      21:24  93%      21:25  92%      21:26  85%      21:29  80%
//
// which is about 2%/min over the first stretch and about 0.7%/min once settled. Most of that early
// drop is not capacity leaving the pack; it is a voltage-based gauge re-converging after the load
// stepped up. A two-point fit across the transition predicted "flat within the hour" when the real
// figure was several times longer.
//
// Three things defend against that, and each addresses a different failure:
//
//   - A settling period is skipped at the start of a discharge run, because samples taken while the
//     gauge is still converging describe the gauge, not the battery.
//   - The slope is a Theil-Sen estimator — the median of all pairwise slopes — rather than a least
//     squares fit. A median is unmoved by a minority of wild samples, where a mean is dragged by
//     them, and the samples this sees are wild in bursts.
//   - Pairs closer together than a minimum separation are not counted, because the percentage moves
//     in whole steps. Two samples a few seconds apart differ by either 0% or 1%, so their slope is
//     either zero or enormous, and neither is a measurement of anything.
//
// Everything here is pure. No clock, no filesystem, no globals.
package estimate

import (
	"sort"
	"time"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/history"
)

// State is what the pack is doing, as far as the samples can tell.
type State string

const (
	// Discharging means the percentage is falling.
	Discharging State = "discharging"
	// Charging means it is rising.
	Charging State = "charging"
	// Steady means neither, over a span long enough for that to be meaningful.
	Steady State = "steady"
	// Unknown means there is not yet enough history to say.
	Unknown State = "unknown"
)

// Tuning values. Named and gathered because every one of them is a judgement about this specific
// hardware, and a reader deserves to see them together rather than find them inline.
const (
	// Settle is how much of the start of a discharge run to ignore. Sized from the observed
	// re-convergence above, which was substantially complete within ninety seconds.
	Settle = 90 * time.Second

	// MinPairSpan is the closest two samples may be and still contribute a slope. Below this the
	// quantisation of a whole-number percentage dominates the real change.
	MinPairSpan = 60 * time.Second

	// MinSpan is how much settled history is needed before any figure is offered. Under a minute of
	// data cannot distinguish a discharge from a rounding step.
	MinSpan = 3 * time.Minute

	// MinSamples is the fewest settled samples worth taking a median of.
	MinSamples = 4
)

// Estimate is the answer, including enough of its own provenance to be argued with.
//
// TimeToEmpty is a pointer because "no estimate yet" and "zero time left" are opposite situations
// and must not render alike. Samples and Span are reported so that a reader can see how much
// evidence is behind the number — an estimate from four minutes of data is a different claim from
// one from forty, and the tool should not present them identically.
type Estimate struct {
	State          State          `json:"state"`
	PercentPerHour *float64       `json:"percent_per_hour,omitempty"`
	TimeToEmpty    *time.Duration `json:"-"`
	// TimeToEmptyS is the JSON rendering of TimeToEmpty, in seconds, because a Go Duration
	// marshals as an integer nanosecond count that no other language wants to read.
	TimeToEmptyS *float64 `json:"time_to_empty_s,omitempty"`
	Samples      int      `json:"samples"`
	Span         float64  `json:"span_s"`
	// Note explains an absent estimate. Empty when there is one.
	Note string `json:"note,omitempty"`
}

// Run returns the tail of samples belonging to the current discharge or charge run.
//
// A run ends wherever the direction reverses. Mixing samples from before a reversal into the slope
// would average a discharge against a charge and report something that never happened.
func Run(samples []history.Sample) []history.Sample {
	if len(samples) < 2 {
		return samples
	}
	last := samples[len(samples)-1]
	falling := false
	for i := len(samples) - 1; i > 0; i-- {
		if samples[i].Percent != last.Percent {
			// An earlier sample that is *higher* than the latest means the number has come down,
			// which is a discharge. Reading this the other way round classifies every discharge as
			// a charge and collapses the run to its final sample.
			falling = samples[i].Percent > last.Percent
			break
		}
	}
	start := 0
	for i := len(samples) - 1; i > 0; i-- {
		prev, cur := samples[i-1], samples[i]
		// A step the other way ends the run. Equal percentages continue it, because a plateau is
		// normal on a gauge that reports whole numbers.
		if falling && cur.Percent > prev.Percent {
			start = i
			break
		}
		if !falling && cur.Percent < prev.Percent {
			start = i
			break
		}
	}
	return samples[start:]
}

// Settled drops the first Settle of a run.
//
// Returns everything when that would leave too little to work with: a short run with the settling
// period removed is not better evidence than the same run intact, and reporting nothing at all
// during the first ninety seconds of every power cut would remove the estimate exactly when it is
// wanted. The Note on the result is what tells a reader which of the two they are looking at.
func Settled(run []history.Sample) ([]history.Sample, bool) {
	if len(run) == 0 {
		return run, false
	}
	cutoff := run[0].At.Add(Settle)
	out := make([]history.Sample, 0, len(run))
	for _, s := range run {
		if s.At.Before(cutoff) {
			continue
		}
		out = append(out, s)
	}
	if len(out) < MinSamples {
		return run, false
	}
	return out, true
}

// Slope returns the median of the pairwise slopes, in percent per hour, and how many pairs it had.
//
// Negative means discharging. Pairs closer than MinPairSpan are skipped for the quantisation reason
// given in the package comment.
func Slope(samples []history.Sample) (float64, int) {
	var slopes []float64
	for i := 0; i < len(samples); i++ {
		for j := i + 1; j < len(samples); j++ {
			dt := samples[j].At.Sub(samples[i].At)
			if dt < MinPairSpan {
				continue
			}
			dp := float64(samples[j].Percent - samples[i].Percent)
			slopes = append(slopes, dp/dt.Hours())
		}
	}
	if len(slopes) == 0 {
		return 0, 0
	}
	sort.Float64s(slopes)
	return median(slopes), len(slopes)
}

// median of a sorted slice.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// Trend splits the samples in half by time and measures each half separately.
//
// A voltage-based gauge does not settle at a fixed moment; it converges over several minutes, so a
// fixed cut-off cannot reliably separate transient from truth. What it can do is notice that the
// rate is still falling. Measured during a real mains failure the halves ran roughly 250%/h and
// then 70%/h across seven minutes, still decelerating at the end of the window.
//
// decelerating means the recent half is materially shallower than the earlier one, which is the
// signature of a gauge still converging rather than a battery whose drain has genuinely eased.
func Trend(samples []history.Sample) (early, recent float64, decelerating bool) {
	if len(samples) < 2*MinSamples {
		return 0, 0, false
	}
	mid := samples[0].At.Add(samples[len(samples)-1].At.Sub(samples[0].At) / 2)
	var a, b []history.Sample
	for _, s := range samples {
		if s.At.Before(mid) {
			a = append(a, s)
		} else {
			b = append(b, s)
		}
	}
	earlyRate, earlyPairs := Slope(a)
	recentRate, recentPairs := Slope(b)
	if earlyPairs == 0 || recentPairs == 0 || earlyRate >= 0 || recentRate >= 0 {
		return earlyRate, recentRate, false
	}
	// Materially shallower, not merely noisier. A quarter is enough to be outside sampling noise on
	// a whole-number percentage and small enough to catch a still-converging gauge.
	return earlyRate, recentRate, recentRate > earlyRate*0.75
}

// From produces an estimate from a history.
//
// falling reports whether some other source — the mains-detection GPIO, say — believes the pack is
// discharging. It is used only to sharpen the wording when the percentage has not yet moved: a pack
// that is definitely on battery but has not dropped a whole percent is not "steady", it is early.
func From(samples []history.Sample, falling bool) Estimate {
	if len(samples) < 2 {
		return Estimate{State: Unknown, Samples: len(samples),
			Note: "not enough history yet; leave it running"}
	}

	run := Run(samples)
	settled, wasSettled := Settled(run)
	span := settled[len(settled)-1].At.Sub(settled[0].At)

	est := Estimate{Samples: len(settled), Span: span.Seconds()}

	if span < MinSpan || len(settled) < MinSamples {
		est.State = Unknown
		est.Note = "too little settled history for a rate"
		if falling {
			est.Note = "on battery, but too early to measure a rate"
		}
		return est
	}

	rate, pairs := Slope(settled)
	if pairs == 0 {
		est.State = Unknown
		est.Note = "samples too closely spaced to measure a rate"
		return est
	}

	// When the gauge is still converging, the median over the whole window is dragged steep by the
	// early samples. The recent half describes the machine as it is now, which is what a reader is
	// asking about, so prefer it and say that the answer is still moving.
	_, recent, decelerating := Trend(settled)
	stillSettling := false
	if decelerating {
		rate = recent
		stillSettling = true
	}
	est.PercentPerHour = &rate

	switch {
	case rate < 0:
		est.State = Discharging
	case rate > 0:
		est.State = Charging
		est.Note = "charging; no time-to-empty"
		return est
	default:
		est.State = Steady
		est.Note = "no measurable change"
		if falling {
			// The percentage has not moved but something authoritative says mains is gone. Saying
			// "steady" here would be true of the number and misleading about the situation.
			est.Note = "on battery; percentage has not moved yet"
		}
		return est
	}

	// Extrapolate from the newest percentage, not the oldest: the question is how long from now.
	now := settled[len(settled)-1].Percent
	hours := float64(now) / -rate
	d := time.Duration(hours * float64(time.Hour))
	secs := d.Seconds()
	est.TimeToEmpty = &d
	est.TimeToEmptyS = &secs
	switch {
	case stillSettling:
		// The most important note of the three: this is the state the tool is in during the first
		// minutes of a power cut, which is precisely when somebody is reading it and deciding
		// whether to panic. Saying the figure will improve is the difference between a useful
		// warning and a misleading one.
		est.Note = "rate still settling; expect this to lengthen"
	case !wasSettled:
		est.Note = "includes the settling period after the load changed; likely pessimistic"
	}
	return est
}
