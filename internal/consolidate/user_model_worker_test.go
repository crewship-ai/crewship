package consolidate

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/memory"
)

func userModelWorkerDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbh, err := database.Open("file:" + dir + "/umw.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), dbh.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = dbh.Close() })
	return dbh.DB
}

// seedUserModelFixture creates a workspace, crew, user, agent and a
// chat with enough messages to cross the threshold.
func seedUserModelFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1','ws1','Crew','crew')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u1','u1@x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, slug, name, agent_role)
		VALUES ('a1','ws1','cr1','dev','Dev','AGENT')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, message_count, started_at, ended_at)
		VALUES ('ch1','a1','ws1','u1',20,?,?)`,
		time.Now().UTC().Add(-1*time.Hour).Format(time.RFC3339),
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
}

// fixedExtractor returns a constant body so the sweep produces a write.
type fixedExtractor struct{ body string }

func (f fixedExtractor) Extract(_ context.Context, _ UserModelCandidate, _ string) (string, error) {
	return f.body, nil
}

func TestRunUserModelSync_WritesForThresholdCrosser(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()

	sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: base,
		Extractor:      fixedExtractor{body: "- tone: terse"},
	})
	if err != nil {
		t.Fatalf("RunUserModelSync: %v", err)
	}
	if sum.Candidates != 1 || sum.Writes != 1 {
		t.Errorf("expected 1 candidate / 1 write; got %+v", sum)
	}
	// The model landed in the crew-shared memory of cr1.
	paths := memory.UserModelPaths{SharedDir: base + "/crews/cr1/shared/.memory"}
	body, _ := memory.LoadUserModel(paths, "u1", "ws1")
	if body == "" {
		t.Errorf("expected a user model written to crew-shared memory")
	}
}

// The extractor receives the PRIOR model so it can merge. Verify the
// sweep loads the existing on-disk model and passes it through.
func TestRunUserModelSync_PassesPriorToExtractor(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: base + "/crews/cr1/shared/.memory"}
	if err := memory.WriteUserModel(paths, "u1", "ws1", "- timezone: UTC+1"); err != nil {
		t.Fatalf("seed prior: %v", err)
	}

	var gotPrior string
	ex := priorCapturingExtractor{seen: &gotPrior, body: "- tone: terse"}
	if _, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: base,
		Extractor:      ex,
	}); err != nil {
		t.Fatalf("RunUserModelSync: %v", err)
	}
	if gotPrior != "- timezone: UTC+1" {
		t.Errorf("extractor did not receive prior model; got %q", gotPrior)
	}
	// And the merged result preserves the silent prior field.
	body, _ := memory.LoadUserModel(paths, "u1", "ws1")
	if !strings.Contains(body, "timezone: UTC+1") || !strings.Contains(body, "tone: terse") {
		t.Errorf("merged model missing preserved/new fields: %q", body)
	}
}

type priorCapturingExtractor struct {
	seen *string
	body string
}

func (e priorCapturingExtractor) Extract(_ context.Context, _ UserModelCandidate, prior string) (string, error) {
	*e.seen = prior
	return e.body, nil
}

func TestNoopUserModelExtractor(t *testing.T) {
	body, err := NoopUserModelExtractor{}.Extract(context.Background(), UserModelCandidate{}, "prior")
	if err != nil || body != "" {
		t.Errorf("expected empty,nil; got %q,%v", body, err)
	}
}

func TestRunUserModelSync_RequiresBasePath(t *testing.T) {
	db := userModelWorkerDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{}); err == nil {
		t.Errorf("expected error when OutputBasePath is empty")
	}
}

// Worker goroutine: a short FirstRunDelay + tiny TickInterval triggers
// at least one sweep, then stop tears it down cleanly.
func TestStartUserModelSyncWorker_FiresAndStops(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	StartUserModelSyncWorker(db, logger, UserModelWorkerConfig{
		BasePath:      base,
		Extractor:     fixedExtractor{body: "- tone: terse"},
		TickInterval:  50 * time.Millisecond,
		FirstRunDelay: 1 * time.Millisecond,
	}, stop, &wg)

	// Wait for the first sweep to land a model.
	paths := memory.UserModelPaths{SharedDir: base + "/crews/cr1/shared/.memory"}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if body, _ := memory.LoadUserModel(paths, "u1", "ws1"); body != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if body, _ := memory.LoadUserModel(paths, "u1", "ws1"); body == "" {
		close(stop)
		wg.Wait()
		t.Fatal("worker never wrote a model within the deadline")
	}
	close(stop)
	wg.Wait()
}

// Missing BasePath → worker refuses to start (logs + returns; wg stays
// at zero so Wait returns immediately).
func TestStartUserModelSyncWorker_NoBasePath(t *testing.T) {
	db := userModelWorkerDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	StartUserModelSyncWorker(db, logger, UserModelWorkerConfig{}, stop, &wg)
	close(stop)
	wg.Wait() // must not block — worker never added to wg
}

// errExtractor always fails; the sweep counts it as an error and keeps
// going.
type errExtractor struct{}

func (errExtractor) Extract(_ context.Context, _ UserModelCandidate, _ string) (string, error) {
	return "", errFromExtractor
}

var errFromExtractor = &extractErr{}

type extractErr struct{}

func (*extractErr) Error() string { return "extractor boom" }

func TestRunUserModelSync_ExtractorError(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()
	sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: base,
		Extractor:      errExtractor{},
	})
	if err != nil {
		t.Fatalf("RunUserModelSync: %v", err)
	}
	if sum.Errors != 1 || sum.Writes != 0 {
		t.Errorf("expected 1 error / 0 writes; got %+v", sum)
	}
}

// Below-threshold candidate → SkippedThresh counter. (message_count
// small, short session.)
func TestRunUserModelSync_SkipThresholdCounter(t *testing.T) {
	db := userModelWorkerDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1','ws1','C','c')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u1','u1@x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, slug, name, agent_role)
		VALUES ('a1','ws1','cr1','dev','Dev','AGENT')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, message_count, started_at, ended_at)
		VALUES ('ch1','a1','ws1','u1',2,?,?)`,
		now.Add(-30*time.Second).Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: t.TempDir(),
		Extractor:      fixedExtractor{body: "- tone: terse"},
	})
	if err != nil {
		t.Fatalf("RunUserModelSync: %v", err)
	}
	if sum.SkippedThresh != 1 {
		t.Errorf("expected 1 skip_threshold; got %+v", sum)
	}
}

