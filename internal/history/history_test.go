package history

import (
	"strings"
	"testing"
	"time"
)

var base = time.Date(2026, 9, 8, 21, 0, 0, 0, time.UTC)

func TestParseRenderRoundTrip(t *testing.T) {
	want := []Sample{
		{At: base, Percent: 94, VoltageV: 4.147},
		{At: base.Add(time.Minute), Percent: 93, VoltageV: 4.0612},
	}
	got := Parse(Render(want))
	if len(got) != len(want) {
		t.Fatalf("round trip produced %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if !got[i].At.Equal(want[i].At) || got[i].Percent != want[i].Percent {
			t.Errorf("sample %d = %+v, want %+v", i, got[i], want[i])
		}
		if diff := got[i].VoltageV - want[i].VoltageV; diff > 1e-4 || diff < -1e-4 {
			t.Errorf("sample %d voltage = %v, want %v", i, got[i].VoltageV, want[i].VoltageV)
		}
	}
}

// A power cut during a write leaves a truncated final line. That is the exact event this program
// exists to observe, so it must cost at most the one sample rather than the whole history.
func TestParseSurvivesATruncatedTail(t *testing.T) {
	text := "1788000000 94 4.1470\n1788000060 93 4.0610\n1788000120 92 4.05"
	if got := Parse(text + "\n178800018"); len(got) != 3 {
		t.Fatalf("got %d samples, want 3 good ones with the torn line dropped", len(got))
	}
}

func TestParseSkipsRubbishLines(t *testing.T) {
	text := strings.Join([]string{
		"1788000000 94 4.1470",
		"",
		"not a sample at all",
		"1788000060 xx 4.0610",
		"1788000120 92 4.0500",
	}, "\n")
	if got := Parse(text); len(got) != 2 {
		t.Fatalf("got %d samples, want 2", len(got))
	}
}

func TestParseSortsByTime(t *testing.T) {
	text := "1788000120 92 4.05\n1788000000 94 4.147\n1788000060 93 4.061\n"
	got := Parse(text)
	for i := 1; i < len(got); i++ {
		if got[i].At.Before(got[i-1].At) {
			t.Fatalf("sample %d is out of order", i)
		}
	}
}

func TestPruneDropsOldAndCaps(t *testing.T) {
	var samples []Sample
	for i := 0; i < 100; i++ {
		samples = append(samples, Sample{At: base.Add(time.Duration(i) * time.Minute), Percent: 100 - i})
	}
	now := base.Add(99 * time.Minute)

	windowed := Prune(samples, now, 30*time.Minute, 0)
	if len(windowed) != 31 {
		t.Errorf("window kept %d, want 31", len(windowed))
	}
	capped := Prune(samples, now, 24*time.Hour, 10)
	if len(capped) != 10 {
		t.Fatalf("cap kept %d, want 10", len(capped))
	}
	if capped[len(capped)-1].Percent != 1 {
		t.Error("cap kept the wrong end: the newest samples must survive")
	}
}

func TestAppendPersists(t *testing.T) {
	store, content := Memory("")
	got, err := Append(store, Sample{At: base, Percent: 90, VoltageV: 4.1}, time.Hour, 100)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("returned %d samples, want 1", len(got))
	}
	if !strings.Contains(*content, "90") {
		t.Errorf("stored content %q does not contain the sample", *content)
	}

	got, err = Append(store, Sample{At: base.Add(time.Minute), Percent: 89, VoltageV: 4.05}, time.Hour, 100)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("returned %d samples, want 2", len(got))
	}
}

// A Pi has no real time clock, so it boots in 1970 and jumps forward when NTP lands. Samples from
// "the future" would sort before ones already recorded and make every interval negative.
func TestAppendDiscardsSamplesFromTheFuture(t *testing.T) {
	stale := Render([]Sample{
		{At: base.Add(time.Hour), Percent: 50, VoltageV: 3.7},
	})
	store, _ := Memory(stale)

	got, err := Append(store, Sample{At: base, Percent: 90, VoltageV: 4.1}, 24*time.Hour, 100)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("kept %d samples, want only the new one", len(got))
	}
	if got[0].Percent != 90 {
		t.Errorf("kept %d%%, want the newly recorded 90%%", got[0].Percent)
	}
}

func TestAppendOnAbsentStoreStartsFresh(t *testing.T) {
	store := Store{
		Read:  func() (string, bool) { return "", false },
		Write: func(string) error { return nil },
	}
	got, err := Append(store, Sample{At: base, Percent: 77, VoltageV: 4.0}, time.Hour, 100)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if len(got) != 1 || got[0].Percent != 77 {
		t.Errorf("got %+v, want a single fresh sample", got)
	}
}
