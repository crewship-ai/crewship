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
	if s.UsedBytes != s.TotalBytes-s.FreeBytes {
		t.Errorf("UsedBytes %d != Total-Free %d", s.UsedBytes, s.TotalBytes-s.FreeBytes)
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
