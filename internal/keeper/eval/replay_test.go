package eval

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/llm"
)

// stubProvider is a fake llm.Provider that answers each prompt via respond and
// records the last request so tests can assert the replay settings.
type stubProvider struct {
	respond func(prompt string) (string, error)
	last    llm.Request
	calls   int
}

func (s *stubProvider) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	s.last = req
	s.calls++
	content, err := s.respond(req.Messages[0].Content)
	if err != nil {
		return nil, err
	}
	return &llm.Response{Content: content}, nil
}

func (s *stubProvider) Stream(_ context.Context, _ llm.Request, _ func(llm.StreamEvent) error) (*llm.Response, error) {
	return nil, errors.New("not implemented")
}

func (s *stubProvider) Name() string { return "stub" }

func candidate(p llm.Provider) Candidate {
	return Candidate{Label: "cand", Provider: p, Model: "test-model"}
}

func TestReplayCandidate_AssemblesRowsAndFlips(t *testing.T) {
	corpus := []CorpusRow{
		{ID: "guard", Prompt: "P-guard", Recorded: Deny, RecordedRisk: 8},
		{ID: "ok", Prompt: "P-ok", Recorded: Allow, RecordedRisk: 2},
	}
	// The guard prompt is answered ALLOW (a dangerous downgrade); the allow
	// prompt is answered ALLOW (agreement).
	prov := &stubProvider{respond: func(prompt string) (string, error) {
		switch prompt {
		case "P-guard":
			return `{"decision":"allow","risk":2}`, nil
		default:
			return `{"decision":"allow","risk":1}`, nil
		}
	}}

	rows, err := ReplayCandidate(context.Background(), candidate(prov), corpus, 2)
	if err != nil {
		t.Fatalf("ReplayCandidate: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	for i, r := range rows {
		if len(r.Replays) != 2 {
			t.Fatalf("row %d: got %d passes, want 2", i, len(r.Replays))
		}
	}
	// The recorded fields carry through untouched.
	if rows[0].Recorded != Deny || rows[0].RecordedRisk != 8 {
		t.Errorf("row0 recorded = %v/%d, want DENY/8", rows[0].Recorded, rows[0].RecordedRisk)
	}

	// The scorer must see exactly one dangerous flip (the guard row).
	v := Score(rows)
	if v.DangerousFlipRows != 1 {
		t.Errorf("DangerousFlipRows = %d, want 1", v.DangerousFlipRows)
	}
}

func TestReplayOnce_UsesProductionSettings(t *testing.T) {
	prov := &stubProvider{respond: func(string) (string, error) {
		return `{"decision":"allow","risk":1}`, nil
	}}
	_, err := ReplayCandidate(context.Background(), candidate(prov), []CorpusRow{{Prompt: "x", Recorded: Allow, RecordedRisk: 1}}, 1)
	if err != nil {
		t.Fatalf("ReplayCandidate: %v", err)
	}
	if prov.last.MaxTokens != replayMaxTokens {
		t.Errorf("MaxTokens = %d, want %d", prov.last.MaxTokens, replayMaxTokens)
	}
	if prov.last.Temperature == nil || *prov.last.Temperature != replayTemperature {
		t.Errorf("Temperature = %v, want %v", prov.last.Temperature, replayTemperature)
	}
	if prov.last.Model != "test-model" {
		t.Errorf("Model = %q, want test-model", prov.last.Model)
	}
}

func TestReplayOnce_ProviderErrorIsFailClosedDeny(t *testing.T) {
	prov := &stubProvider{respond: func(string) (string, error) {
		return "", errors.New("model down")
	}}
	rows, err := ReplayCandidate(context.Background(), candidate(prov), []CorpusRow{{Prompt: "x", Recorded: Allow, RecordedRisk: 1}}, 1)
	if err != nil {
		t.Fatalf("ReplayCandidate: %v", err)
	}
	if got := rows[0].Replays[0]; got.Decision != Deny || got.Risk != 10 {
		t.Errorf("provider error → %v/%d, want DENY/10", got.Decision, got.Risk)
	}
}

func TestReplayOnce_UnparseableIsFailClosedDeny(t *testing.T) {
	prov := &stubProvider{respond: func(string) (string, error) {
		return "the model rambled without JSON", nil
	}}
	rows, err := ReplayCandidate(context.Background(), candidate(prov), []CorpusRow{{Prompt: "x", Recorded: Allow, RecordedRisk: 1}}, 1)
	if err != nil {
		t.Fatalf("ReplayCandidate: %v", err)
	}
	if got := rows[0].Replays[0]; got.Decision != Deny || got.Risk != 10 {
		t.Errorf("unparseable → %v/%d, want DENY/10", got.Decision, got.Risk)
	}
}

func TestReplayCandidate_PassesFloor(t *testing.T) {
	prov := &stubProvider{respond: func(string) (string, error) {
		return `{"decision":"allow","risk":1}`, nil
	}}
	rows, err := ReplayCandidate(context.Background(), candidate(prov), []CorpusRow{{Prompt: "x", Recorded: Allow, RecordedRisk: 1}}, 0)
	if err != nil {
		t.Fatalf("ReplayCandidate: %v", err)
	}
	if len(rows[0].Replays) != 1 {
		t.Errorf("passes=0 should floor to 1, got %d", len(rows[0].Replays))
	}
}

func TestReplayCandidate_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	prov := &stubProvider{respond: func(string) (string, error) { return "", nil }}
	_, err := ReplayCandidate(ctx, candidate(prov), []CorpusRow{{Prompt: "x", Recorded: Allow}}, 1)
	if err == nil {
		t.Fatal("want error on cancelled context")
	}
}
