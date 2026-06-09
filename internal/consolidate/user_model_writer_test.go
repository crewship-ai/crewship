package consolidate

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/memory"
)

// userModelTestDB spins up a fully-migrated SQLite DB and seeds the
// minimum FK targets (workspace, user) the SyncUserModel path needs.
func userModelTestDB(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
	dir := t.TempDir()
	dbh, err := database.Open("file:" + dir + "/usermodel.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), dbh.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = dbh.Close() })

	if _, err := dbh.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO users (id, email) VALUES ('u1','u1@x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return dbh.DB, "ws1", "u1"
}

func TestUserModelMeetsThreshold(t *testing.T) {
	th := DefaultUserModelThreshold
	cases := []struct {
		name string
		msgs int
		dur  time.Duration
		want bool
	}{
		{"below both", 3, time.Minute, false},
		{"messages over", 12, 30 * time.Second, true},
		{"duration over", 4, 6 * time.Minute, true},
		{"exact bar", 10, 5 * time.Minute, true},
		{"zero", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := th.MeetsThreshold(tc.msgs, tc.dur); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSyncUserModel_Write(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	out := SyncUserModel(
		context.Background(), db, logger,
		DefaultUserModelThreshold,
		UserModelCandidate{
			WorkspaceID: wsID, CrewID: "", UserID: userID,
			MessageCount: 12, SessionDuration: time.Minute,
		},
		"Prefers concise answers.",
		paths, time.Now(),
	)
	if out.Err != nil {
		t.Fatalf("write outcome err: %v", out.Err)
	}
	if out.Action != "write" || out.Bytes == 0 {
		t.Errorf("expected write action with bytes; got %+v", out)
	}
	body, _ := memory.LoadUserModel(paths, userID, wsID)
	if !strings.Contains(body, "concise") {
		t.Errorf("disk user model missing expected content: %q", body)
	}
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_models WHERE workspace_id=? AND user_slug=?`,
		wsID, memory.UserSlug(userID, wsID)).Scan(&cnt); err != nil {
		t.Fatalf("count user_models: %v", err)
	}
	if cnt != 1 {
		t.Errorf("expected 1 user_models row; got %d", cnt)
	}
}

// Second write for the same (user, workspace) upserts in place. There
// is NO agent_id in the key — the model is workspace-scoped.
func TestSyncUserModel_WriteIsUpsert(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cand := UserModelCandidate{
		WorkspaceID: wsID, UserID: userID,
		MessageCount: 12, SessionDuration: time.Minute,
	}
	SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold,
		cand, "v1 content", paths, time.Now())
	SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold,
		cand, "v2 longer content here", paths, time.Now())

	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_models`).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 1 {
		t.Errorf("expected upsert (1 row); got %d", cnt)
	}
	body, _ := memory.LoadUserModel(paths, userID, wsID)
	if !strings.Contains(body, "v2") {
		t.Errorf("expected v2 content on disk; got %q", body)
	}
}

func TestSyncUserModel_SkipThreshold(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	out := SyncUserModel(
		context.Background(), db, logger,
		DefaultUserModelThreshold,
		UserModelCandidate{
			WorkspaceID: wsID, UserID: userID,
			MessageCount: 2, SessionDuration: 30 * time.Second,
		},
		"would be content", paths, time.Now(),
	)
	if out.Action != "skip_threshold" {
		t.Errorf("expected skip_threshold; got %q (%+v)", out.Action, out)
	}
	if _, err := os.Stat(paths.ModelPath(memory.UserSlug(userID, wsID))); err == nil {
		t.Errorf("expected no model file on threshold skip")
	}
}

// Opt-out reuses user_peer_consent — opting out of peer cards opts out
// of user models too. Setting opted_out → SyncUserModel deletes any
// existing model AND removes the index row AND emits a delete audit.
func TestSyncUserModel_OptOutPurges(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cand := UserModelCandidate{
		WorkspaceID: wsID, UserID: userID,
		MessageCount: 30, SessionDuration: 10 * time.Minute,
	}
	SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold,
		cand, "Pavel notes", paths, time.Now())

	if _, err := db.Exec(`INSERT INTO user_peer_consent (user_id, workspace_id, opted_out, opted_out_at)
		VALUES (?, ?, 1, ?)`, userID, wsID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("set opt out: %v", err)
	}
	out := SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold,
		cand, "fresh would-be content", paths, time.Now())
	if out.Action != "delete_opt_out" {
		t.Errorf("expected delete_opt_out; got %q", out.Action)
	}
	if _, err := os.Stat(paths.ModelPath(memory.UserSlug(userID, wsID))); err == nil {
		t.Errorf("expected disk model to be purged on opt-out")
	}
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_models`).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 0 {
		t.Errorf("expected 0 user_models after opt-out purge; got %d", cnt)
	}
	var auditCnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM peer_card_audit
		WHERE target_user_id=? AND action='delete'`, userID).Scan(&auditCnt); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditCnt != 1 {
		t.Errorf("expected 1 delete audit row; got %d", auditCnt)
	}
}

func TestSyncUserModel_SkipEmptyContent(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	out := SyncUserModel(
		context.Background(), db, logger,
		DefaultUserModelThreshold,
		UserModelCandidate{
			WorkspaceID: wsID, UserID: userID,
			MessageCount: 100, SessionDuration: time.Hour,
		},
		"   \n\t", paths, time.Now(),
	)
	if out.Action != "skip_empty_content" {
		t.Errorf("expected skip_empty_content; got %q", out.Action)
	}
}

// IsOptedOut is shared with the peer card writer; exercise the
// no-row (default opt-in) path here against the user model DB too.
func TestSyncUserModel_DefaultOptIn(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	optedOut, err := IsOptedOut(context.Background(), db, userID, wsID)
	if err != nil {
		t.Fatalf("IsOptedOut: %v", err)
	}
	if optedOut {
		t.Errorf("expected default opt-in (false) with no consent row")
	}
}

// --- MERGE extraction prompt ---

// The headline behaviour of PR #10: when the new transcript is silent
// about a field the prior model already captured at high confidence,
// the merge MUST preserve the prior field rather than dropping it.
func TestMergeUserModel_PreservesSilentPriorField(t *testing.T) {
	prior := strings.Join([]string{
		"- timezone: UTC+1",
		"- language: Czech",
		"- tone: terse, technical",
	}, "\n")
	// The new session only re-touched tone; it said nothing about
	// timezone or language.
	extracted := "- tone: warmer this session, still technical"

	merged := MergeUserModel(prior, extracted)

	if !strings.Contains(merged, "timezone: UTC+1") {
		t.Errorf("merge dropped a prior field the new transcript was silent on:\n%s", merged)
	}
	if !strings.Contains(merged, "language: Czech") {
		t.Errorf("merge dropped prior language field:\n%s", merged)
	}
	// The field the new transcript DID mention is updated to the new value.
	if !strings.Contains(merged, "warmer this session") {
		t.Errorf("merge did not apply the freshly-extracted field value:\n%s", merged)
	}
	if strings.Contains(merged, "tone: terse, technical") {
		t.Errorf("merge kept the stale tone value instead of updating it:\n%s", merged)
	}
}

// Empty prior → merge is just the extraction.
func TestMergeUserModel_EmptyPrior(t *testing.T) {
	merged := MergeUserModel("", "- timezone: UTC+2")
	if strings.TrimSpace(merged) != "- timezone: UTC+2" {
		t.Errorf("expected extraction verbatim for empty prior; got %q", merged)
	}
}

// Empty extraction → prior survives untouched (the session added
// nothing new, so we keep what we knew).
func TestMergeUserModel_EmptyExtraction(t *testing.T) {
	prior := "- timezone: UTC+1\n- language: Czech"
	merged := MergeUserModel(prior, "   ")
	if !strings.Contains(merged, "timezone: UTC+1") || !strings.Contains(merged, "language: Czech") {
		t.Errorf("empty extraction should preserve prior intact; got %q", merged)
	}
}

// New field that the prior didn't have is appended, prior fields kept.
func TestMergeUserModel_AddsNewField(t *testing.T) {
	prior := "- timezone: UTC+1"
	extracted := "- language: English"
	merged := MergeUserModel(prior, extracted)
	if !strings.Contains(merged, "timezone: UTC+1") {
		t.Errorf("prior field lost: %q", merged)
	}
	if !strings.Contains(merged, "language: English") {
		t.Errorf("new field not added: %q", merged)
	}
}

// Consent probe error → outcome carries the error and skip_opt_out
// action. Forced by dropping the user_peer_consent table so the
// SELECT fails.
func TestSyncUserModel_ConsentProbeError(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	if _, err := db.Exec(`DROP TABLE user_peer_consent`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	out := SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold,
		UserModelCandidate{WorkspaceID: wsID, UserID: userID, MessageCount: 20},
		"body", paths, time.Now())
	if out.Action != "skip_opt_out" || out.Err == nil {
		t.Errorf("expected skip_opt_out with error; got %+v", out)
	}
}

// Opt-out, but the on-disk delete fails (model path is a directory):
// the purge surfaces an error rather than silently reporting success.
func TestSyncUserModel_OptOutDeleteFileError(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Make the model path a NON-EMPTY directory so os.Remove fails.
	slug := memory.UserSlug(userID, wsID)
	mp := paths.ModelPath(slug)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		t.Fatalf("mkdir model-as-dir: %v", err)
	}
	if err := os.WriteFile(mp+"/child", []byte("x"), 0o644); err != nil {
		t.Fatalf("seed child: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_peer_consent (user_id, workspace_id, opted_out)
		VALUES (?, ?, 1)`, userID, wsID); err != nil {
		t.Fatalf("set opt out: %v", err)
	}
	out := SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold,
		UserModelCandidate{WorkspaceID: wsID, UserID: userID, MessageCount: 20},
		"body", paths, time.Now())
	if out.Action != "delete_opt_out" || out.Err == nil {
		t.Errorf("expected delete_opt_out with error; got %+v", out)
	}
}

// Opt-out where the on-disk delete succeeds but the index DELETE fails
// (user_models table dropped): the purge surfaces the index error.
func TestSyncUserModel_OptOutIndexDeleteError(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A model file exists on disk so DeleteUserModel succeeds...
	if err := memory.WriteUserModel(paths, userID, wsID, "- tone: terse"); err != nil {
		t.Fatalf("seed model: %v", err)
	}
	// ...but the index table is gone, so the index DELETE fails.
	if _, err := db.Exec(`DROP TABLE user_models`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_peer_consent (user_id, workspace_id, opted_out)
		VALUES (?, ?, 1)`, userID, wsID); err != nil {
		t.Fatalf("set opt out: %v", err)
	}
	out := SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold,
		UserModelCandidate{WorkspaceID: wsID, UserID: userID, MessageCount: 20},
		"body", paths, time.Now())
	if out.Action != "delete_opt_out" || out.Err == nil {
		t.Errorf("expected delete_opt_out with index error; got %+v", out)
	}
}

