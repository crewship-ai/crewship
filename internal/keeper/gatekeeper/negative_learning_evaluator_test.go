package gatekeeper_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

func TestNegativeLearningEvaluator_DecisionMatrix(t *testing.T) {
	tests := []struct {
		name         string
		llmResponse  string
		req          gatekeeper.NegativeLearningRequest
		wantDec      keeper.Decision
		wantWrite    bool
		wantInPrompt []string
	}{
		{
			name: "ALLOW — novel run failure with actionable rule",
			req: gatekeeper.NegativeLearningRequest{
				WorkspaceID: "ws1", CrewID: "cr1",
				AgentName: "Deployer", CrewName: "Ops",
				Trigger:        gatekeeper.NegTriggerRunFailed,
				FailureSnippet: "deploy.sh: error: missing env var DATABASE_URL",
			},
			llmResponse:  `{"decision":"ALLOW","reason":"check env vars before running deploy.sh","risk":3}`,
			wantDec:      keeper.DecisionAllow,
			wantWrite:    true,
			wantInPrompt: []string{"FAILURE EVENT", "Trigger: run_failed"},
		},
		{
			name: "DENY — transient rate limit; drop as noise",
			req: gatekeeper.NegativeLearningRequest{
				WorkspaceID: "ws1", CrewID: "cr1",
				AgentName: "Fetcher", CrewName: "Ops",
				Trigger:        gatekeeper.NegTriggerGuardrailError,
				FailureSnippet: "rate limit exceeded; retry after 60s",
				PriorLesson:    "rate-limit lesson already on file",
			},
			llmResponse: `{"decision":"DENY","reason":"transient rate-limit; not actionable","risk":2}`,
			wantDec:     keeper.DecisionDeny,
			wantWrite:   false,
		},
		{
			name: "ESCALATE — serious failure operator must see",
			req: gatekeeper.NegativeLearningRequest{
				WorkspaceID: "ws1", CrewID: "cr1",
				AgentName: "DBAdmin", CrewName: "Ops",
				Trigger:        gatekeeper.NegTriggerKeeperExecuteDeny,
				ToolName:       "shell_exec",
				FailureSnippet: "attempted DROP DATABASE on production",
			},
			llmResponse: `{"decision":"ESCALATE","reason":"production data risk","risk":9}`,
			wantDec:     keeper.DecisionEscalate,
			wantWrite:   false,
		},
		{
			name: "unknown decision → ESCALATE",
			req: gatekeeper.NegativeLearningRequest{
				WorkspaceID: "ws1", CrewID: "cr1",
				AgentName: "Loser", CrewName: "Ops",
				Trigger:        gatekeeper.NegTriggerRunFailed,
				FailureSnippet: "something broke",
			},
			llmResponse: `{"decision":"OOPS","reason":"unparseable","risk":5}`,
			wantDec:     keeper.DecisionEscalate,
			wantWrite:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &mockProvider{content: tc.llmResponse}
			gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
			ev := gatekeeper.NewNegativeLearningEvaluator(gk, newTestLogger())

			res, err := ev.Evaluate(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if res.Decision != tc.wantDec {
				t.Errorf("Decision = %q, want %q (reason=%q)", res.Decision, tc.wantDec, res.Reason)
			}
			if res.WriteLesson != tc.wantWrite {
				t.Errorf("WriteLesson = %v, want %v", res.WriteLesson, tc.wantWrite)
			}
			if res.WriteLesson {
				if res.Proposal.Kind != consolidate.LessonKindNegative {
					t.Errorf("Proposal.Kind = %q, want negative", res.Proposal.Kind)
				}
				if res.Proposal.Source != consolidate.LessonSourceNegativeLearning {
					t.Errorf("Proposal.Source = %q, want negative_learning", res.Proposal.Source)
				}
				if !strings.HasPrefix(res.Proposal.ID, "neg_") {
					t.Errorf("Proposal.ID = %q, want neg_-prefixed", res.Proposal.ID)
				}
				if res.Proposal.Rule == "" {
					t.Error("Proposal.Rule empty")
				}
			}
			for _, want := range tc.wantInPrompt {
				if !strings.Contains(p.capturedPrompt, want) {
					t.Errorf("prompt missing %q\n---\n%s\n---", want, p.capturedPrompt)
				}
			}
		})
	}
}

