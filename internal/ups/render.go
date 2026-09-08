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

// Text renders the reading for a person.
func Text(r *Reading) string {
	if r == nil {
		return ""
	}
	lines := make([]string, 0, 4)
	if line := batteryLine(r.Battery); line != "" {
		lines = append(lines, line)
	}
	lines = append(lines, powerLines(r.Power)...)
	if line := estimateLine(r.Estimate); line != "" {
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// estimateLine renders the time remaining, or why there is not one.
//
// An absent estimate is printed rather than omitted. Silence would be indistinguishable from the
// feature not existing, and "not enough history yet" is a useful thing to be told — it means leave
// it running, which is exactly what a reader needs to know.
func estimateLine(e *estimate.Estimate) string {
	if e == nil {
		return ""
	}
	parts := []string{label("remaining")}
	if e.TimeToEmpty != nil {
		parts = append(parts, humanDuration(*e.TimeToEmpty))
	} else {
		parts = append(parts, "—")
	}
	if e.PercentPerHour != nil && e.State == estimate.Discharging {
		parts = append(parts, fmt.Sprintf("%.1f%%/h", *e.PercentPerHour))
	}
	if e.Note != "" {
		parts = append(parts, "("+e.Note+")")
	} else if e.State != estimate.Discharging {
		parts = append(parts, "("+string(e.State)+")")
	}
	return strings.Join(parts, "   ")
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

// batteryLine renders the gauge, or "" when there is none.
func batteryLine(b *Battery) string {
	if b == nil {
		return ""
	}
	parts := []string{label("battery")}
	if b.Percent != nil {
		parts = append(parts, fmt.Sprintf("%3d%%", *b.Percent))
	} else {
		parts = append(parts, "  ?%")
	}
	if b.VoltageV != nil {
		parts = append(parts, fmt.Sprintf("%.3f V", *b.VoltageV))
	}
	parts = append(parts, strings.ToLower(b.Status))
	if b.Present != nil && !*b.Present {
		parts = append(parts, "NO PACK FITTED")
	}
	return strings.Join(parts, "   ")
}

// powerLines renders the rail, and separately the shunt it was derived from.
//
// The shunt gets its own line because it is the one number that silently scales the three above it.
// Anyone reading a wattage that looks wrong should be able to see what it was divided by without
// going looking.
func powerLines(p *Power) []string {
	if p == nil {
		return nil
	}
	parts := []string{label("power")}
	if p.BusV != nil {
		parts = append(parts, fmt.Sprintf("%.2f V", *p.BusV))
	}
	if p.CurrentA != nil {
		parts = append(parts, fmt.Sprintf("%.3f A", *p.CurrentA))
	}
	if p.WattsW != nil {
		parts = append(parts, fmt.Sprintf("%.2f W", *p.WattsW))
	}
	lines := []string{strings.Join(parts, "   ")}

	detail := make([]string, 0, 3)
	if p.ShuntOhms != nil {
		detail = append(detail, fmt.Sprintf("shunt %.3f mOhm", *p.ShuntOhms*1000))
	}
	if p.ShuntMV != nil {
		detail = append(detail, fmt.Sprintf("drop %.0f mV", *p.ShuntMV))
	}
	if p.Chip != "" {
		detail = append(detail, p.Chip)
	}
	if len(detail) > 0 {
		lines = append(lines, label("")+strings.Join(detail, ", "))
	}
	return lines
}

// label pads a row's first column so the values line up.
func label(name string) string {
	const width = 10
	if len(name) >= width {
		return name + " "
	}
	return name + strings.Repeat(" ", width-len(name))
}
