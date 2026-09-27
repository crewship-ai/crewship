package server

import "testing"

func TestHostCPUPercentUsesDeltaAcrossAllCores(t *testing.T) {
	before, err := parseHostCPUCounters([]byte("cpu 100 0 0 100 0 0 0 0 0 0\ncpu0 1 2 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := parseHostCPUCounters([]byte("cpu 120 0 0 130 0 0 0 0 0 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := hostCPUPercent(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if got != 40 {
		t.Fatalf("CPU = %v%%, want 40%%", got)
	}
	if _, err := hostCPUPercent(after, before); err == nil {
		t.Fatal("counter rollback should be unavailable, not reported as idle")
	}
}
