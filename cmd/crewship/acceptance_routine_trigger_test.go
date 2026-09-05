package main

// B8 (#2359) acceptance — `crewship routine save --cron ...` and
// `crewship routine schedules activate` driven through the BUILT BINARY
// against the REAL api router, proving:
//   - a bad --cron rolls the whole save back (the routine never exists);
//   - a good --cron names the first fire time in the CLI's own output;
//   - --draft creates the trigger disabled and raises one approval item,
//     and `schedules activate` is the CLI's own path to turning it on.
//
// Mirrors the shape of acceptance_feedback_test.go: a real migrated DB, a
// real api.NewRouter, and the CLI binary run as a subprocess against it —
// the two extra setters (SetSaveTokenSecret, SetScheduleStore) are what
// cmd_start.go's boot path wires in production and NewRouter alone does not.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const routineTriggerAcceptanceWorkspaceID = "ctriggerws000000000001"

// unusedAgentRunner satisfies pipeline.AgentRunner purely so /pipelines/
// test_run and /internal/pipelines/test_run clear their "runner not wired"
// 503 guard. Dry-run validation never actually invokes an agent step (you
// cannot run one "dry" — its scripts have real side effects), so this
// should never be called; it errors loudly if it ever is.
type unusedAgentRunner struct{}

func (unusedAgentRunner) RunStep(context.Context, pipeline.AgentStepRequest) (pipeline.AgentStepResult, error) {
	return pipeline.AgentStepResult{}, errors.New("acceptance test runner: dry-run must not invoke an agent")
}

