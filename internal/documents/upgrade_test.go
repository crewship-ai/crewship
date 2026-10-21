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
	r, e := s.UpgradeRequest(ctx, k, lease, "upgrade-three", "original", 2, 3, 0, &generation, allow)
	if e != nil {
		t.Fatal(e)
	}
	after, e := s.Read(ctx, k, allow)
	if e != nil {
		t.Fatal(e)
	}
	if after.Schema != 3 || after.HeadRev != 1 || after.Epoch != before.Epoch+1 || after.Generation != before.Generation+1 || !bytes.Equal(after.Snapshot, before.Snapshot) || !bytes.Equal(after.Projection, before.Projection) || len(after.Updates) != 1 || !bytes.Equal(after.Updates[0].Payload, []byte{0, 0}) {
		t.Fatalf("upgrade changed original content or failed fences: before=%+v after=%+v", before, after)
	}
	if _, e = s.Commit(ctx, k, Commit{Lease: lease, ID: "old-client", IfRev: 1, Schema: 2, Update: []byte("stale"), CandidateSnapshot: []byte("stale"), Projection: projection, Kind: "human"}, allow); !errors.Is(e, ErrFence) {
		t.Fatalf("old writer admitted: %v", e)
	}
	if e = s.RecoverEngine(ctx, "replacement-engine"); e != nil {
		t.Fatal(e)
	}
	retry, e := s.UpgradeRequest(ctx, k, lease, "upgrade-three", "original", 2, 3, 0, &generation, allow)
	if e != nil || retry != r {
		t.Fatalf("exact retry changed receipt: %+v %v", retry, e)
	}
	if _, e = s.UpgradeRequest(ctx, k, lease, "upgrade-three", "changed", 2, 3, 0, &generation, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("changed retry admitted: %v", e)
	}
	revoked := func(context.Context, *sql.Tx) (string, error) { return "", ErrNotFound }
	if _, e = s.UpgradeRequest(ctx, k, lease, "upgrade-three", "original", 2, 3, 0, &generation, revoked); !errors.Is(e, ErrNotFound) {
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
			if _, e = s.UpgradeRequest(ctx, k, lease, "upgrade", "original", 2, 3, 0, &generation, allow); e == nil {
				t.Fatal("invalid upgrade admitted")
			}
			v, e := s.Read(ctx, k, allow)
			if e != nil || v.Schema != 2 || v.HeadRev != 0 || len(v.Updates) != 0 {
				t.Fatalf("denied upgrade changed original state: %+v %v", v, e)
			}
		})
	}
}
