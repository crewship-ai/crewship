package server

// CodeRabbit regressions on PR-C (PR #470).
//
// Pins:
//
//   - sqlSkillPersister.WriteInboxItem fans out one inbox row per
//     workspace that has the skill assigned (was LIMIT 1, dropping all
//     but one workspace's notification).
//   - inbox.Insert errors propagate through WriteInboxItem and
//     TriggerConsolidation (was silently dropped — sweeps reported
//     success even when the inbox row never landed).

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"

	_ "modernc.org/sqlite"
)

func krLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func krDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := database.Open("file:" + filepath.Join(t.TempDir(), "kr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := database.Migrate(context.Background(), d.DB, krLogger()); err != nil {
		t.Fatal(err)
	}
	return d.DB
}

// TestCR_SkillRoutine_FansOutInboxPerWorkspace asserts the F4.1 routine
// writes one inbox_items row per workspace that has at least one enabled
// agent_skills entry on the skill. The previous LIMIT 1 select dropped
// all but one workspace's notification.
func TestCR_SkillRoutine_FansOutInboxPerWorkspace(t *testing.T) {
	db := krDB(t)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}

	// Two workspaces, each with one agent, both agents share the same
	// skill enabled. The routine must emit two inbox rows.
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','WS1','ws1')`)
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws2','WS2','ws2')`)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1','ws1','C1','c1')`)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr2','ws2','C2','c2')`)
	mustExec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a1','cr1','ws1','A1','a1')`)
	mustExec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a2','cr2','ws2','A2','a2')`)
	mustExec(`INSERT INTO skills (id, name, slug, display_name) VALUES ('sk1','do','do','Do')`)
	mustExec(`INSERT INTO agent_skills (agent_id, skill_id, enabled) VALUES ('a1','sk1',1)`)
	mustExec(`INSERT INTO agent_skills (agent_id, skill_id, enabled) VALUES ('a2','sk1',1)`)

	pers := &sqlSkillPersister{db: db, logger: krLogger()}
	if err := pers.WriteInboxItem(context.Background(), "sk1", "fanout test", false); err != nil {
		t.Fatalf("WriteInboxItem: %v", err)
	}

	var rows int
	if err := db.QueryRow(
		`SELECT COUNT(DISTINCT workspace_id) FROM inbox_items WHERE source_id LIKE 'skill_review_sk1_%'`,
	).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		t.Errorf("distinct workspaces with inbox row = %d, want 2 (regression: LIMIT 1 dropped fanout)", rows)
	}
}

// TestCR_SkillRoutine_NoAssignment_NoOp ensures the no-workspace case
// remains silent (returns nil, no inbox write) when the skill isn't
// assigned to any live agent — keeps the behaviour stable while the
// fanout change widens the "happy path" side.
func TestCR_SkillRoutine_NoAssignment_NoOp(t *testing.T) {
	db := krDB(t)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO skills (id, name, slug, display_name) VALUES ('orphan','o','o','O')`)

	pers := &sqlSkillPersister{db: db, logger: krLogger()}
	if err := pers.WriteInboxItem(context.Background(), "orphan", "no targets", false); err != nil {
		t.Fatalf("WriteInboxItem: %v", err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("inbox rows = %d, want 0 for orphan skill", rows)
	}
}

// TestCR_SkillRoutine_SanitizesInfraError pins that a curator LLM outage
// never leaks its raw "Keeper LLM unavailable: paymaster…" plumbing into
// the inbox body. The skill still needs review, so the row is kept — but
// the body is the friendly fallback and the raw text is tucked into
// payload.raw_reason for operators/logs.
func TestCR_SkillRoutine_SanitizesInfraError(t *testing.T) {
	db := krDB(t)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','WS1','ws1')`)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1','ws1','C1','c1')`)
	mustExec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a1','cr1','ws1','A1','a1')`)
	mustExec(`INSERT INTO skills (id, name, slug, display_name) VALUES ('sk1','do','do','Do')`)
	mustExec(`INSERT INTO agent_skills (agent_id, skill_id, enabled) VALUES ('a1','sk1',1)`)

	leak := "Curator unavailable or returned unparseable response — operator review (underlying: Keeper LLM unavailable: paymaster: workspace_id required — deny by default)"
	pers := &sqlSkillPersister{db: db, logger: krLogger()}
	if err := pers.WriteInboxItem(context.Background(), "sk1", leak, false); err != nil {
		t.Fatalf("WriteInboxItem: %v", err)
	}

	var body, payload string
	if err := db.QueryRow(
		`SELECT COALESCE(body_md,''), payload_json FROM inbox_items WHERE source_id = 'skill_review_sk1_ws1'`,
	).Scan(&body, &payload); err != nil {
		t.Fatalf("select: %v", err)
	}
	for _, leaked := range []string{"paymaster", "deny by default", "LLM", "underlying:"} {
		if containsStr(body, leaked) {
			t.Errorf("inbox body leaks %q: %q", leaked, body)
		}
	}
	if !containsStr(payload, "raw_reason") {
		t.Errorf("payload missing raw_reason for operators: %q", payload)
	}
}

// TestCR_MemoryHealthAdvisory_InfraSuppressed pins the inbox-flooding fix:
// when the memory-health evaluator's reason is an infrastructure outage
// (curator LLM down), the advisory carries nothing actionable and is
// SUPPRESSED — no inbox row. A real finding still lands, with a clean body.
func TestCR_MemoryHealthAdvisory_InfraSuppressed(t *testing.T) {
	db := krDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','WS1','ws1')`); err != nil {
		t.Fatal(err)
	}
	pers := &sqlMemoryHealthPersister{db: db, logger: krLogger()}

	// Infra outage → suppressed, zero rows.
	leak := "MemoryHealth Curator unavailable or unparseable — operator review (underlying: Keeper LLM unavailable: paymaster: workspace_id required — deny by default)"
	if err := pers.WriteInboxItem(context.Background(), "ws1", "crewA", leak, false); err != nil {
		t.Fatalf("WriteInboxItem(infra): %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("infra advisory wrote %d rows, want 0 (should be suppressed)", n)
	}

	// Real finding → one row, clean body.
	real := "Crew memory has 4 contradictions in deployment runbook entries."
	if err := pers.WriteInboxItem(context.Background(), "ws1", "crewB", real, false); err != nil {
		t.Fatalf("WriteInboxItem(real): %v", err)
	}
	var body string
	if err := db.QueryRow(`SELECT COALESCE(body_md,'') FROM inbox_items`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != real {
		t.Errorf("real-finding body = %q, want %q", body, real)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return len(sub) == 0
}
