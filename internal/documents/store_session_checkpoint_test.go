package documents

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// History lists checkpoints only. Without session checkpoints a document that
// was edited for days still lists just "Revision 0" (dev2 baseline G05).
func TestCommitPinsSessionCheckpoints(t *testing.T) {
	type step struct {
		actor     string
		ageOfLast string // SQLite modifier applied to every checkpoint before the commit, "" = none
	}
	other := func(context.Context, *sql.Tx) (string, error) { return "user:other", nil }
	for _, tc := range []struct {
		name  string
		steps []step
		want  []int64 // pinned revisions, newest first
	}{
		{"quick edits by the creator keep only the initial checkpoint", []step{{"self", ""}, {"self", ""}, {"self", ""}}, []int64{0}},
		{"an edit after the session interval pins the new head", []step{{"self", ""}, {"self", "-11 minutes"}, {"self", ""}}, []int64{2, 0}},
		{"another author after the minimum gap pins the new head", []step{{"self", ""}, {"other", "-2 minutes"}, {"other", ""}}, []int64{2, 0}},
		{"interleaved authors within a minute do not pin every commit", []step{{"other", ""}, {"self", ""}, {"other", ""}, {"self", ""}}, []int64{0}},
		{"a long session pins once per interval", []step{{"self", "-11 minutes"}, {"self", ""}, {"self", "-11 minutes"}, {"self", ""}}, []int64{3, 1, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _ := fixture(t, Options{})
			l := acquire(t, s)
			for i, st := range tc.steps {
				if st.ageOfLast != "" {
					if _, e := db.Exec(`UPDATE document_checkpoints SET created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now',?)`, st.ageOfLast); e != nil {
						t.Fatal(e)
					}
				}
				a := allow
				if st.actor == "other" {
					a = other
				}
				c := commit(l, fmt.Sprint("edit-", i), int64(i))
				c.CandidateSnapshot = []byte(fmt.Sprint("state after edit ", i))
				if _, e := s.Commit(ctx, key, c, a); e != nil {
					t.Fatal(i, e)
				}
			}
			rows, e := s.Checkpoints(ctx, key, allow)
			if e != nil {
				t.Fatal(e)
			}
			var got []int64
			for _, r := range rows {
				got = append(got, r["rev"].(int64))
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("pinned revisions %v, want %v", got, tc.want)
			}
		})
	}
}

