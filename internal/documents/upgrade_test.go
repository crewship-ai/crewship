package documents

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestDocumentSchemaThreeUpgradePreservesBytesFencesAndExactAuthorizedRetry(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	k := Key{key.WorkspaceID, "page-c"}
	if _, e := s.InitRequest(ctx, k, "init-two", "init", 2, []byte("original-binary"), projection, allow); e != nil {
		t.Fatal(e)
	}
	lease, e := s.Acquire(ctx, k, "engine", allow)
	if e != nil {
		t.Fatal(e)
	}
	before, e := s.Read(ctx, k, allow)
	if e != nil {
		t.Fatal(e)
	}
	generation := before.Generation
	r, e := s.UpgradeRequest(ctx, k, lease, "upgrade-three", "original", 2, LatestSchema, 0, &generation, allow)
	if e != nil {
		t.Fatal(e)
	}
	after, e := s.Read(ctx, k, allow)
	if e != nil {
		t.Fatal(e)
	}
	if after.Schema != LatestSchema || after.HeadRev != 1 || after.Epoch != before.Epoch+1 || after.Generation != before.Generation+1 || !bytes.Equal(after.Snapshot, before.Snapshot) || !bytes.Equal(after.Projection, before.Projection) || len(after.Updates) != 1 || !bytes.Equal(after.Updates[0].Payload, []byte{0, 0}) {
		t.Fatalf("upgrade changed original content or failed fences: before=%+v after=%+v", before, after)
	}
	if _, e = s.Commit(ctx, k, Commit{Lease: lease, ID: "old-client", IfRev: 1, Schema: 2, Update: []byte("stale"), CandidateSnapshot: []byte("stale"), Projection: projection, Kind: "human"}, allow); !errors.Is(e, ErrFence) {
		t.Fatalf("old writer admitted: %v", e)
	}
	if e = s.RecoverEngine(ctx, "replacement-engine"); e != nil {
		t.Fatal(e)
	}
	retry, e := s.UpgradeRequest(ctx, k, lease, "upgrade-three", "original", 2, LatestSchema, 0, &generation, allow)
	if e != nil || retry != r {
		t.Fatalf("exact retry changed receipt: %+v %v", retry, e)
	}
	if _, e = s.UpgradeRequest(ctx, k, lease, "upgrade-three", "changed", 2, LatestSchema, 0, &generation, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("changed retry admitted: %v", e)
	}
	revoked := func(context.Context, *sql.Tx) (string, error) { return "", ErrNotFound }
	if _, e = s.UpgradeRequest(ctx, k, lease, "upgrade-three", "original", 2, LatestSchema, 0, &generation, revoked); !errors.Is(e, ErrNotFound) {
		t.Fatalf("revoked retry admitted: %v", e)
	}
	var count int
	if e = db.QueryRow("SELECT count(*) FROM document_receipts WHERE workspace_id=? AND page_id=? AND kind='upgrade'", k.WorkspaceID, k.PageID).Scan(&count); e != nil || count != 1 {
		t.Fatalf("duplicate revision/receipt: %d %v", count, e)
	}
}
func TestDocumentSchemaThreeCannotUpgradeThroughOrdinaryCommit(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	k := Key{key.WorkspaceID, "page-c"}
	if _, e := s.InitRequest(ctx, k, "init-two", "init", 2, []byte("binary"), projection, allow); e != nil {
		t.Fatal(e)
	}
	lease, e := s.Acquire(ctx, k, "engine", allow)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Commit(ctx, k, Commit{Lease: lease, ID: "implicit-upgrade", IfRev: 0, Schema: 3, Update: []byte{0, 0}, CandidateSnapshot: []byte("binary"), Projection: projection, Kind: "human"}, allow); !errors.Is(e, ErrSchema) {
		t.Fatalf("implicit schema upgrade admitted: %v", e)
	}
	v, e := s.Read(ctx, k, allow)
	if e != nil || v.Schema != 2 || v.HeadRev != 0 {
		t.Fatalf("denied upgrade changed head: %+v %v", v, e)
	}
}
func TestDocumentSchemaThreeUpgradeRollbackAndStalePreconditions(t *testing.T) {
	for _, mode := range []string{"stale", "quota", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			opts := Options{}
			if mode == "quota" {
				opts.MaxCommits = 1
			}
			s, db, _ := fixture(t, opts)
			k := Key{key.WorkspaceID, "page-c"}
			if _, e := s.InitRequest(ctx, k, "init-two", "init", 2, []byte("binary"), projection, allow); e != nil {
				t.Fatal(e)
			}
			lease, e := s.Acquire(ctx, k, "engine", allow)
			if e != nil {
				t.Fatal(e)
			}
			generation := int64(0)
			if mode == "stale" {
				generation = 99
			}
			if mode == "rollback" {
				if _, e = db.Exec("CREATE TRIGGER deny_upgrade_receipt BEFORE INSERT ON document_receipts WHEN NEW.kind='upgrade' BEGIN SELECT RAISE(ABORT,'injected failure'); END"); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = s.UpgradeRequest(ctx, k, lease, "upgrade", "original", 2, LatestSchema, 0, &generation, allow); e == nil {
				t.Fatal("invalid upgrade admitted")
			}
			v, e := s.Read(ctx, k, allow)
			if e != nil || v.Schema != 2 || v.HeadRev != 0 || len(v.Updates) != 0 {
				t.Fatalf("denied upgrade changed original state: %+v %v", v, e)
			}
		})
	}
}