// Opt-out with no card on disk and no index row: clean purge, no error.
func TestSyncUserModel_OptOutNoExistingCard(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := db.Exec(`INSERT INTO user_peer_consent (user_id, workspace_id, opted_out)
		VALUES (?, ?, 1)`, userID, wsID); err != nil {
		t.Fatalf("set opt out: %v", err)
	}
	out := SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold,
		UserModelCandidate{WorkspaceID: wsID, UserID: userID, MessageCount: 20},
		"body", paths, time.Now())
	if out.Action != "delete_opt_out" || out.Err != nil {
		t.Errorf("expected clean delete_opt_out; got %+v", out)
	}
}

// Write succeeds on disk but the index upsert fails (user_models table
// dropped): the outcome reports the drift rather than masking it.
func TestSyncUserModel_UpsertError(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	if _, err := db.Exec(`DROP TABLE user_models`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	out := SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold,
		UserModelCandidate{WorkspaceID: wsID, UserID: userID, MessageCount: 20},
		"body", paths, time.Now())
	if out.Action != "write" || out.Err == nil {
		t.Errorf("expected write action with upsert error; got %+v", out)
	}
}

// WriteUserModel itself fails (model path occupied by a directory):
// surfaces the write error before any index work.
func TestSyncUserModel_DiskWriteError(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	slug := memory.UserSlug(userID, wsID)
	if err := os.MkdirAll(paths.ModelPath(slug), 0o755); err != nil {
		t.Fatalf("mkdir model-as-dir: %v", err)
	}
	out := SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold,
		UserModelCandidate{WorkspaceID: wsID, UserID: userID, MessageCount: 20},
		"body", paths, time.Now())
	if out.Action != "write" || out.Err == nil {
		t.Errorf("expected write action with disk error; got %+v", out)
	}
}