func startRoutineTriggerAcceptanceServer(t *testing.T) (cfgPath, authorCrewID string) {
	t.Helper()

	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Trigger', 'trigger-ws')`, routineTriggerAcceptanceWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('trg-owner', 'owner@trg-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('trgm-owner', ?, 'trg-owner', 'OWNER')`,
		routineTriggerAcceptanceWorkspaceID)
	authorCrewID = "trg-crew"
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
		VALUES (?, ?, 'Crew', 'trg-crew', 'free', 4096, 2.0)`, authorCrewID, routineTriggerAcceptanceWorkspaceID)
	mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('trg-agent', ?, ?, 'Agent', 'trg-agent', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`,
		routineTriggerAcceptanceWorkspaceID, authorCrewID)

	const ownerToken = "crewship_cli_trgowner0000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-trg-owner', 'trg-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	// Wired by cmd_start.go in production (cfg.Auth.InternalToken and the
	// scheduler's ScheduleStore respectively) — NewRouter alone leaves both
	// unset, which would 503 the schedule endpoints and reject every
	// save_token.
	router.PipelinesHandler.SetSaveTokenSecret([]byte("acceptance-test-save-token-secret-32b"))
	router.PipelinesHandler.SetScheduleStore(pipeline.NewScheduleStore(db))
	router.PipelinesHandler.SetRunner(unusedAgentRunner{})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath = filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + routineTriggerAcceptanceWorkspaceID +
		"\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath, authorCrewID
}

func runRoutineTriggerCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeRoutineDefinition(t *testing.T, name string) string {
	t.Helper()
	def := map[string]any{
		"dsl_version": "1.0",
		"name":        name,
		"description": "acceptance routine",
		"steps": []map[string]any{
			{"id": "a", "type": "agent_run", "agent_slug": "trg-agent", "prompt": "hi"},
		},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	path := filepath.Join(t.TempDir(), name+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write definition: %v", err)
	}
	return path
}

// TestAcceptance_RoutineSave_Trigger_NamesFirstFireTime drives `routine
// save --cron` through the CLI binary and checks its own stdout states the
// first fire time — the same sentence the routine-author skill's final
// message must produce for an agent-authored save.
func TestAcceptance_RoutineSave_Trigger_NamesFirstFireTime(t *testing.T) {
	cfgPath, crewID := startRoutineTriggerAcceptanceServer(t)
	defPath := writeRoutineDefinition(t, "acceptance-scheduled")

	out, err := runRoutineTriggerCLI(t, cfgPath, "routine", "save",
		"--name", "acceptance-scheduled",
		"--definition", defPath,
		"--author-crew", crewID,
		"--author-agent", "trg-agent",
		"--cron", "0 9 * * 1-5",
		"--timezone", "Europe/Prague",
	)
	if err != nil {
		t.Fatalf("routine save failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Trigger: schedule 0 9 * * 1-5") || !strings.Contains(out, "first run") {
		t.Errorf("expected the save output to name the first fire time, got:\n%s", out)
	}
}

// TestAcceptance_RoutineSave_Trigger_BadCron_RollsBack proves the rollback
// through the CLI: the routine never exists after a save with a broken
// cron expression.
func TestAcceptance_RoutineSave_Trigger_BadCron_RollsBack(t *testing.T) {
	cfgPath, crewID := startRoutineTriggerAcceptanceServer(t)
	defPath := writeRoutineDefinition(t, "acceptance-badcron")

	out, err := runRoutineTriggerCLI(t, cfgPath, "routine", "save",
		"--name", "acceptance-badcron",
		"--definition", defPath,
		"--author-crew", crewID,
		"--author-agent", "trg-agent",
		"--cron", "not a cron expression",
	)
	if err == nil {
		t.Fatalf("expected routine save to fail on a bad cron expression, got success:\n%s", out)
	}

	getOut, getErr := runRoutineTriggerCLI(t, cfgPath, "routine", "get", "acceptance-badcron")
	if getErr == nil {
		t.Fatalf("expected the routine to not exist after a rolled-back save, but `routine get` succeeded:\n%s", getOut)
	}
}

// TestAcceptance_RoutineSave_Draft_ActivateFlow drives the full draft →
// approval → activate cycle through the CLI: `routine save --draft` creates
// a disabled trigger and reports it awaiting approval, then `routine
// schedules activate` turns it on.
func TestAcceptance_RoutineSave_Draft_ActivateFlow(t *testing.T) {
	cfgPath, crewID := startRoutineTriggerAcceptanceServer(t)
	defPath := writeRoutineDefinition(t, "acceptance-draft")

	saveOut, err := runRoutineTriggerCLI(t, cfgPath, "routine", "save",
		"--name", "acceptance-draft",
		"--definition", defPath,
		"--author-crew", crewID,
		"--author-agent", "trg-agent",
		"--cron", "0 9 * * 1-5",
		"--draft",
	)
	if err != nil {
		t.Fatalf("routine save --draft failed: %v\n%s", err, saveOut)
	}
	if !strings.Contains(saveOut, "DISABLED") || !strings.Contains(saveOut, "awaiting MANAGER approval") {
		t.Fatalf("expected the draft save to report DISABLED/awaiting approval, got:\n%s", saveOut)
	}

	// Pull the schedule id the save printed out of "... crewship routine
	// schedules activate <id>".
	const marker = "schedules activate "
	idx := strings.Index(saveOut, marker)
	if idx == -1 {
		t.Fatalf("expected the save output to name the schedule id to activate, got:\n%s", saveOut)
	}
	rest := strings.TrimSpace(saveOut[idx+len(marker):])
	scheduleID := strings.Fields(rest)[0]

	activateOut, err := runRoutineTriggerCLI(t, cfgPath, "routine", "schedules", "activate", scheduleID)
	if err != nil {
		t.Fatalf("routine schedules activate failed: %v\n%s", err, activateOut)
	}
	if !strings.Contains(activateOut, "activated") {
		t.Errorf("expected the activate output to confirm activation, got:\n%s", activateOut)
	}

	// A second activate must now be refused — nothing left to activate.
	secondOut, err := runRoutineTriggerCLI(t, cfgPath, "routine", "schedules", "activate", scheduleID)
	if err == nil {
		t.Fatalf("expected a second activate to fail, got success:\n%s", secondOut)
	}
}
