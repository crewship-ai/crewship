package consolidate

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// #1693 — provenance is written beside the file, never inside it, and it
// follows the file's lifecycle exactly: written where the file is written,
// nothing on a dry run, gone where the file is purged.

// evidenceExtractor is a UserModelEvidenceExtractor with a fixed body and
// fixed evidence — what the real extractor hands back once Verify has run.
type evidenceExtractor struct {
	body     string
	evidence []UserModelEvidence
}

func (e evidenceExtractor) Extract(_ context.Context, _ UserModelCandidate, _ string) (string, error) {
	return e.body, nil
}

func (e evidenceExtractor) ExtractWithEvidence(_ context.Context, _ UserModelCandidate, _ string) (string, []UserModelEvidence, error) {
	return e.body, e.evidence, nil
}

func provenanceRows(t *testing.T, db *sql.DB, wsID, slug string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_model_provenance WHERE workspace_id = ? AND user_slug = ?`,
		wsID, slug).Scan(&n); err != nil {
		t.Fatalf("count provenance: %v", err)
	}
	return n
}

func TestSyncUserModel_WritesProvenanceBesideTheFile(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	slug := memory.UserSlug(userID, wsID)
	cand := UserModelCandidate{WorkspaceID: wsID, UserID: userID, MessageCount: 12}
	at := time.Date(2026, 9, 15, 5, 0, 0, 0, time.UTC)

	out := SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold, cand,
		"- role: runs the platform team\n- timezone: UTC+1",
		[]UserModelEvidence{
			{Key: "role", Value: "runs the platform team", Quote: "I run the platform team here", MessageID: "msg-1", Source: "stated"},
			{Key: "timezone", Value: "UTC+1", Quote: "I'm on UTC+1", MessageID: "msg-2", Source: "stated"},
		}, paths, dir, at)
	if out.Err != nil || out.Action != "write" {
		t.Fatalf("outcome = %+v", out)
	}

	got, err := LoadUserModelProvenance(context.Background(), db, wsID, slug)
	if err != nil {
		t.Fatalf("LoadUserModelProvenance: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("provenance for %d key(s), want 2: %+v", len(got), got)
	}
	role := got["role"]
	if role.Quote != "I run the platform team here" || role.MessageID != "msg-1" ||
		role.SourceType != "stated" || role.Value != "runs the platform team" {
		t.Errorf("role provenance = %+v", role)
	}
	if role.RecordedAt != "2026-09-15T05:00:00.000Z" {
		t.Errorf("recorded_at = %q, want the sync's own clock in fixed-width T-form", role.RecordedAt)
	}
	// The file itself carries none of it — the cap is the whole reason the
	// store is beside the file.
	body, _ := memory.LoadUserModel(paths, userID, wsID)
	if body != "- role: runs the platform team\n- timezone: UTC+1" {
		t.Errorf("the file changed shape: %q", body)
	}
}

// Correction is append, never mutate: a re-sync of the same key adds a row
// and the read resolves the newest. The older row is still there — it is
// the history of what the person said, and a later row is a visible
// correction rather than a rewrite.
func TestSyncUserModel_ResyncAppendsAndReadResolvesNewest(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	slug := memory.UserSlug(userID, wsID)
	cand := UserModelCandidate{WorkspaceID: wsID, UserID: userID, MessageCount: 12}

	day1 := time.Date(2026, 9, 14, 5, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)
	for _, step := range []struct {
		at    time.Time
		value string
		quote string
		msg   string
	}{
		{day1, "runs the platform team", "I run the platform team", "msg-1"},
		{day2, "leads the platform team", "I lead the platform team now", "msg-9"},
	} {
		out := SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold, cand,
			"- role: "+step.value,
			[]UserModelEvidence{{Key: "role", Value: step.value, Quote: step.quote, MessageID: step.msg, Source: "stated"}},
			paths, dir, step.at)
		if out.Err != nil {
			t.Fatalf("sync at %s: %v", step.at, out.Err)
		}
	}

	if n := provenanceRows(t, db, wsID, slug); n != 2 {
		t.Errorf("rows after two syncs = %d, want 2 (append, never mutate)", n)
	}
	got, err := LoadUserModelProvenance(context.Background(), db, wsID, slug)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got["role"].Quote != "I lead the platform team now" || got["role"].MessageID != "msg-9" {
		t.Errorf("read resolved %+v, want the newest row", got["role"])
	}
}

// Evidence for a fact that did not reach the file is not recorded: the
// cap trim drops lines from the end, and the merge keeps a prior value
// the extraction did not restate. A row must point at a line that exists.
func TestEvidenceInContent_KeepsOnlyFactsInTheFile(t *testing.T) {
	ev := []UserModelEvidence{
		{Key: "role", Value: "runs the platform team", Quote: "q1"},
		{Key: "Timezone", Value: "UTC+1", Quote: "q2"},           // key case differs from the bullet
		{Key: "owns", Value: "the deploy pipeline", Quote: "q3"}, // trimmed off
		{Key: "language", Value: "Czech", Quote: "q4"},           // file kept the prior "English"
	}
	got := evidenceInContent("- role: runs the platform team\n- timezone: UTC+1\n- language: English", ev)
	if len(got) != 2 || got[0].Key != "role" || got[1].Key != "Timezone" {
		t.Errorf("kept %+v, want role and Timezone only", got)
	}
	if evidenceInContent("- role: x", nil) != nil {
		t.Errorf("nil evidence should stay nil")
	}
}

func TestRunUserModelSync_RecordsTheExtractorsEvidence(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()

	sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: base,
		Extractor: evidenceExtractor{
			body:     "- tone: terse",
			evidence: []UserModelEvidence{{Key: "tone", Value: "terse", Quote: "keep it terse", MessageID: "m1", Source: "stated"}},
		},
	})
	if err != nil || sum.Writes != 1 {
		t.Fatalf("sum=%+v err=%v", sum, err)
	}
	got, err := LoadUserModelProvenance(context.Background(), db, "ws1", memory.UserSlug("u1", "ws1"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got["tone"].Quote != "keep it terse" || got["tone"].MessageID != "m1" {
		t.Errorf("sweep did not record the extractor's evidence: %+v", got)
	}

	// An extractor without evidence is still a working extractor.
	sum, err = RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: base,
		Extractor:      fixedExtractor{body: "- tone: warm"},
	})
	if err != nil || sum.Writes != 1 || sum.Errors != 0 {
		t.Fatalf("plain extractor: sum=%+v err=%v", sum, err)
	}
}

// The dry-run seam (#1702) promises nothing lands. That now includes the
// provenance table.
func TestRunUserModelSync_DryRunWritesNoProvenance(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: t.TempDir(),
		Extractor: evidenceExtractor{
			body:     "- tone: terse",
			evidence: []UserModelEvidence{{Key: "tone", Value: "terse", Quote: "keep it terse", MessageID: "m1", Source: "stated"}},
		},
		DryRun: true,
	})
	if err != nil || sum.Writes != 1 || !sum.DryRun {
		t.Fatalf("sum=%+v err=%v", sum, err)
	}
	if n := provenanceRows(t, db, "ws1", memory.UserSlug("u1", "ws1")); n != 0 {
		t.Errorf("dry run wrote %d provenance row(s)", n)
	}
}

// Delete path 1 of 4: the sweep's opt-out purge takes the evidence with
// the model. Seed both, opt out, sweep, assert zero.
func TestRunUserModelSync_OptOutPurgesProvenance(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()
	slug := memory.UserSlug("u1", "ws1")

	// A real write first, so the purge has something to reach.
	if sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: base,
		Extractor: evidenceExtractor{
			body:     "- tone: terse",
			evidence: []UserModelEvidence{{Key: "tone", Value: "terse", Quote: "keep it terse", MessageID: "m1", Source: "stated"}},
		},
	}); err != nil || sum.Writes != 1 {
		t.Fatalf("seed sweep: sum=%+v err=%v", sum, err)
	}
	if n := provenanceRows(t, db, "ws1", slug); n != 1 {
		t.Fatalf("seed left %d provenance row(s), want 1", n)
	}

	if _, err := db.Exec(`INSERT INTO user_peer_consent (user_id, workspace_id, opted_out) VALUES ('u1','ws1',1)`); err != nil {
		t.Fatalf("opt out: %v", err)
	}
	sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: base,
		Extractor:      fixedExtractor{body: "- tone: warm"},
	})
	if err != nil || sum.PurgedOptOut != 1 || sum.Errors != 0 {
		t.Fatalf("opt-out sweep: sum=%+v err=%v", sum, err)
	}
	if n := provenanceRows(t, db, "ws1", slug); n != 0 {
		t.Errorf("%d provenance row(s) survived the opt-out purge", n)
	}
	paths := memory.UserModelPaths{SharedDir: filepath.Join(base, "crews", "cr1", "shared", ".memory")}
	if body, _ := memory.LoadUserModel(paths, "u1", "ws1"); body != "" {
		t.Errorf("model survived opt-out: %q", body)
	}
}

// The two purge helpers the API surfaces call: whole-model and per-key.
// Per-key takes every row for the key, not only the newest.
func TestPurgeUserModelProvenance_WholeAndPerKey(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	slug := memory.UserSlug(userID, wsID)
	ctx := context.Background()
	now := time.Now()
	seed := func() {
		t.Helper()
		if _, err := RecordUserModelProvenance(ctx, db, wsID, userID, slug, []UserModelEvidence{
			{Key: "role", Value: "a", Quote: "qa", Source: "stated"},
			{Key: "role", Value: "b", Quote: "qb", Source: "stated"},
			{Key: "timezone", Value: "UTC+1", Quote: "qc", Source: "stated"},
		}, now); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	seed()
	n, err := PurgeUserModelProvenanceKey(ctx, db, wsID, slug, "ROLE ")
	if err != nil || n != 2 {
		t.Fatalf("per-key purge removed %d (err %v), want 2", n, err)
	}
	if left := provenanceRows(t, db, wsID, slug); left != 1 {
		t.Errorf("rows after per-key purge = %d, want the timezone row only", left)
	}

	// Re-seeding restores the two role rows; the timezone row is identical
	// to the one still there and is not duplicated — three rows in all.
	seed()
	n, err = PurgeUserModelProvenance(ctx, db, wsID, slug)
	if err != nil || n != 3 {
		t.Fatalf("whole purge removed %d (err %v), want 3", n, err)
	}
	if left := provenanceRows(t, db, wsID, slug); left != 0 {
		t.Errorf("rows after whole purge = %d, want 0", left)
	}

	// Nothing to record is not an error, and an entry without a quote is
	// not evidence.
	n2, err := RecordUserModelProvenance(ctx, db, wsID, userID, slug, []UserModelEvidence{{Key: "role", Value: "v"}}, now)
	if err != nil || n2 != 0 {
		t.Errorf("quote-less entry recorded %d row(s) (err %v), want 0", n2, err)
	}
}

// The same evidence re-found on the next day's sweep is not new evidence:
// the newest row is kept as it is, dated when the person first said it.
// Anything that differs is appended.
func TestRecordUserModelProvenance_IdenticalEvidenceIsNotDuplicated(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	slug := memory.UserSlug(userID, wsID)
	ctx := context.Background()
	day1 := time.Date(2026, 9, 14, 5, 0, 0, 0, time.UTC)
	same := UserModelEvidence{Key: "role", Value: "runs the platform team", Quote: "I run the platform team", MessageID: "msg-1", Source: "stated"}

	cases := []struct {
		name     string
		evidence UserModelEvidence
		wantRows int
	}{
		{"first sighting is recorded", same, 1},
		{"the same sighting the next day is not", same, 1},
		{"the same words in a later message are", UserModelEvidence{Key: "role", Value: same.Value, Quote: same.Quote, MessageID: "msg-7", Source: "stated"}, 2},
		{"a different quote for the same value is", UserModelEvidence{Key: "role", Value: same.Value, Quote: "the platform team is mine to run", MessageID: "msg-8", Source: "stated"}, 3},
		{"a different value is", UserModelEvidence{Key: "role", Value: "leads the platform team", Quote: same.Quote, MessageID: "msg-8", Source: "stated"}, 4},
		{"and the first sighting again, now that it is no longer the newest, is", same, 5},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RecordUserModelProvenance(ctx, db, wsID, userID, slug, []UserModelEvidence{tc.evidence}, day1.Add(time.Duration(i)*24*time.Hour)); err != nil {
				t.Fatalf("record: %v", err)
			}
			if n := provenanceRows(t, db, wsID, slug); n != tc.wantRows {
				t.Errorf("rows = %d, want %d", n, tc.wantRows)
			}
		})
	}
	got, err := LoadUserModelProvenance(ctx, db, wsID, slug)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got["role"].RecordedAt != "2026-09-19T05:00:00.000Z" {
		t.Errorf("newest row dated %s, want the last append", got["role"].RecordedAt)
	}
}