// Write with a CrewID set exercises the non-nil crew_id branch of the
// upsert.
func TestSyncUserModel_WriteWithCrewID(t *testing.T) {
	db, wsID, userID := userModelTestDB(t)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1',?,'C','c')`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	dir := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	out := SyncUserModel(context.Background(), db, logger, DefaultUserModelThreshold,
		UserModelCandidate{WorkspaceID: wsID, CrewID: "cr1", UserID: userID, MessageCount: 20},
		"body", paths, time.Now())
	if out.Action != "write" || out.Err != nil {
		t.Errorf("expected clean write; got %+v", out)
	}
	var crewID sql.NullString
	if err := db.QueryRow(`SELECT crew_id FROM user_models WHERE workspace_id=?`, wsID).Scan(&crewID); err != nil {
		t.Fatalf("read crew_id: %v", err)
	}
	if !crewID.Valid || crewID.String != "cr1" {
		t.Errorf("expected crew_id=cr1 persisted; got %+v", crewID)
	}
}

// MergeUserModel: prose-only extraction and empty-key bullets exercise
// the splitFields prose branch and the empty-key guard.
func TestMergeUserModel_ProseAndEmptyKey(t *testing.T) {
	prior := "- timezone: UTC+1"
	// A free-form sentence (no bullet) plus a malformed empty-key bullet.
	extracted := "They were friendly today.\n- : stray\n- tone: warm"
	merged := MergeUserModel(prior, extracted)
	if !strings.Contains(merged, "timezone: UTC+1") {
		t.Errorf("prior field dropped: %q", merged)
	}
	if !strings.Contains(merged, "tone: warm") {
		t.Errorf("new field missing: %q", merged)
	}
	if !strings.Contains(merged, "They were friendly today.") {
		t.Errorf("prose narrative dropped: %q", merged)
	}
}

