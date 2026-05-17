package consolidate

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestExplainProposal_ReturnsScores asserts the v91 score_json column
// surfaces back through ExplainProposal so the HTTP explain endpoint
// can render per-signal breakdowns without re-querying the DB.
func TestExplainProposal_ReturnsScores(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	// Seed a proposal via the real writeProposal path so score_json
	// gets populated by encodeProposalScores.
	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"explain me","action":"act","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.75}]`
	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: reply}, Logger: quietLogger()}
	cfg := Config{
		WorkspaceID: "ws_test", CrewID: "crew_test", Since: time.Hour,
		MinEntries: 10, OutputDir: t.TempDir(), ProposalMode: true,
	}
	if _, err := c.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var proposalID string
	if err := db.QueryRow(`SELECT id FROM memory_proposals WHERE workspace_id = 'ws_test' LIMIT 1`).Scan(&proposalID); err != nil {
		t.Fatalf("read id: %v", err)
	}

	exp, err := ExplainProposal(context.Background(), db, proposalID)
	if err != nil {
		t.Fatalf("ExplainProposal: %v", err)
	}
	if len(exp.Scores) == 0 {
		t.Fatalf("Scores empty; want populated JSON")
	}

	// Parse: shape is {"<pattern>": ScoreResult}
	var scores map[string]struct {
		Composite float64 `json:"composite"`
		Promoted  bool    `json:"promoted"`
		Signals   struct {
			Relevance float64 `json:"relevance"`
		} `json:"signals"`
	}
	if err := json.Unmarshal(exp.Scores, &scores); err != nil {
		t.Fatalf("score shape unparseable: %v\nraw=%s", err, exp.Scores)
	}
	got, ok := scores["explain me"]
	if !ok {
		t.Fatalf("missing rule 'explain me' in Scores: %v", scores)
	}
	// Mirror the LLM confidence 0.75 into the Relevance signal.
	if got.Signals.Relevance < 0.74 || got.Signals.Relevance > 0.76 {
		t.Errorf("relevance signal = %v, want ~0.75", got.Signals.Relevance)
	}
	// First-time proposal -> recall/unique gates block promotion.
	if got.Promoted {
		t.Errorf("first-time proposal should not be promoted (got Promoted=true, composite=%v)", got.Composite)
	}
}

// TestExplainProposal_PreV91Rows_EmptyScoresObject asserts the
// COALESCE fallback: if a row was written before v91 (or score_json
// is empty), ExplainProposal returns Scores = "{}" — not nil and not
// invalid JSON — so the HTTP layer can blindly Unmarshal.
func TestExplainProposal_PreV91Rows_EmptyScoresObject(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	applyV89Schema(t, db)

	// Insert a workspace + a memory_proposals row WITHOUT score_json
	// to simulate a pre-v91 row that just defaulted to '{}'.
	if _, err := db.Exec(`INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, status) VALUES ('mp_pre','ws_x','crew_y','/tmp/p.md','pending')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	exp, err := ExplainProposal(context.Background(), db, "mp_pre")
	if err != nil {
		t.Fatalf("ExplainProposal: %v", err)
	}
	if string(exp.Scores) != "{}" {
		t.Errorf("Scores = %q, want '{}' for pre-v91 row", string(exp.Scores))
	}
}
