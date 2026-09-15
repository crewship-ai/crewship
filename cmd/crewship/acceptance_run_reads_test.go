package main

// Acceptance for the three run reads — `run list`, `run get`, `run insights`
// — driven through the BUILT BINARY against a REAL api.Router over a REAL
// migrated database.
//
// Until now `run list` and `run insights` were covered by stub tests only
// (cmd_run_cov_test.go), which can confirm what the CLI prints for a body it
// was handed and nothing about what the server does with the query. Two
// contracts here need the real thing:
//
//   - `run list` reads BOTH engines (#2284): an ad-hoc agent run keyed on
//     trace_id and a routine run keyed on actor_id, told apart by the KIND
//     column. Only the journal CTE decides that; a stub answers whatever it
//     is handed.
//   - `run insights --agent/--crew` scope the aggregate BEFORE it is
//     computed (`journal.RunInsightsScoped`). A flag the binary parses and
//     drops prints a perfectly plausible workspace-wide snapshot — the one
//     failure mode a stub cannot see.

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// runReadsWorkspaceID is CUID-shaped so the CLI treats it as an already
// resolved workspace id and fires no slug→id round-trip.
const runReadsWorkspaceID = "crunreadsws000000001a"

// startRunReadsServer builds the real router over a migrated DB holding two
// crews (Backend: viktor; Growth: nadia), three ad-hoc runs and one routine
// run, all inside the 24h window, and writes a CLI config pointing at it.
func startRunReadsServer(t *testing.T) string {
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
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Runs', 'runs-ws')`, runReadsWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('rr-owner', 'owner@rr-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('rrm-owner', ?, 'rr-owner', 'OWNER')`,
		runReadsWorkspaceID)
	const ownerToken = "crewship_cli_rrowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-rr-owner', 'rr-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	for _, c := range [][2]string{{"rr-crew-backend", "Backend"}, {"rr-crew-growth", "Growth"}} {
		mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
			VALUES (?, ?, ?, ?, 'free', 4096, 2.0)`, c[0], runReadsWorkspaceID, c[1], strings.ToLower(c[1]))
	}
	for _, a := range [][3]string{{"rr-agent-viktor", "Viktor", "rr-crew-backend"}, {"rr-agent-nadia", "Nadia", "rr-crew-growth"}} {
		mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled)
			VALUES (?, ?, ?, ?, ?, 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`,
			a[0], runReadsWorkspaceID, a[2], a[1], strings.ToLower(a[1]))
	}

	// Journal rows in the shape the orchestrator writes them (see
	// internal/api/runs_insights_test.go emitRunRowFull): run.started on the
	// trace, crew_id + agent_id stamped, one terminal entry when finished.
	insert := func(id, agentID, crewID, entryType, actorType, actorID, traceID any, ts time.Time, payload string) {
		t.Helper()
		mustExec(`INSERT INTO journal_entries
				(id, workspace_id, crew_id, agent_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id)
			VALUES (?, ?, ?, ?, ?, ?, 'info', 'normal', ?, ?, 'r', ?, '{}', ?)`,
			id, runReadsWorkspaceID, crewID, agentID, ts.UTC().Format("2006-01-02T15:04:05.000Z"),
			entryType, actorType, actorID, payload, traceID)
	}
	now := time.Now().UTC()
	adhoc := func(trace, agentID, crewID, terminal, trigger string, started time.Time, dur time.Duration) {
		insert(trace+"_s", agentID, crewID, "run.started", "sidecar", agentID, trace, started, `{"trigger_type":"`+trigger+`"}`)
		if terminal != "" {
			insert(trace+"_t", agentID, crewID, terminal, "sidecar", agentID, trace, started.Add(dur), `{"exit_code":0}`)
		}
	}
	// Backend / viktor: two completed USER runs. Growth / nadia: one failed
	// WEBHOOK run. Nothing older than the 24h window, so every scope below
	// is exact.
	adhoc("rr-run-v1", "rr-agent-viktor", "rr-crew-backend", "run.completed", "USER", now.Add(-5*time.Hour), 10*time.Second)
	adhoc("rr-run-v2", "rr-agent-viktor", "rr-crew-backend", "run.completed", "USER", now.Add(-4*time.Hour), 20*time.Second)
	adhoc("rr-run-n1", "rr-agent-nadia", "rr-crew-growth", "run.failed", "WEBHOOK", now.Add(-3*time.Hour), 40*time.Second)
	// One routine run, keyed on actor_id with no trace_id — the #2284 shape
	// `run list` must read alongside the ad-hoc rows.
	insert("rr-prun-1_s", "rr-agent-viktor", "rr-crew-backend", "pipeline.run.started", "orchestrator", "rr-prun-1", nil,
		now.Add(-2*time.Hour), `{"mode":"run","pipeline_id":"pl_rr","pipeline_slug":"nightly-digest","run_id":"rr-prun-1"}`)
	insert("rr-prun-1_t", "rr-agent-viktor", "rr-crew-backend", "pipeline.run.completed", "orchestrator", "rr-prun-1", nil,
		now.Add(-2*time.Hour+time.Minute), `{"total_duration_ms":60000,"pipeline_id":"pl_rr","pipeline_slug":"nightly-digest","run_id":"rr-prun-1"}`)

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + runReadsWorkspaceID +
		"\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runRunReadsCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// insightsTotals decodes the `-f json` shape far enough to pin the scope.
type insightsTotals struct {
	Window string `json:"window"`
	Totals struct {
		Total     int `json:"total"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	} `json:"totals"`
	ByCrew []struct {
		Name  string `json:"name"`
		Total int    `json:"total"`
	} `json:"by_crew"`
}

