package diskusage

import (
	"runtime"
	"testing"
)

func TestUsage_RealFilesystem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("statfs unsupported on windows")
	}
	s, err := Usage(t.TempDir())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if s.TotalBytes == 0 {
		t.Error("TotalBytes = 0, want a real capacity")
	}
	if s.FreeBytes > s.TotalBytes {
		t.Errorf("FreeBytes %d > TotalBytes %d", s.FreeBytes, s.TotalBytes)
	}
	if s.UsedBytes > s.TotalBytes {
		t.Errorf("UsedBytes %d > TotalBytes %d", s.UsedBytes, s.TotalBytes)
	}
	// df-style math: used counts reserved blocks, free is avail-to-
	// unprivileged, so used + free <= total (equal only when nothing is
	// reserved). It must never exceed total.
	if s.UsedBytes+s.FreeBytes > s.TotalBytes {
		t.Errorf("UsedBytes %d + FreeBytes %d > TotalBytes %d", s.UsedBytes, s.FreeBytes, s.TotalBytes)
	}
	if s.UsedPct < 0 || s.UsedPct > 100 {
		t.Errorf("UsedPct = %.2f, want 0..100", s.UsedPct)
	}
	if s.Path == "" {
		t.Error("Path not echoed back")
	}
}

func TestUsage_BadPathErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("statfs unsupported on windows")
	}
	if _, err := Usage("/no/such/path/really/nope"); err == nil {
		t.Error("Usage(nonexistent) = nil error, want failure")
	}
}
