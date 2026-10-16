package documents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func TestCheckpointRestoreAndBoundedReplay(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	l := acquire(t, s)
	if _, e := s.Checkpoint(ctx, key, l, "pinned", []byte("pinned full state"), 0, allow); e != nil {
		t.Fatal(e)
	}
	for i := int64(0); i < 40; i++ {
		if _, e := s.Commit(ctx, key, commit(l, fmt.Sprint(i), i), allow); e != nil {
			t.Fatal(e)
		}
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.HeadRev != 40 || len(v.Updates) != 8 || string(v.Snapshot) != "candidate" {
		t.Fatalf("compaction %#v %v", v, e)
	}
	var n int
	if e = db.QueryRow("SELECT count(*) FROM document_receipts").Scan(&n); e != nil || n != 42 {
		t.Fatal(n, e)
	}
	if _, e = s.Restore(ctx, key, l, "pinned", 39, allow); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	stats, e := s.Stats(ctx, key, allow)
	if e != nil || stats.Receipts != 42 || stats.Checkpoints != 1 || stats.ReplayUpdates != 8 || stats.ReplayBytes != int64(8*len("incremental")) {
		t.Fatal(stats, e)
	}
	pinned, e := s.Checkpoint(ctx, key, l, "pinned", []byte("pinned full state"), 0, allow)
	if e != nil || pinned.Rev != 0 {
		t.Fatal(pinned, e)
	}
	r, e := s.RestoreRequest(ctx, key, l, "pinned", "restore-request", 40, allow)
	if e != nil || r.Rev != 41 || r.Epoch != l.Epoch+1 {
		t.Fatal(r, e)
	}
	replayed, e := s.RestoreRequest(ctx, key, l, "pinned", "restore-request", 40, allow)
	if e != nil || replayed != r {
		t.Fatal(replayed, e)
	}
	if _, e := s.RestoreRequest(ctx, key, l, "pinned", "restore-request", 41, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatal(e)
	}
	v, e = s.Read(ctx, key, allow)
	if e != nil || string(v.Snapshot) != "pinned full state" || len(v.Updates) != 0 {
		t.Fatal(v, e)
	}
	if _, e = s.Commit(ctx, key, commit(l, "stale", 41), allow); !errors.Is(e, ErrFence) {
		t.Fatal(e)
	}
	if _, e = s.Acquire(ctx, key, "new-process", allow); e != nil {
		t.Fatal(e)
	}
}
func TestQuotaAndAtomicRollback(t *testing.T) {
	s, db, _ := fixture(t, Options{MaxCommits: 2, MaxDocuments: 2, MaxHistoryBytes: 50})
	l := acquire(t, s)
	if _, e := s.Commit(ctx, key, commit(l, "one", 0), allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Commit(ctx, key, commit(l, "two", 1), allow); !errors.Is(e, ErrQuota) {
		t.Fatal(e)
	}
	s = New(db.DB, Options{MaxHistoryBytes: 50, MaxDocuments: 2})
	if _, e := s.Checkpoint(ctx, key, l, "a", []byte("checkpoint"), 1, allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Checkpoint(ctx, key, l, "b", []byte("checkpoint"), 1, allow); !errors.Is(e, ErrQuota) {
		t.Fatal(e)
	}
	other := Key{"workspace-a", "page-c"}
	if _, e := s.Init(ctx, other, 1, []byte("other"), projection, allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Init(ctx, Key{"w", "p"}, 1, []byte("third"), projection, allow); !errors.Is(e, ErrQuota) {
		t.Fatal(e)
	}
	// A database failure after update/receipt insertion rolls the entire commit back.
	s = New(db.DB, Options{})
	if _, e := db.Exec("CREATE TRIGGER fail_document_head BEFORE UPDATE OF head_rev ON documents BEGIN SELECT RAISE(ABORT, 'injected storage failure'); END"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Commit(ctx, key, commit(l, "rollback", 1), allow); e == nil {
		t.Fatal("expected injected failure")
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.HeadRev != 1 || len(v.Updates) != 1 {
		t.Fatal(v, e)
	}
	var n int
	if e = db.QueryRow("SELECT count(*) FROM document_receipts WHERE id='rollback'").Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}

func TestAllMutationReceiptsRetainOriginalFingerprint(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	// Exact initialization retry returns the established document; mismatch cannot overwrite it.
	if _, e := s.InitRequest(ctx, key, "__init", "internal-init", 1, []byte("baseline"), projection, allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.InitRequest(ctx, key, "__init", "changed", 1, []byte("baseline"), projection, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatal(e)
	}
	l := acquire(t, s)
	if _, e := s.CheckpointRequest(ctx, key, l, "original-checkpoint", "semantic-checkpoint-fingerprint", []byte("checkpoint"), 0, allow); e != nil {
		t.Fatal(e)
	}
	checkpointReceipt, found, e := s.LookupReceipt(ctx, key, "original-checkpoint", "semantic-checkpoint-fingerprint", allow)
	if e != nil || !found || checkpointReceipt.Kind != "checkpoint" {
		t.Fatal(checkpointReceipt, found, e)
	}
	r, e := s.RestoreRequestWithFingerprint(ctx, key, l, "original-checkpoint", "original-restore", "semantic-restore-fingerprint", 0, allow)
	if e != nil {
		t.Fatal(e)
	}
	got, found, e := s.LookupReceipt(ctx, key, "original-restore", "semantic-restore-fingerprint", allow)
	if e != nil || !found || got != r {
		t.Fatal(got, found, e)
	}
	otherActor := func(context.Context, *sql.Tx) (string, error) { return "another-authorized-actor", nil }
	if _, _, e = s.LookupReceipt(ctx, key, "original-restore", "semantic-restore-fingerprint", otherActor); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("cross-actor receipt leaked: %v", e)
	}
	if _, e = s.RestoreRequestWithFingerprint(ctx, key, l, "original-checkpoint", "original-restore", "different", 0, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("changed semantic fingerprint accepted: %v", e)
	}
	if _, e = s.CheckpointRequest(ctx, key, l, "original-checkpoint", "semantic-checkpoint-fingerprint", []byte("changed candidate"), 0, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("changed checkpoint payload accepted: %v", e)
	}
}

func TestCheckpointRetriesBindOriginalGenerationAndActor(t *testing.T) {
	revoked := errors.New("revoked")
	for _, tt := range []struct {
		name       string
		generation int64
		actor      string
		rotate     bool
		deny       error
		want       error
	}{
		{"exact retry", 0, "server-actor", false, nil, nil},
		{"exact retry after boot fencing", 0, "server-actor", true, nil, nil},
		{"changed generation", 1, "server-actor", false, nil, ErrDuplicate},
		{"changed actor", 0, "other-actor", false, nil, ErrDuplicate},
		{"revoked authority", 0, "", false, revoked, revoked},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := fixture(t, Options{})
			lease := acquire(t, s)
			generation := int64(0)
			original, err := s.CheckpointRequestAtGeneration(ctx, key, lease, "pinned", "checkpoint-fingerprint", []byte("checkpoint"), 0, &generation, allow)
			if err != nil {
				t.Fatal(err)
			}
			if tt.rotate {
				if err := s.InvalidateLeases(ctx); err != nil {
					t.Fatal(err)
				}
				lease = acquire(t, s)
			}
			auth := func(context.Context, *sql.Tx) (string, error) { return tt.actor, tt.deny }
			got, err := s.CheckpointRequestAtGeneration(ctx, key, lease, "pinned", "checkpoint-fingerprint", []byte("checkpoint"), 0, &tt.generation, auth)
			if !errors.Is(err, tt.want) {
				t.Fatalf("checkpoint retry %#v: %v, want %v", got, err, tt.want)
			}
			if err == nil && (got.ID != original.ID || got.Rev != original.Rev || string(got.Snapshot) != string(original.Snapshot) || got.Actor != original.Actor) {
				t.Fatalf("checkpoint retry changed state: %#v", got)
			}
		})
	}
}

func TestRestoreRetriesBindOriginalGenerationAndActor(t *testing.T) {
	revoked := errors.New("revoked")
	for _, tt := range []struct {
		name       string
		generation int64
		actor      string
		rotate     bool
		deny       error
		want       error
	}{
		{"exact retry after restore fencing", 0, "server-actor", false, nil, nil},
		{"exact retry after boot fencing", 0, "server-actor", true, nil, nil},
		{"changed generation", 1, "server-actor", false, nil, ErrDuplicate},
		{"changed actor", 0, "other-actor", false, nil, ErrDuplicate},
		{"revoked authority", 0, "", false, revoked, revoked},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := fixture(t, Options{})
			lease := acquire(t, s)
			generation := int64(0)
			if _, err := s.Checkpoint(ctx, key, lease, "pinned", []byte("checkpoint"), 0, allow); err != nil {
				t.Fatal(err)
			}
			original, err := s.RestoreRequestWithFingerprintAtGeneration(ctx, key, lease, "pinned", "restore-request", "restore-fingerprint", 0, &generation, allow)
			if err != nil {
				t.Fatal(err)
			}
			if tt.rotate {
				if err := s.InvalidateLeases(ctx); err != nil {
					t.Fatal(err)
				}
				lease = acquire(t, s)
			}
			auth := func(context.Context, *sql.Tx) (string, error) { return tt.actor, tt.deny }
			got, err := s.RestoreRequestWithFingerprintAtGeneration(ctx, key, lease, "pinned", "restore-request", "restore-fingerprint", 0, &tt.generation, auth)
			if !errors.Is(err, tt.want) {
				t.Fatalf("restore retry %#v: %v, want %v", got, err, tt.want)
			}
			if err == nil && got != original {
				t.Fatalf("restore retry changed receipt: %#v", got)
			}
		})
	}
}

func TestCheckpointOriginalGuardsAndAtomicReceipt(t *testing.T) {
	denied := errors.New("original target changed")
	for _, tt := range []struct {
		name            string
		rev, generation int64
		guard           MutationGuard
		inject          bool
		want            error
	}{
		{"stale revision", 1, 0, nil, false, ErrConflict},
		{"stale generation", 0, 1, nil, false, ErrConflict},
		{"guard denies target", 0, 0, func(context.Context, *sql.Tx, string, State) error { return denied }, false, denied},
		{"receipt storage failure", 0, 0, nil, true, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, db, _ := fixture(t, Options{})
			lease := acquire(t, s)
			if tt.inject {
				if _, err := db.Exec("CREATE TRIGGER fail_checkpoint_receipt BEFORE INSERT ON document_receipts WHEN NEW.kind='checkpoint' BEGIN SELECT RAISE(ABORT, 'injected receipt failure'); END"); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.CheckpointRequestGuarded(ctx, key, lease, "guarded", "original", []byte("checkpoint"), tt.rev, &tt.generation, allow, tt.guard)
			if err == nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("checkpoint error %v, want %v", err, tt.want)
			}
			for _, table := range []string{"document_checkpoints", "document_receipts"} {
				var count int
				if err := db.QueryRow("SELECT count(*) FROM " + table + " WHERE id='guarded'").Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s rows=%d: %v", table, count, err)
				}
			}
			state, err := s.Read(ctx, key, allow)
			if err != nil || state.HeadRev != 0 || string(state.Snapshot) != "baseline" {
				t.Fatalf("rejected checkpoint changed document %#v: %v", state, err)
			}
		})
	}
}