func decodeInsights(t *testing.T, out string) insightsTotals {
	t.Helper()
	var body insightsTotals
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("decode insights json: %v\n%s", err, out)
	}
	return body
}

func TestAcceptance_RunList_ShowsBothEnginesWithKind(t *testing.T) {
	cfg := startRunReadsServer(t)

	out, err := runRunReadsCLI(t, cfg, "run", "list")
	if err != nil {
		t.Fatalf("run list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "KIND") {
		t.Errorf("run list has no KIND column:\n%s", out)
	}
	// Three ad-hoc rows and one routine row, each labelled by its engine.
	for _, want := range []string{"rr-run-v1", "rr-run-v2", "rr-run-n1", "rr-prun-1", "viktor", "nadia", "COMPLETED", "FAILED"} {
		if !strings.Contains(out, want) {
			t.Errorf("run list is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "pipeline") || !strings.Contains(out, "agent") {
		t.Errorf("run list should label both kinds (agent, pipeline):\n%s", out)
	}
}

func TestAcceptance_RunGet_ShowsKindRow(t *testing.T) {
	cfg := startRunReadsServer(t)

	out, err := runRunReadsCLI(t, cfg, "run", "get", "rr-run-v1")
	if err != nil {
		t.Fatalf("run get: %v\n%s", err, out)
	}
	for _, want := range []string{"Kind:", "agent", "Status:", "COMPLETED", "Agent:", "viktor"} {
		if !strings.Contains(out, want) {
			t.Errorf("run get is missing %q:\n%s", want, out)
		}
	}
}

func TestAcceptance_RunInsights_WorkspaceWideByDefault(t *testing.T) {
	cfg := startRunReadsServer(t)

	out, err := runRunReadsCLI(t, cfg, "run", "insights", "-f", "json")
	if err != nil {
		t.Fatalf("run insights: %v\n%s", err, out)
	}
	body := decodeInsights(t, out)
	// Ad-hoc runs only — the routine run is deliberately NOT counted
	// (journal.RunInsights was never widened by #2284).
	if body.Totals.Total != 3 || body.Totals.Succeeded != 2 || body.Totals.Failed != 1 {
		t.Errorf("unscoped totals = %+v, want total 3 / ok 2 / failed 1 (ad-hoc runs only)", body.Totals)
	}
}

func TestAcceptance_RunInsights_AgentScopesTheAggregate(t *testing.T) {
	cfg := startRunReadsServer(t)

	// By slug — the spelling a person has on hand. The CLI resolves it to the
	// id the server scopes on.
	out, err := runRunReadsCLI(t, cfg, "run", "insights", "--agent", "nadia", "-f", "json")
	if err != nil {
		t.Fatalf("run insights --agent: %v\n%s", err, out)
	}
	body := decodeInsights(t, out)
	if body.Totals.Total != 1 || body.Totals.Failed != 1 || body.Totals.Succeeded != 0 {
		t.Errorf("--agent nadia totals = %+v, want exactly her one failed run", body.Totals)
	}

	// An unknown agent is refused up front, not answered with an empty
	// snapshot that reads as "no runs".
	out, err = runRunReadsCLI(t, cfg, "run", "insights", "--agent", "nobody")
	if err == nil {
		t.Fatalf("expected --agent nobody to fail, got:\n%s", out)
	}
	if !strings.Contains(out, "agent not found") {
		t.Errorf("want an 'agent not found' error, got:\n%s", out)
	}
}

func TestAcceptance_RunInsights_CrewScopesTheAggregate(t *testing.T) {
	cfg := startRunReadsServer(t)

	out, err := runRunReadsCLI(t, cfg, "run", "insights", "--crew", "backend", "-f", "json")
	if err != nil {
		t.Fatalf("run insights --crew: %v\n%s", err, out)
	}
	body := decodeInsights(t, out)
	if body.Totals.Total != 2 || body.Totals.Succeeded != 2 || body.Totals.Failed != 0 {
		t.Errorf("--crew backend totals = %+v, want Backend's two completed runs", body.Totals)
	}
	for _, c := range body.ByCrew {
		if c.Name == "Growth" {
			t.Errorf("--crew backend leaked the Growth crew into by_crew: %+v", body.ByCrew)
		}
	}

	// Human rendering still works with a scope, and says what it covers.
	human, err := runRunReadsCLI(t, cfg, "run", "insights", "--crew", "backend")
	if err != nil {
		t.Fatalf("run insights --crew (table): %v\n%s", err, human)
	}
	if !strings.Contains(human, "2 runs") || !strings.Contains(human, "2 ok") {
		t.Errorf("human snapshot should report Backend's two runs:\n%s", human)
	}
}
