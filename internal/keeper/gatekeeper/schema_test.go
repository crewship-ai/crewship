package gatekeeper_test

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// decisionEnum digs the decision enum out of the constrained-decoding schema the
// judge sent, so a test can assert the decision SPACE rather than the verdict.
func decisionEnum(t *testing.T, format any) []string {
	t.Helper()
	if format == nil {
		t.Fatal("no Format sent — constrained decoding is off, so a chatty model can still answer in prose")
	}
	obj, ok := format.(map[string]any)
	if !ok {
		t.Fatalf("Format is %T, want a JSON-schema object", format)
	}
	props, ok := obj["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %#v", obj)
	}
	dec, ok := props["decision"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no decision property: %#v", props)
	}
	enum, ok := dec["enum"].([]string)
	if !ok {
		t.Fatalf("decision enum is %T, want []string", dec["enum"])
	}
	return enum
}

func hasVerb(enum []string, verb string) bool {
	for _, v := range enum {
		if v == verb {
			return true
		}
	}
	return false
}

// One llm.Request literal serves ALL FIVE prompt types (buildPrompt switches on
// RequestType), so a single hard-coded decision enum silently narrows whichever
// path asks a different question. The behavior watchdog is that path: its prompt
// enumerates FOUR verbs and WARN is a first-class outcome —
// classifyBehaviorDecision re-parses the raw body specifically to recover it,
// because the credential path's normaliser folds WARN into DENY.
//
// A three-verb schema makes WARN undecodable rather than merely unnormalised.
// Every would-be WARN then lands on one of the three, and applyBehaviorPolicy
// turns a DENY in "block" mode into an interrupted tool call — where the design
// says surface it to the operator and let the agent continue. The schema would
// have converted a monitoring signal into a production stop.
func TestEvaluate_BehaviorSchemaKeepsWARNDecodable(t *testing.T) {
	p := &mockProvider{content: `{"decision":"WARN","reason":"broad delete glob","risk":5}`}
	g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger())

	_, err := g.Evaluate(context.Background(), gatekeeper.EvalRequest{
		RequestType: keeper.RequestTypeBehavior,
		AgentName:   "riley",
		CrewName:    "ops",
		Behavior: &gatekeeper.BehaviorInput{
			ToolName:        "bash",
			ToolArgsSnippet: `{"command":"rm -rf ./build/*"}`,
			RecentToolCalls: []string{"read", "bash"},
			BehaviorMode:    "block",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	enum := decisionEnum(t, p.capturedReq.Format)
	if !hasVerb(enum, "WARN") {
		t.Errorf("behavior schema enum = %v, want WARN present — the prompt asks for four verbs and the evaluator recovers WARN from the raw body; a schema without it makes the answer unreachable", enum)
	}
	for _, verb := range []string{
		string(keeper.DecisionAllow), string(keeper.DecisionDeny), string(keeper.DecisionEscalate),
	} {
		if !hasVerb(enum, verb) {
			t.Errorf("behavior schema enum = %v, missing %s", enum, verb)
		}
	}
}

// The credential path must NOT gain WARN. It has no handler for it:
// NormalizeRawResponse folds anything outside the closed set to DENY, so a
// schema that could emit WARN would manufacture refusals on the one path where a
// refusal blocks real work.
func TestEvaluate_CredentialSchemaStaysThreeVerbs(t *testing.T) {
	p := &mockProvider{content: `{"decision":"ALLOW","reason":"proportional","risk":2}`}
	g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger())

	_, err := g.Evaluate(context.Background(), gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			Intent:            "Push the release tag so CI can build the artifact",
		},
		SecurityLevel:  keeper.SecurityLevelL2,
		CredentialName: "github-deploy-token",
		AgentName:      "DeployBot",
		CrewName:       "Platform",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	enum := decisionEnum(t, p.capturedReq.Format)
	if hasVerb(enum, "WARN") {
		t.Errorf("credential schema enum = %v, must not offer WARN — the credential path folds it to DENY", enum)
	}
	if len(enum) != 3 {
		t.Errorf("credential schema enum = %v, want exactly the three closed-set decisions", enum)
	}
}
