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
		{ID: "guard", Prompt: "P-guard", Label: Deny, LabelSource: LabelHuman, IncumbentRisk: 8},
		{ID: "ok", Prompt: "P-ok", Label: Allow, LabelSource: LabelHuman, IncumbentRisk: 2},
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
	// The label, its provenance, and the recorded risk carry through untouched —
	// the driver must never re-decide what a row is worth.
	if rows[0].Label != Deny || rows[0].IncumbentRisk != 8 || rows[0].Source != LabelHuman {
		t.Errorf("row0 = %v/%d/%q, want DENY/8/human", rows[0].Label, rows[0].IncumbentRisk, rows[0].Source)
	}

	// The scorer must see exactly one dangerous flip (the guard row).
	v := Score(rows)
	if v.Human.DangerousFlipRows != 1 {
		t.Errorf("Human.DangerousFlipRows = %d, want 1", v.Human.DangerousFlipRows)
	}
}

func TestReplayOnce_UsesProductionSettings(t *testing.T) {
	prov := &stubProvider{respond: func(string) (string, error) {
		return `{"decision":"allow","risk":1}`, nil
	}}
	_, err := ReplayCandidate(context.Background(), candidate(prov), []CorpusRow{{Prompt: "x", Label: Allow, LabelSource: LabelHuman, IncumbentRisk: 1}}, 1)
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
	// "Production settings" includes reasoning being off. A candidate replayed
	// with thinking ON spends the same 256 tokens the live judge has on a chain
	// of thought and scores as a blanket DENY — so the eval would rank every
	// thinking model as maximally conservative and rank it for the wrong reason.
	if prov.last.Think == nil || *prov.last.Think {
		t.Errorf("Think = %v, want false — replay must score candidates the way production calls them", prov.last.Think)
	}
}

func TestReplayOnce_ProviderErrorIsFailClosedDeny(t *testing.T) {
	prov := &stubProvider{respond: func(string) (string, error) {
		return "", errors.New("model down")
	}}
	rows, err := ReplayCandidate(context.Background(), candidate(prov), []CorpusRow{{Prompt: "x", Label: Allow, LabelSource: LabelHuman, IncumbentRisk: 1}}, 1)
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
	rows, err := ReplayCandidate(context.Background(), candidate(prov), []CorpusRow{{Prompt: "x", Label: Allow, LabelSource: LabelHuman, IncumbentRisk: 1}}, 1)
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
	rows, err := ReplayCandidate(context.Background(), candidate(prov), []CorpusRow{{Prompt: "x", Label: Allow, LabelSource: LabelHuman, IncumbentRisk: 1}}, 0)
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
	_, err := ReplayCandidate(ctx, candidate(prov), []CorpusRow{{Prompt: "x", Label: Allow, LabelSource: LabelHuman}}, 1)
	if err == nil {
		t.Fatal("want error on cancelled context")
	}
}

// "Production settings" now includes constrained decoding. A replay without it
// asks the candidate to volunteer well-formed JSON from prose, which is a harder
// question than production asks: the model's reply opens with prose,
// NormalizeRawResponse fails, and replayOnce records the fail-closed DENY at
// risk 10 — so a model that works perfectly in production is scored as unusable
// and the harness recommends against the judge the operator already has.
func TestReplayCandidate_MirrorsProductionConstrainedDecoding(t *testing.T) {
	prov := &stubProvider{respond: func(string) (string, error) {
		return `{"decision":"allow","risk":1}`, nil
	}}
	_, err := ReplayCandidate(context.Background(), candidate(prov),
		[]CorpusRow{{Prompt: "x", Label: Allow, LabelSource: LabelHuman}}, 1)
	if err != nil {
		t.Fatalf("ReplayCandidate: %v", err)
	}
	if prov.last.Format == nil {
		t.Fatal("replay sent no Format — it measures a harder question than the gatekeeper asks")
	}
	obj, ok := prov.last.Format.(map[string]any)
	if !ok {
		t.Fatalf("Format is %T, want a JSON-schema object", prov.last.Format)
	}
	props, _ := obj["properties"].(map[string]any)
	dec, _ := props["decision"].(map[string]any)
	enum, _ := dec["enum"].([]string)
	if len(enum) != 3 {
		t.Errorf("decision enum = %v, want the credential path's three verbs", enum)
	}
}

// The harness compared two different things and called the gap instability.
//
// keeper_requests.decision is the decision production RECORDED — the judge's
// verdict AFTER the tier floor. gatekeeper.go applies ApplyTierPolicyFloor to
// every access and execute flow, so an L4 credential turns the model's ALLOW
// into ESCALATE before the row is written. replayOnce returned the raw
// normalized verdict and never applied that floor, so every row where policy
// changed the answer scored as a disagreement.
//
// Measured 2026-08-02 on dev2: vs-INCUMBENT 0.625, which was read as "the judge
// contradicts itself in a third of runs" and very nearly bought a self-
// consistency feature to fix it. The self-agreement split, added the next day,
// says the opposite — 1.000 in both halves, over 20 rows each. The model is
// perfectly deterministic on replay. The 37.5% was the tier floor, measured as
// noise.
//
// replayOnce's own comment already promised this: "normalizes the response ...
// so a replayed decision is scored identically to how production would have
// recorded it". It was half true, which is the worst kind.

func TestApplyRecordedPolicy_L4AllowBecomesEscalateJustAsProductionRecordsIt(t *testing.T) {
	got := applyRecordedPolicy(Allow, 4)
	if got != Escalate {
		t.Errorf("got %q, want ESCALATE — production stores the post-floor decision, "+
			"so comparing a raw ALLOW against it counts policy as disagreement", got)
	}
}

// The floor only ever tightens. A DENY must survive it, or the harness would
// invent agreement where production refused.
func TestApplyRecordedPolicy_NeverLoosensAVerdict(t *testing.T) {
	for _, d := range []Decision{Deny, Escalate} {
		if got := applyRecordedPolicy(d, 4); got != d {
			t.Errorf("L4 %s became %s; a tier may only tighten", d, got)
		}
	}
}

// Below the human-approval tier an ALLOW is an ALLOW, and forcing escalation
// there would make every low-tier row disagree instead.
func TestApplyRecordedPolicy_LeavesLowerTiersAlone(t *testing.T) {
	for _, lvl := range []int{1, 2} {
		if got := applyRecordedPolicy(Allow, lvl); got != Allow {
			t.Errorf("L%d ALLOW became %s", lvl, got)
		}
	}
}

// A row whose tier could not be resolved — no credential, a legacy row — must
// pass through untouched rather than being floored on a guess. Inventing a tier
// would move a verdict on evidence nobody has.
func TestApplyRecordedPolicy_UnknownTierChangesNothing(t *testing.T) {
	if got := applyRecordedPolicy(Allow, 0); got != Allow {
		t.Errorf("got %q, want ALLOW unchanged when the tier is unknown", got)
	}
}
