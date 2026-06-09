package server

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"

	_ "modernc.org/sqlite"
)

func siLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// siDB opens a fully-migrated SQLite (through v102, which lands
// skill_invocations + the skills lifecycle columns) and seeds the
// workspace/crew/agent/skill fixtures the observer resolves against.
func siDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := database.Open("file:" + filepath.Join(t.TempDir(), "si.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := database.Migrate(context.Background(), d.DB, siLogger()); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','WS1','ws1')`)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1','ws1','C1','c1')`)
	mustExec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a1','cr1','ws1','A1','a1')`)
	// "deploy" is assigned + enabled; "lint" is assigned but disabled;
	// "orphan" exists but is unassigned. Only "deploy" should ever match.
	mustExec(`INSERT INTO skills (id, name, slug, display_name) VALUES ('sk_deploy','deploy','deploy','Deploy')`)
	mustExec(`INSERT INTO skills (id, name, slug, display_name) VALUES ('sk_lint','lint','lint','Lint')`)
	mustExec(`INSERT INTO skills (id, name, slug, display_name) VALUES ('sk_orphan','orphan','orphan','Orphan')`)
	mustExec(`INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as1','a1','sk_deploy',1)`)
	mustExec(`INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as2','a1','sk_lint',0)`)
	return d.DB
}

func countInvocations(t *testing.T, db *sql.DB, skillID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM skill_invocations WHERE skill_id = ?`, skillID).Scan(&n); err != nil {
		t.Fatalf("count invocations: %v", err)
	}
	return n
}

func skillUsage(t *testing.T, db *sql.DB, skillID string) (count, errCount int, lastUsed sql.NullString) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT usage_count, error_count, last_used_at FROM skills WHERE id = ?`, skillID).
		Scan(&count, &errCount, &lastUsed); err != nil {
		t.Fatalf("read usage: %v", err)
	}
	return count, errCount, lastUsed
}

// drainWriter flushes the journal writer so the asynchronous batched
// emit lands on disk before the test reads it back.
func drainWriter(t *testing.T, w *journal.Writer) {
	t.Helper()
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("journal flush: %v", err)
	}
}

func TestSkillInvocationObserver_Match(t *testing.T) {
	cases := []struct {
		name        string
		obs         orchestrator.SkillInvocation
		wantInvoke  bool
		wantSkillID string
	}{
		{
			name: "skill_tool_with_slug_input",
			obs: orchestrator.SkillInvocation{
				WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
				ToolName: "Skill",
				Payload:  map[string]any{"input": map[string]any{"skill": "deploy"}},
			},
			wantInvoke:  true,
			wantSkillID: "sk_deploy",
		},
		{
			name: "tool_name_matches_assigned_slug",
			obs: orchestrator.SkillInvocation{
				WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
				ToolName: "deploy",
			},
			wantInvoke:  true,
			wantSkillID: "sk_deploy",
		},
		{
			name: "skill_tool_command_field_input",
			obs: orchestrator.SkillInvocation{
				WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
				ToolName: "Skill",
				Payload:  map[string]any{"input": map[string]any{"command": "deploy"}},
			},
			wantInvoke:  true,
			wantSkillID: "sk_deploy",
		},
		{
			name: "read_tool_no_match",
			obs: orchestrator.SkillInvocation{
				WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
				ToolName: "Read",
				Payload:  map[string]any{"input": map[string]any{"file_path": "/x"}},
			},
			wantInvoke: false,
		},
		{
			name: "bash_tool_no_match",
			obs: orchestrator.SkillInvocation{
				WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
				ToolName: "Bash",
				Payload:  map[string]any{"input": map[string]any{"command": "ls"}},
			},
			wantInvoke: false,
		},
		{
			name: "skill_tool_unassigned_slug_no_match",
			obs: orchestrator.SkillInvocation{
				WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
				ToolName: "Skill",
				Payload:  map[string]any{"input": map[string]any{"skill": "orphan"}},
			},
			wantInvoke: false,
		},
		{
			name: "disabled_assignment_no_match",
			obs: orchestrator.SkillInvocation{
				WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
				ToolName: "lint",
			},
			wantInvoke: false,
		},
		{
			name: "unknown_agent_no_match",
			obs: orchestrator.SkillInvocation{
				WorkspaceID: "ws1", CrewID: "cr1", AgentID: "ghost",
				ToolName: "deploy",
			},
			wantInvoke: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := siDB(t)
			w := journal.NewWriter(db, siLogger(), journal.WriterOptions{})
			defer func() { _ = w.Close() }()
			o := newSkillInvocationObserver(siLogger(), db, w)

			o.Observe(tc.obs)
			drainWriter(t, w)

			got := countInvocations(t, db, tc.wantSkillID)
			if tc.wantInvoke && got != 1 {
				t.Fatalf("invocations(%s) = %d, want 1", tc.wantSkillID, got)
			}
			if !tc.wantInvoke {
				// No skill_invocations row should have landed at all.
				var total int
				if err := db.QueryRow(`SELECT COUNT(*) FROM skill_invocations`).Scan(&total); err != nil {
					t.Fatal(err)
				}
				if total != 0 {
					t.Fatalf("expected no invocation rows, got %d", total)
				}
			}
		})
	}
}

