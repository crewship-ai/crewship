package documents

import "testing"

// The content lineage (database identity, content generation) is what the
// public Docs epoch is derived from (#5163). A plain restart (engine boot
// fence, lease invalidation) must keep it, so a pending edit is replayed;
// History restore and schema upgrade replace content and must advance it.
func TestContentLineageSurvivesRestartAndAdvancesOnContentReplacement(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	read := func() State {
		t.Helper()
		v, e := s.Read(ctx, key, allow)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	initial := read()
	if len(initial.Database) < 32 || initial.ContentGeneration != 0 {
		t.Fatalf("initial lineage database=%q content=%d", initial.Database, initial.ContentGeneration)
	}
	var stored string
	if e := db.QueryRow(`SELECT value FROM document_database_identity WHERE id=1`).Scan(&stored); e != nil || stored != initial.Database {
		t.Fatalf("database identity row=%q read=%q: %v", stored, initial.Database, e)
	}
	for _, restart := range []struct {
		name string
		run  func() error
	}{
		{"engine boot", func() error { return s.RecoverEngine(ctx, "synthetic-lineage-engine") }},
		{"replacement engine boot", func() error { return s.RecoverEngine(ctx, "synthetic-lineage-next") }},
		{"lease invalidation", func() error { return s.InvalidateLeases(ctx) }},
	} {
		t.Run(restart.name, func(t *testing.T) {
			before := read()
			if e := restart.run(); e != nil {
				t.Fatal(e)
			}
			after := read()
			if after.Generation != before.Generation+1 || after.Epoch != before.Epoch+1 {
				t.Fatalf("restart did not fence writers: before=%+v after=%+v", before, after)
			}
			if after.Database != initial.Database || after.ContentGeneration != initial.ContentGeneration {
				t.Fatalf("restart changed content lineage: %q/%d", after.Database, after.ContentGeneration)
			}
		})
	}
	l := acquire(t, s)
	if _, e := s.Checkpoint(ctx, key, l, "lineage", []byte("lineage state"), 0, allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RestoreRequest(ctx, key, l, "lineage", "lineage-restore", 0, allow); e != nil {
		t.Fatal(e)
	}
	restored := read()
	if restored.Database != initial.Database || restored.ContentGeneration != 1 {
		t.Fatalf("History restore lineage %q/%d", restored.Database, restored.ContentGeneration)
	}
	l = acquire(t, s)
	generation := restored.Generation
	if _, e := s.UpgradeRequest(ctx, key, l, "lineage-upgrade", "lineage", restored.Schema, LatestSchema, restored.HeadRev, &generation, allow); e != nil {
		t.Fatal(e)
	}
	if upgraded := read(); upgraded.Database != initial.Database || upgraded.ContentGeneration != 2 {
		t.Fatalf("upgrade lineage %q/%d", upgraded.Database, upgraded.ContentGeneration)
	}
}
