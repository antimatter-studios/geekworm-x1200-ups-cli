// Package ups turns what the kernel publishes about the UPS into one reading.
//
// The decision that shapes this package is that it reads sysfs and never touches I2C. Once the
// max17040_battery and ina2xx drivers are bound they own their addresses exclusively:
//
//	$ i2cget -y 1 0x36 0x02 w
//	Error: Could not set address to 0x36: Device or resource busy
//
// Speaking I2C directly would mean forcing past that — racy, since it can interleave with a driver
// mid-transaction — or unbinding the drivers, which would cost `sensors`, `upower` and Netdata's
// automatic charting. Reading what the drivers already decoded keeps all of it, and has the useful
// side effect that no chip address, register number or scaling constant appears anywhere in this
// program. The kernel does that arithmetic and is better placed to.
//
// Devices are therefore found by shape rather than by part number, so this keeps working if the HAT
// is replaced by anything else the kernel supports.
//
// Every function is pure given its FS argument. Nothing here reads a clock, a global, or a file.
package ups

import (
	"errors"
	"path"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/estimate"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/sysfs"
)

// Battery is what a fuel gauge can say.
//
// The optional fields are pointers because on this hardware most of them are absent, and absent has
// to stay distinguishable from zero. The MAX17040 is a voltage-based gauge: it publishes a
// percentage and a cell voltage and nothing else — no charge_full, no current_now — so a pack at 0%
// and a gauge that cannot report a percentage must not render the same way.
type Battery struct {
	Name    string `json:"name"`
	Percent *int   `json:"percent,omitempty"`
	// VoltageV is one cell. The X1200 carries two 18650s in parallel (1S2P), so the pack voltage
	// and the cell voltage are the same number, and a fully charged pack reads about 4.2 V.
	VoltageV *float64 `json:"voltage_v,omitempty"`
	// Status is the kernel's own word, and on this hardware it reads "Unknown" permanently. A fuel
	// gauge measures charge; whether the pack is charging or discharging is the charger's business,
	// and there is no charger chip on the bus to ask. The X1200 signals mains presence on a GPIO
	// instead, which is a different source and not read here.
	Status  string `json:"status"`
	Present *bool  `json:"present,omitempty"`
}

// Power is what a current and power monitor can say about the rail it sits on.
type Power struct {
	Name     string   `json:"name"`
	Chip     string   `json:"chip,omitempty"`
	BusV     *float64 `json:"bus_v,omitempty"`
	ShuntMV  *float64 `json:"shunt_mv,omitempty"`
	CurrentA *float64 `json:"current_a,omitempty"`
	WattsW   *float64 `json:"watts_w,omitempty"`
	// ShuntOhms is the resistance the driver divides by, reported rather than hidden because the
	// three values above are all directly proportional to it. Wrong here is wrong everywhere by the
	// same factor, and a reader cannot tell unless the value is on show. The ina2xx default of
	// 0.01 is provably wrong for this board — see the README.
	ShuntOhms *float64 `json:"shunt_ohms,omitempty"`
}

// Supply is where the machine's power is actually coming from.
//
// This is the only authoritative answer to "am I on battery", which is usually the whole point of
// owning a UPS. Nothing on the I2C bus can provide it: the fuel gauge measures charge and not
// direction, and the INA219 sits downstream of the changeover so it sees the same load either way.
// The board signals it on a GPIO instead.
type Supply struct {
	// OnMains is true when the adapter is supplying power.
	OnMains bool `json:"on_mains"`
	// Line is the GPIO the answer came from, so a reader can check it themselves.
	Line uint32 `json:"gpio_line"`
	// Caveat records why a positive reading is weaker evidence than a negative one. The line is
	// read with a pull-up and mains-present is high, so a disconnected or intermittent pin also
	// reads high — and this HAT connects through pogo pins, which is exactly the contact that goes
	// intermittent. A stuck "on mains" is therefore a possible fault, not proof.
	Caveat string `json:"caveat,omitempty"`
}

// Charging is whether the board is currently allowed to charge the pack.
type Charging struct {
	Enabled bool   `json:"enabled"`
	Line    uint32 `json:"gpio_line"`
}

// Reading is the whole picture at one moment. Either half may be missing.
type Reading struct {
	Battery *Battery `json:"battery,omitempty"`
	Power   *Power   `json:"power,omitempty"`
	// Supply and Charging come from GPIO rather than sysfs, so Read does not populate them either.
	Supply   *Supply   `json:"supply,omitempty"`
	Charging *Charging `json:"charging,omitempty"`
	// Estimate is how long the pack has left, and is the one field Read does not populate.
	//
	// It cannot: a duration is derived from how the numbers have moved, which needs stored samples
	// and a clock, and this package has neither by design. The caller records the sample and
	// attaches the result, which keeps Read pure and keeps the estimator testable without a disk.
	Estimate *estimate.Estimate `json:"estimate,omitempty"`
}

// ErrNoDevices is returned when neither driver is bound.
var ErrNoDevices = errors.New("no battery or power monitor found: are the max17040_battery and ina2xx drivers bound, and is /dev/i2c-1 present?")

// FindBattery returns the first power supply the kernel classes as a battery, or nil.
func FindBattery(fs sysfs.FS) *Battery {
	for _, dir := range fs.Glob("class/power_supply/*") {
		if kind, ok := fs.Read(dir + "/type"); !ok || kind != "Battery" {
			continue
		}
		return &Battery{
			Name:     path.Base(dir),
			Percent:  sysfs.IntPtr(fs, dir+"/capacity"),
			VoltageV: sysfs.ScaledPtr(fs, dir+"/voltage_now", 1e6), // microvolts
			Status:   sysfs.TextOr(fs, dir+"/status", "Unknown"),
			Present:  sysfs.BoolPtr(fs, dir+"/present"),
		}
	}
	return nil
}

// FindPower returns the first hwmon that looks like a power monitor, or nil.
//
// Identified by shape, not by name: a hwmon publishing a bus voltage, a current and a power is a
// power monitor whatever chip is underneath. Matching the string "ina219" would mean editing this
// program to understand an INA226, for no gain — and the chip's name is reported anyway, so nothing
// is lost by not matching on it.
func FindPower(fs sysfs.FS) *Power {
	for _, dir := range fs.Glob("class/hwmon/*") {
		bus := sysfs.ScaledPtr(fs, dir+"/in1_input", 1e3)      // millivolts
		amps := sysfs.ScaledPtr(fs, dir+"/curr1_input", 1e3)   // milliamps
		watts := sysfs.ScaledPtr(fs, dir+"/power1_input", 1e6) // microwatts
		if bus == nil || amps == nil || watts == nil {
			continue
		}
		return &Power{
			Name: path.Base(dir),
			Chip: sysfs.TextOr(fs, dir+"/name", ""),
			BusV: bus,
			// in0 is the drop across the shunt itself, in millivolts. It is the only thing here
			// actually measured — current and power are both derived from it — so it is worth
			// showing next to the values that depend on it.
			ShuntMV:   sysfs.ScaledPtr(fs, dir+"/in0_input", 1),
			CurrentA:  amps,
			WattsW:    watts,
			ShuntOhms: sysfs.ScaledPtr(fs, dir+"/shunt_resistor", 1e6), // microohms
		}
	}
	return nil
}

// Read gathers everything available.
func Read(fs sysfs.FS) (*Reading, error) {
	battery, power := FindBattery(fs), FindPower(fs)
	if battery == nil && power == nil {
		return nil, ErrNoDevices
	}
	return &Reading{Battery: battery, Power: power}, nil
}
