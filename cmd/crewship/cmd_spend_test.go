package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/journal"
	_ "modernc.org/sqlite"
)

// spendTestSchema is the minimal real schema journal.Spend reads:
// journal_entries (cost.incurred source for by-agent + total) and
// pipeline_runs (by-routine + top-N source). Kept in step with the
// columns Spend's queries actually touch — the acceptance test below
// exercises the REAL journal.Spend aggregation over rows inserted here,
// not a canned JSON response.
const spendTestSchema = `
CREATE TABLE journal_entries (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id      TEXT,
    agent_id     TEXT,
    ts           TEXT NOT NULL,
    entry_type   TEXT NOT NULL,
    severity     TEXT NOT NULL DEFAULT 'info',
    priority     TEXT NOT NULL DEFAULT 'normal',
    actor_type   TEXT NOT NULL DEFAULT 'system',
    summary      TEXT NOT NULL DEFAULT '',
    payload      TEXT NOT NULL DEFAULT '{}',
    refs         TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE pipeline_runs (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    pipeline_id   TEXT NOT NULL,
    pipeline_slug TEXT NOT NULL,
    started_at    TEXT NOT NULL,
    cost_usd      REAL NOT NULL DEFAULT 0
);
`

// newSpendBackedServer stands up an httptest server whose /journal/spend
// handler calls the REAL journal.Spend against a real sqlite DB seeded
// with real cost.incurred rows and pipeline_runs — the same computation
// the production api.JournalHandler.Spend performs (minus JWT auth). The
// compiled crewship binary drives this end-to-end, so the test proves the
// real CLI → real query → real rendering contract, not a stubbed shape.
func newSpendBackedServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "spend.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), spendTestSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}

	const wsID = "c000000000000000000acc"
	tsFmt := func(tm time.Time) string { return tm.UTC().Format("2006-01-02T15:04:05.000000000Z07:00") }
	now := time.Now().UTC()

	// Two cost.incurred rows for agent_a → total 2.00, one by-agent bucket.
	for i, c := range []float64{1.25, 0.75} {
		if _, err := db.Exec(
			`INSERT INTO journal_entries (id, workspace_id, crew_id, agent_id, ts, entry_type, payload)
			 VALUES (?, ?, 'crew_a', 'agent_a', ?, 'cost.incurred', ?)`,
			"je-"+string(rune('a'+i)), wsID, tsFmt(now.Add(-time.Duration(i+1)*time.Hour)),
			`{"cost_usd":`+jsonNum(c)+`,"provider":"anthropic","model":"claude-haiku-4-5"}`,
		); err != nil {
			t.Fatalf("seed cost row: %v", err)
		}
	}
	// One routine run → by_routine + top_routines/top_runs.
	if _, err := db.Exec(
		`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, started_at, cost_usd)
		 VALUES ('run_1', ?, 'pln_1', 'nightly-audit', ?, 3.50)`,
		wsID, tsFmt(now.Add(-30*time.Minute)),
	); err != nil {
		t.Fatalf("seed pipeline_run: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/journal/spend", func(w http.ResponseWriter, r *http.Request) {
		window := journal.RunInsightsWindow(r.URL.Query().Get("window"))
		res, err := journal.Spend(r.Context(), db, wsID, window, 5)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func jsonNum(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func writeSpendCLIConfig(t *testing.T) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("token: test-token\nworkspace: c000000000000000000acc\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// TestAcceptance_Spend_JSON drives the compiled crewship binary end-to-end
// against the real journal.Spend path and asserts the rollup is COMPUTED
// from the seeded rows (total 2.00 from two cost.incurred entries; the
// nightly-audit routine's 3.50 run) — not echoed from a canned response.
func TestAcceptance_Spend_JSON(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := newSpendBackedServer(t)
	cfgPath := writeSpendCLIConfig(t)

	cmd := exec.Command(bin, "spend", "--window", "24h", "--top", "5", "--server", srv.URL, "--format", "json")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}

	var res journal.SpendResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode CLI json: %v\noutput: %s", err, out)
	}
	if res.Window != "24h" {
		t.Errorf("Window = %q, want 24h", res.Window)
	}
	if res.TotalCostUSD < 1.9999 || res.TotalCostUSD > 2.0001 {
		t.Errorf("TotalCostUSD = %v, want 2.0 (1.25+0.75 from real cost.incurred rows)", res.TotalCostUSD)
	}
	if len(res.ByAgent) != 1 || res.ByAgent[0].AgentID != "agent_a" {
		t.Errorf("ByAgent = %+v, want a single agent_a bucket", res.ByAgent)
	}
	if len(res.ByRoutine) != 1 || res.ByRoutine[0].PipelineSlug != "nightly-audit" {
		t.Errorf("ByRoutine = %+v, want the nightly-audit routine", res.ByRoutine)
	}
	if len(res.TopRuns) != 1 || res.TopRuns[0].ID != "run_1" {
		t.Errorf("TopRuns = %+v, want run_1", res.TopRuns)
	}
}

// TestAcceptance_Spend_Human drives the same real path but through the
// default human renderer, asserting the binary prints the computed total
// and the routine label.
func TestAcceptance_Spend_Human(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := newSpendBackedServer(t)
	cfgPath := writeSpendCLIConfig(t)

	cmd := exec.Command(bin, "spend", "--window", "24h", "--server", srv.URL, "--no-color")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	got := string(out)
	for _, want := range []string{"window=24h", "$2.0000", "nightly-audit", "agent_a"} {
		if !strings.Contains(got, want) {
			t.Errorf("human output missing %q\nfull output:\n%s", want, got)
		}
	}
}

// TestAcceptance_Spend_BadWindow proves the binary rejects an invalid
// window before making any HTTP call (real flag validation in RunE).
func TestAcceptance_Spend_BadWindow(t *testing.T) {
	bin := buildCrewshipBinary(t)
	cfgPath := writeSpendCLIConfig(t)

	cmd := exec.Command(bin, "spend", "--window", "3years", "--server", "http://127.0.0.1:0")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit for bad --window; output: %s", out)
	}
	if !strings.Contains(string(out), "bad --window") {
		t.Errorf("output missing bad-window error:\n%s", out)
	}
}

// TestFetchSpend_PropagatesError keeps the in-process guard that a
// non-2xx response surfaces as an error to the command layer.
func TestFetchSpend_PropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad window"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := fetchSpend(cli.NewClient(srv.URL, "t", ""), "bogus", 5)
	if err == nil {
		t.Fatal("expected error")
	}
}