// TestSkillInvocationObserver_ProducerToConsumer is the end-to-end
// integration: a single Skill tool call lands exactly one
// skill_invocations row, bumps skills.usage_count to 1 + last_used_at,
// emits a skill.invoked journal entry, and makes loadSkillSweepInputs
// report a non-zero InvocationCount for the matched skill.
func TestSkillInvocationObserver_ProducerToConsumer(t *testing.T) {
	db := siDB(t)
	w := journal.NewWriter(db, siLogger(), journal.WriterOptions{})
	defer func() { _ = w.Close() }()
	o := newSkillInvocationObserver(siLogger(), db, w)

	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
		ToolName: "Skill",
		Payload:  map[string]any{"input": map[string]any{"skill": "deploy"}},
	})
	drainWriter(t, w)

	// One skill_invocations row.
	if n := countInvocations(t, db, "sk_deploy"); n != 1 {
		t.Fatalf("skill_invocations rows = %d, want 1", n)
	}

	// usage_count == 1, error_count == 0 (exit 0), last_used_at set.
	count, errCount, lastUsed := skillUsage(t, db, "sk_deploy")
	if count != 1 {
		t.Fatalf("usage_count = %d, want 1", count)
	}
	if errCount != 0 {
		t.Fatalf("error_count = %d, want 0", errCount)
	}
	if !lastUsed.Valid || lastUsed.String == "" {
		t.Fatalf("last_used_at not set: %+v", lastUsed)
	}

	// A skill.invoked journal entry landed.
	var jrows int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM journal_entries WHERE entry_type = 'skill.invoked' AND agent_id = 'a1'`).
		Scan(&jrows); err != nil {
		t.Fatalf("count journal: %v", err)
	}
	if jrows != 1 {
		t.Fatalf("skill.invoked journal entries = %d, want 1", jrows)
	}

	// loadSkillSweepInputs now reports a non-zero InvocationCount for
	// the matched skill — the consumer (F4.1 sweep) sees the telemetry.
	inputs, err := loadSkillSweepInputs(context.Background(), db, siLogger())
	if err != nil {
		t.Fatalf("loadSkillSweepInputs: %v", err)
	}
	var found bool
	for _, in := range inputs {
		if in.Skill.ID == "sk_deploy" {
			found = true
			if in.Stats.InvocationCount < 1 {
				t.Fatalf("InvocationCount = %d, want >= 1", in.Stats.InvocationCount)
			}
		}
	}
	if !found {
		t.Fatal("sk_deploy missing from sweep inputs")
	}
}

// TestSkillInvocationObserver_ErrorExitBumpsErrorCount asserts a
// non-zero exit_code in the payload bumps skills.error_count.
func TestSkillInvocationObserver_ErrorExitBumpsErrorCount(t *testing.T) {
	db := siDB(t)
	w := journal.NewWriter(db, siLogger(), journal.WriterOptions{})
	defer func() { _ = w.Close() }()
	o := newSkillInvocationObserver(siLogger(), db, w)

	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
		ToolName: "Skill",
		Payload: map[string]any{
			"input":     map[string]any{"skill": "deploy"},
			"exit_code": float64(2),
		},
	})
	drainWriter(t, w)

	count, errCount, _ := skillUsage(t, db, "sk_deploy")
	if count != 1 {
		t.Fatalf("usage_count = %d, want 1", count)
	}
	if errCount != 1 {
		t.Fatalf("error_count = %d, want 1", errCount)
	}
}

// TestSkillInvocationObserver_CachePerAgent confirms the slug cache is
// keyed per agent_id: a second agent's assignments don't leak through.
func TestSkillInvocationObserver_CachePerAgent(t *testing.T) {
	db := siDB(t)
	// Second agent with a different assigned skill.
	for _, q := range []string{
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr2','ws1','C2','c2')`,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a2','cr2','ws1','A2','a2')`,
		`INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as3','a2','sk_orphan',1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	w := journal.NewWriter(db, siLogger(), journal.WriterOptions{})
	defer func() { _ = w.Close() }()
	o := newSkillInvocationObserver(siLogger(), db, w)

	// a1 calls "deploy" (assigned) → match; "orphan" (a2's) → no match.
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", ToolName: "deploy"})
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", ToolName: "orphan"})
	// a2 calls "orphan" (its own) → match; "deploy" (a1's) → no match.
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr2", AgentID: "a2", ToolName: "orphan"})
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr2", AgentID: "a2", ToolName: "deploy"})
	drainWriter(t, w)

	if n := countInvocations(t, db, "sk_deploy"); n != 1 {
		t.Fatalf("deploy invocations = %d, want 1 (only a1)", n)
	}
	if n := countInvocations(t, db, "sk_orphan"); n != 1 {
		t.Fatalf("orphan invocations = %d, want 1 (only a2)", n)
	}
}

// TestSkillInvocationObserver_SlugCacheTTLRefresh proves an assignment
// change made after the cache is warm is reflected once the TTL elapses
// (and not before) — guards against the process-lifetime-staleness that
// would otherwise miss a newly-enabled skill.
func TestSkillInvocationObserver_SlugCacheTTLRefresh(t *testing.T) {
	db := siDB(t)
	w := journal.NewWriter(db, siLogger(), journal.WriterOptions{})
	defer func() { _ = w.Close() }()
	o := newSkillInvocationObserver(siLogger(), db, w)

	base := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := base
	o.now = func() time.Time { return clk }

	// sk_lint is assigned to a1 but DISABLED (as2.enabled=0): calling it
	// must not match, and this warms the cache for a1.
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", ToolName: "lint"})
	drainWriter(t, w)
	if n := countInvocations(t, db, "sk_lint"); n != 0 {
		t.Fatalf("disabled skill should not match; got %d", n)
	}

	// Operator enables sk_lint for a1.
	if _, err := db.Exec(`UPDATE agent_skills SET enabled = 1 WHERE id = 'as2'`); err != nil {
		t.Fatal(err)
	}

	// Within the TTL window the warm (stale) cache still misses.
	clk = base.Add(skillSlugCacheTTL / 2)
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", ToolName: "lint"})
	drainWriter(t, w)
	if n := countInvocations(t, db, "sk_lint"); n != 0 {
		t.Fatalf("stale cache should still miss within TTL; got %d", n)
	}

	// Past the TTL the cache refreshes and the now-enabled skill matches.
	clk = base.Add(skillSlugCacheTTL + time.Second)
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", ToolName: "lint"})
	drainWriter(t, w)
	if n := countInvocations(t, db, "sk_lint"); n != 1 {
		t.Fatalf("after TTL the enabled skill should match; got %d", n)
	}
}

func TestMatchSkillSlug(t *testing.T) {
	// Matching is gated on the agent's assigned slugs.
	assigned := map[string]string{"deploy": "sk_deploy", "code-reviewer": "sk_cr"}
	cases := []struct {
		name    string
		tool    string
		payload map[string]any
		want    string
	}{
		{"skill_skill_key", "Skill", map[string]any{"input": map[string]any{"skill": "deploy"}}, "deploy"},
		{"skill_command_key", "Skill", map[string]any{"input": map[string]any{"command": "deploy"}}, "deploy"},
		{"skill_name_key", "Skill", map[string]any{"input": map[string]any{"name": "deploy"}}, "deploy"},
		{"skill_slug_key", "Skill", map[string]any{"input": map[string]any{"slug": "deploy"}}, "deploy"},
		// Key-agnostic: slug under an unexpected key is still found by the value scan.
		{"skill_unknown_key", "Skill", map[string]any{"input": map[string]any{"args": "code-reviewer"}}, "code-reviewer"},
		// Slug-led command string resolves via the leading token.
		{"skill_command_with_args", "Skill", map[string]any{"input": map[string]any{"command": "code-reviewer review main.go"}}, "code-reviewer"},
		// A value that is not an assigned slug must not match (no false positive).
		{"skill_unassigned", "Skill", map[string]any{"input": map[string]any{"skill": "unknown-skill"}}, ""},
		{"skill_no_key", "Skill", map[string]any{"input": map[string]any{"foo": "bar"}}, ""},
		{"skill_empty_value", "Skill", map[string]any{"input": map[string]any{"skill": ""}}, ""},
		{"skill_nil_payload", "Skill", nil, ""},
		{"direct_tool_name", "deploy", nil, "deploy"},
		// A non-skill tool (Read) is no longer mistaken for a skill.
		{"read_not_a_skill", "Read", map[string]any{"input": map[string]any{"file_path": "/x"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchSkillSlug(tc.tool, tc.payload, assigned); got != tc.want {
				t.Fatalf("matchSkillSlug(%q) = %q, want %q", tc.tool, got, tc.want)
			}
		})
	}
}

func TestPayloadInput(t *testing.T) {
	if got := payloadInput(nil); len(got) != 0 {
		t.Fatalf("nil payload → %v, want empty", got)
	}
	// "input" present but not a map → empty map.
	if got := payloadInput(map[string]any{"input": "notamap"}); len(got) != 0 {
		t.Fatalf("malformed input → %v, want empty", got)
	}
	in := map[string]any{"k": "v"}
	if got := payloadInput(map[string]any{"input": in}); got["k"] != "v" {
		t.Fatalf("valid input not returned: %v", got)
	}
}

func TestPayloadInt(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    int
	}{
		{"nil", nil, 0},
		{"float64", map[string]any{"exit_code": float64(3)}, 3},
		{"int", map[string]any{"exit_code": 5}, 5},
		{"int64", map[string]any{"exit_code": int64(7)}, 7},
		{"string_ignored", map[string]any{"exit_code": "9"}, 0},
		{"missing_key", map[string]any{"other": 1}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := payloadInt(tc.payload, "exit_code"); got != tc.want {
				t.Fatalf("payloadInt = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestMarshalInvocationPayload(t *testing.T) {
	// Normal case: slug + input.
	js := marshalInvocationPayload(map[string]any{"input": map[string]any{"k": "v"}}, "deploy")
	if js == "" || js == "{}" {
		t.Fatalf("unexpected marshal: %q", js)
	}

	// Overflow: an oversized input forces the truncated fallback.
	big := make([]byte, 5000)
	for i := range big {
		big[i] = 'x'
	}
	overflow := marshalInvocationPayload(
		map[string]any{"input": map[string]any{"blob": string(big)}}, "deploy")
	if len(overflow) > 200 {
		t.Fatalf("overflow not truncated: len=%d", len(overflow))
	}
	if !strings.Contains(overflow, "truncated") {
		t.Fatalf("truncated marker missing: %q", overflow)
	}
}

// TestSkillInvocationObserver_DBErrorPaths drives the resolve + record
// error branches by closing the DB before Observe runs.
func TestSkillInvocationObserver_DBErrorPaths(t *testing.T) {
	db := siDB(t)
	w := journal.NewWriter(db, siLogger(), journal.WriterOptions{})
	defer func() { _ = w.Close() }()
	o := newSkillInvocationObserver(siLogger(), db, w)
	// Close the DB so QueryContext (assignedSlugs) errors.
	_ = db.Close()
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", ToolName: "deploy"})
	// No panic + no row is the assertion (DB is closed; can't query).
}

// TestSkillInvocationObserver_RecordErrorPath primes the slug cache for a
// resolvable agent, then closes the DB so the record txn fails — exercising
// the "record failed" branch without a resolve error.
func TestSkillInvocationObserver_RecordErrorPath(t *testing.T) {
	db := siDB(t)
	w := journal.NewWriter(db, siLogger(), journal.WriterOptions{})
	defer func() { _ = w.Close() }()
	o := newSkillInvocationObserver(siLogger(), db, w)
	// Warm the per-agent slug cache so the second Observe skips the query.
	if _, err := o.assignedSlugs(context.Background(), "a1"); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	_ = db.Close() // now BeginTx in record() fails
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", ToolName: "deploy"})
	// No panic; record error is logged, not returned.
}

// TestSkillInvocationObserver_InsertErrorInTxn drops the
// skill_invocations table so BeginTx succeeds but the INSERT fails,
// exercising the record() INSERT error-return branch (rollback path).
func TestSkillInvocationObserver_InsertErrorInTxn(t *testing.T) {
	db := siDB(t)
	w := journal.NewWriter(db, siLogger(), journal.WriterOptions{})
	defer func() { _ = w.Close() }()
	o := newSkillInvocationObserver(siLogger(), db, w)
	if _, err := db.Exec(`DROP TABLE skill_invocations`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", ToolName: "deploy"})
	// usage_count must NOT have advanced — the txn rolled back.
	var count int
	if err := db.QueryRow(`SELECT usage_count FROM skills WHERE id = 'sk_deploy'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("usage_count = %d, want 0 (txn must roll back on INSERT failure)", count)
	}
}