// Noop extractor over a fresh threshold-crosser → empty merge → empty
// content → SkippedEmpty counter (and no model file).
func TestRunUserModelSync_SkipEmptyCounter(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()
	sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: base,
		Extractor:      NoopUserModelExtractor{},
	})
	if err != nil {
		t.Fatalf("RunUserModelSync: %v", err)
	}
	if sum.SkippedEmpty != 1 || sum.Writes != 0 {
		t.Errorf("expected 1 skip_empty / 0 writes with noop extractor; got %+v", sum)
	}
}

// Opt-out crosser with an existing model → PurgedOptOut counter.
func TestRunUserModelSync_PurgeOptOutCounter(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: base + "/crews/cr1/shared/.memory"}
	if err := memory.WriteUserModel(paths, "u1", "ws1", "- tone: terse"); err != nil {
		t.Fatalf("seed model: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_peer_consent (user_id, workspace_id, opted_out)
		VALUES ('u1','ws1',1)`); err != nil {
		t.Fatalf("opt out: %v", err)
	}
	sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: base,
		Extractor:      fixedExtractor{body: "- tone: warm"},
	})
	if err != nil {
		t.Fatalf("RunUserModelSync: %v", err)
	}
	if sum.PurgedOptOut != 1 {
		t.Errorf("expected 1 purged_opt_out; got %+v", sum)
	}
}

// A chat opened against an agent with NULL crew_id → candidate has an
// empty CrewID → userModelPathsFor falls back to the workspace-level
// shared dir.
func TestRunUserModelSync_NullCrewFallbackPath(t *testing.T) {
	db := userModelWorkerDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u1','u1@x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Agent with NO crew (COORDINATOR-style — crew_id NULL).
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, slug, name, agent_role)
		VALUES ('a1','ws1','co','Co','COORDINATOR')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, message_count, started_at, ended_at)
		VALUES ('ch1','a1','ws1','u1',20,?,?)`,
		now.Add(-time.Hour).Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()
	sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: base,
		Extractor:      fixedExtractor{body: "- tone: terse"},
	})
	if err != nil {
		t.Fatalf("RunUserModelSync: %v", err)
	}
	if sum.Writes != 1 {
		t.Errorf("expected 1 write; got %+v", sum)
	}
	// Model landed in the workspace-level fallback shared dir.
	fb := memory.UserModelPaths{SharedDir: base + "/workspace/shared/.memory"}
	if body, _ := memory.LoadUserModel(fb, "u1", "ws1"); body == "" {
		t.Errorf("expected model in workspace fallback dir")
	}
}

