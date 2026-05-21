package routines_test

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keeper/routines"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/skills"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// staticProvider returns the same canned response for every Complete
// call — enough to drive RunSkillReview / RunMemoryHealthCheck through
// their decision branches.
type staticProvider struct {
	content string
}

func (s *staticProvider) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: s.content}, nil
}
func (s *staticProvider) Stream(ctx context.Context, req llm.Request, h func(llm.StreamEvent) error) (*llm.Response, error) {
	resp, _ := s.Complete(ctx, req)
	_ = h(llm.StreamEvent{Type: "done", Response: resp})
	return resp, nil
}
func (s *staticProvider) Name() string { return "static" }

// fakeSkillPersister records every side-effect call so tests can
// assert on counts without a real DB.
type fakeSkillPersister struct {
	mu               sync.Mutex
	verified         []string
	unverified       []string
	lifecycle        map[string]skills.LifecycleState
	inboxBlocking    []string
	inboxNonBlocking []string
}

func (f *fakeSkillPersister) MarkVerified(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verified = append(f.verified, id)
	return nil
}
func (f *fakeSkillPersister) MarkUnverified(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unverified = append(f.unverified, id)
	return nil
}
func (f *fakeSkillPersister) SetLifecycle(ctx context.Context, id string, next skills.LifecycleState, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lifecycle == nil {
		f.lifecycle = map[string]skills.LifecycleState{}
	}
	f.lifecycle[id] = next
	return nil
}
func (f *fakeSkillPersister) WriteInboxItem(ctx context.Context, id, reason string, blocking bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if blocking {
		f.inboxBlocking = append(f.inboxBlocking, id)
	} else {
		f.inboxNonBlocking = append(f.inboxNonBlocking, id)
	}
	return nil
}

func TestRunSkillReview_RoutesDecisionsThroughPersister(t *testing.T) {
	now := time.Now().UTC()
	day := 24 * time.Hour

	p := &staticProvider{content: `{"decision":"ALLOW","reason":"active use","risk":2}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewSkillReviewEvaluator(gk, newTestLogger())
	per := &fakeSkillPersister{}

	inputs := []routines.SkillSweepInput{
		{
			Skill: routines.SkillRow{
				ID: "sk_a", Name: "a", LifecycleState: skills.LifecycleActive,
				LastUsedAt: now.Add(-2 * day), Assignments: 1,
				WorkspaceID: "ws1",
			},
			AssignedAgents: []string{"agent-x"},
		},
		{
			Skill: routines.SkillRow{
				ID: "sk_b", Name: "b", LifecycleState: skills.LifecycleActive,
				LastUsedAt: now.Add(-2 * day), Assignments: 1,
				WorkspaceID: "ws1",
			},
			AssignedAgents: []string{"agent-y"},
		},
	}
	sum, err := routines.RunSkillReview(context.Background(), ev, per, inputs, newTestLogger())
	if err != nil {
		t.Fatalf("RunSkillReview: %v", err)
	}
	if sum.ScannedSkills != 2 {
		t.Errorf("ScannedSkills = %d, want 2", sum.ScannedSkills)
	}
	if sum.VerifiedAllowed != 2 {
		t.Errorf("VerifiedAllowed = %d, want 2", sum.VerifiedAllowed)
	}
	if got := len(per.verified); got != 2 {
		t.Errorf("verified count = %d, want 2", got)
	}
}

func TestRunSkillReview_DenyTriggersBlockingInbox(t *testing.T) {
	now := time.Now().UTC()
	p := &staticProvider{content: `{"decision":"DENY","reason":"abandoned","risk":8}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewSkillReviewEvaluator(gk, newTestLogger())
	per := &fakeSkillPersister{}

	inputs := []routines.SkillSweepInput{
		{
			// no assignments + unused → snapshot proposes stale, evaluator returns DENY.
			Skill: routines.SkillRow{
				ID: "sk_dead", Name: "dead", LifecycleState: skills.LifecycleActive,
				LastUsedAt:  now.Add(-60 * 24 * time.Hour),
				WorkspaceID: "ws1",
			},
		},
	}
	sum, err := routines.RunSkillReview(context.Background(), ev, per, inputs, newTestLogger())
	if err != nil {
		t.Fatalf("RunSkillReview: %v", err)
	}
	if sum.UnverifiedDenied != 1 {
		t.Errorf("UnverifiedDenied = %d, want 1", sum.UnverifiedDenied)
	}
	if got := len(per.inboxBlocking); got != 1 {
		t.Errorf("blocking inbox count = %d, want 1", got)
	}
	if sum.LifecycleFlipped != 1 {
		t.Errorf("LifecycleFlipped = %d, want 1 (active→stale)", sum.LifecycleFlipped)
	}
	if got := per.lifecycle["sk_dead"]; got != skills.LifecycleStale {
		t.Errorf("lifecycle = %q, want stale", got)
	}
}

// fakeHealthPersister mirrors the skill persister for F4.3.
type fakeHealthPersister struct {
	mu             sync.Mutex
	consolidations []string // crew_ids
	inbox          []string
}

func (f *fakeHealthPersister) TriggerConsolidation(ctx context.Context, ws, crew, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consolidations = append(f.consolidations, crew)
	return nil
}
func (f *fakeHealthPersister) WriteInboxItem(ctx context.Context, ws, crew, reason string, blocking bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inbox = append(f.inbox, crew)
	return nil
}

func TestRunMemoryHealthCheck_RoutesDecisions(t *testing.T) {
	p := &staticProvider{content: `{"decision":"DENY","reason":"bloated","risk":7}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewMemoryHealthEvaluator(gk, newTestLogger())
	per := &fakeHealthPersister{}

	scopes := []routines.MemoryHealthScope{
		{
			WorkspaceID: "ws1", CrewID: "cr1", CrewName: "Ops",
			Snapshot:     consolidate.HealthSnapshot{Overall: 30},
			AgentMDBytes: 3800,
		},
		{
			WorkspaceID: "ws1", CrewID: "cr2", CrewName: "Dev",
			Snapshot:           consolidate.HealthSnapshot{Overall: 35},
			ContradictionCount: 2, // forces ESCALATE
		},
	}
	sum, err := routines.RunMemoryHealthCheck(context.Background(), ev, per, scopes, newTestLogger())
	if err != nil {
		t.Fatalf("RunMemoryHealthCheck: %v", err)
	}
	if sum.ScannedScopes != 2 {
		t.Errorf("ScannedScopes = %d, want 2", sum.ScannedScopes)
	}
	if sum.AutoConsolidated != 1 {
		t.Errorf("AutoConsolidated = %d, want 1", sum.AutoConsolidated)
	}
	if sum.EscalatedToInbox != 1 {
		t.Errorf("EscalatedToInbox = %d, want 1", sum.EscalatedToInbox)
	}
	if got := len(per.consolidations); got != 1 || per.consolidations[0] != "cr1" {
		t.Errorf("consolidations = %v, want [cr1]", per.consolidations)
	}
	if got := len(per.inbox); got != 1 || per.inbox[0] != "cr2" {
		t.Errorf("inbox = %v, want [cr2]", per.inbox)
	}
}

func TestRoutineKind_Validate(t *testing.T) {
	if err := routines.RoutineKindSkillReview.Validate(); err != nil {
		t.Errorf("skill_review rejected: %v", err)
	}
	if err := routines.RoutineKindMemoryHealthCheck.Validate(); err != nil {
		t.Errorf("memory_health_check rejected: %v", err)
	}
	if err := routines.RoutineKind("yolo").Validate(); err == nil {
		t.Error("yolo accepted")
	}
}
