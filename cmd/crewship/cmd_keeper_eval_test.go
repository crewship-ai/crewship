//go:build !clionly

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/eval"
	"github.com/crewship-ai/crewship/internal/llm"

	_ "modernc.org/sqlite"
)

// evalStubProvider answers every prompt with a fixed decision, so a test can
// state "this candidate always says DENY" and read the score that produces.
type evalStubProvider struct{ decision string }

func (s evalStubProvider) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: fmt.Sprintf(`{"decision":%q,"reason":"stub","risk":5}`, s.decision)}, nil
}

func (s evalStubProvider) Stream(context.Context, llm.Request, func(llm.StreamEvent) error) (*llm.Response, error) {
	return nil, errors.New("not implemented")
}

func (s evalStubProvider) Name() string { return "stub" }

// stubEvalProviders points the command at canned answers keyed by model name and
// restores the real builder afterwards.
func stubEvalProviders(t *testing.T, byModel map[string]string) {
	t.Helper()
	prev := newKeeperEvalProvider
	t.Cleanup(func() { newKeeperEvalProvider = prev })
	newKeeperEvalProvider = func(_, model string) llm.Provider {
		decision, ok := byModel[model]
		if !ok {
			t.Fatalf("unexpected model dialled: %q", model)
		}
		return evalStubProvider{decision: decision}
	}
}

func newEvalDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE keeper_requests (
			id TEXT PRIMARY KEY,
			requesting_agent_id TEXT NOT NULL DEFAULT '',
			credential_id TEXT NOT NULL DEFAULT '',
			request_type TEXT NOT NULL DEFAULT 'access',
			ollama_prompt TEXT,
			decision TEXT,
			risk_score INTEGER,
			created_at TEXT NOT NULL
		);
		CREATE TABLE escalations (
			id TEXT PRIMARY KEY,
			from_agent_id TEXT NOT NULL,
			credential_id TEXT,
			status TEXT NOT NULL DEFAULT 'PENDING',
			action TEXT DEFAULT 'approve',
			resolved_by TEXT,
			resolved_at TEXT
		);
		CREATE TABLE inbox_items (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			source_id TEXT NOT NULL,
			state TEXT NOT NULL DEFAULT 'unread',
			resolved_action TEXT,
			resolved_by_user_id TEXT
		)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// seedHumanRejected writes n keeper requests the keeper ALLOWed and a human then
// rejected. Ground truth is DENY on every row while the incumbent's own decision
// says ALLOW, so the two labels disagree on the whole corpus — which is what
// makes the scores tell them apart.
func seedHumanRejected(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := db.Exec(`
			INSERT INTO keeper_requests
				(id, requesting_agent_id, credential_id, request_type, ollama_prompt, decision, risk_score, created_at)
			VALUES (?, ?, ?, 'access', ?, 'ALLOW', 3, ?)`,
			fmt.Sprintf("k%02d", i), fmt.Sprintf("ag%02d", i), fmt.Sprintf("cr%02d", i),
			fmt.Sprintf("prompt-%02d", i), fmt.Sprintf("2026-01-01T00:00:%02dZ", i)); err != nil {
			t.Fatalf("seed keeper_requests: %v", err)
		}
		if _, err := db.Exec(`
			INSERT INTO escalations (id, from_agent_id, credential_id, status, action, resolved_by, resolved_at)
			VALUES (?, ?, ?, 'RESOLVED', 'reject', 'user', '2026-02-01T00:00:00Z')`,
			fmt.Sprintf("e%02d", i), fmt.Sprintf("ag%02d", i), fmt.Sprintf("cr%02d", i)); err != nil {
			t.Fatalf("seed escalations: %v", err)
		}
	}
}