// A session checkpoint holds the committed state, its author and revision, and
// can be read and restored like any other checkpoint.
func TestSessionCheckpointContentAuthorAndRestore(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	l := acquire(t, s)
	if _, e := db.Exec(`UPDATE document_checkpoints SET created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-11 minutes')`); e != nil {
		t.Fatal(e)
	}
	other := func(context.Context, *sql.Tx) (string, error) { return "user:other", nil }
	c := commit(l, "later", 0)
	c.CandidateSnapshot = []byte("later state")
	if _, e := s.Commit(ctx, key, c, other); e != nil {
		t.Fatal(e)
	}
	rows, e := s.Checkpoints(ctx, key, allow)
	if e != nil || len(rows) != 2 || rows[0]["rev"] != int64(1) || rows[0]["actor"] != "user:other" {
		t.Fatalf("%v %v", rows, e)
	}
	id := rows[0]["id"].(string)
	pinned, e := s.CheckpointRead(ctx, key, id, allow)
	if e != nil || string(pinned.Snapshot) != "later state" || pinned.Rev != 1 || pinned.Actor != "user:other" {
		t.Fatalf("%#v %v", pinned, e)
	}
	// No receipt: session pins never consume the operation admission window.
	var receipts int
	if e = db.QueryRow(`SELECT count(*) FROM document_receipts WHERE id=?`, id).Scan(&receipts); e != nil || receipts != 0 {
		t.Fatal(receipts, e)
	}
	c = commit(l, "after", 1)
	if _, e = s.Commit(ctx, key, c, allow); e != nil {
		t.Fatal(e)
	}
	if r, e := s.RestoreRequest(ctx, key, l, id, "restore-session", 2, allow); e != nil || r.Rev != 3 {
		t.Fatal(r, e)
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || string(v.Snapshot) != "later state" {
		t.Fatal(string(v.Snapshot), e)
	}
}

func ageCheckpoints(t *testing.T, db *database.DB) {
	t.Helper()
	if _, e := db.Exec(`UPDATE document_checkpoints SET created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-11 minutes')`); e != nil {
		t.Fatal(e)
	}
}

func checkpointRows(t *testing.T, db *database.DB) (ids []string, bytes int) {
	t.Helper()
	rows, e := db.Query(`SELECT id,length(snapshot)+length(projection) FROM document_checkpoints ORDER BY rev DESC`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if e = rows.Scan(&id, &n); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
		bytes += n
	}
	return ids, bytes
}

// Session pins share at most half of the History budget and are never
// deleted (R71 §0.11 has no retention spec yet). When the next pin would not
// fit it is skipped, the edit commits, and SessionHistoryFull reports it so
// History can say why no new automatic snapshots appear.
func TestSessionCheckpointsStopAtTheirHalfOfTheBudget(t *testing.T) {
	// Initial checkpoint: "baseline" (8) + projection (26) = 34 bytes. Each session pin is 16 + 26 = 42.
	s, db, _ := fixture(t, Options{MaxHistoryBytes: 200})
	l := acquire(t, s)
	full, e := s.SessionHistoryFull(ctx, key, allow)
	if e != nil || full {
		t.Fatal("fresh document reported a full History", full, e)
	}
	for i := int64(0); i < 3; i++ {
		ageCheckpoints(t, db)
		c := commit(l, fmt.Sprint("edit-", i), i)
		c.CandidateSnapshot = []byte("sixteen-byte-st" + fmt.Sprint(i))
		if _, e := s.Commit(ctx, key, c, allow); e != nil {
			t.Fatal("a skipped pin refused the edit:", i, e)
		}
	}
	ids, size := checkpointRows(t, db)
	if len(ids) != 3 || !strings.HasPrefix(ids[2], "initial-") || size != 34+84 {
		t.Fatalf("want the two first session pins plus the initial checkpoint, nothing deleted: %v (%d bytes)", ids, size)
	}
	rows, _ := s.Checkpoints(ctx, key, allow)
	if rows[0]["rev"] != int64(2) || rows[1]["rev"] != int64(1) {
		t.Fatalf("existing session pins must be kept, the newest skipped: %v", rows)
	}
	if v, e := s.Read(ctx, key, allow); e != nil || v.HeadRev != 3 {
		t.Fatal("edit not committed", v.HeadRev, e)
	}
	if full, e = s.SessionHistoryFull(ctx, key, allow); e != nil || !full {
		t.Fatal("a skipped pin must be reported as a full History", full, e)
	}
	if _, e := s.Checkpoint(ctx, key, l, "explicit", []byte("explicit"), 3, allow); e != nil {
		t.Fatal("explicit checkpoint lost its headroom:", e)
	}
	// A pin larger than half the budget is skipped too; the edit still commits.
	ageCheckpoints(t, db)
	c := commit(l, "huge", 3)
	c.CandidateSnapshot = []byte(strings.Repeat("x", 90))
	if _, e := s.Commit(ctx, key, c, allow); e != nil {
		t.Fatal("an oversized session pin refused the edit:", e)
	}
	if ids, _ = checkpointRows(t, db); len(ids) != 4 {
		t.Fatalf("oversized pin must be skipped without deleting others: %v", ids)
	}
}

// SessionHistoryFull runs the caller's authorization like every other read.
func TestSessionHistoryFullRequiresAuthority(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	deny := func(context.Context, *sql.Tx) (string, error) { return "", ErrNotFound }
	if _, e := s.SessionHistoryFull(ctx, key, deny); e == nil {
		t.Fatal("unauthorized History limit read succeeded")
	}
}

// The workspace storage budget refuses checkpoint rows with a trigger abort.
// A refused session pin is undone and the edit commits as before.
func TestSessionCheckpointRefusedByStorageBudgetKeepsTheEdit(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	l := acquire(t, s)
	used := func() (n int64) {
		t.Helper()
		if e := db.QueryRow(`SELECT used_bytes FROM artifact_storage_workspaces WHERE workspace_id='workspace-a'`).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	before := used()
	if _, e := s.Commit(ctx, key, commit(l, "measure", 0), allow); e != nil {
		t.Fatal(e)
	}
	editCost := used() - before
	if _, e := db.Exec(`UPDATE artifact_storage_workspaces SET max_bytes=used_bytes+?+64 WHERE workspace_id='workspace-a'`, editCost); e != nil {
		t.Fatal(e)
	}
	ageCheckpoints(t, db)
	r, e := s.Commit(ctx, key, commit(l, "near-quota", 1), allow)
	if e != nil || r.Rev != 2 {
		t.Fatal("a refused session pin refused the edit:", r, e)
	}
	if ids, _ := checkpointRows(t, db); len(ids) != 1 {
		t.Fatalf("the refused pin must be undone: %v", ids)
	}
	if v, e := s.Read(ctx, key, allow); e != nil || v.HeadRev != 2 {
		t.Fatal(v.HeadRev, e)
	}
}

// An unreadable timestamp never fails the edit, and an exact retry of a pinning
// commit does not pin twice.
func TestSessionCheckpointTimestampAndRetry(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	l := acquire(t, s)
	if _, e := db.Exec(`UPDATE document_checkpoints SET created_at='not a time'`); e != nil {
		t.Fatal(e)
	}
	c := commit(l, "due", 0)
	if _, e := s.Commit(ctx, key, c, allow); e != nil {
		t.Fatal("unreadable checkpoint time failed the edit:", e)
	}
	if _, e := s.Commit(ctx, key, c, allow); e != nil {
		t.Fatal(e)
	}
	if ids, _ := checkpointRows(t, db); len(ids) != 2 || !strings.HasPrefix(ids[0], "session-") {
		t.Fatalf("want one session pin after a retried commit: %v", ids)
	}
}

// An explicit checkpoint whose client-chosen ID starts with "session-" counts
// in the explicit half, not against automatic session pins, and is kept.
func TestExplicitCheckpointsNamedLikeSessionPinsAreKept(t *testing.T) {
	s, db, _ := fixture(t, Options{MaxHistoryBytes: 200})
	l := acquire(t, s)
	if _, e := s.Checkpoint(ctx, key, l, "session-explicit", []byte("explicit-sixteen"), 0, allow); e != nil {
		t.Fatal(e)
	}
	for i := int64(0); i < 4; i++ {
		ageCheckpoints(t, db)
		c := commit(l, fmt.Sprint("edit-", i), i)
		c.CandidateSnapshot = []byte("sixteen-byte-st" + fmt.Sprint(i))
		if _, e := s.Commit(ctx, key, c, allow); e != nil {
			t.Fatal(i, e)
		}
	}
	ids, _ := checkpointRows(t, db)
	found := false
	for _, id := range ids {
		found = found || id == "session-explicit"
	}
	if !found {
		t.Fatalf("explicit checkpoint was evicted: %v", ids)
	}
	if _, e := s.CheckpointRead(ctx, key, "session-explicit", allow); e != nil {
		t.Fatal(e)
	}
}
