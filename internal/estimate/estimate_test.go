package estimate

import (
	"math"
	"testing"
	"time"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/history"
)

var base = time.Date(2026, 9, 8, 21, 0, 0, 0, time.UTC)

// at builds a sample n seconds after base.
func at(secs int, pct int, volts float64) history.Sample {
	return history.Sample{At: base.Add(time.Duration(secs) * time.Second), Percent: pct, VoltageV: volts}
}

// steady builds a run discharging at a constant percent per hour, sampled every interval.
func steady(startPct int, perHour float64, interval, total time.Duration) []history.Sample {
	var out []history.Sample
	for t := time.Duration(0); t <= total; t += interval {
		pct := float64(startPct) + perHour*t.Hours()
		out = append(out, history.Sample{
			At:       base.Add(t),
			Percent:  int(math.Round(pct)),
			VoltageV: 3.0 + pct/100,
		})
	}
	return out
}

func TestFromNeedsHistory(t *testing.T) {
	if got := From(nil, false); got.State != Unknown {
		t.Fatalf("no samples: state = %q, want %q", got.State, Unknown)
	}
	one := []history.Sample{at(0, 90, 4.1)}
	if got := From(one, false); got.State != Unknown || got.TimeToEmpty != nil {
		t.Fatalf("one sample: %+v, want unknown with no estimate", got)
	}
}

func TestFromMeasuresASteadyDischarge(t *testing.T) {
	// 12%/hour from 60%: five hours to empty.
	samples := steady(60, -12, 30*time.Second, 30*time.Minute)
	got := From(samples, true)

	if got.State != Discharging {
		t.Fatalf("state = %q, want %q (note %q)", got.State, Discharging, got.Note)
	}
	if got.TimeToEmpty == nil {
		t.Fatal("no time to empty for a clean discharge")
	}
	hours := got.TimeToEmpty.Hours()
	// The percentage is rounded to whole numbers, so the recovered rate cannot be exact.
	if hours < 4.0 || hours > 6.0 {
		t.Errorf("time to empty = %.2fh, want about 5h", hours)
	}
	if got.PercentPerHour == nil || *got.PercentPerHour > -8 || *got.PercentPerHour < -16 {
		t.Errorf("rate = %v, want about -12", got.PercentPerHour)
	}
}

// The regression this package exists for: the observed mains failure, where the gauge re-converged
// steeply for the first minute and a half and then settled to its real rate. A two point fit across
// the whole thing reports far too little time.
func TestFromIgnoresTheSettlingTransient(t *testing.T) {
	samples := []history.Sample{
		at(0, 94, 4.147),
		at(30, 94, 4.147),
		at(60, 93, 4.061),
		at(90, 92, 4.050),
		// Settled from here: about 0.7%/min == 42%/hour.
		at(150, 91, 4.054),
		at(240, 90, 4.039),
		at(330, 89, 4.030),
		at(420, 88, 4.024),
		at(510, 87, 4.021),
		at(600, 86, 4.019),
	}
	got := From(samples, true)

	if got.State != Discharging {
		t.Fatalf("state = %q, want %q", got.State, Discharging)
	}
	if got.PercentPerHour == nil {
		t.Fatal("no rate")
	}
	// Naive first-to-last across the transient is (86-94)/600s == -48%/hour. The settled portion is
	// about -40%/hour. The point is that the early cliff must not drag the answer steeper than the
	// settled truth.
	if *got.PercentPerHour < -46 {
		t.Errorf("rate = %.1f%%/h, too steep: the settling transient leaked in", *got.PercentPerHour)
	}
}

func TestFromReportsCharging(t *testing.T) {
	samples := steady(40, +20, 30*time.Second, 20*time.Minute)
	got := From(samples, false)
	if got.State != Charging {
		t.Fatalf("state = %q, want %q", got.State, Charging)
	}
	if got.TimeToEmpty != nil {
		t.Error("charging should not carry a time to empty")
	}
}

// A pack that is genuinely on battery but has not yet dropped a whole percent must not be described
// as steady, because the number being still is an artefact of quantisation rather than the truth.
func TestFromDistinguishesEarlyDischargeFromSteady(t *testing.T) {
	var flat []history.Sample
	for i := 0; i <= 20; i++ {
		flat = append(flat, at(i*30, 80, 4.02))
	}

	onBattery := From(flat, true)
	if onBattery.State != Steady {
		t.Fatalf("state = %q, want %q", onBattery.State, Steady)
	}
	if onBattery.Note == "" || onBattery.Note == "no measurable change" {
		t.Errorf("note = %q, want it to say the pack is on battery", onBattery.Note)
	}

	onMains := From(flat, false)
	if onMains.Note != "no measurable change" {
		t.Errorf("note = %q, want the plain steady wording", onMains.Note)
	}
}