// userModelPathsFor unit: empty crew → workspace fallback; non-empty →
// crew-scoped.
func TestUserModelPathsFor(t *testing.T) {
	if got := userModelPathsFor("/base", "").SharedDir; got != "/base/workspace/shared/.memory" {
		t.Errorf("empty crew fallback path wrong: %q", got)
	}
	if got := userModelPathsFor("/base", "cr1").SharedDir; got != "/base/crews/cr1/shared/.memory" {
		t.Errorf("crew-scoped path wrong: %q", got)
	}
}

// Candidate query error → RunUserModelSync returns a wrapped error.
func TestRunUserModelSync_CandidateQueryError(t *testing.T) {
	db := userModelWorkerDB(t)
	if _, err := db.Exec(`DROP TABLE chats`); err != nil {
		t.Fatalf("drop chats: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: t.TempDir(),
	}); err == nil {
		t.Errorf("expected error when chats table is missing")
	}
}

// runUserModelSweepAll over a DB with no workspaces is a no-op (the
// "no active workspaces" branch). Driven through the worker with a
// tiny delay so the sweep fires once.
func TestStartUserModelSyncWorker_NoWorkspaces(t *testing.T) {
	db := userModelWorkerDB(t) // migrated, but zero workspaces
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	StartUserModelSyncWorker(db, logger, UserModelWorkerConfig{
		BasePath:      t.TempDir(),
		Extractor:     NoopUserModelExtractor{},
		TickInterval:  time.Hour,
		FirstRunDelay: 1 * time.Millisecond,
	}, stop, &wg)
	// Give the first sweep a moment to run, then stop.
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// runUserModelSweepAll across multiple workspaces aggregates writes.
func TestStartUserModelSyncWorker_MultiWorkspaceTick(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db) // ws1 + crew + user + chat
	// A second workspace with its own crew/user/chat.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws2','W2','w2')`); err != nil {
		t.Fatalf("seed ws2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr2','ws2','C2','c2')`); err != nil {
		t.Fatalf("seed crew2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u2','u2@x')`); err != nil {
		t.Fatalf("seed user2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, slug, name, agent_role)
		VALUES ('a2','ws2','cr2','dev2','Dev2','AGENT')`); err != nil {
		t.Fatalf("seed agent2: %v", err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, message_count, started_at, ended_at)
		VALUES ('ch2','a2','ws2','u2',20,?,?)`,
		now.Add(-time.Hour).Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed chat2: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	StartUserModelSyncWorker(db, logger, UserModelWorkerConfig{
		BasePath:      base,
		Extractor:     fixedExtractor{body: "- tone: terse"},
		TickInterval:  30 * time.Millisecond,
		FirstRunDelay: 1 * time.Millisecond,
	}, stop, &wg)

	deadline := time.Now().Add(3 * time.Second)
	p1 := memory.UserModelPaths{SharedDir: base + "/crews/cr1/shared/.memory"}
	p2 := memory.UserModelPaths{SharedDir: base + "/crews/cr2/shared/.memory"}
	for time.Now().Before(deadline) {
		b1, _ := memory.LoadUserModel(p1, "u1", "ws1")
		b2, _ := memory.LoadUserModel(p2, "u2", "ws2")
		if b1 != "" && b2 != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if b1, _ := memory.LoadUserModel(p1, "u1", "ws1"); b1 == "" {
		t.Errorf("ws1 model not written")
	}
	if b2, _ := memory.LoadUserModel(p2, "u2", "ws2"); b2 == "" {
		t.Errorf("ws2 model not written")
	}
}
