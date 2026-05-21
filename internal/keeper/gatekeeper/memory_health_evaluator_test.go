package gatekeeper_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

func TestMemoryHealthEvaluator_DecisionMatrix(t *testing.T) {
	healthy := consolidate.HealthSnapshot{
		Overall: 82, Freshness: 90, Coverage: 70, Coherence: 80,
		Efficiency: 75, Reachability: 80,
	}
	bloated := consolidate.HealthSnapshot{Overall: 35}

	tests := []struct {
		name             string
		req              gatekeeper.MemoryHealthRequest
		llmResponse      string
		wantDec          keeper.Decision
		wantConsolidate  bool
		wantReasonSubstr string
		wantInPrompt     []string
	}{
		{
			name: "ALLOW — healthy snapshot",
			req: gatekeeper.MemoryHealthRequest{
				WorkspaceID: "ws1", CrewID: "cr1",
				AgentName:    "Auditor",
				CrewName:     "Ops",
				Snapshot:     healthy,
				AgentMDBytes: 1024, PersonaMDBytes: 600, CrewMDBytes: 2000,
				StalestEntryDays: 10,
			},
			llmResponse:     `{"decision":"ALLOW","reason":"healthy","risk":2}`,
			wantDec:         keeper.DecisionAllow,
			wantConsolidate: false,
			wantInPrompt:    []string{"MEMORY SNAPSHOT", "Refutes relations (contradictions): 0"},
		},
		{
			name: "DENY (no contradictions) — auto-consolidate",
			req: gatekeeper.MemoryHealthRequest{
				WorkspaceID: "ws1", CrewID: "cr1",
				AgentName: "Auditor", CrewName: "Ops",
				Snapshot:         bloated,
				AgentMDBytes:     3800, // >80% of 4KB cap
				StalestEntryDays: 70,
			},
			llmResponse:     `{"decision":"DENY","reason":"bloat + stale","risk":7}`,
			wantDec:         keeper.DecisionDeny,
			wantConsolidate: true,
		},
		{
			name: "DENY but contradictions present → ESCALATE override",
			req: gatekeeper.MemoryHealthRequest{
				WorkspaceID: "ws1", CrewID: "cr1",
				AgentName: "Auditor", CrewName: "Ops",
				Snapshot:           bloated,
				ContradictionCount: 3,
			},
			llmResponse:      `{"decision":"DENY","reason":"bloat","risk":7}`,
			wantDec:          keeper.DecisionEscalate,
			wantConsolidate:  false,
			wantReasonSubstr: "contradiction",
		},
		{
			name: "ESCALATE — mixed signals",
			req: gatekeeper.MemoryHealthRequest{
				WorkspaceID: "ws1", CrewID: "cr1",
				AgentName: "Auditor", CrewName: "Ops",
				Snapshot: healthy, ContradictionCount: 1,
			},
			llmResponse:     `{"decision":"ESCALATE","reason":"contradictions present","risk":6}`,
			wantDec:         keeper.DecisionEscalate,
			wantConsolidate: false,
		},
		{
			name: "unknown decision → ESCALATE (fail-soft)",
			req: gatekeeper.MemoryHealthRequest{
				WorkspaceID: "ws1", CrewID: "cr1",
				AgentName: "Auditor", CrewName: "Ops",
			},
			llmResponse:     `{"decision":"YOLO","reason":"no idea","risk":5}`,
			wantDec:         keeper.DecisionEscalate,
			wantConsolidate: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &mockProvider{content: tc.llmResponse}
			gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
			ev := gatekeeper.NewMemoryHealthEvaluator(gk, newTestLogger())

			res, err := ev.Evaluate(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if res.Decision != tc.wantDec {
				t.Errorf("Decision = %q, want %q (reason=%q)", res.Decision, tc.wantDec, res.Reason)
			}
			if res.AutoConsolidate != tc.wantConsolidate {
				t.Errorf("AutoConsolidate = %v, want %v", res.AutoConsolidate, tc.wantConsolidate)
			}
			if tc.wantReasonSubstr != "" && !strings.Contains(res.Reason, tc.wantReasonSubstr) {
				t.Errorf("Reason %q missing substring %q", res.Reason, tc.wantReasonSubstr)
			}
			for _, want := range tc.wantInPrompt {
				if !strings.Contains(p.capturedPrompt, want) {
					t.Errorf("prompt missing %q\n---\n%s\n---", want, p.capturedPrompt)
				}
			}
		})
	}
}

func TestMemoryHealthEvaluator_RejectsEmptyWorkspace(t *testing.T) {
	gk := gatekeeper.New(&mockProvider{}, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewMemoryHealthEvaluator(gk, newTestLogger())
	_, err := ev.Evaluate(context.Background(), gatekeeper.MemoryHealthRequest{})
	if err == nil {
		t.Error("expected error on empty WorkspaceID")
	}
}
