package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test that makes this package a single source of truth rather than one copy among several.
//
// The Debian package and the tarball ship generated files; `x1200 systemd` prints from the code.
// If those diverge, the tool samples differently depending on how it was installed, and the symptom
// is missing data weeks later with nothing to point at. Regenerate with `make units`.
func TestPackagedUnitsMatchTheCode(t *testing.T) {
	for _, name := range Names() {
		path := filepath.Join("..", "..", "packaging", name)
		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run `make units`)", path, err)
		}
		if want := Files(DefaultBinary)[name]; string(onDisk) != want {
			t.Errorf("%s has drifted from internal/service; run `make units`", path)
		}
	}
}

// The interaction that would fail silently: the integrator refuses gaps longer than its tolerance,
// so a timer slower than the tolerance it passes yields a coverage of zero and no charge figures at
// all — not an error, just absent output.
func TestUnitPassesAGapToleranceThatMatchesTheTimer(t *testing.T) {
	unit := Unit(DefaultBinary)
	if !strings.Contains(unit, "--max-gap "+Interval.String()) {
		t.Errorf("unit does not pass --max-gap %s:\n%s", Interval, unit)
	}
	if !strings.Contains(Timer(), "OnUnitActiveSec="+Interval.String()) {
		t.Errorf("timer interval does not match Interval %s", Interval)
	}
}

func TestUnitIsAOneshotThatWritesTheArchive(t *testing.T) {
	unit := Unit(DefaultBinary)
	for _, want := range []string{"Type=oneshot", "ExecStart=" + DefaultBinary + " record", ArchivePath, "StateDirectory=x1200"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
}

// A sampler needs sysfs, one GPIO line and one file. Anything more is a bug waiting to be
// interesting, so the hardening directives are asserted rather than assumed.
func TestUnitIsHardened(t *testing.T) {
	unit := Unit(DefaultBinary)
	for _, want := range []string{
		"ProtectSystem=strict", "ProtectHome=yes", "NoNewPrivileges=yes",
		"CapabilityBoundingSet=", "SystemCallFilter=@system-service",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing hardening %q", want)
		}
	}
}

// A bus that is not up yet, an unseated HAT, an unconfigured pin: all real, all transient, none
// improved by systemd remembering the failure and refusing to try again.
func TestUnitToleratesAFailedSample(t *testing.T) {
	if !strings.Contains(Unit(DefaultBinary), "SuccessExitStatus=1") {
		t.Error("a failed sample would mark the unit broken")
	}
}

// The outage is the thing being measured, so the first sample after power returns is the valuable
// one. Persistent=true is what makes the machine take it rather than skip the whole gap.
func TestTimerCatchesUpAfterDowntime(t *testing.T) {
	timer := Timer()
	if !strings.Contains(timer, "Persistent=true") {
		t.Error("timer would silently skip an outage")
	}
	if !strings.Contains(timer, "WantedBy=timers.target") {
		t.Error("timer cannot be enabled")
	}
}

// A tarball user who installed somewhere other than /usr/bin needs the unit to point at their path.
func TestUnitHonoursACustomBinaryPath(t *testing.T) {
	if !strings.Contains(Unit("/opt/x1200/bin/x1200"), "ExecStart=/opt/x1200/bin/x1200 record") {
		t.Error("--binary is not reflected in ExecStart")
	}
}

func TestIntervalIsSane(t *testing.T) {
	if Interval < time.Minute || Interval > time.Hour {
		t.Errorf("Interval = %s; outside anything defensible for a sampler", Interval)
	}
}
