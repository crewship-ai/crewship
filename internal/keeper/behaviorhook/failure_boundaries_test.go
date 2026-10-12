package behaviorhook_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/keeper/behaviorhook"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type recordingBehaviorProvider struct {
	cannedProvider
	requests []llm.Request
	fail     bool
}

func (p *recordingBehaviorProvider) Complete(ctx context.Context, r llm.Request) (*llm.Response, error) {
	p.requests = append(p.requests, r)
	if p.fail {
		return nil, errors.New("evaluator offline")
	}
	return p.cannedProvider.Complete(ctx, r)
}

func TestHookPolicyFailureBlocksWithoutInventingAVerdict(t *testing.T) {
	db := testutil.MigratedDB(t)
	resolver := policy.NewResolver(db.DB)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	provider := &recordingBehaviorProvider{}
	ev := gatekeeper.NewBehaviorEvaluator(gatekeeper.New(provider, "fixture", newLogger()), newLogger())
	hook := behaviorhook.New(ev, resolver, nil)
	sample, err := hook.MaybeEvaluateEvery(t.Context(), hooks.EventContext{CrewID: "cr1"}, 1)
	if err != nil || sample == nil || sample.Blocked == nil || sample.Verdict != nil {
		t.Fatalf("policy failure must block without a verdict: %+v, %v", sample, err)
	}
	if sample.Blocked.Result.Outcome != hooks.OutcomeBlock {
		t.Fatal("sample did not block")
	}
	if len(provider.requests) != 0 {
		t.Fatal("consulted evaluator without resolved policy")
	}
}

func TestHookCarriesArgumentsAndPreservesFailureClassification(t *testing.T) {
	resolver := setupDB(t, "cr1", "guided", "warn")
	provider := &recordingBehaviorProvider{cannedProvider: cannedProvider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`}}
	ev := gatekeeper.NewBehaviorEvaluator(gatekeeper.New(provider, "fixture", newLogger()), newLogger())
	hook := behaviorhook.New(ev, resolver, nil)
	ec := hooks.EventContext{CrewID: "cr1", WorkspaceID: "ws1", AgentID: "agent", ToolName: "shell", Payload: map[string]any{"command": "ls", "count": 2}}
	sample, err := hook.MaybeEvaluateEvery(t.Context(), ec, 1)
	if err != nil || sample == nil || sample.Verdict == nil || sample.Blocked != nil {
		t.Fatalf("sample: %+v, %v", sample, err)
	}
	var prompt strings.Builder
	for _, request := range provider.requests {
		for _, message := range request.Messages {
			prompt.WriteString(message.Content)
		}
	}
	for _, want := range []string{"command", "ls", "count", "2"} {
		if !strings.Contains(prompt.String(), want) {
			t.Fatalf("tool argument %q missing from review", want)
		}
	}
	provider.fail = true
	sample, err = hook.MaybeEvaluateEvery(t.Context(), ec, 1)
	if err != nil || sample == nil || sample.Verdict == nil || sample.Blocked != nil {
		t.Fatalf("offline evaluator did not return a non-blocking escalation: %+v, %v", sample, err)
	}
	if sample.Verdict.Decision != gatekeeper.BehaviorEscalate || sample.Verdict.PolicyDecision != policy.DecisionAutoLogInbox {
		t.Fatalf("infrastructure failure lost its operator-review classification: %+v", sample.Verdict)
	}
	ec.ToolName = ""
	sample, err = hook.MaybeEvaluateEvery(t.Context(), ec, 1)
	if err != nil || sample == nil || sample.Verdict != nil || sample.Blocked != nil {
		t.Fatalf("invalid evaluation fabricated verdict: %+v, %v", sample, err)
	}
	calls := len(provider.requests)
	hook.SetSampleEvery(-1)
	if sample, err := hook.MaybeEvaluate(t.Context(), ec); err != nil || sample != nil {
		t.Fatalf("disabled hook sampled: %+v, %v", sample, err)
	}
	if len(provider.requests) != calls {
		t.Fatal("disabled hook called evaluator")
	}
}