// TestRunKeeperEval_ScoresAgainstHumanNotIncumbent is the command-level proof of
// P4. The incumbent model reproduces its own past ALLOWs perfectly and is wrong
// every time; the candidate contradicts them and is right every time. Before the
// relabelling the report would have crowned the incumbent.
func TestRunKeeperEval_ScoresAgainstHumanNotIncumbent(t *testing.T) {
	db := newEvalDB(t)
	seedHumanRejected(t, db, eval.MinHumanRowsForRate+5)
	stubEvalProviders(t, map[string]string{
		"old-model": "ALLOW", // agrees with its own history, disagrees with people
		"new-model": "DENY",  // the human verdict
	})

	var out bytes.Buffer
	err := runKeeperEval(context.Background(), db, keeperEvalOptions{
		Endpoint:   "http://127.0.0.1:11434",
		Incumbent:  "old-model",
		Candidates: []string{"new-model"},
		Passes:     1,
		Format:     "json",
	}, &out, io.Discard)
	if err != nil {
		t.Fatalf("runKeeperEval: %v", err)
	}

	var report eval.Report
	if jerr := json.Unmarshal(out.Bytes(), &report); jerr != nil {
		t.Fatalf("parse report: %v\n%s", jerr, out.String())
	}
	byLabel := map[string]eval.RankedCandidate{}
	for _, c := range report.Candidates {
		byLabel[c.Label] = c
	}

	inc, cand := byLabel["old-model"], byLabel["new-model"]
	if inc.AgreementRate != 0 {
		t.Errorf("incumbent human agreement = %v, want 0 — it reproduces its own "+
			"past decisions, every one of which a person overturned", inc.AgreementRate)
	}
	if inc.IncumbentLabelAgreement != 0 || inc.IncumbentLabelRows != 0 {
		t.Errorf("every row here is human-labelled, so the weak segment must be empty: %+v", inc)
	}
	if cand.AgreementRate != 1 {
		t.Errorf("candidate human agreement = %v, want 1", cand.AgreementRate)
	}
	if !cand.Viable {
		t.Errorf("the candidate that matches the human on every row must be viable: %+v", cand)
	}
	// The incumbent DENYs nothing, so it downgrades every guard the human set.
	if inc.DangerousFlipRate != 1 {
		t.Errorf("incumbent dangerous-flip rate = %v, want 1", inc.DangerousFlipRate)
	}
}

// TestRunKeeperEval_TinyCorpusWithholdsRates: the honesty requirement, checked
// on the rendered table an operator actually reads.
func TestRunKeeperEval_TinyCorpusWithholdsRates(t *testing.T) {
	db := newEvalDB(t)
	seedHumanRejected(t, db, 3)
	stubEvalProviders(t, map[string]string{"old-model": "ALLOW", "new-model": "DENY"})

	var out bytes.Buffer
	err := runKeeperEval(context.Background(), db, keeperEvalOptions{
		Endpoint:   "http://127.0.0.1:11434",
		Incumbent:  "old-model",
		Candidates: []string{"new-model"},
		Passes:     1,
	}, &out, io.Discard)
	if err != nil {
		t.Fatalf("runKeeperEval: %v", err)
	}

	table := out.String()
	if !strings.Contains(table, "anecdote") {
		t.Errorf("a 3-row corpus must say so in words:\n%s", table)
	}
	if strings.Contains(table, "1.000") {
		t.Errorf("a 3-row corpus must not print a confident rate:\n%s", table)
	}
	if !strings.Contains(table, "NO: corpus too small") {
		t.Errorf("the candidate must be blocked with the corpus-size reason:\n%s", table)
	}
}

// TestRunKeeperEval_IncumbentIsNotAlsoACandidate: listing the baseline as a
// candidate would print it twice with a zero delta and read as a second,
// corroborating measurement.
func TestRunKeeperEval_IncumbentIsNotAlsoACandidate(t *testing.T) {
	db := newEvalDB(t)
	seedHumanRejected(t, db, eval.MinHumanRowsForRate)
	stubEvalProviders(t, map[string]string{"old-model": "ALLOW"})

	var out bytes.Buffer
	if err := runKeeperEval(context.Background(), db, keeperEvalOptions{
		Endpoint:   "http://127.0.0.1:11434",
		Incumbent:  "old-model",
		Candidates: []string{"old-model"},
		Passes:     1,
		Format:     "json",
	}, &out, io.Discard); err != nil {
		t.Fatalf("runKeeperEval: %v", err)
	}
	var report eval.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	if len(report.Candidates) != 1 {
		t.Fatalf("want just the incumbent row, got %d: %+v", len(report.Candidates), report.Candidates)
	}
}

