package ups

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/estimate"
)

// JSON renders the reading as machine-readable output.
//
// Absent values are omitted rather than rendered as zero, preserving the same distinction the
// pointer fields exist for: no "percent" key means the gauge cannot report one, whereas
// "percent": 0 means the pack is flat.
func JSON(r *Reading) (string, error) {
	if r == nil {
		return "", ErrNoDevices
	}
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

// row is one labelled value.
type row struct{ key, value string }

// group is a named set of rows, rendered as a block.
type group struct {
	name string
	rows []row
}

// Text renders the reading for a person.
//
// Every value carries its own key. The earlier layout put values in columns and left the reader to
// work out what each one was, which failed on the gauge's status in particular: a bare "unknown"
// sitting at the end of a line gives no clue that it is describing charge direction, and this
// hardware prints it every single time.
//
// Keys are aligned across all groups rather than within each one, so the values form a single
// column down the page and the eye has one thing to follow rather than three.
func Text(r *Reading) string {
	if r == nil {
		return ""
	}
	groups := []group{
		{name: "battery", rows: batteryRows(r.Battery, r.Estimate, r.Supply)},
		{name: "power", rows: powerRows(r.Power)},
		{name: "supply", rows: supplyRows(r.Supply, r.Charging)},
		{name: "delivered", rows: deliveredRows(r.Delivered)},
		{name: "estimate", rows: estimateRows(r.Estimate)},
	}

	width := 0
	for _, g := range groups {
		for _, row := range g.rows {
			if n := len(row.key); n > width {
				width = n
			}
		}
	}
	if width == 0 {
		return ""
	}

	var blocks []string
	for _, g := range groups {
		if len(g.rows) == 0 {
			continue
		}
		lines := []string{g.name}
		for _, row := range g.rows {
			// +1 for the colon, so the values align rather than the keys.
			lines = append(lines, fmt.Sprintf("  %-*s %s", width+1, row.key+":", row.value))
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// unknownStatus is what the kernel reports for this gauge, permanently.
const unknownStatus = "unknown"

// inferred returns the direction the samples imply, or Unknown when they imply nothing.
//
// Only a definite direction counts. Steady means the percentage has not moved, which on a pack that
// is genuinely on mains and full looks identical to one on battery that has not yet lost a whole
// percent — so it is not evidence of either and must not be presented as though it were.
func inferred(e *estimate.Estimate) estimate.State {
	if e == nil {
		return estimate.Unknown
	}
	switch e.State {
	case estimate.Discharging, estimate.Charging:
		return e.State
	}
	return estimate.Unknown
}

// batteryRows renders the gauge.
//
// The estimate is passed in for one reason: the gauge's own status field reads "unknown" on this
// hardware permanently, and printing that beside an estimate that has worked out the direction from
// the samples is contradictory on its face. Where the samples imply a direction, say so and label it
// as inferred — it is a weaker claim than a hardware signal and must not be dressed up as one.
//
// This is a stand-in. The X1200 reports mains presence on GPIO6, which is instant and authoritative
// where an inference needs minutes of history and cannot distinguish a pack sitting full on mains
// from one on battery that has not yet dropped a percent. Once that pin is read, it supersedes this.
func batteryRows(b *Battery, e *estimate.Estimate, supply *Supply) []row {
	if b == nil {
		return nil
	}
	rows := make([]row, 0, 4)
	if b.Percent != nil {
		rows = append(rows, row{"charge", fmt.Sprintf("%d%%", *b.Percent)})
	} else {
		// Distinct from 0%: the gauge cannot say, which is not the same as an empty pack.
		rows = append(rows, row{"charge", "unreadable"})
	}
	if b.VoltageV != nil {
		rows = append(rows, row{"voltage", fmt.Sprintf("%.3f V", *b.VoltageV)})
	}

	state := strings.ToLower(b.Status)
	if state == unknownStatus {
		switch {
		case supply != nil && !supply.OnMains:
			state = "on battery — mains lost"
		case supply != nil:
			state = "on mains"
		case inferred(e) == estimate.Discharging:
			state = "discharging — inferred from samples, not measured"
		case inferred(e) == estimate.Charging:
			state = "charging — inferred from samples, not measured"
		default:
			// Explained inline because it never improves on its own and is otherwise the most
			// confusing thing in the output. A fuel gauge measures charge; direction is the
			// charger's business, and there is no charger chip on the bus to ask.
			state += " — gauge cannot tell charging from discharging"
		}
	}
	rows = append(rows, row{"state", state})

	if b.Present != nil && !*b.Present {
		rows = append(rows, row{"pack", "NOT FITTED"})
	}
	return rows
}

// powerRows renders the rail.
//
// The shunt is shown with the measurement it came from because it is the one number that silently
// scales the three above it: current and draw are both derived from the drop divided by this
// resistance, so a reader whose wattage looks wrong can see what it was divided by without going
// looking for it.
func powerRows(p *Power) []row {
	if p == nil {
		return nil
	}
	rows := make([]row, 0, 4)
	if p.BusV != nil {
		rows = append(rows, row{"bus", fmt.Sprintf("%.2f V", *p.BusV)})
	}
	if p.CurrentA != nil {
		rows = append(rows, row{"current", fmt.Sprintf("%.3f A", *p.CurrentA)})
	}
	if p.WattsW != nil {
		rows = append(rows, row{"draw", fmt.Sprintf("%.2f W", *p.WattsW)})
	}
	if p.ShuntOhms != nil {
		detail := make([]string, 0, 2)
		if p.ShuntMV != nil {
			detail = append(detail, fmt.Sprintf("drop %.0f mV", *p.ShuntMV))
		}
		if p.Chip != "" {
			detail = append(detail, p.Chip)
		}
		value := fmt.Sprintf("%.3f mOhm", *p.ShuntOhms*1000)
		if len(detail) > 0 {
			value += "  (" + strings.Join(detail, ", ") + ")"
		}
		rows = append(rows, row{"shunt", value})
		// Said in the output rather than only in the README, because every current, power, mAh and
		// Wh figure above and below is scaled by this constant. A reader who does not know it is the
		// driver's untouched guess has no way to tell that the numbers are about half what they
		// should be — and "about half" is the difference between a UPS lasting an hour and two.
		if p.ShuntUncalibrated() {
			rows = append(rows, row{"UNCALIBRATED",
				"shunt is the ina2xx default and is provably wrong here; every current, power, mAh and Wh figure is likely ~2x low. run `x1200 calibrate`"})
		}
	}
	return rows
}

// supplyRows renders where the power is coming from, and whether charging is permitted.
//
// This is the group a reader looks at first during an outage, and the one that was missing entirely
// until GPIO6 was read: everything else in the output describes the pack, not the situation.
func supplyRows(s *Supply, c *Charging) []row {
	rows := make([]row, 0, 3)
	if s != nil {
		source := "battery — MAINS LOST"
		if s.OnMains {
			source = "mains"
		}
		rows = append(rows, row{"source", fmt.Sprintf("%s  (GPIO%d)", source, s.Line)})
		// Above the caveat, and worded as a warning rather than a footnote: this is the caveat
		// having actually happened, not a general reservation about the sensor.
		if s.Suspect != "" {
			rows = append(rows, row{"SUSPECT", s.Suspect})
		}
		// Only shown when on mains: the caveat is that a stuck-high line reads as healthy, so it
		// qualifies a positive reading and has nothing to say about a negative one.
		if s.OnMains && s.Caveat != "" {
			rows = append(rows, row{"caveat", s.Caveat})
		}
	}
	if c != nil {
		state := "disabled"
		if c.Enabled {
			state = "enabled"
		}
		rows = append(rows, row{"charging", fmt.Sprintf("%s  (GPIO%d)", state, c.Line)})
	}
	return rows
}

// deliveredRows renders charge measured to have flowed.
//
// Separate from the battery group on purpose. The percentage is a model and this is a measurement,
// and the gauge has been observed reporting an eleven-point swing in sixty seconds that no charge
// movement could account for. Presenting the two side by side under one heading would invite exactly
// the confusion the group exists to prevent.
func deliveredRows(d *Delivered) []row {
	if d == nil {
		return nil
	}
	rows := []row{
		{"charge", fmt.Sprintf("%.1f mAh", d.MilliampHours)},
		{"energy", fmt.Sprintf("%.2f Wh", d.WattHours)},
		{"mean", fmt.Sprintf("%.3f A", d.MeanCurrentA)},
	}
	// Both times, always, and never just one. Equal values say the tool ran throughout; a covered
	// time well below the span says the totals describe only the minutes somebody was watching.
	rows = append(rows, row{"measured over", fmt.Sprintf("%s of %s elapsed",
		humanDuration(time.Duration(d.CoveredS)*time.Second),
		humanDuration(time.Duration(d.SpanS)*time.Second))})

	if d.RuntimeS != nil && d.CapacityMAh != nil {
		rows = append(rows, row{"runtime", fmt.Sprintf("%s at this rate  (assumes %.0f mAh declared, NOT measured)",
			humanDuration(time.Duration(*d.RuntimeS)*time.Second), *d.CapacityMAh)})
	}
	if d.ImpliedCapacityMAh != nil {
		value := fmt.Sprintf("%.0f mAh, from the discharge trend at the measured current", *d.ImpliedCapacityMAh)
		if d.CapacityMAh != nil && *d.CapacityMAh > 0 {
			ratio := *d.CapacityMAh / *d.ImpliedCapacityMAh
			if ratio >= 1.5 {
				value += fmt.Sprintf("  —  the declared %.0f mAh is %.1fx higher", *d.CapacityMAh, ratio)
			}
		}
		rows = append(rows, row{"implied capacity", value})
	}
	if d.Note != "" {
		rows = append(rows, row{"note", d.Note})
	}
	return rows
}

// estimateRows renders the time remaining, or why there is not one.
//
// An absent estimate is still printed. Silence would be indistinguishable from the feature not
// existing, whereas "not enough history yet" tells a reader to leave it running, which is exactly
// what they need to know.
func estimateRows(e *estimate.Estimate) []row {
	if e == nil {
		return nil
	}
	rows := []row{{"state", string(e.State)}}
	if e.TimeToEmpty != nil {
		rows = append(rows, row{"remaining", humanDuration(*e.TimeToEmpty)})
	}
	if e.PercentPerHour != nil && e.State == estimate.Discharging {
		rows = append(rows, row{"rate", fmt.Sprintf("%.1f%%/h", *e.PercentPerHour)})
	}
	if e.Note != "" {
		rows = append(rows, row{"note", e.Note})
	}
	return rows
}

// humanDuration renders a duration the way somebody worried about a UPS wants to read it.
//
// Deliberately coarse. A Go Duration prints as "4h37m12.483s", and the seconds there are false
// precision: the estimate behind them is a median of noisy slopes and is not accurate to the second,
// so printing one invites more trust than the number deserves.
func humanDuration(d time.Duration) string {
	if d < 0 {
		return "—"
	}
	if d < time.Minute {
		return "under a minute"
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if hours == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dh%02dm", hours, mins)
}
