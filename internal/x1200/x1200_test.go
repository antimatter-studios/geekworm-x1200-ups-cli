package x1200

import (
	"errors"
	"testing"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/gpio"
)

// The vendor's table: "Low-power supply failed, High-power supply OK".
func TestMainsPolarity(t *testing.T) {
	onMains := &gpio.Fake{Levels: map[uint32]bool{PLDLine: true}}
	if got, err := Mains(onMains, "chip"); err != nil || !got {
		t.Errorf("high = %v, %v; want true (mains present)", got, err)
	}

	onBattery := &gpio.Fake{Levels: map[uint32]bool{PLDLine: false}}
	if got, err := Mains(onBattery, "chip"); err != nil || got {
		t.Errorf("low = %v, %v; want false (running on battery)", got, err)
	}
}

// An unconfigured pin has no readable level, and that must be an error rather than a false. A false
// here would be read as "on battery" and would announce a power cut that is not happening.
func TestMainsFailsLoudlyOnAnUnreadablePin(t *testing.T) {
	_, err := Mains(&gpio.Fake{Levels: map[uint32]bool{}}, "chip")
	if err == nil {
		t.Fatal("unreadable pin returned a level instead of an error")
	}
	if !errors.Is(err, ErrNoPin) {
		t.Errorf("err = %v; want it to wrap ErrNoPin so the fix can be suggested", err)
	}
}

// The inversion that makes this board dangerous to guess at: the vendor disables charging with
// `pinctrl set 16 op dh` and enables it with `dl`. High is OFF.
func TestChargingIsActiveLow(t *testing.T) {
	high := &gpio.Fake{Levels: map[uint32]bool{ChargeLine: true}}
	if got, err := Charging(high, "chip"); err != nil || got {
		t.Errorf("high = %v, %v; want false — high DISABLES charging on this board", got, err)
	}

	low := &gpio.Fake{Levels: map[uint32]bool{ChargeLine: false}}
	if got, err := Charging(low, "chip"); err != nil || !got {
		t.Errorf("low = %v, %v; want true — low ENABLES charging", got, err)
	}
}

func TestSetChargingWritesTheInverse(t *testing.T) {
	f := &gpio.Fake{}
	if err := SetCharging(f, "chip", true); err != nil {
		t.Fatal(err)
	}
	if len(f.Written) != 1 || f.Written[0].Offset != ChargeLine || f.Written[0].Value {
		t.Errorf("enabling charging wrote %+v; want GPIO%d driven LOW", f.Written, ChargeLine)
	}

	f = &gpio.Fake{}
	if err := SetCharging(f, "chip", false); err != nil {
		t.Fatal(err)
	}
	if len(f.Written) != 1 || !f.Written[0].Value {
		t.Errorf("disabling charging wrote %+v; want GPIO%d driven HIGH", f.Written, ChargeLine)
	}
}

// Round-tripping catches an inversion that is applied consistently in both directions and would
// therefore pass both single-ended tests above.
func TestChargingRoundTrips(t *testing.T) {
	for _, want := range []bool{true, false} {
		f := &gpio.Fake{}
		if err := SetCharging(f, "chip", want); err != nil {
			t.Fatal(err)
		}
		got, err := Charging(f, "chip")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("set %v then read %v", want, got)
		}
	}
}

func TestPersistedNamesTheRightPin(t *testing.T) {
	got := Persisted()
	if len(got) != 1 || got[0] != "gpio=6=ip,pu" {
		t.Errorf("Persisted() = %v; want the PLD pin as a pulled-up input", got)
	}
	// The charging line must not be forced at boot: an undriven pin lets the board charge normally,
	// and a level here would impose a policy nobody reading config.txt would connect to charging.
	for _, line := range got {
		if line == "gpio=16=op,dh" || line == "gpio=16=op,dl" {
			t.Errorf("Persisted() drives the charging line: %q", line)
		}
	}
}

func TestErrorsPropagate(t *testing.T) {
	boom := errors.New("boom")
	if _, err := Mains(&gpio.Fake{Err: boom}, "chip"); err == nil {
		t.Error("Mains swallowed a port error")
	}
	if _, err := Charging(&gpio.Fake{Err: boom}, "chip"); err == nil {
		t.Error("Charging swallowed a port error")
	}
	if err := SetCharging(&gpio.Fake{Err: boom}, "chip", true); err == nil {
		t.Error("SetCharging swallowed a port error")
	}
}