func TestDocumentSchemaFiveUpgradeFromEverySupportedSchema(t *testing.T) {
	for _, from := range []int{1, 2, 3, 4} {
		s, _, _ := fixture(t, Options{})
		k := Key{key.WorkspaceID, "page-c"}
		if _, e := s.InitRequest(ctx, k, "init", "init", from, []byte("binary"), projection, allow); e != nil {
			t.Fatal(from, e)
		}
		lease, e := s.Acquire(ctx, k, "engine", allow)
		if e != nil {
			t.Fatal(e)
		}
		generation := int64(0)
		for _, target := range []int{from, 3, 4, 6} {
			if target == LatestSchema {
				continue
			}
			if _, e = s.UpgradeRequest(ctx, k, lease, "wrong-target", "original", from, target, 0, &generation, allow); !errors.Is(e, ErrSchema) {
				t.Fatalf("schema %d -> %d admitted: %v", from, target, e)
			}
		}
		if _, e = s.UpgradeRequest(ctx, k, lease, "upgrade-five", "original", from, LatestSchema, 0, &generation, allow); e != nil {
			t.Fatalf("schema %d upgrade: %v", from, e)
		}
		v, e := s.Read(ctx, k, allow)
		if e != nil || v.Schema != 5 || string(v.Snapshot) != "binary" || v.HeadRev != 1 {
			t.Fatalf("schema %d upgrade state: %+v %v", from, v, e)
		}
		lease, e = s.Acquire(ctx, k, "engine", allow)
		if e != nil {
			t.Fatal(e)
		}
		generation = v.Generation
		if _, e = s.UpgradeRequest(ctx, k, lease, "upgrade-again", "original", 5, LatestSchema, 1, &generation, allow); !errors.Is(e, ErrSchema) {
			t.Fatalf("schema 5 upgraded again: %v", e)
		}
	}
	if !RichSchema(3) || !RichSchema(4) || !RichSchema(5) || RichSchema(2) || RichSchema(6) {
		t.Fatal("rich schema family changed")
	}
}

// #5155 follow-up: restoring history recorded before the schema 5 upgrade must
// not take the Document back below schema 5. Older content is valid schema 5
// content (the upgrade is a pure schema bump), and a silent downgrade would
// re-enable schema 4 agent proposals on a Document that may hold mentions.
func TestDocumentRestoreKeepsLatestSchema(t *testing.T) {
	for _, from := range []int{1, 2, 3, 4} {
		s, _, _ := fixture(t, Options{})
		k := Key{key.WorkspaceID, "page-c"}
		if _, e := s.InitRequest(ctx, k, "init", "init", from, []byte("binary"), projection, allow); e != nil {
			t.Fatal(from, e)
		}
		lease, e := s.Acquire(ctx, k, "engine", allow)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Checkpoint(ctx, k, lease, "before-upgrade", []byte("old content"), 0, allow); e != nil {
			t.Fatal(from, e)
		}
		v, e := s.Read(ctx, k, allow)
		if e != nil {
			t.Fatal(e)
		}
		generation := v.Generation
		if _, e = s.UpgradeRequest(ctx, k, lease, "upgrade-five", "original", from, LatestSchema, v.HeadRev, &generation, allow); e != nil {
			t.Fatalf("schema %d upgrade: %v", from, e)
		}
		if lease, e = s.Acquire(ctx, k, "engine", allow); e != nil {
			t.Fatal(e)
		}
		v, e = s.Read(ctx, k, allow)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RestoreRequest(ctx, k, lease, "before-upgrade", "restore", v.HeadRev, allow); e != nil {
			t.Fatalf("schema %d restore: %v", from, e)
		}
		after, e := s.Read(ctx, k, allow)
		if e != nil || after.Schema != LatestSchema || string(after.Snapshot) != "old content" {
			t.Fatalf("schema %d checkpoint restored onto schema 5: %+v %v", from, after, e)
		}
		if after.ContentGeneration != v.ContentGeneration+1 {
			t.Fatalf("schema %d restore must advance content lineage exactly once: before=%d after=%d", from, v.ContentGeneration, after.ContentGeneration)
		}
	}
}

// Below the latest schema a restore keeps the checkpoint's own schema.
func TestDocumentRestoreBelowLatestKeepsCheckpointSchema(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	k := Key{key.WorkspaceID, "page-c"}
	if _, e := s.InitRequest(ctx, k, "init", "init", 4, []byte("binary"), projection, allow); e != nil {
		t.Fatal(e)
	}
	lease, e := s.Acquire(ctx, k, "engine", allow)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Checkpoint(ctx, k, lease, "four", []byte("four content"), 0, allow); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RestoreRequest(ctx, k, lease, "four", "restore", 0, allow); e != nil {
		t.Fatal(e)
	}
	if after, e := s.Read(ctx, k, allow); e != nil || after.Schema != 4 {
		t.Fatalf("schema 4 restore changed schema: %+v %v", after, e)
	}
}
