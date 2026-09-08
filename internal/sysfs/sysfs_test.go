package sysfs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOSReadsAndTrims(t *testing.T) {
	root := t.TempDir()
	write(t, root, "class/power_supply/battery/capacity", "95\n")
	write(t, root, "class/power_supply/battery/temp", "   \n") // present but empty

	fs := OS(root)

	if got, ok := fs.Read("class/power_supply/battery/capacity"); !ok || got != "95" {
		t.Errorf("capacity = %q, %v; want \"95\", true", got, ok)
	}
	// An attribute the chip cannot answer is published as whitespace. That is not a reading.
	if got, ok := fs.Read("class/power_supply/battery/temp"); ok {
		t.Errorf("empty temp = %q, %v; want absent", got, ok)
	}
	if _, ok := fs.Read("class/power_supply/battery/nonexistent"); ok {
		t.Error("missing file reported as present")
	}
}

func TestOSDefaultsToSys(t *testing.T) {
	if fs := OS(""); fs.Read == nil || fs.Glob == nil {
		t.Fatal("OS(\"\") returned an unusable FS")
	}
	// Reading through the default root must not panic on a machine without the hardware.
	if _, ok := OS("").Read("class/power_supply/definitely-not-here/capacity"); ok {
		t.Error("unexpectedly found a device")
	}
}

func TestOSGlobReturnsPathsRelativeToRoot(t *testing.T) {
	root := t.TempDir()
	write(t, root, "class/hwmon/hwmon0/name", "cpu_thermal\n")
	write(t, root, "class/hwmon/hwmon6/name", "ina219\n")

	got := OS(root).Glob("class/hwmon/*")
	want := []string{"class/hwmon/hwmon0", "class/hwmon/hwmon6"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Glob = %v; want %v", got, want)
	}
}

func TestOSGlobBadPattern(t *testing.T) {
	if got := OS(t.TempDir()).Glob("["); got != nil {
		t.Errorf("malformed pattern returned %v; want nil", got)
	}
}

func TestRelativeFallsBackWhenItCannotRelativise(t *testing.T) {
	// Rel only errors when it cannot express one path in terms of the other at all — here an
	// absolute root against a relative target. Glob never produces that, but if it somehow did the
	// path is kept rather than silently dropped from the results.
	got := relative("/sys", []string{"relative/file"})
	if want := []string{"relative/file"}; !reflect.DeepEqual(got, want) {
		t.Errorf("relative = %v; want %v", got, want)
	}
	// A sibling path relativises successfully rather than erroring, which is worth pinning down
	// because it is the opposite of what the name suggests.
	if got := relative("/sys", []string{"/elsewhere/file"}); !reflect.DeepEqual(got, []string{"../elsewhere/file"}) {
		t.Errorf("sibling relative = %v", got)
	}
}

func TestMapReadAndGlob(t *testing.T) {
	fs := Map(map[string]string{
		"class/hwmon/hwmon6/name":    "ina219\n",
		"class/hwmon/hwmon0/name":    "cpu_thermal",
		"class/hwmon/hwmon1/nothing": "",
	})

	if got, ok := fs.Read("class/hwmon/hwmon6/name"); !ok || got != "ina219" {
		t.Errorf("Read = %q, %v; want \"ina219\", true", got, ok)
	}
	if _, ok := fs.Read("class/hwmon/hwmon1/nothing"); ok {
		t.Error("empty value reported as present")
	}
	if _, ok := fs.Read("absent"); ok {
		t.Error("absent key reported as present")
	}

	got := fs.Glob("class/hwmon/*")
	want := []string{"class/hwmon/hwmon0", "class/hwmon/hwmon1", "class/hwmon/hwmon6"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Glob = %v; want %v", got, want)
	}
}

func TestMapGlobMatchesFilesAsWellAsDirectories(t *testing.T) {
	fs := Map(map[string]string{"class/hwmon/hwmon6/name": "ina219"})
	if got := fs.Glob("class/hwmon/hwmon6/*"); !reflect.DeepEqual(got, []string{"class/hwmon/hwmon6/name"}) {
		t.Errorf("Glob = %v", got)
	}
}