// A run reverses when the pack starts charging again. Samples from before the reversal describe a
// different situation and must not be averaged in.
func TestRunStopsAtAReversal(t *testing.T) {
	samples := []history.Sample{
		at(0, 90, 4.1), at(60, 88, 4.0), at(120, 86, 3.9), // discharging
		at(180, 87, 4.0), at(240, 89, 4.1), at(300, 91, 4.2), // then charging
	}
	run := Run(samples)
	if len(run) != 4 {
		t.Fatalf("run length = %d, want 4 (the reversal plus what follows)", len(run))
	}
	if run[0].Percent != 86 {
		t.Errorf("run starts at %d%%, want the 86%% trough", run[0].Percent)
	}
}

func TestRunTreatsAPlateauAsContinuous(t *testing.T) {
	samples := []history.Sample{
		at(0, 90, 4.1), at(60, 89, 4.05), at(120, 89, 4.04), at(180, 89, 4.03), at(240, 88, 4.02),
	}
	if run := Run(samples); len(run) != len(samples) {
		t.Fatalf("run length = %d, want %d: a plateau is not a reversal", len(run), len(samples))
	}
}

// Closely spaced samples differ by 0% or 1% and their slopes are meaningless. Skipping them is what
// stops a `--watch 1s` loop reporting nonsense.
func TestSlopeIgnoresPairsTooCloseTogether(t *testing.T) {
	var dense []history.Sample
	for i := 0; i <= 10; i++ {
		dense = append(dense, at(i, 90, 4.1)) // one second apart
	}
	if _, pairs := Slope(dense); pairs != 0 {
		t.Errorf("pairs = %d, want 0: everything is inside MinPairSpan", pairs)
	}
}

func TestSlopeIsUnmovedByAWildSample(t *testing.T) {
	clean := steady(80, -10, time.Minute, 30*time.Minute)
	rate, _ := Slope(clean)

	dirty := make([]history.Sample, len(clean))
	copy(dirty, clean)
	dirty[5].Percent = 5 // a gauge glitch

	dirtyRate, _ := Slope(dirty)
	if math.Abs(dirtyRate-rate) > 3 {
		t.Errorf("one bad sample moved the median from %.1f to %.1f", rate, dirtyRate)
	}
}

// The real capture, verbatim, from the mains failure of 2026-09-08. Seven minutes of a gauge
// converging, which is the hardest input this will get and the one it was built for.
func realTransient() []history.Sample {
	raw := []struct {
		secs int
		pct  int
		v    float64
	}{
		{0, 94, 4.147}, {146, 93, 4.061}, {153, 92, 4.050}, {161, 91, 4.054},
		{247, 85, 4.039}, {333, 82, 4.021}, {360, 81, 4.024}, {409, 80, 4.021}, {437, 80, 4.023},
	}
	var out []history.Sample
	for _, r := range raw {
		out = append(out, at(r.secs, r.pct, r.v))
	}
	return out
}

// A median over the whole window gives about -155%/h and half an hour left. The settled tail is
// nearer -69%/h. Reporting the former during a power cut would tell somebody they had thirty
// minutes when they had well over an hour, which is the wrong way round for a mistake to be.
func TestFromPrefersTheSettledTailWhileConverging(t *testing.T) {
	got := From(realTransient(), true)

	if got.State != Discharging {
		t.Fatalf("state = %q, want %q", got.State, Discharging)
	}
	if got.PercentPerHour == nil || got.TimeToEmpty == nil {
		t.Fatal("no rate or no duration")
	}
	if *got.PercentPerHour < -100 {
		t.Errorf("rate = %.1f%%/h; the early transient leaked in, want nearer -69", *got.PercentPerHour)
	}
	if mins := got.TimeToEmpty.Minutes(); mins < 45 {
		t.Errorf("time to empty = %.0fm; too pessimistic, the settled tail implies over an hour", mins)
	}
	if got.Note == "" {
		t.Error("a still-converging estimate must say so; silence implies a settled figure")
	}
}

// Trend is measured on settled samples, as From measures it, and the distinction matters here.
// The raw capture opens with 94% held for 146 seconds before the gauge reacts at all, so a Trend
// taken across the whole thing compares a flat prelude against the cliff that follows and concludes
// the discharge is speeding up. Dropping the prelude is what makes the comparison meaningful.
func TestTrendDetectsDeceleration(t *testing.T) {
	settled, _ := Settled(Run(realTransient()))
	early, recent, decel := Trend(settled)
	if !decel {
		t.Fatalf("not detected: early %.1f, recent %.1f", early, recent)
	}
	if recent <= early {
		t.Errorf("recent %.1f should be shallower than early %.1f", recent, early)
	}
}

// A genuinely steady discharge must not be labelled as settling, or the caveat becomes noise that
// a reader learns to ignore.
func TestTrendLeavesASteadyDischargeAlone(t *testing.T) {
	if _, _, decel := Trend(steady(80, -10, time.Minute, 40*time.Minute)); decel {
		t.Error("steady discharge reported as decelerating")
	}
	got := From(steady(80, -10, time.Minute, 40*time.Minute), true)
	if got.Note != "" {
		t.Errorf("note = %q, want none for a clean steady discharge", got.Note)
	}
}
