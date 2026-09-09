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
	// Suspect is set when the pin claims mains while the pack is measurably draining.
	//
	// This is the failure the Caveat warns about, actually caught. The two sources are independent:
	// the pin is an absence of signal, while a falling percentage is a measurement, and a
	// measurement beats an absence. A daemon that trusted the pin alone would sit through a power
	// cut it could not see, so the contradiction is surfaced rather than resolved silently in favour
	// of the hardware.
	Suspect string `json:"suspect,omitempty"`
}

// Delivered is charge measured to have flowed, by integrating the INA219.
//
// Kept in its own group and named for what it is, because the whole point is that it is a
// measurement where the battery percentage is a model. The gauge was observed reporting 85% under
// load and 96% a minute after the load came off — cell sag read as depletion — so the two must never
// be presentable as the same kind of claim.
//
// # Why the totals are withheld by default
//
// Integrating a current only means something if you know which circuit it flows through, and on this
// board that is not established. Measured on hardware against a load that doubled, the INA219 read
// 525 mA while idle, 267 mA at full load, and a sustained 417 mA only after the load was killed. It
// was also flat across a mains transition, reading 0.264-0.271 A on battery and 0.266-0.301 A on
// mains. There is no correlation with the Pi's consumption in either direction, and Geekworm
// documents neither the chip nor its shunt.
//
// So the signal is real, stable and correctly read — and unidentified. Turning it into milliamp
// hours and then into an implied pack capacity would produce confident figures from a current whose
// circuit nobody can name, which is worse than reporting nothing: it looks like a measurement.
//
// The mean current and the coverage are still reported, because those are facts about the signal
// itself rather than interpretations of it. The totals require --trust-current, which is a claim the
// operator makes and the tool cannot.
type Delivered struct {
	MilliampHours float64 `json:"mah"`
	WattHours     float64 `json:"wh"`
	MeanCurrentA  float64 `json:"mean_current_a"`
	// CoveredS is time actually integrated; SpanS is wall clock. They differ whenever the tool was
	// not running, and the difference is the reader's only way to tell "this covers two hours" from
	// "this covers the eleven minutes somebody was watching".
	CoveredS float64 `json:"covered_s"`
	SpanS    float64 `json:"span_s"`
	// Note qualifies the figures; empty when they stand unqualified.
	Note string `json:"note,omitempty"`
	// Unverified is set when the current's circuit is not established, and is the reason the totals
	// are absent. Empty when the operator has asserted --trust-current.
	Unverified string `json:"unverified,omitempty"`
	// RuntimeS is how long a declared capacity would last at the measured mean current, and is
	// present only when a capacity was declared. It inherits that declaration's error in full.
	RuntimeS *float64 `json:"runtime_s,omitempty"`
	// CapacityMAh is the declared figure the runtime rests on, echoed so nobody has to guess which
	// number produced the answer.
	CapacityMAh *float64 `json:"declared_capacity_mah,omitempty"`
	// ImpliedCapacityMAh is the pack's capacity worked backwards from the gauge's own discharge
	// trend at the measured mean current, and it is the closest thing available to a measurement.
	//
	// Two independent sources are being combined: how fast the percentage is falling, and how much
	// current is actually flowing. If the pack really held its declared capacity those would agree.
	// Where they diverge, the declared figure is the suspect one — cells sold as 5000 mAh routinely
	// hold half that, and the arithmetic says by how much rather than leaving it as a suspicion.
	ImpliedCapacityMAh *float64 `json:"implied_capacity_mah,omitempty"`
}

// DriverDefaultShuntOhms is the value ina2xx assumes when nothing tells it otherwise.
//
// Worth naming because it is provably wrong on this board rather than merely unverified: 10 mOhm
// implies 1.34 W entering a board whose own PMIC reports 2.73 W delivered to rails downstream of it,
// and input cannot be less than what it feeds. Every current, power, mAh and Wh figure scales
// linearly with it, so a reading taken at the default is roughly half what it should be.
const DriverDefaultShuntOhms = 0.01

// PreciseShuntMV returns the shunt drop at the instrument's real resolution, in millivolts.
//
// The obvious source, in0_input, is quantised to whole millivolts by hwmon, and on this board the
// entire signal is only a few millivolts wide. Measured against a load that doubled, in0 moved
// 3, 3, 4, 5 while the current moved 266 -> 395 mA: the rounding step is around 37% of the signal,
// so a slope fitted to it is fitted to rounding error. That is what made the first calibration
// attempt refuse to produce a figure — correctly, but for an artefact rather than a real
// disagreement between the instruments.
//
// The current register carries the same measurement at 10 uV per LSB, a hundredfold better, because
// the driver computes it from the raw register before anything is rounded. Multiplying it back by
// the resistance the driver divided by recovers the true drop:
//
//	drop = curr1_input * shunt_resistor
//
// The driver's assumed resistance cancels exactly. It computed current as drop/assumed, so
// multiplying by assumed returns drop whatever the assumption was — which is what makes this usable
// for calibration, where the assumption is precisely the thing not yet known. Verified against the
// raw register read by hand before the drivers claimed the address: 266 mA x 0.01 ohm = 2.66 mV.
//
// in0_input is still what gets displayed, because it is what the kernel publishes. Nothing
// quantitative is built on it.
func (p *Power) PreciseShuntMV() (float64, bool) {
	if p == nil || p.CurrentA == nil || p.ShuntOhms == nil || *p.ShuntOhms <= 0 {
		return 0, false
	}
	return *p.CurrentA * *p.ShuntOhms * 1000, true
}

// ShuntUncalibrated reports whether the shunt resistance is still the driver's guess.
func (p *Power) ShuntUncalibrated() bool {
	return p != nil && p.ShuntOhms != nil && *p.ShuntOhms == DriverDefaultShuntOhms
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
	// Delivered is charge integrated from measured current, as distinct from the gauge's model.
	Delivered *Delivered `json:"delivered,omitempty"`
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