func TestMapGlobIsDeterministic(t *testing.T) {
	// Map iteration order in Go is deliberately randomised. A Glob that inherited it would produce
	// a test that fails roughly once a month for reasons nobody can reproduce.
	files := map[string]string{}
	for _, n := range []string{"9", "3", "7", "1", "5"} {
		files["class/hwmon/hwmon"+n+"/name"] = "chip"
	}
	first := Map(files).Glob("class/hwmon/*")
	for i := 0; i < 50; i++ {
		if got := Map(files).Glob("class/hwmon/*"); !reflect.DeepEqual(got, first) {
			t.Fatalf("Glob order varied: %v then %v", first, got)
		}
	}
}

func TestContains(t *testing.T) {
	if !contains([]string{"a", "b"}, "b") {
		t.Error("contains missed a present value")
	}
	if contains([]string{"a"}, "z") || contains(nil, "z") {
		t.Error("contains found an absent value")
	}
}

func TestSortStrings(t *testing.T) {
	for _, tc := range []struct{ in, want []string }{
		{nil, nil},
		{[]string{"a"}, []string{"a"}},
		{[]string{"c", "a", "b"}, []string{"a", "b", "c"}},
		{[]string{"b", "b", "a"}, []string{"a", "b", "b"}},
	} {
		got := append([]string(nil), tc.in...)
		sortStrings(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("sortStrings(%v) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

func TestInt(t *testing.T) {
	fs := Map(map[string]string{
		"good":     "95\n",
		"negative": "-3",
		"words":    "Unknown",
	})
	if n, ok := Int(fs, "good"); !ok || n != 95 {
		t.Errorf("Int(good) = %d, %v", n, ok)
	}
	if n, ok := Int(fs, "negative"); !ok || n != -3 {
		t.Errorf("Int(negative) = %d, %v", n, ok)
	}
	// A non-numeric attribute is not a zero. Reporting it as one would turn "Unknown" into 0%.
	if _, ok := Int(fs, "words"); ok {
		t.Error("Int parsed a non-number")
	}
	if _, ok := Int(fs, "absent"); ok {
		t.Error("Int found an absent file")
	}
}

func TestScaled(t *testing.T) {
	fs := Map(map[string]string{"voltage_now": "4152500"})
	if v, ok := Scaled(fs, "voltage_now", 1e6); !ok || v != 4.1525 {
		t.Errorf("Scaled = %v, %v; want 4.1525, true", v, ok)
	}
	if _, ok := Scaled(fs, "absent", 1e6); ok {
		t.Error("Scaled found an absent file")
	}
	// Guarded rather than returning +Inf, which would render as a plausible-looking disaster.
	if _, ok := Scaled(fs, "voltage_now", 0); ok {
		t.Error("Scaled divided by zero")
	}
}

func TestPointerHelpers(t *testing.T) {
	fs := Map(map[string]string{
		"capacity": "95",
		"present":  "1",
		"missing":  "",
		"zero":     "0",
	})

	if got := IntPtr(fs, "capacity"); got == nil || *got != 95 {
		t.Errorf("IntPtr = %v", got)
	}
	if got := IntPtr(fs, "absent"); got != nil {
		t.Errorf("IntPtr(absent) = %v; want nil", got)
	}
	if got := ScaledPtr(fs, "capacity", 100); got == nil || *got != 0.95 {
		t.Errorf("ScaledPtr = %v", got)
	}
	if got := ScaledPtr(fs, "absent", 100); got != nil {
		t.Errorf("ScaledPtr(absent) = %v; want nil", got)
	}
	if got := BoolPtr(fs, "present"); got == nil || !*got {
		t.Errorf("BoolPtr(present) = %v", got)
	}
	if got := BoolPtr(fs, "zero"); got == nil || *got {
		t.Errorf("BoolPtr(zero) = %v; want pointer to false", got)
	}
	if got := BoolPtr(fs, "absent"); got != nil {
		t.Errorf("BoolPtr(absent) = %v; want nil", got)
	}
	// The distinction the whole package exists to preserve: a real zero is a value, not an absence.
	if got := IntPtr(fs, "zero"); got == nil || *got != 0 {
		t.Errorf("IntPtr(zero) = %v; want pointer to 0", got)
	}
}

func TestTextOr(t *testing.T) {
	fs := Map(map[string]string{"status": "Discharging"})
	if got := TextOr(fs, "status", "Unknown"); got != "Discharging" {
		t.Errorf("TextOr = %q", got)
	}
	if got := TextOr(fs, "absent", "Unknown"); got != "Unknown" {
		t.Errorf("TextOr(absent) = %q; want fallback", got)
	}
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