func TestCheckpointRestoreNilGenerationIdentity(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	lease := acquire(t, s)
	if _, err := s.CheckpointRequest(ctx, key, lease, "nil-checkpoint", "fp", []byte("checkpoint"), 0, allow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreRequestWithFingerprint(ctx, key, lease, "nil-checkpoint", "nil-restore", "restore-fp", 0, allow); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ id, digest string }{
		{"nil-checkpoint", "40f43f5c882c2796933ac71143cebca0583821196503ecdac0100254b58f3ced"},
		{"nil-restore", "b6f356f15ad0d236aaf04f0b9d854d125f2b842ab5a5a04495a8f528106311f2"},
	} {
		var got string
		if err := db.QueryRow("SELECT digest FROM document_receipts WHERE id=?", tt.id).Scan(&got); err != nil || got != tt.digest {
			t.Fatalf("%s original nil-generation digest %q: %v", tt.id, got, err)
		}
	}
}

func TestRestoreOriginalPreconditionsAndAtomicReceipt(t *testing.T) {
	for _, tt := range []struct {
		name            string
		rev, generation int64
		checkpoint      string
		inject          bool
		want            error
	}{
		{"stale revision", 0, 0, "pinned", false, ErrConflict},
		{"stale generation", 1, 1, "pinned", false, ErrConflict},
		{"missing checkpoint", 1, 0, "missing", false, ErrNotFound},
		{"receipt storage failure", 1, 0, "pinned", true, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, db, _ := fixture(t, Options{})
			lease := acquire(t, s)
			if _, err := s.Checkpoint(ctx, key, lease, "pinned", []byte("checkpoint"), 0, allow); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Commit(ctx, key, commit(lease, "edit", 0), allow); err != nil {
				t.Fatal(err)
			}
			if tt.inject {
				if _, err := db.Exec("CREATE TRIGGER fail_restore_receipt BEFORE INSERT ON document_receipts WHEN NEW.kind='restore' BEGIN SELECT RAISE(ABORT, 'injected receipt failure'); END"); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.RestoreRequestWithFingerprintAtGeneration(ctx, key, lease, tt.checkpoint, "rejected-restore", "original", tt.rev, &tt.generation, allow)
			if err == nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("restore error %v, want %v", err, tt.want)
			}
			state, err := s.Read(ctx, key, allow)
			if err != nil || state.HeadRev != 1 || state.Generation != 0 || state.Epoch != lease.Epoch || string(state.Snapshot) != "baseline" || len(state.Updates) != 1 {
				t.Fatalf("rejected restore changed document %#v: %v", state, err)
			}
			if _, found, err := s.LookupReceipt(ctx, key, "rejected-restore", "original", allow); err != nil || found {
				t.Fatalf("rejected restore left receipt: %v %v", found, err)
			}
			if _, err := s.Renew(ctx, key, lease, allow); err != nil {
				t.Fatalf("rejected restore fenced original writer: %v", err)
			}
		})
	}
}

func TestCheckpointMetadataRequiresCurrentAuthorityAndScope(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	lease := acquire(t, s)
	if _, err := s.Checkpoint(ctx, key, lease, "pinned", []byte("checkpoint"), 0, allow); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("authority revoked")
	auth := func(context.Context, *sql.Tx) (string, error) { return "", denied }
	if _, err := s.CheckpointRead(ctx, key, "pinned", auth); !errors.Is(err, denied) {
		t.Fatalf("checkpoint read ignored current authority: %v", err)
	}
	if _, err := s.Stats(ctx, key, auth); !errors.Is(err, denied) {
		t.Fatalf("stats ignored current authority: %v", err)
	}
	if _, err := s.CheckpointRead(ctx, Key{"workspace-b", key.PageID}, "pinned", allow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("checkpoint read crossed workspace scope: %v", err)
	}
	got, err := s.CheckpointRead(ctx, key, "pinned", allow)
	if err != nil || got.ID != "pinned" || got.Actor != "server-actor" || got.Schema != 1 || string(got.Snapshot) != "checkpoint" {
		t.Fatalf("checkpoint read %#v: %v", got, err)
	}
}
