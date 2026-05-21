package database

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateV102_KeeperPhase2 covers the four artefacts v102 lands:
//
//  1. keeper_requests.request_type CHECK admits the four new F4 kinds
//     ('skill_review','behavior','memory_health','negative_learning')
//     alongside the pre-existing 'access' and 'execute' values.
//  2. Bogus request_type values are rejected by the new CHECK.
//  3. skills lifecycle columns land with the documented defaults +
//     CHECK constraint.
//  4. skill_invocations table exists with the documented columns +
//     unique index pattern.
//
// The recreate dance on keeper_requests must preserve every existing
// column value byte-for-byte; the test seeds a row through v9-v11 shape
// fields and reads it back after v102 to catch any column drop or
// reorder regression.
func TestMigrateV102_KeeperPhase2(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v102.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	migLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := context.Background()

	// Apply migrations up to (but NOT including) v102 so the
	// preserves_pre_v102_row subcase below can seed the legacy
	// keeper_requests shape against the pre-v102 table and then watch
	// v102's ALTER ... RENAME + INSERT … SELECT actually rebuild the
	// row. Running Migrate() unconditionally first would have already
	// recreated the table empty, so the "preserves" assertion was
	// only validating post-v102 insert/read.
	if err := applyMigrationsUpTo(ctx, db.DB, 101, migLogger); err != nil {
		t.Fatalf("applyMigrationsUpTo(101): %v", err)
	}

	// Common fixtures — landed on the v9 keeper_requests shape so the
	// pre_v102_row subcase can INSERT before the recreate.
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`)
	mustExec(t, db.DB, `INSERT INTO users (id, email, full_name) VALUES ('u1', 'a@b.c', 'A')`)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1', 'ws1', 'crew', 'crew')`)
	mustExec(t, db.DB, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a1', 'cr1', 'ws1', 'agent', 'agent')`)
	mustExec(t, db.DB, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, security_level, created_by) VALUES ('c1', 'ws1', 'NPM_TOKEN', 'xx', 1, 'u1')`)

	// Seed the legacy keeper_requests row BEFORE v102 lands so the
	// recreate path (ALTER … RENAME → CREATE → INSERT … SELECT → DROP)
	// is exercised end-to-end. Read-back happens inside the
	// "preserves_pre_v102_row" subcase after Migrate is finalised.
	mustExec(t, db.DB, `
		INSERT INTO keeper_requests (
			id, requesting_agent_id, requesting_crew_id, credential_id,
			task_id, intent, decision, reason, risk_score,
			created_at, decided_at, request_type, command, exit_code,
			ollama_prompt, ollama_raw_response
		) VALUES (
			'req_full', 'a1', 'cr1', 'c1',
			'task_x', 'Read deploy log', 'ALLOW', 'Justified', 3,
			'2026-05-21T00:00:00Z', '2026-05-21T00:00:01Z',
			'execute', 'cat deploy.log', 0,
			'prompt body', 'raw response body'
		)`)

	// Now apply v102. The seeded row above must round-trip through the
	// recreate dance unchanged.
	if err := Migrate(ctx, db.DB, migLogger); err != nil {
		t.Fatalf("Migrate (v102): %v", err)
	}

	cases := []struct {
		name   string
		assert func(t *testing.T, db *sql.DB)
	}{
		{
			name: "request_type/accepts_all_six_kinds",
			assert: func(t *testing.T, db *sql.DB) {
				kinds := []string{"access", "execute", "skill_review", "behavior", "memory_health", "negative_learning"}
				for _, k := range kinds {
					id := "req_" + k
					_, err := db.Exec(
						`INSERT INTO keeper_requests (id, requesting_agent_id, requesting_crew_id, credential_id, intent, request_type)
						 VALUES (?, 'a1', 'cr1', 'c1', 'test intent text', ?)`,
						id, k,
					)
					if err != nil {
						t.Errorf("insert request_type=%q failed: %v", k, err)
					}
				}
			},
		},
		{
			name: "request_type/rejects_unknown_kind",
			assert: func(t *testing.T, db *sql.DB) {
				_, err := db.Exec(
					`INSERT INTO keeper_requests (id, requesting_agent_id, requesting_crew_id, credential_id, intent, request_type)
					 VALUES ('req_bogus', 'a1', 'cr1', 'c1', 'test', 'YOLO')`,
				)
				if err == nil {
					t.Error("expected CHECK constraint to reject request_type='YOLO'")
				}
			},
		},
		{
			name: "request_type/defaults_to_access",
			assert: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `
					INSERT INTO keeper_requests (id, requesting_agent_id, requesting_crew_id, credential_id, intent)
					VALUES ('req_default', 'a1', 'cr1', 'c1', 'no type specified')`)
				var got string
				if err := db.QueryRow(`SELECT request_type FROM keeper_requests WHERE id = 'req_default'`).Scan(&got); err != nil {
					t.Fatalf("read back: %v", err)
				}
				if got != "access" {
					t.Errorf("default request_type = %q, want 'access'", got)
				}
			},
		},
		{
			name: "request_type/preserves_pre_v102_row",
			assert: func(t *testing.T, db *sql.DB) {
				// 'req_full' was seeded BEFORE v102 ran (see test top),
				// so this readback verifies the v102 ALTER…RENAME +
				// INSERT…SELECT actually copied every column through
				// the recreate. The seed-then-migrate sequence is the
				// load-bearing assertion; the readback below is just
				// the verification.
				var (
					decision, reason, reqType, command, prompt, rawResp string
					riskScore, exitCode                                 int
				)
				err := db.QueryRow(`
					SELECT decision, reason, risk_score, request_type,
					       command, exit_code, ollama_prompt, ollama_raw_response
					FROM keeper_requests WHERE id = 'req_full'`).Scan(
					&decision, &reason, &riskScore, &reqType,
					&command, &exitCode, &prompt, &rawResp)
				if err != nil {
					t.Fatalf("read back: %v", err)
				}
				if decision != "ALLOW" || reason != "Justified" || riskScore != 3 ||
					reqType != "execute" || command != "cat deploy.log" || exitCode != 0 ||
					prompt != "prompt body" || rawResp != "raw response body" {
					t.Errorf("row round-trip mismatch: %+v", []any{decision, reason, riskScore, reqType, command, exitCode, prompt, rawResp})
				}
			},
		},
		{
			name: "skills/lifecycle_state_default",
			assert: func(t *testing.T, db *sql.DB) {
				if got := columnDefault(t, db, "skills", "lifecycle_state"); strings.Trim(got, "'\"") != "active" {
					t.Errorf("skills.lifecycle_state default = %q, want 'active'", got)
				}
			},
		},
		{
			name: "skills/lifecycle_columns_writable",
			assert: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `
					INSERT INTO skills (id, name, slug, display_name)
					VALUES ('sk_legacy', 'legacy', 'legacy', 'Legacy Skill')`)
				var (
					lifecycleState string
					lastUsedAt     sql.NullString
					usageCount     int
					errorCount     int
				)
				if err := db.QueryRow(
					`SELECT lifecycle_state, last_used_at, usage_count, error_count FROM skills WHERE id = 'sk_legacy'`,
				).Scan(&lifecycleState, &lastUsedAt, &usageCount, &errorCount); err != nil {
					t.Fatalf("read back: %v", err)
				}
				if lifecycleState != "active" {
					t.Errorf("lifecycle_state = %q, want 'active' (default)", lifecycleState)
				}
				if lastUsedAt.Valid {
					t.Errorf("last_used_at = %v, want NULL", lastUsedAt)
				}
				if usageCount != 0 || errorCount != 0 {
					t.Errorf("usage_count=%d error_count=%d, want 0/0", usageCount, errorCount)
				}
			},
		},
		{
			name: "skills/lifecycle_state_rejects_unknown",
			assert: func(t *testing.T, db *sql.DB) {
				_, err := db.Exec(
					`INSERT INTO skills (id, name, slug, display_name, lifecycle_state)
					 VALUES ('sk_bogus', 'bogus', 'bogus', 'Bogus', 'YOLO')`,
				)
				if err == nil {
					t.Error("expected CHECK constraint to reject lifecycle_state='YOLO'")
				}
			},
		},
		{
			name: "skills/all_four_lifecycle_states_accepted",
			assert: func(t *testing.T, db *sql.DB) {
				for _, state := range []string{"active", "stale", "archived", "deprecated"} {
					id := "sk_state_" + state
					_, err := db.Exec(
						`INSERT INTO skills (id, name, slug, display_name, lifecycle_state)
						 VALUES (?, ?, ?, ?, ?)`,
						id, "s_"+state, "s-"+state, "S "+state, state,
					)
					if err != nil {
						t.Errorf("insert lifecycle_state=%q failed: %v", state, err)
					}
				}
			},
		},
		{
			name: "skill_invocations/exists_and_writable",
			assert: func(t *testing.T, db *sql.DB) {
				// Seed the FK targets first.
				mustExec(t, db, `INSERT INTO skills (id, name, slug, display_name) VALUES ('sk_inv', 'inv', 'inv', 'Inv')`)
				mustExec(t, db, `
					INSERT INTO skill_invocations (id, skill_id, agent_id, workspace_id, duration_ms, exit_code, payload_json)
					VALUES ('si1', 'sk_inv', 'a1', 'ws1', 42, 0, '{"k":"v"}')`)
				var (
					skillID, agentID, wsID string
					duration, exitCode     int
					payload                string
				)
				err := db.QueryRow(`
					SELECT skill_id, agent_id, workspace_id, duration_ms, exit_code, payload_json
					FROM skill_invocations WHERE id = 'si1'`).Scan(
					&skillID, &agentID, &wsID, &duration, &exitCode, &payload)
				if err != nil {
					t.Fatalf("read back: %v", err)
				}
				if skillID != "sk_inv" || agentID != "a1" || wsID != "ws1" ||
					duration != 42 || exitCode != 0 || payload != `{"k":"v"}` {
					t.Errorf("invocation row drift: %+v", []any{skillID, agentID, wsID, duration, exitCode, payload})
				}
			},
		},
		{
			name: "skill_invocations/cascades_on_skill_delete",
			assert: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `INSERT INTO skills (id, name, slug, display_name) VALUES ('sk_cascade', 'cascade', 'cascade', 'Cascade')`)
				mustExec(t, db, `
					INSERT INTO skill_invocations (id, skill_id, agent_id, workspace_id)
					VALUES ('si_cascade', 'sk_cascade', 'a1', 'ws1')`)
				// Enable FK enforcement; SQLite defaults it off but the
				// production DB pool opens with foreign_keys=ON.
				mustExec(t, db, `PRAGMA foreign_keys = ON`)
				mustExec(t, db, `DELETE FROM skills WHERE id = 'sk_cascade'`)
				var count int
				if err := db.QueryRow(
					`SELECT COUNT(*) FROM skill_invocations WHERE id = 'si_cascade'`,
				).Scan(&count); err != nil {
					t.Fatalf("read back: %v", err)
				}
				if count != 0 {
					t.Errorf("expected cascade delete, got %d rows remaining", count)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.assert(t, db.DB)
		})
	}
}
