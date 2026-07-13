package consolidate

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// #1044: lessons.md had no entry ceiling — distinct-per-event IDs accumulate
// forever, so every flock'd read/write and every memory.read{tier:lessons}
// grows unboundedly. writeLessonToDir now keeps the newest maxLessonEntries by
// CapturedAt.
func TestWriteLesson_EntryCap_KeepsNewest(t *testing.T) {
	// Shrink the cap so the test writes a handful of entries instead of 500+.
	orig := maxLessonEntries
	maxLessonEntries = 10
	defer func() { maxLessonEntries = orig }()

	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	total := maxLessonEntries + 5 // 15 distinct entries, cap 10

	for i := 0; i < total; i++ {
		entry := LessonEntry{
			ID:         fmt.Sprintf("lesson-%03d", i),
			Kind:       LessonKindNeutral,
			Source:     LessonSourceManual,
			Rule:       fmt.Sprintf("rule number %d", i),
			CapturedAt: base.Add(time.Duration(i) * time.Minute), // strictly increasing
		}
		if err := WriteLesson(context.Background(), dir, entry); err != nil {
			t.Fatalf("WriteLesson %d: %v", i, err)
		}
	}

	got, err := ReadLessons(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("ReadLessons: %v", err)
	}
	if len(got) != maxLessonEntries {
		t.Fatalf("len = %d, want cap %d", len(got), maxLessonEntries)
	}

	// The oldest `total-cap` entries (0..4) must have been evicted; the newest
	// `cap` (5..14) survive.
	surviving := make(map[string]bool, len(got))
	for _, e := range got {
		surviving[e.ID] = true
	}
	for i := 0; i < total-maxLessonEntries; i++ {
		if surviving[fmt.Sprintf("lesson-%03d", i)] {
			t.Errorf("oldest entry lesson-%03d should have been evicted", i)
		}
	}
	for i := total - maxLessonEntries; i < total; i++ {
		if !surviving[fmt.Sprintf("lesson-%03d", i)] {
			t.Errorf("newest entry lesson-%03d should have survived", i)
		}
	}
}

// Eviction picks victims by CapturedAt but must NOT reorder survivors on disk
// (the "retries order-stable on disk" guarantee). Insert with CapturedAt that
// is NON-monotonic vs insertion order, then assert the survivors keep their
// insertion order rather than a timestamp sort.
func TestWriteLesson_EntryCap_PreservesOnDiskOrder(t *testing.T) {
	orig := maxLessonEntries
	maxLessonEntries = 3
	defer func() { maxLessonEntries = orig }()

	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// Insertion order A,B,C,D. CapturedAt: A oldest (evicted), then D,B,C newer
	// but NOT in insertion order — survivors B,C,D must stay in insertion order.
	entries := []struct {
		id  string
		min int
	}{
		{"A", 0},  // oldest → evicted (cap 3, 4 entries)
		{"B", 30}, // survivor
		{"C", 40}, // survivor
		{"D", 20}, // survivor, but earliest-captured of the survivors
	}
	for _, e := range entries {
		if err := WriteLesson(context.Background(), dir, LessonEntry{
			ID: e.id, Kind: LessonKindNeutral, Source: LessonSourceManual,
			Rule: "r", CapturedAt: base.Add(time.Duration(e.min) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadLessons(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := make([]string, len(got))
	for i, e := range got {
		gotIDs[i] = e.ID
	}
	want := []string{"B", "C", "D"} // insertion order of survivors, NOT D,B,C by time
	if len(gotIDs) != len(want) {
		t.Fatalf("ids = %v, want %v", gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("on-disk order = %v, want insertion order %v (eviction reordered survivors)", gotIDs, want)
		}
	}
}

// Idempotent re-writes of the SAME id must not count toward the cap or evict
// unrelated entries — a corrected rule body overwrites in place.
func TestWriteLesson_EntryCap_IdempotentReplaceDoesNotEvict(t *testing.T) {
	orig := maxLessonEntries
	maxLessonEntries = 5
	defer func() { maxLessonEntries = orig }()

	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < maxLessonEntries; i++ {
		if err := WriteLesson(context.Background(), dir, LessonEntry{
			ID: fmt.Sprintf("l-%d", i), Kind: LessonKindNeutral, Source: LessonSourceManual,
			Rule: "r", CapturedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Re-write the oldest id with a corrected body — must stay, at the cap.
	if err := WriteLesson(context.Background(), dir, LessonEntry{
		ID: "l-0", Kind: LessonKindNeutral, Source: LessonSourceManual,
		Rule: "corrected", CapturedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLessons(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxLessonEntries {
		t.Fatalf("len = %d, want %d (idempotent replace must not grow past cap)", len(got), maxLessonEntries)
	}
	var found bool
	for _, e := range got {
		if e.ID == "l-0" {
			found = true
			if e.Rule != "corrected" {
				t.Errorf("l-0 rule = %q, want corrected", e.Rule)
			}
		}
	}
	if !found {
		t.Error("l-0 was evicted by an idempotent replace of itself")
	}
}
