package pmic

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// Real vcgencmd output, trimmed. The shape is the point: voltages and currents are separate lines
// distinguished only by a name suffix, so they have to be paired by rail before they mean anything.
const sample = `3V7_WL_SW_A current(0)=0.05605000A
3V7_WL_SW_V volt(1)=3.69824000V
3V3_SYS_A current(2)=0.11035156A
3V3_SYS_V volt(3)=3.30273438V
1V8_SYS_A current(4)=0.20117188A
1V8_SYS_V volt(5)=1.79980469V
EXT5V_V volt(24)=5.08691000V
`

func TestParseSumsRailPower(t *testing.T) {
	got, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rails != 3 {
		t.Errorf("rails = %d, want 3", got.Rails)
	}
	want := 0.05605*3.69824 + 0.11035156*3.30273438 + 0.20117188*1.79980469
	if math.Abs(got.Watts-want) > 1e-4 {
		t.Errorf("watts = %.4f, want %.4f", got.Watts, want)
	}
	if math.Abs(got.EXT5V-5.08691) > 1e-5 {
		t.Errorf("EXT5V = %v", got.EXT5V)
	}
}

// Summing currents and voltages independently would be meaningless — they belong to different rails
// at different voltages — so a current with no matching voltage is dropped rather than assumed.
func TestParseDropsAnUnpairedCurrent(t *testing.T) {
	got, err := Parse(sample + "ORPHAN_A current(9)=1.00000000A\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.Rails != 3 {
		t.Errorf("rails = %d; the unpaired current should not count", got.Rails)
	}
	// A watt of phantom power is exactly the kind of error that makes a calibration confidently wrong.
	if got.Watts > 2 {
		t.Errorf("watts = %.4f; the orphan current was included", got.Watts)
	}
}

// A format change must be an error rather than a total of zero, which would read as "the machine is
// using no power" and calibrate against it.
func TestParseRejectsOutputItCannotUnderstand(t *testing.T) {
	for _, text := range []string{"", "not vcgencmd output at all", "EXT5V_V volt(24)=5.08V\n"} {
		if _, err := Parse(text); err == nil {
			t.Errorf("accepted unusable output: %q", text)
		}
	}
}

func TestParseIgnoresRubbishLines(t *testing.T) {
	got, err := Parse("garbage\n\n= =\nFOO_A current(0)=notanumber\n" + sample)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rails != 3 {
		t.Errorf("rails = %d, want the 3 real ones", got.Rails)
	}
}

// A machine that is not a Pi, or has no vcgencmd, must say so as a missing prerequisite rather than
// crash — nothing in the normal reporting path depends on this package.
func TestReadReportsUnavailability(t *testing.T) {
	boom := errors.New("executable file not found in $PATH")
	_, err := Read(func(string, ...string) ([]byte, error) { return nil, boom })

	var unavailable ErrUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !errors.Is(err, boom) {
		t.Error("the underlying cause was not wrapped")
	}
	if !strings.Contains(err.Error(), "vcgencmd") {
		t.Errorf("err = %q; want it to name the command so the reader knows what to install", err)
	}
}

func TestReadParsesWhatTheRunnerReturns(t *testing.T) {
	got, err := Read(func(name string, args ...string) ([]byte, error) {
		if name != "vcgencmd" || len(args) != 1 || args[0] != "pmic_read_adc" {
			t.Errorf("ran %q %v", name, args)
		}
		return []byte(sample), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Rails != 3 {
		t.Errorf("rails = %d", got.Rails)
	}
}
