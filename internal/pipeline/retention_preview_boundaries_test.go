package pipeline

import (
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func TestRetentionPreviewMatchesDeletionAndPreservesProtectedRuns(t *testing.T) {
	db := openRetentionTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	old := tsformat.Format(now.Add(-100 * 24 * time.Hour))
	recent := tsformat.Format(now.Add(-time.Hour))
	for _, row := range []struct{ id, ws, state, date, replay string }{
		{"expired", "ws_a", "completed", old, ""},
		{"failed", "ws_a", "failed", old, ""},
		{"inflight", "ws_a", "running", old, ""},
		{"approved-later", "ws_a", "completed", old, ""},
		{"parent", "ws_a", "completed", old, ""},
		{"replay", "ws_a", "completed", recent, "parent"},
		{"other", "ws_b", "completed", old, ""},
	} {
		insertRunForRetention(t, db, row.id, row.ws, "pln_a", row.state, row.date, row.replay)
	}
	if _, err := db.Exec(`INSERT INTO pipeline_waitpoints(token,workspace_id,pipeline_run_id,status) VALUES('approval','ws_a','approved-later','pending')`); err != nil {
		t.Fatal(err)
	}
	count, err := CountRunRetention(t.Context(), db, "ws_a", 90, -1, now)
	if err != nil || count != 2 {
		t.Fatalf("preview = %d, %v", count, err)
	}
	if !runExists(t, db, "expired") {
		t.Fatal("preview deleted history")
	}
	removed, err := SweepRunRetention(t.Context(), db, nil, "ws_a", 90, 0)
	if err != nil || int64(removed) != count {
		t.Fatalf("preview %d disagrees with sweep %d: %v", count, removed, err)
	}
	for _, id := range []string{"inflight", "approved-later", "parent", "replay", "other"} {
		if !runExists(t, db, id) {
			t.Errorf("protected %s deleted", id)
		}
	}
	for _, tc := range []struct {
		ws   string
		days int
	}{{"", 90}, {"ws_a", 0}, {"ws_a", -1}} {
		if count, err := CountRunRetention(t.Context(), nil, tc.ws, tc.days, 0, now); err != nil || count != 0 {
			t.Fatalf("disabled preview touched storage: %d, %v", count, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := CountRunRetention(t.Context(), db, "ws_a", 90, 0, now); err == nil {
		t.Fatal("storage refusal presented as empty preview")
	}
}