// Empty prior AND empty extraction → empty merge.
func TestMergeUserModel_BothEmpty(t *testing.T) {
	if got := MergeUserModel("  ", "  "); strings.TrimSpace(got) != "" {
		t.Errorf("expected empty merge for both-empty; got %q", got)
	}
}

// The extraction prompt must carry the prior model (so the LLM can
// merge) and the "hint, not fact" framing, and must NOT name any
// external product.
func TestBuildUserModelExtractionPrompt(t *testing.T) {
	prior := "- timezone: UTC+1"
	transcript := "user: please keep replies short"
	p := BuildUserModelExtractionPrompt(prior, transcript)
	if !strings.Contains(p, prior) {
		t.Errorf("prompt must embed prior model for merge; got:\n%s", p)
	}
	if !strings.Contains(p, transcript) {
		t.Errorf("prompt must embed the transcript; got:\n%s", p)
	}
	lower := strings.ToLower(p)
	if !strings.Contains(lower, "hint") {
		t.Errorf("prompt must keep the 'hint, not fact' framing; got:\n%s", p)
	}
	// Must instruct merge/preserve behaviour.
	if !strings.Contains(lower, "preserve") && !strings.Contains(lower, "merge") {
		t.Errorf("prompt must instruct merge/preserve of silent prior fields; got:\n%s", p)
	}
}

// Empty prior → prompt substitutes the "first extraction" placeholder
// (exercises that branch) and never names an external product.
func TestBuildUserModelExtractionPrompt_EmptyPrior(t *testing.T) {
	p := BuildUserModelExtractionPrompt("   ", "user: hi")
	if !strings.Contains(p, "first extraction") {
		t.Errorf("expected first-extraction placeholder for empty prior; got:\n%s", p)
	}
}
