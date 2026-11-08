package documents

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/storageheadroom"
)

// documentStorageSnapshot is every row a legacy document write can touch plus
// the aggregate accounting the SQL triggers keep. A refused write must leave
// all of it byte-identical: no partial write and no accounting drift.
func documentStorageSnapshot(t *testing.T, db *database.DB) string {
	t.Helper()
	var out strings.Builder
	for _, q := range []string{
		`SELECT workspace_id,page_id,schema_version,head_rev,snapshot_rev,epoch,generation,hex(snapshot),hex(projection) FROM documents ORDER BY workspace_id,page_id`,
		`SELECT workspace_id,page_id,rev,hex(payload) FROM document_updates ORDER BY workspace_id,page_id,rev`,
		`SELECT workspace_id,page_id,id,digest,rev,epoch,actor,kind FROM document_receipts ORDER BY workspace_id,page_id,id`,
		`SELECT workspace_id,page_id,id,rev,hex(snapshot),hex(projection) FROM document_checkpoints ORDER BY workspace_id,page_id,id`,
		`SELECT workspace_id,used_bytes,reserved_bytes FROM artifact_storage_workspaces ORDER BY workspace_id`,
		`SELECT used_bytes,reserved_bytes FROM artifact_storage_global`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&out, values...)
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out.String()
}

// setFreeDiskReserve makes the real free-disk probe report "below reserve":
// no filesystem has 2^62 bytes free, so every growing write must be refused.
func setFreeDiskReserve(t *testing.T, db *database.DB, reserve int64) {
	t.Helper()
	if _, err := db.Exec(`UPDATE artifact_storage_global SET min_free_bytes=? WHERE id=1`, reserve); err != nil {
		t.Fatal(err)
	}
}

type headroomFixture struct {
	store             *Store
	db                *database.DB
	lease, otherLease Lease
	other             Key
	generation        int64
}

// newHeadroomFixture confirms a document with one checkpoint and a schema-2
// document while disk is available, then drops free disk below the reserve.
func newHeadroomFixture(t *testing.T) headroomFixture {
	t.Helper()
	s, db, _ := fixture(t, Options{})
	f := headroomFixture{store: s, db: db, other: Key{key.WorkspaceID, "page-c"}}
	if _, err := s.InitRequest(ctx, f.other, "init-two", "init", 2, []byte("schema-two"), projection, allow); err != nil {
		t.Fatal(err)
	}
	var err error
	if f.lease, err = s.Acquire(ctx, key, "engine", allow); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CheckpointRequest(ctx, key, f.lease, "confirmed", "confirmed", []byte("baseline"), 0, allow); err != nil {
		t.Fatal(err)
	}
	if f.otherLease, err = s.Acquire(ctx, f.other, "engine", allow); err != nil {
		t.Fatal(err)
	}
	otherState, err := s.Read(ctx, f.other, allow)
	if err != nil {
		t.Fatal(err)
	}
	f.generation = otherState.Generation
	setFreeDiskReserve(t, db, 1<<62)
	return f
}

func headroomEdit(l Lease) Commit {
	return Commit{Lease: l, ID: "low-disk-edit", Fingerprint: "edit", IfRev: 0, Schema: 1, Update: []byte("unsaved-edit"), CandidateSnapshot: []byte("unsaved-snapshot"), Projection: projection, Kind: "human"}
}

// B9 (#4826): legacy document writes must go through the same free-disk
// admission as native Craft writes, refuse with the shared storage-full
// sentinel, write nothing, and keep every confirmed revision. Each path runs
// on its own fixture so one admitted write cannot mask another.
func TestLegacyDocumentWritesRefuseBelowFreeDiskReserve(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(headroomFixture) error
	}{
		{"init", func(f headroomFixture) error {
			_, e := f.store.InitRequest(ctx, Key{"workspace-b", "page-b"}, "low-disk-init", "init", 1, []byte("new"), projection, allow)
			return e
		}},
		{"commit", func(f headroomFixture) error {
			_, e := f.store.Commit(ctx, key, headroomEdit(f.lease), allow)
			return e
		}},
		{"checkpoint", func(f headroomFixture) error {
			_, e := f.store.CheckpointRequest(ctx, key, f.lease, "low-disk-checkpoint", "checkpoint", []byte("unsaved-snapshot"), 0, allow)
			return e
		}},
		{"restore", func(f headroomFixture) error {
			_, e := f.store.RestoreRequest(ctx, key, f.lease, "confirmed", "low-disk-restore", 0, allow)
			return e
		}},
		{"upgrade", func(f headroomFixture) error {
			_, e := f.store.UpgradeRequest(ctx, f.other, f.otherLease, "low-disk-upgrade", "upgrade", 2, LatestSchema, 0, &f.generation, allow)
			return e
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHeadroomFixture(t)
			before := documentStorageSnapshot(t, f.db)
			if err := tc.run(f); !errors.Is(err, storageheadroom.ErrInsufficient) {
				t.Fatalf("%s admitted below the free-disk reserve: %v", tc.name, err)
			}
			if after := documentStorageSnapshot(t, f.db); after != before {
				t.Fatalf("%s refusal changed stored rows or accounting:\nbefore:\n%s\nafter:\n%s", tc.name, before, after)
			}
		})
	}
}

// Below the reserve, confirmed work stays readable, exact replays still answer
// (they write nothing) and lease renewal (no growth) keeps the session alive.
// Once space returns, the client's retained draft saves unchanged.
func TestLegacyDocumentBelowFreeDiskReserveKeepsWork(t *testing.T) {
	f := newHeadroomFixture(t)
	s := f.store
	if _, err := s.Commit(ctx, key, headroomEdit(f.lease), allow); !errors.Is(err, storageheadroom.ErrInsufficient) {
		t.Fatalf("edit admitted below reserve: %v", err)
	}
	if state, err := s.Read(ctx, key, allow); err != nil || string(state.Snapshot) != "baseline" || state.HeadRev != 0 {
		t.Fatalf("confirmed document changed or unreadable below reserve: %+v %v", state, err)
	}
	if _, err := s.CheckpointRequest(ctx, key, f.lease, "confirmed", "confirmed", []byte("baseline"), 0, allow); err != nil {
		t.Fatalf("exact checkpoint replay refused: %v", err)
	}
	if _, err := s.InitRequest(ctx, f.other, "init-two", "init", 2, []byte("schema-two"), projection, allow); err != nil {
		t.Fatalf("exact init replay refused: %v", err)
	}
	lease, err := s.Renew(ctx, key, f.lease, allow)
	if err != nil {
		t.Fatalf("lease renewal refused below reserve: %v", err)
	}
	setFreeDiskReserve(t, f.db, 0)
	if r, err := s.Commit(ctx, key, headroomEdit(lease), allow); err != nil || r.Rev != 1 {
		t.Fatalf("retained draft did not save after space returned: %+v %v", r, err)
	}
}
