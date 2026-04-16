package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

// setupOnboardingDB returns a fresh DB with migrations + a workspace +
// a user in the OnBoarding-needed state.
func setupOnboardingDB(t *testing.T) (*database.DB, string, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "ob.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO users (id, email, onboarding_completed, created_at, updated_at) VALUES ('u1','u1@example.com',0,?,?)`, now, now); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','WS','ws',?,?)`, now, now); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES ('wm1','w1','u1','OWNER',?)`, now); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return db, "u1", "w1"
}

// idGen returns a deterministic monotonic ID generator.
func idGen() func() string {
	var n int64
	return func() string {
		i := atomic.AddInt64(&n, 1)
		return "id_" + time.Now().Format("150405") + "_" + itoa(i)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func newTestSvc(t *testing.T, db *database.DB) *OnboardingService {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewOnboardingService(db.DB, logger, idGen())
}

// TestOnboardingSetup_Happy creates everything in one shot and verifies
// each row landed.
func TestOnboardingSetup_Happy(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	db, uid, wid := setupOnboardingDB(t)
	svc := newTestSvc(t, db)
	now := time.Now().UTC().Format(time.RFC3339)

	llmModel := "claude-sonnet-4-20250514"
	res, err := svc.Setup(context.Background(), SetupParams{
		UserID:          uid,
		WorkspaceID:     wid,
		WorkspaceName:   "My Workspace",
		CrewName:        "Solo",
		CrewSlug:        "solo",
		AgentName:       "Pilot",
		AgentSlug:       "pilot",
		CliAdapter:      "CLAUDE_CODE",
		LLMProvider:     "ANTHROPIC",
		LLMModel:        &llmModel,
		EnvVarName:      "ANTHROPIC_API_KEY",
		CredentialName:  "Anthropic",
		CredentialValue: "sk-ant-test",
		Now:             now,
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if res.WorkspaceID != wid || res.CrewID == "" || res.AgentID == "" || res.CredentialID == "" {
		t.Fatalf("unexpected result: %+v", res)
	}

	// Crew row exists.
	var crewSlug string
	if err := db.QueryRow("SELECT slug FROM crews WHERE id=?", res.CrewID).Scan(&crewSlug); err != nil || crewSlug != "solo" {
		t.Errorf("crew row missing/wrong: %q err=%v", crewSlug, err)
	}
	// Agent row exists.
	var agSlug string
	if err := db.QueryRow("SELECT slug FROM agents WHERE id=?", res.AgentID).Scan(&agSlug); err != nil || agSlug != "pilot" {
		t.Errorf("agent missing/wrong: %q err=%v", agSlug, err)
	}
	// Credential row exists with type AI_CLI_TOKEN.
	var ctype string
	if err := db.QueryRow("SELECT type FROM credentials WHERE id=?", res.CredentialID).Scan(&ctype); err != nil || ctype != "AI_CLI_TOKEN" {
		t.Errorf("credential missing/wrong: type=%q err=%v", ctype, err)
	}
	// Workspace name updated.
	var wsName string
	if err := db.QueryRow("SELECT name FROM workspaces WHERE id=?", wid).Scan(&wsName); err != nil || wsName != "My Workspace" {
		t.Errorf("workspace name not updated: %q err=%v", wsName, err)
	}
	// Onboarding completed flag set.
	var completed int
	if err := db.QueryRow("SELECT onboarding_completed FROM users WHERE id=?", uid).Scan(&completed); err != nil || completed != 1 {
		t.Errorf("onboarding flag not set: %d err=%v", completed, err)
	}
	// Crew member row inserted.
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM crew_members WHERE crew_id=? AND user_id=?", res.CrewID, uid).Scan(&n); err != nil || n != 1 {
		t.Errorf("crew_member missing: n=%d err=%v", n, err)
	}
	// Agent credential link inserted.
	var ac int
	if err := db.QueryRow("SELECT COUNT(*) FROM agent_credentials WHERE agent_id=? AND credential_id=?", res.AgentID, res.CredentialID).Scan(&ac); err != nil || ac != 1 {
		t.Errorf("agent_credentials missing: %d err=%v", ac, err)
	}
}

// TestOnboardingSetup_AlreadyCompleted exercises the CAS guard: the second
// call must fail with ErrOnboardingAlreadyCompleted and not duplicate rows.
func TestOnboardingSetup_AlreadyCompleted(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	db, uid, wid := setupOnboardingDB(t)
	svc := newTestSvc(t, db)
	now := time.Now().UTC().Format(time.RFC3339)
	llm := "m"
	p := SetupParams{
		UserID: uid, WorkspaceID: wid, CrewName: "C", CrewSlug: "c",
		AgentName: "A", AgentSlug: "a", CliAdapter: "CLAUDE_CODE",
		LLMProvider: "ANTHROPIC", LLMModel: &llm,
		EnvVarName: "X", CredentialName: "X", CredentialValue: "v",
		Now: now,
	}
	if _, err := svc.Setup(context.Background(), p); err != nil {
		t.Fatalf("first setup: %v", err)
	}
	// Use distinct slugs in the second attempt to prove the failure isn't
	// a UNIQUE-constraint side effect.
	p.CrewSlug = "c2"
	p.AgentSlug = "a2"
	_, err := svc.Setup(context.Background(), p)
	if !errors.Is(err, ErrOnboardingAlreadyCompleted) {
		t.Fatalf("expected ErrOnboardingAlreadyCompleted, got %v", err)
	}
	// Verify second attempt did NOT insert a new crew (rollback must have happened).
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE slug='c2'").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected no second crew, got %d rows", n)
	}
}

// TestOnboardingSetup_DuplicateAgentSlug fails inside the agent INSERT and
// rolls back everything that came before in the transaction.
func TestOnboardingSetup_DuplicateAgentSlug(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	db, uid, wid := setupOnboardingDB(t)
	now := time.Now().UTC().Format(time.RFC3339)
	// Pre-insert an agent that conflicts on the slug we'll use.
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('crw0','`+wid+`','Pre','pre',?,?)`, now, now); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, created_at, updated_at) VALUES ('agX','crw0','`+wid+`','Pre','dup-agent',?,?)`, now, now); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	svc := newTestSvc(t, db)
	llm := "m"
	_, err := svc.Setup(context.Background(), SetupParams{
		UserID: uid, WorkspaceID: wid, CrewName: "C", CrewSlug: "fresh",
		AgentName: "A", AgentSlug: "dup-agent", CliAdapter: "CLAUDE_CODE",
		LLMProvider: "ANTHROPIC", LLMModel: &llm,
		Now: now,
	})
	if err == nil {
		t.Fatal("expected agent slug duplicate error")
	}
	// Crew "fresh" must NOT exist (rollback).
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM crews WHERE slug='fresh'").Scan(&n)
	if n != 0 {
		t.Errorf("expected rollback to discard crew, got %d", n)
	}
}