// TestSkillInvocationObserver_UpdateErrorInTxn installs a trigger that
// aborts the skills UPDATE so the INSERT succeeds but the denormalisation
// fails — covers record()'s UPDATE error-return + rollback branch.
func TestSkillInvocationObserver_UpdateErrorInTxn(t *testing.T) {
	db := siDB(t)
	w := journal.NewWriter(db, siLogger(), journal.WriterOptions{})
	defer func() { _ = w.Close() }()
	o := newSkillInvocationObserver(siLogger(), db, w)
	if _, err := db.Exec(
		`CREATE TRIGGER block_skill_update BEFORE UPDATE ON skills
		 BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", ToolName: "deploy"})
	// Whole txn rolled back: no invocation row either.
	if n := countInvocations(t, db, "sk_deploy"); n != 0 {
		t.Fatalf("invocations = %d, want 0 (UPDATE failure rolls back the INSERT)", n)
	}
}

// TestSkillInvocationObserver_NilJournal records the invocation but skips
// the journal emit when no writer is wired — covers emit()'s nil guard.
func TestSkillInvocationObserver_NilJournal(t *testing.T) {
	db := siDB(t)
	o := newSkillInvocationObserver(siLogger(), db, nil)
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", ToolName: "deploy"})
	// The skill_invocations row still lands; only the journal emit is skipped.
	if n := countInvocations(t, db, "sk_deploy"); n != 1 {
		t.Fatalf("invocations = %d, want 1 even without journal", n)
	}
}

func TestSkillInvocationObserver_NilSafe(t *testing.T) {
	// nil DB / writer must not panic — best-effort hot path.
	o := newSkillInvocationObserver(siLogger(), nil, nil)
	o.Observe(orchestrator.SkillInvocation{
		WorkspaceID: "ws1", AgentID: "a1", ToolName: "deploy"})
	// empty agent/workspace are silently ignored.
	db := siDB(t)
	w := journal.NewWriter(db, siLogger(), journal.WriterOptions{})
	defer func() { _ = w.Close() }()
	o2 := newSkillInvocationObserver(siLogger(), db, w)
	o2.Observe(orchestrator.SkillInvocation{ToolName: "deploy"})
	o2.Observe(orchestrator.SkillInvocation{WorkspaceID: "ws1", ToolName: "deploy"})
	o2.Observe(orchestrator.SkillInvocation{WorkspaceID: "ws1", AgentID: "a1", ToolName: ""})
	drainWriter(t, w)
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM skill_invocations`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("expected no rows from invalid observations, got %d", total)
	}
}