func TestRunKeeperEval_RefusesWithoutCandidates(t *testing.T) {
	db := newEvalDB(t)
	seedHumanRejected(t, db, 1)
	err := runKeeperEval(context.Background(), db, keeperEvalOptions{
		Endpoint: "http://127.0.0.1:11434", Incumbent: "old-model",
	}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--candidate") {
		t.Fatalf("err = %v, want a prompt to pass --candidate", err)
	}
}

// TestRunKeeperEval_EmptyCorpusIsAnErrorNotAZeroScore: a run with nothing to
// replay must not render a table of zeroes, which looks like a candidate that
// failed rather than a corpus that does not exist.
func TestRunKeeperEval_EmptyCorpusIsAnErrorNotAZeroScore(t *testing.T) {
	db := newEvalDB(t)
	stubEvalProviders(t, map[string]string{})
	err := runKeeperEval(context.Background(), db, keeperEvalOptions{
		Endpoint: "http://127.0.0.1:11434", Incumbent: "old-model",
		Candidates: []string{"new-model"},
	}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "corpus is empty") {
		t.Fatalf("err = %v, want an empty-corpus error", err)
	}
}

// --- judge resolution -----------------------------------------------------

func newKeeperSettingsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE keeper_runtime_settings (
			id TEXT PRIMARY KEY,
			enabled INTEGER,
			judge_provider TEXT NOT NULL DEFAULT '',
			judge_endpoint_url TEXT NOT NULL DEFAULT '',
			judge_wire TEXT NOT NULL DEFAULT '',
			judge_model TEXT NOT NULL DEFAULT '',
			judge_timeout_ms INTEGER,
			judge_profile TEXT NOT NULL DEFAULT '',
			judge_evidence INTEGER,
			judge_evidence_facts TEXT NOT NULL DEFAULT '',
			judge_hard_gate INTEGER,
			judge_precedent INTEGER,
			judge_precedent_n INTEGER,
			judge_consistency_samples INTEGER,
			judge_prompt_budget_tokens INTEGER,
			updated_at TEXT NOT NULL DEFAULT '',
			updated_by TEXT
		)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestResolveKeeperEvalJudge_FallsBackToInstanceSettings(t *testing.T) {
	db := newKeeperSettingsDB(t)
	if _, err := db.Exec(`
		INSERT INTO keeper_runtime_settings (id, judge_endpoint_url, judge_model, updated_at)
		VALUES ('singleton', 'http://10.0.0.5:11434', 'qwen2.5:7b', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	endpoint, incumbent, err := resolveKeeperEvalJudge(context.Background(), db, "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if endpoint != "http://10.0.0.5:11434" || incumbent != "qwen2.5:7b" {
		t.Fatalf("resolved %q/%q, want the configured judge", endpoint, incumbent)
	}

	// A flag overrides one half without discarding the other.
	endpoint, incumbent, err = resolveKeeperEvalJudge(context.Background(), db, "http://localhost:11434", "")
	if err != nil {
		t.Fatalf("resolve with flag: %v", err)
	}
	if endpoint != "http://localhost:11434" || incumbent != "qwen2.5:7b" {
		t.Fatalf("resolved %q/%q, want the flag endpoint and the configured model", endpoint, incumbent)
	}
}

// TestResolveKeeperEvalJudge_UnconfiguredSaysWhatToDo: an instance configured
// only through KEEPER_* env resolves to nothing here, because that layer belongs
// to the server process. Guessing would measure against a model the server never
// uses, so the command has to stop and say which flag is missing.
func TestResolveKeeperEvalJudge_UnconfiguredSaysWhatToDo(t *testing.T) {
	db := newKeeperSettingsDB(t)

	_, _, err := resolveKeeperEvalJudge(context.Background(), db, "", "")
	if err == nil || !strings.Contains(err.Error(), "--endpoint") {
		t.Fatalf("err = %v, want the missing-endpoint hint", err)
	}

	_, _, err = resolveKeeperEvalJudge(context.Background(), db, "http://localhost:11434", "")
	if err == nil || !strings.Contains(err.Error(), "--incumbent") {
		t.Fatalf("err = %v, want the missing-incumbent hint", err)
	}
}
