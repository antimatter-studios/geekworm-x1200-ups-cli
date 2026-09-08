package ups

import (
	"errors"
	"testing"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/sysfs"
)

// real is the machine this was written for, as its sysfs actually reads: a Geekworm X1200 with two
// 18650s in parallel and an INA219 on the 5V rail, alongside the Pi's own hwmons.
func real() map[string]string {
	return map[string]string{
		"class/power_supply/battery/type":        "Battery",
		"class/power_supply/battery/capacity":    "95",
		"class/power_supply/battery/voltage_now": "4152500",
		"class/power_supply/battery/status":      "Unknown",
		"class/power_supply/battery/present":     "1",
		"class/power_supply/battery/temp":        "", // published, unanswerable

		"class/hwmon/hwmon0/name":        "cpu_thermal",
		"class/hwmon/hwmon0/temp1_input": "47000",

		"class/hwmon/hwmon6/name":           "ina219",
		"class/hwmon/hwmon6/in0_input":      "3",
		"class/hwmon/hwmon6/in1_input":      "5060",
		"class/hwmon/hwmon6/curr1_input":    "267",
		"class/hwmon/hwmon6/power1_input":   "1340000",
		"class/hwmon/hwmon6/shunt_resistor": "10000",
	}
}

func TestFindBattery(t *testing.T) {
	b := FindBattery(sysfs.Map(real()))
	if b == nil {
		t.Fatal("no battery found")
	}
	if b.Name != "battery" {
		t.Errorf("Name = %q", b.Name)
	}
	if b.Percent == nil || *b.Percent != 95 {
		t.Errorf("Percent = %v; want 95", b.Percent)
	}
	if b.VoltageV == nil || *b.VoltageV != 4.1525 {
		t.Errorf("VoltageV = %v; want 4.1525", b.VoltageV)
	}
	// Permanently Unknown on this hardware: a fuel gauge cannot see the charger.
	if b.Status != "Unknown" {
		t.Errorf("Status = %q", b.Status)
	}
	if b.Present == nil || !*b.Present {
		t.Errorf("Present = %v", b.Present)
	}
}

func TestFindBatterySkipsNonBatteries(t *testing.T) {
	fs := sysfs.Map(map[string]string{
		"class/power_supply/usb/type":         "USB",
		"class/power_supply/usb/online":       "1",
		"class/power_supply/battery/type":     "Battery",
		"class/power_supply/battery/capacity": "42",
	})
	b := FindBattery(fs)
	if b == nil || b.Name != "battery" {
		t.Fatalf("picked %v; want the Battery, not the USB supply", b)
	}
}

func TestFindBatteryAbsent(t *testing.T) {
	if b := FindBattery(sysfs.Map(map[string]string{})); b != nil {
		t.Errorf("FindBattery = %v; want nil", b)
	}
	// A directory with no type file is not a battery.
	if b := FindBattery(sysfs.Map(map[string]string{"class/power_supply/x/capacity": "5"})); b != nil {
		t.Errorf("FindBattery = %v; want nil", b)
	}
}

func TestFindBatteryDefaultsStatusWhenAbsent(t *testing.T) {
	fs := sysfs.Map(map[string]string{"class/power_supply/b/type": "Battery"})
	b := FindBattery(fs)
	if b == nil || b.Status != "Unknown" {
		t.Fatalf("Status = %v; want Unknown", b)
	}
	// Nothing else was published, so nothing else may be claimed.
	if b.Percent != nil || b.VoltageV != nil || b.Present != nil {
		t.Errorf("invented values from an empty battery: %+v", b)
	}
}

func TestFindPower(t *testing.T) {
	p := FindPower(sysfs.Map(real()))
	if p == nil {
		t.Fatal("no power monitor found")
	}
	// hwmon0 is the CPU thermal sensor and must be passed over: it has a temperature and none of
	// the three values that make something a power monitor.
	if p.Name != "hwmon6" {
		t.Errorf("Name = %q; want hwmon6", p.Name)
	}
	if p.Chip != "ina219" {
		t.Errorf("Chip = %q", p.Chip)
	}
	if p.BusV == nil || *p.BusV != 5.06 {
		t.Errorf("BusV = %v; want 5.06", p.BusV)
	}
	if p.CurrentA == nil || *p.CurrentA != 0.267 {
		t.Errorf("CurrentA = %v; want 0.267", p.CurrentA)
	}
	if p.WattsW == nil || *p.WattsW != 1.34 {
		t.Errorf("WattsW = %v; want 1.34", p.WattsW)
	}
	if p.ShuntMV == nil || *p.ShuntMV != 3 {
		t.Errorf("ShuntMV = %v; want 3", p.ShuntMV)
	}
	if p.ShuntOhms == nil || *p.ShuntOhms != 0.01 {
		t.Errorf("ShuntOhms = %v; want 0.01", p.ShuntOhms)
	}
}

func TestFindPowerIdentifiesByShapeNotByName(t *testing.T) {
	// An INA226 or anything else exposing the same three attributes must be found without this
	// program being taught its name.
	fs := sysfs.Map(map[string]string{
		"class/hwmon/hwmon2/name":         "ina226",
		"class/hwmon/hwmon2/in1_input":    "12000",
		"class/hwmon/hwmon2/curr1_input":  "500",
		"class/hwmon/hwmon2/power1_input": "6000000",
	})
	p := FindPower(fs)
	if p == nil || p.Chip != "ina226" {
		t.Fatalf("FindPower = %v; want the ina226", p)
	}
}

func TestFindPowerRequiresAllThreeValues(t *testing.T) {
	// A voltage regulator publishing only a voltage is not a power monitor, and treating it as one
	// would produce a confident reading with no current in it.
	for _, missing := range []string{"in1_input", "curr1_input", "power1_input"} {
		files := map[string]string{
			"class/hwmon/hwmon1/name":         "partial",
			"class/hwmon/hwmon1/in1_input":    "5000",
			"class/hwmon/hwmon1/curr1_input":  "100",
			"class/hwmon/hwmon1/power1_input": "500000",
		}
		delete(files, "class/hwmon/hwmon1/"+missing)
		if p := FindPower(sysfs.Map(files)); p != nil {
			t.Errorf("without %s, FindPower = %v; want nil", missing, p)
		}
	}
}

func TestFindPowerAbsent(t *testing.T) {
	if p := FindPower(sysfs.Map(map[string]string{})); p != nil {
		t.Errorf("FindPower = %v; want nil", p)
	}
}

func TestRead(t *testing.T) {
	r, err := Read(sysfs.Map(real()))
	if err != nil {
		t.Fatal(err)
	}
	if r.Battery == nil || r.Power == nil {
		t.Fatalf("Read = %+v; want both halves", r)
	}
}

func TestReadWithOnlyOneHalf(t *testing.T) {
	// The gauge alone is still worth reporting: it is the half that says how much is left.
	onlyBattery := sysfs.Map(map[string]string{
		"class/power_supply/battery/type":     "Battery",
		"class/power_supply/battery/capacity": "80",
	})
	r, err := Read(onlyBattery)
	if err != nil || r.Battery == nil || r.Power != nil {
		t.Fatalf("Read = %+v, %v", r, err)
	}
}

func TestReadWithNothingBound(t *testing.T) {
	_, err := Read(sysfs.Map(map[string]string{}))
	if !errors.Is(err, ErrNoDevices) {
		t.Errorf("err = %v; want ErrNoDevices", err)
	}
}