// TestOnboardingSetup_NoMembership rejects users who don't belong to the
// workspace they're trying to onboard against.
func TestOnboardingSetup_NoMembership(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	db, _, _ := setupOnboardingDB(t)
	svc := newTestSvc(t, db)
	llm := "m"
	_, err := svc.Setup(context.Background(), SetupParams{
		UserID: "ghost", WorkspaceID: "w1", CrewName: "C", CrewSlug: "c",
		AgentName: "A", AgentSlug: "a", CliAdapter: "CLAUDE_CODE",
		LLMProvider: "ANTHROPIC", LLMModel: &llm,
		Now: time.Now().UTC().Format(time.RFC3339),
	})
	if !errors.Is(err, ErrWorkspaceNotFound) {
		t.Errorf("expected ErrWorkspaceNotFound, got %v", err)
	}
}

// TestOnboardingSetup_WithoutCredential leaves CredentialID empty when no
// credential value is supplied. (Common when user defers credential setup.)
func TestOnboardingSetup_WithoutCredential(t *testing.T) {
	db, uid, wid := setupOnboardingDB(t)
	svc := newTestSvc(t, db)
	llm := "m"
	res, err := svc.Setup(context.Background(), SetupParams{
		UserID: uid, WorkspaceID: wid, CrewName: "C", CrewSlug: "c",
		AgentName: "A", AgentSlug: "a", CliAdapter: "CLAUDE_CODE",
		LLMProvider: "ANTHROPIC", LLMModel: &llm,
		Now: time.Now().UTC().Format(time.RFC3339),
		// CredentialValue intentionally empty.
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if res.CredentialID != "" {
		t.Errorf("expected empty credential id, got %q", res.CredentialID)
	}
	// No agent_credential link inserted.
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM agent_credentials WHERE agent_id=?", res.AgentID).Scan(&n)
	if n != 0 {
		t.Errorf("expected zero agent_credentials, got %d", n)
	}
}

// TestOnboardingSetup_NoWorkspaceNameLeavesItAlone covers the branch where
// WorkspaceName is empty and the workspace name should not be updated.
func TestOnboardingSetup_NoWorkspaceNameLeavesItAlone(t *testing.T) {
	db, uid, wid := setupOnboardingDB(t)
	svc := newTestSvc(t, db)
	llm := "m"
	res, err := svc.Setup(context.Background(), SetupParams{
		UserID: uid, WorkspaceID: wid,
		// WorkspaceName intentionally empty
		CrewName: "C", CrewSlug: "c-no-ws",
		AgentName: "A", AgentSlug: "a-no-ws", CliAdapter: "CLAUDE_CODE",
		LLMProvider: "ANTHROPIC", LLMModel: &llm,
		Now: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if res.CrewID == "" {
		t.Error("expected crew created")
	}
	// Workspace name should remain "WS" (the seed value).
	var name string
	_ = db.QueryRow("SELECT name FROM workspaces WHERE id=?", wid).Scan(&name)
	if name != "WS" {
		t.Errorf("workspace name unexpectedly changed to %q", name)
	}
}

// TestOnboardingSetup_DuplicateCrewSlug surfaces SQL UNIQUE-constraint
// failures and rolls everything back atomically.
func TestOnboardingSetup_DuplicateCrewSlug(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	db, uid, wid := setupOnboardingDB(t)
	now := time.Now().UTC().Format(time.RFC3339)
	// Pre-insert a crew with the slug we'll try to use.
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('precx','`+wid+`','Pre','dup',?,?)`, now, now); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := newTestSvc(t, db)
	llm := "m"
	_, err := svc.Setup(context.Background(), SetupParams{
		UserID: uid, WorkspaceID: wid, CrewName: "Dup", CrewSlug: "dup",
		AgentName: "A", AgentSlug: "a", CliAdapter: "CLAUDE_CODE",
		LLMProvider: "ANTHROPIC", LLMModel: &llm,
		Now: now,
	})
	if err == nil {
		t.Fatal("expected duplicate slug error")
	}
	// onboarding_completed must be back to 0 (rollback worked).
	var oc int
	_ = db.QueryRow("SELECT onboarding_completed FROM users WHERE id=?", uid).Scan(&oc)
	if oc != 0 {
		t.Errorf("expected onboarding rolled back, got %d", oc)
	}
}

// TestOnboardingSetup_EncryptionMissing rolls back when ENCRYPTION_KEY is
// unset (Encrypt fails) and credential was requested.
func TestOnboardingSetup_EncryptionMissing(t *testing.T) {
	// Explicitly clear ENCRYPTION_KEY in this test.
	os.Unsetenv("ENCRYPTION_KEY")

	db, uid, wid := setupOnboardingDB(t)
	svc := newTestSvc(t, db)
	llm := "m"
	_, err := svc.Setup(context.Background(), SetupParams{
		UserID: uid, WorkspaceID: wid, CrewName: "C", CrewSlug: "c",
		AgentName: "A", AgentSlug: "a", CliAdapter: "CLAUDE_CODE",
		LLMProvider: "ANTHROPIC", LLMModel: &llm,
		EnvVarName: "X", CredentialName: "X", CredentialValue: "secret",
		Now: time.Now().UTC().Format(time.RFC3339),
	})
	if err == nil {
		t.Fatal("expected encrypt failure when ENCRYPTION_KEY is missing")
	}
	// Atomicity check: nothing should have been committed (no crew, no agent,
	// onboarding flag still 0).
	var crews int
	_ = db.QueryRow("SELECT COUNT(*) FROM crews").Scan(&crews)
	if crews != 0 {
		t.Errorf("expected 0 crews after rollback, got %d", crews)
	}
	var oc int
	_ = db.QueryRow("SELECT onboarding_completed FROM users WHERE id=?", uid).Scan(&oc)
	if oc != 0 {
		t.Errorf("expected onboarding_completed=0 after rollback, got %d", oc)
	}
}
