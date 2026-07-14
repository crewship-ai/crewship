//go:build eval

// This file is the operator's run harness for the M2a governance-model
// selection. It is gated behind the `eval` build tag so it NEVER runs in normal
// CI (`go test ./...`) — it needs a real Ollama with the candidate models
// pre-pulled and a real keeper_requests corpus.
//
// Run it like:
//
//	KEEPER_EVAL_DB=/path/to/crewship.db \
//	KEEPER_EVAL_INCUMBENT=qwen2.5:3b-instruct \
//	KEEPER_EVAL_CANDIDATES=llama3.2:3b-instruct,phi3.5:3.8b,qwen2.5:7b-instruct \
//	KEEPER_EVAL_OLLAMA_URL=http://localhost:11434 \
//	KEEPER_EVAL_PASSES=3 KEEPER_EVAL_LIMIT=500 \
//	KEEPER_EVAL_JSON=/tmp/keeper-eval.json \
//	go test -tags eval -run TestReplay -v ./internal/keeper/eval/
//
// It prints the ranked table (post it to #1001 for the model pick) and, if
// KEEPER_EVAL_JSON is set, writes the machine-readable JSON alongside.
package eval

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/llm"
	_ "modernc.org/sqlite"
)

func TestReplay(t *testing.T) {
	dbPath := os.Getenv("KEEPER_EVAL_DB")
	incumbent := os.Getenv("KEEPER_EVAL_INCUMBENT")
	candidatesEnv := os.Getenv("KEEPER_EVAL_CANDIDATES")
	if dbPath == "" || incumbent == "" || candidatesEnv == "" {
		t.Skip("set KEEPER_EVAL_DB, KEEPER_EVAL_INCUMBENT, KEEPER_EVAL_CANDIDATES to run the replay harness")
	}

	ollamaURL := os.Getenv("KEEPER_EVAL_OLLAMA_URL")
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}
	passes := envInt(t, "KEEPER_EVAL_PASSES", DefaultPasses)
	limit := envInt(t, "KEEPER_EVAL_LIMIT", 0)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db %s: %v", dbPath, err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	corpus, err := LoadCorpus(ctx, db, limit)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	if len(corpus) == 0 {
		t.Skip("no scoreable keeper_requests rows in the corpus")
	}
	t.Logf("corpus: %d rows, %d passes each", len(corpus), passes)

	// The incumbent is scored on the exact same corpus + code path — it is the
	// reference ceiling, not a trivial 100%.
	incVerdict := scoreModel(ctx, t, ollamaURL, incumbent, corpus, passes)

	var cands []LabeledVerdict
	for _, m := range splitCSV(candidatesEnv) {
		if m == incumbent {
			continue // already scored as the baseline
		}
		cands = append(cands, LabeledVerdict{Label: m, Verdict: scoreModel(ctx, t, ollamaURL, m, corpus, passes)})
	}

	report := BuildReport(LabeledVerdict{Label: incumbent, Verdict: incVerdict}, cands, 0.0)
	t.Log("\n" + report.Table())

	if out := os.Getenv("KEEPER_EVAL_JSON"); out != "" {
		blob, err := report.JSON()
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		if err := os.WriteFile(out, blob, 0o644); err != nil {
			t.Fatalf("write %s: %v", out, err)
		}
		t.Logf("wrote JSON report to %s", out)
	}
}

func scoreModel(ctx context.Context, t *testing.T, ollamaURL, model string, corpus []CorpusRow, passes int) Verdict {
	t.Helper()
	prov := llm.NewOllama(ollamaURL, model)
	rows, err := ReplayCandidate(ctx, Candidate{Label: model, Provider: prov, Model: model}, corpus, passes)
	if err != nil {
		t.Fatalf("replay %s: %v", model, err)
	}
	v := Score(rows)
	t.Logf("%s: agree=%.3f danger_flip=%.3f (%d) risk_mae=%.2f",
		model, v.AgreementRate, v.DangerousFlipRate, v.DangerousFlipRows, v.RiskMAE)
	return v
}

func envInt(t *testing.T, key string, def int) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s=%q: %v", key, raw, err)
	}
	return n
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
