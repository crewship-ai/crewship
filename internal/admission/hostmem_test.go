package admission

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const sampleMeminfo = `MemTotal:       16150312 kB
MemFree:          387764 kB
MemAvailable:    2097152 kB
Buffers:          123456 kB
Cached:          8123456 kB
SwapTotal:             0 kB
`

const samplePressure = `some avg10=12.34 avg60=4.56 avg300=1.23 total=987654
full avg10=0.50 avg60=0.10 avg300=0.02 total=12345
`

// MemAvailable is printed in KiB, so 2097152 kB is exactly 2048 MiB. A
// divide-by-1000 (the "MB" the label literally says) would give 2097, a 2.4%
// silent inflation of every headroom check.
func TestParseMeminfo_ReportsMiBNotMB(t *testing.T) {
	hm, err := parseMeminfo([]byte(sampleMeminfo))
	if err != nil {
		t.Fatalf("parseMeminfo: %v", err)
	}
	if hm.AvailableMB != 2048 {
		t.Errorf("AvailableMB = %d, want 2048 (2097152 KiB / 1024)", hm.AvailableMB)
	}
	if hm.TotalMB != 15771 {
		t.Errorf("TotalMB = %d, want 15771 (16150312 KiB / 1024, truncated)", hm.TotalMB)
	}
}

// A kernel without MemAvailable (pre-3.14) must report the signal as
// unavailable rather than silently gating on a zero — a zero would hold every
// run on the instance forever.
func TestParseMeminfo_MissingMemAvailable_IsUnavailableNotZero(t *testing.T) {
	_, err := parseMeminfo([]byte("MemTotal: 16150312 kB\nMemFree: 387764 kB\n"))
	if !errors.Is(err, ErrHostSignalUnavailable) {
		t.Fatalf("err = %v, want ErrHostSignalUnavailable", err)
	}
}

// An unrecognised unit must fail loudly. Accepting it would compare a number
// in some other scale against a MiB threshold.
func TestParseMeminfo_UnknownUnit_IsRejected(t *testing.T) {
	_, err := parseMeminfo([]byte("MemAvailable:    2097152 pages\n"))
	if !errors.Is(err, ErrHostSignalUnavailable) {
		t.Fatalf("err = %v, want ErrHostSignalUnavailable", err)
	}
}

func TestParsePressureSomeAvg10(t *testing.T) {
	v, ok := parsePressureSomeAvg10([]byte(samplePressure))
	if !ok {
		t.Fatal("parsePressureSomeAvg10: not ok")
	}
	// Must be the `some` line's avg10 (12.34), not `full`'s (0.50) and not
	// `some`'s avg60 (4.56).
	if v != 12.34 {
		t.Errorf("some avg10 = %v, want 12.34", v)
	}
}

func TestParsePressureSomeAvg10_AbsentFile_NotOK(t *testing.T) {
	if _, ok := parsePressureSomeAvg10([]byte("")); ok {
		t.Error("empty PSI file reported ok; a missing reading must not read as 0 pressure")
	}
}

// The macOS case, expressed exactly as it occurs: /proc/meminfo does not
// exist. No build tags — the platform difference IS the missing file, so this
// test runs and means the same thing on Linux and on darwin.
func TestReadHostMemoryFrom_NoProcFS_ReportsUnavailable(t *testing.T) {
	dir := t.TempDir()
	_, err := readHostMemoryFrom(filepath.Join(dir, "meminfo"), filepath.Join(dir, "pressure"))
	if !errors.Is(err, ErrHostSignalUnavailable) {
		t.Fatalf("err = %v, want ErrHostSignalUnavailable", err)
	}
}

// A Linux host with CONFIG_PSI=n still has MemAvailable. The reading must
// succeed with SomeStallPct == PressureUnknown, not fail and not read as 0.
func TestReadHostMemoryFrom_NoPSI_StillReadsMemAvailable(t *testing.T) {
	dir := t.TempDir()
	meminfo := filepath.Join(dir, "meminfo")
	if err := os.WriteFile(meminfo, []byte(sampleMeminfo), 0o600); err != nil {
		t.Fatal(err)
	}
	hm, err := readHostMemoryFrom(meminfo, filepath.Join(dir, "absent-pressure"))
	if err != nil {
		t.Fatalf("readHostMemoryFrom: %v", err)
	}
	if hm.AvailableMB != 2048 {
		t.Errorf("AvailableMB = %d, want 2048", hm.AvailableMB)
	}
	if hm.SomeStallPct != PressureUnknown {
		t.Errorf("SomeStallPct = %v, want PressureUnknown (%v)", hm.SomeStallPct, PressureUnknown)
	}
}

func TestReadHostMemoryFrom_WithPSI(t *testing.T) {
	dir := t.TempDir()
	meminfo := filepath.Join(dir, "meminfo")
	pressure := filepath.Join(dir, "pressure")
	if err := os.WriteFile(meminfo, []byte(sampleMeminfo), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pressure, []byte(samplePressure), 0o600); err != nil {
		t.Fatal(err)
	}
	hm, err := readHostMemoryFrom(meminfo, pressure)
	if err != nil {
		t.Fatalf("readHostMemoryFrom: %v", err)
	}
	if hm.SomeStallPct != 12.34 {
		t.Errorf("SomeStallPct = %v, want 12.34", hm.SomeStallPct)
	}
}
