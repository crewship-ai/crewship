package scheduler

import (
	"testing"
	"time"
)

func TestNextOccurrenceUsesSchedulerLocalTime(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("UTC+2", 2*60*60)
	t.Cleanup(func() { time.Local = old })
	next, err := NextOccurrence("0 9 * * *", time.Date(2026, 9, 23, 6, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 23, 7, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next=%s, want %s (09:00 local)", next, want)
	}
}