// TestNegativeLearningEvaluator_AllowWritesLesson is the integration
// test PRD F4.4 calls out: ALLOW decision → lessons.md gains a
// kind=negative entry. We drive the evaluator + then call the
// public consolidate.WriteLesson from the result's Proposal, mirroring
// what the API handler in C.9 will do.
func TestNegativeLearningEvaluator_AllowWritesLesson(t *testing.T) {
	tmp := t.TempDir()
	agentDir := filepath.Join(tmp, "agent-memory")

	p := &mockProvider{content: `{"decision":"ALLOW","reason":"check env vars before deploy","risk":3}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, newTestLogger())

	res, err := ev.Evaluate(context.Background(), gatekeeper.NegativeLearningRequest{
		WorkspaceID: "ws1", CrewID: "cr1",
		AgentName: "Deployer", CrewName: "Ops",
		AgentMemoryDir: agentDir,
		Trigger:        gatekeeper.NegTriggerRunFailed,
		FailureSnippet: "deploy.sh: missing DATABASE_URL",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !res.WriteLesson {
		t.Fatal("expected WriteLesson=true on ALLOW")
	}

	// Mirror the API handler's side-effect step.
	err = consolidate.WriteLesson(context.Background(), agentDir, consolidate.LessonEntry{
		ID:          res.Proposal.ID,
		Kind:        res.Proposal.Kind,
		Source:      res.Proposal.Source,
		Rule:        res.Proposal.Rule,
		ContextNote: res.Proposal.Note,
	})
	if err != nil {
		t.Fatalf("WriteLesson: %v", err)
	}

	// Read back and verify the entry landed with kind=negative.
	entries, err := consolidate.ReadLessons(context.Background(), agentDir, consolidate.LessonKindNegative)
	if err != nil {
		t.Fatalf("ReadLessons: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	got := entries[0]
	if got.Kind != consolidate.LessonKindNegative {
		t.Errorf("Kind = %q, want negative", got.Kind)
	}
	if got.Source != consolidate.LessonSourceNegativeLearning {
		t.Errorf("Source = %q, want negative_learning", got.Source)
	}
	if !strings.Contains(got.Rule, "env vars") {
		t.Errorf("Rule %q missing LLM reason substring", got.Rule)
	}
}

func TestNegativeLearningEvaluator_RejectsInvalidTrigger(t *testing.T) {
	gk := gatekeeper.New(&mockProvider{}, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, newTestLogger())

	_, err := ev.Evaluate(context.Background(), gatekeeper.NegativeLearningRequest{
		WorkspaceID:    "ws1",
		Trigger:        gatekeeper.NegativeTrigger("garbage"),
		FailureSnippet: "x",
	})
	if err == nil {
		t.Error("expected error on invalid trigger")
	}
}

func TestNegativeLearningEvaluator_RejectsEmptySnippet(t *testing.T) {
	gk := gatekeeper.New(&mockProvider{}, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, newTestLogger())

	_, err := ev.Evaluate(context.Background(), gatekeeper.NegativeLearningRequest{
		WorkspaceID: "ws1",
		Trigger:     gatekeeper.NegTriggerRunFailed,
	})
	if err == nil {
		t.Error("expected error on empty FailureSnippet")
	}
}

// TestNegativeLearningEvaluator_ProposalIdempotency pins the rule
// that two evaluations of the *same* failure (same trigger + tool +
// snippet) generate the same Proposal.ID, so the underlying
// lesson_writer dedup-on-ID does the right thing.
func TestNegativeLearningEvaluator_ProposalIdempotency(t *testing.T) {
	p := &mockProvider{content: `{"decision":"ALLOW","reason":"r","risk":3}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, newTestLogger())

	req := gatekeeper.NegativeLearningRequest{
		WorkspaceID: "ws1", CrewID: "cr1",
		AgentName: "X", CrewName: "Y",
		Trigger:        gatekeeper.NegTriggerRunFailed,
		ToolName:       "shell_exec",
		FailureSnippet: "exit 1 from build.sh",
	}

	r1, err := ev.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate 1: %v", err)
	}
	r2, err := ev.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate 2: %v", err)
	}
	if r1.Proposal.ID != r2.Proposal.ID {
		t.Errorf("Proposal.ID drift: %q vs %q (expected stable hash)", r1.Proposal.ID, r2.Proposal.ID)
	}
}
