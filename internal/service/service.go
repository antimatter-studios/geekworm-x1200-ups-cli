// Package service holds the systemd units, and is the single source of truth for them.
//
// The problem this solves is that the program is no longer just a binary. A Debian package can
// install a timer, a unit and a state directory; a tarball downloaded from the releases page
// installs a binary and nothing else. If those two paths carry different ideas of what the unit
// should say, then the tool behaves differently depending on how it was installed — and the
// difference shows up as missing data weeks later rather than as an error.
//
// So the unit text lives here, in Go. `x1200 systemd` prints it, which is what a tarball user pipes
// into /etc/systemd/system and what a configuration manager runs to get the canonical content. The
// Debian package ships a copy of the same text, generated from this one and checked by a test, so
// the two cannot drift apart without the test failing.
//
// Nothing here installs anything. Writing to /etc and running systemctl are state changes on a
// machine that may be described declaratively elsewhere, and a tool that reaches around that
// description to configure the machine itself is how a system stops matching what it says it is.
// This package produces text; something else decides what to do with it.
package service

import (
	"fmt"
	"strings"
	"time"
)

// Defaults for the recorded archive.
const (
	// StateDir follows the Filesystem Hierarchy Standard: /var/lib is for state a program needs to
	// keep across reboots, which is exactly what an accumulating record is.
	StateDir = "/var/lib/x1200"
	// ArchivePath is where `x1200 record` accumulates, and is the same store the reporting path
	// reads. One file: the estimator takes its recent tail, the integrator takes all of it.
	ArchivePath = StateDir + "/samples"

	// Interval is how often the timer fires.
	//
	// Five minutes is a compromise between resolution and writes. It is also the number the unit
	// passes to --max-gap, and those two must agree: the integrator refuses to integrate across a
	// gap longer than that tolerance, so a timer slower than the tolerance would silently produce a
	// coverage of zero and no charge figures at all.
	Interval = 5 * time.Minute
)

// Unit returns the service unit text.
//
// Type=oneshot because this is a sampler and not a daemon: it takes a reading, appends it, and
// exits. A long-running process would have to be supervised, would hold the GPIO line open, and
// would gain nothing — there is no state to keep in memory between samples, because the state is
// the file.
func Unit(binary string) string {
	return fmt.Sprintf(`[Unit]
Description=Record X1200 UPS battery and power state
Documentation=https://github.com/antimatter-studios/geekworm-x1200-ups-cli
# The sampler needs the I2C drivers bound and the GPIO line configured. Neither is a systemd unit,
# so this cannot be expressed as a dependency — it is why the unit tolerates failure rather than
# entering a failed state. A sample missed because the bus is not up yet is not an error worth
# waking anybody for; a sample missed for that reason forever is, and that is what
# 'x1200 doctor' is for.
After=local-fs.target

[Service]
Type=oneshot
ExecStart=%s record --archive %s --max-gap %s

# Reads sysfs and one GPIO line, writes one file under %s. Nothing else is needed, so nothing else
# is permitted: a sampler that can only do its job is a sampler that cannot be turned into anything
# more interesting by a bug.
User=root
StateDirectory=x1200
StateDirectoryMode=0755
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
NoNewPrivileges=yes
RestrictSUIDSGID=yes
RestrictRealtime=yes
MemoryDenyWriteExecute=yes
CapabilityBoundingSet=
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

# A reading that fails is not a reason to mark the unit broken. The bus may not be up, the HAT may
# be unseated, the pin may not be configured — all real conditions that the next sample may not
# share, and none improved by systemd remembering the failure.
SuccessExitStatus=1
`, binary, ArchivePath, Interval, StateDir)
}

// Timer returns the timer unit text.
//
// Persistent=true so that a machine which was off catches up with one sample on boot rather than
// silently skipping the whole outage. That matters here more than usual: the outage is the thing
// being measured, and the first sample after power returns is the one that shows the pack recovering.
func Timer() string {
	return fmt.Sprintf(`[Unit]
Description=Sample the X1200 UPS every %s
Documentation=https://github.com/antimatter-studios/geekworm-x1200-ups-cli

[Timer]
OnBootSec=1min
OnUnitActiveSec=%s
# Catch up with one sample after downtime rather than skipping it. The gap itself is data: the
# integrator refuses to integrate across it, so a missing period is visible as reduced coverage
# rather than being quietly averaged over.
Persistent=true
# Fixed rather than randomised. A jittered timer would put samples at unpredictable spacing, and the
# integrator's gap tolerance is set from this interval — spacing that wanders past the tolerance
# would drop intervals for no reason anybody could see.
AccuracySec=10s
Unit=x1200.service

[Install]
WantedBy=timers.target
`, Interval, Interval)
}

// Files returns every unit, keyed by the filename it belongs in.
func Files(binary string) map[string]string {
	return map[string]string{
		"x1200.service": Unit(binary),
		"x1200.timer":   Timer(),
	}
}

// Names returns the filenames in a stable order, so that output and packaging agree.
func Names() []string { return []string{"x1200.service", "x1200.timer"} }

// DefaultBinary is where the Debian package puts the binary, and therefore what the packaged unit
// refers to. A tarball user who installed elsewhere passes their own path.
const DefaultBinary = "/usr/bin/x1200"

// Instructions returns what to do with the text, for a reader who piped it somewhere.
//
// Printed rather than performed. Enabling a timer is a decision about what the machine does, and a
// package that makes it silently is a package that starts writing to a disk nobody asked it to.
func Instructions() string {
	var b strings.Builder
	b.WriteString("# Write these to /etc/systemd/system, then:\n")
	b.WriteString("#   systemctl daemon-reload\n")
	b.WriteString("#   systemctl enable --now x1200.timer\n")
	b.WriteString("# The Debian package installs both files already, but does not enable the timer:\n")
	b.WriteString("# whether the machine starts writing samples is a decision, not a default.\n")
	return b.String()
}
