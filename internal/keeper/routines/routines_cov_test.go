package routines_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keeper/routines"
	"github.com/crewship-ai/crewship/internal/skills"
)

// failingSkillPersister fails every side-effect so the sweep's
// "count the error, keep going" branches are exercised.
type failingSkillPersister struct{}

var errPersist = errors.New("persister down")

func (failingSkillPersister) MarkVerified(context.Context, string) error   { return errPersist }
func (failingSkillPersister) MarkUnverified(context.Context, string) error { return errPersist }
func (failingSkillPersister) SetLifecycle(context.Context, string, skills.LifecycleState, string) error {
	return errPersist
}
func (failingSkillPersister) WriteInboxItem(context.Context, string, string, bool) error {
	return errPersist
}

type failingHealthPersister struct{}

func (failingHealthPersister) TriggerConsolidation(context.Context, string, string, string) error {
	return errPersist
}
func (failingHealthPersister) WriteInboxItem(context.Context, string, string, string, bool) error {
	return errPersist
}

func newSkillEvaluator(t *testing.T, content string) *gatekeeper.SkillReviewEvaluator {
	t.Helper()
	p := &staticProvider{content: content}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
	return gatekeeper.NewSkillReviewEvaluator(gk, newTestLogger())
}

func newHealthEvaluator(t *testing.T, content string) *gatekeeper.MemoryHealthEvaluator {
	t.Helper()
	p := &staticProvider{content: content}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
	return gatekeeper.NewMemoryHealthEvaluator(gk, newTestLogger())
}

func activeSkillInput(id string) routines.SkillSweepInput {
	return routines.SkillSweepInput{
		Skill: routines.SkillRow{
			ID: id, Name: id, LifecycleState: skills.LifecycleActive,
			LastUsedAt: time.Now().UTC().Add(-48 * time.Hour), Assignments: 1,
			WorkspaceID: "ws1",
		},
		AssignedAgents: []string{"agent-x"},
	}
}

func TestRunSkillReview_NilDependencies(t *testing.T) {
	ev := newSkillEvaluator(t, `{"decision":"ALLOW","reason":"ok","risk":1}`)
	per := &fakeSkillPersister{}

	if _, err := routines.RunSkillReview(context.Background(), nil, per, nil, newTestLogger()); err == nil ||
		!strings.Contains(err.Error(), "nil evaluator") {
		t.Errorf("nil evaluator: err = %v, want 'nil evaluator'", err)
	}
	if _, err := routines.RunSkillReview(context.Background(), ev, nil, nil, newTestLogger()); err == nil ||
		!strings.Contains(err.Error(), "nil persister") {
		t.Errorf("nil persister: err = %v, want 'nil persister'", err)
	}
}

func TestRunSkillReview_NilLoggerFallsBackToDefault(t *testing.T) {
	ev := newSkillEvaluator(t, `{"decision":"ALLOW","reason":"ok","risk":1}`)
	sum, err := routines.RunSkillReview(context.Background(), ev, &fakeSkillPersister{}, nil, nil)
	if err != nil {
		t.Fatalf("RunSkillReview: %v", err)
	}
	if sum.ScannedSkills != 0 {
		t.Errorf("ScannedSkills = %d, want 0 for empty input", sum.ScannedSkills)
	}
}

func TestRunSkillReview_EvaluatorErrorCountedAndSweepContinues(t *testing.T) {
	ev := newSkillEvaluator(t, `{"decision":"ALLOW","reason":"ok","risk":1}`)
	per := &fakeSkillPersister{}

	// Empty Skill.ID makes the evaluator itself error; the next input is
	// healthy and must still be processed.
	bad := activeSkillInput("")
	good := activeSkillInput("sk_good")
	sum, err := routines.RunSkillReview(context.Background(), ev, per,
		[]routines.SkillSweepInput{bad, good}, newTestLogger())
	if err != nil {
		t.Fatalf("RunSkillReview: %v", err)
	}
	if sum.ScannedSkills != 2 {
		t.Errorf("ScannedSkills = %d, want 2", sum.ScannedSkills)
	}
	if sum.Errors != 1 {
		t.Errorf("Errors = %d, want 1 (evaluator error)", sum.Errors)
	}
	if sum.VerifiedAllowed != 1 || len(per.verified) != 1 || per.verified[0] != "sk_good" {
		t.Errorf("good skill not processed: VerifiedAllowed=%d verified=%v",
			sum.VerifiedAllowed, per.verified)
	}
}

func TestRunSkillReview_PersisterErrorOnAllow(t *testing.T) {
	ev := newSkillEvaluator(t, `{"decision":"ALLOW","reason":"ok","risk":1}`)
	sum, err := routines.RunSkillReview(context.Background(), ev, failingSkillPersister{},
		[]routines.SkillSweepInput{activeSkillInput("sk_a")}, newTestLogger())
	if err != nil {
		t.Fatalf("RunSkillReview: %v", err)
	}
	if sum.VerifiedAllowed != 1 {
		t.Errorf("VerifiedAllowed = %d, want 1", sum.VerifiedAllowed)
	}
	if sum.Errors != 1 {
		t.Errorf("Errors = %d, want 1 (MarkVerified failure)", sum.Errors)
	}
}

func TestRunSkillReview_PersisterErrorsOnDeny(t *testing.T) {
	now := time.Now().UTC()
	ev := newSkillEvaluator(t, `{"decision":"DENY","reason":"abandoned","risk":8}`)

	// Stale skill (no assignments, long unused) so the lifecycle proposal
	// flips active→stale: MarkUnverified + SetLifecycle + blocking
	// WriteInboxItem all fail → 3 errors.
	in := routines.SkillSweepInput{
		Skill: routines.SkillRow{
			ID: "sk_dead", Name: "dead", LifecycleState: skills.LifecycleActive,
			LastUsedAt:  now.Add(-60 * 24 * time.Hour),
			WorkspaceID: "ws1",
		},
	}
	sum, err := routines.RunSkillReview(context.Background(), ev, failingSkillPersister{},
		[]routines.SkillSweepInput{in}, newTestLogger())
	if err != nil {
		t.Fatalf("RunSkillReview: %v", err)
	}
	if sum.UnverifiedDenied != 1 {
		t.Errorf("UnverifiedDenied = %d, want 1", sum.UnverifiedDenied)
	}
	if sum.LifecycleFlipped != 1 {
		t.Errorf("LifecycleFlipped = %d, want 1", sum.LifecycleFlipped)
	}
	if sum.Errors != 3 {
		t.Errorf("Errors = %d, want 3 (unverify + lifecycle + inbox failures)", sum.Errors)
	}
}

func TestRunSkillReview_EscalateWritesNonBlockingInbox(t *testing.T) {
	ev := newSkillEvaluator(t, `{"decision":"ESCALATE","reason":"needs human","risk":5}`)
	per := &fakeSkillPersister{}
	sum, err := routines.RunSkillReview(context.Background(), ev, per,
		[]routines.SkillSweepInput{activeSkillInput("sk_esc")}, newTestLogger())
	if err != nil {
		t.Fatalf("RunSkillReview: %v", err)
	}
	if sum.EscalatedToInbox != 1 {
		t.Errorf("EscalatedToInbox = %d, want 1", sum.EscalatedToInbox)
	}
	if len(per.inboxNonBlocking) != 1 || per.inboxNonBlocking[0] != "sk_esc" {
		t.Errorf("non-blocking inbox = %v, want [sk_esc]", per.inboxNonBlocking)
	}
	if len(per.inboxBlocking) != 0 {
		t.Errorf("blocking inbox = %v, want empty for ESCALATE", per.inboxBlocking)
	}

	// ESCALATE persister failure also counts.
	sum2, err := routines.RunSkillReview(context.Background(), ev, failingSkillPersister{},
		[]routines.SkillSweepInput{activeSkillInput("sk_esc2")}, newTestLogger())
	if err != nil {
		t.Fatalf("RunSkillReview (failing): %v", err)
	}
	if sum2.Errors != 1 {
		t.Errorf("Errors = %d, want 1 (inbox write failure)", sum2.Errors)
	}
}

func TestRunSkillReview_CancelledContextAbortsSweep(t *testing.T) {
	ev := newSkillEvaluator(t, `{"decision":"ALLOW","reason":"ok","risk":1}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sum, err := routines.RunSkillReview(ctx, ev, &fakeSkillPersister{},
		[]routines.SkillSweepInput{activeSkillInput("sk_a")}, newTestLogger())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if sum.ScannedSkills != 0 {
		t.Errorf("ScannedSkills = %d, want 0 (aborted before first item)", sum.ScannedSkills)
	}
}

func TestRunMemoryHealthCheck_NilDependencies(t *testing.T) {
	ev := newHealthEvaluator(t, `{"decision":"ALLOW","reason":"ok","risk":1}`)
	if _, err := routines.RunMemoryHealthCheck(context.Background(), nil, &fakeHealthPersister{}, nil, newTestLogger()); err == nil ||
		!strings.Contains(err.Error(), "nil evaluator") {
		t.Errorf("nil evaluator: err = %v", err)
	}
	if _, err := routines.RunMemoryHealthCheck(context.Background(), ev, nil, nil, newTestLogger()); err == nil ||
		!strings.Contains(err.Error(), "nil persister") {
		t.Errorf("nil persister: err = %v", err)
	}
}

func TestRunMemoryHealthCheck_AllowCountsHealthy(t *testing.T) {
	ev := newHealthEvaluator(t, `{"decision":"ALLOW","reason":"healthy","risk":1}`)
	per := &fakeHealthPersister{}
	scopes := []routines.MemoryHealthScope{{
		WorkspaceID: "ws1", CrewID: "cr1", CrewName: "Ops",
		Snapshot: consolidate.HealthSnapshot{Overall: 90},
	}}
	sum, err := routines.RunMemoryHealthCheck(context.Background(), ev, per, scopes, nil)
	if err != nil {
		t.Fatalf("RunMemoryHealthCheck: %v", err)
	}
	if sum.HealthyAllowed != 1 {
		t.Errorf("HealthyAllowed = %d, want 1", sum.HealthyAllowed)
	}
	if len(per.consolidations) != 0 || len(per.inbox) != 0 {
		t.Errorf("side effects on ALLOW: consolidations=%v inbox=%v",
			per.consolidations, per.inbox)
	}
}

func TestRunMemoryHealthCheck_EvaluatorErrorCounted(t *testing.T) {
	ev := newHealthEvaluator(t, `{"decision":"ALLOW","reason":"ok","risk":1}`)
	per := &fakeHealthPersister{}
	scopes := []routines.MemoryHealthScope{
		{WorkspaceID: "", CrewID: "cr_bad", CrewName: "Bad"}, // empty workspace → evaluator error
		{WorkspaceID: "ws1", CrewID: "cr_ok", CrewName: "OK",
			Snapshot: consolidate.HealthSnapshot{Overall: 90}},
	}
	sum, err := routines.RunMemoryHealthCheck(context.Background(), ev, per, scopes, newTestLogger())
	if err != nil {
		t.Fatalf("RunMemoryHealthCheck: %v", err)
	}
	if sum.ScannedScopes != 2 {
		t.Errorf("ScannedScopes = %d, want 2", sum.ScannedScopes)
	}
	if sum.Errors != 1 {
		t.Errorf("Errors = %d, want 1", sum.Errors)
	}
	if sum.HealthyAllowed != 1 {
		t.Errorf("HealthyAllowed = %d, want 1 (sweep must continue past the error)", sum.HealthyAllowed)
	}
}

func TestRunMemoryHealthCheck_PersisterErrorsCounted(t *testing.T) {
	// DENY → TriggerConsolidation fails.
	evDeny := newHealthEvaluator(t, `{"decision":"DENY","reason":"bloated","risk":7}`)
	scopes := []routines.MemoryHealthScope{{
		WorkspaceID: "ws1", CrewID: "cr1", CrewName: "Ops",
		Snapshot: consolidate.HealthSnapshot{Overall: 30}, AgentMDBytes: 3800,
	}}
	sum, err := routines.RunMemoryHealthCheck(context.Background(), evDeny,
		failingHealthPersister{}, scopes, newTestLogger())
	if err != nil {
		t.Fatalf("RunMemoryHealthCheck (deny): %v", err)
	}
	if sum.AutoConsolidated != 1 || sum.Errors != 1 {
		t.Errorf("deny: AutoConsolidated=%d Errors=%d, want 1/1", sum.AutoConsolidated, sum.Errors)
	}

	// ESCALATE → WriteInboxItem fails.
	evEsc := newHealthEvaluator(t, `{"decision":"ESCALATE","reason":"needs human","risk":6}`)
	sum2, err := routines.RunMemoryHealthCheck(context.Background(), evEsc,
		failingHealthPersister{}, scopes, newTestLogger())
	if err != nil {
		t.Fatalf("RunMemoryHealthCheck (escalate): %v", err)
	}
	if sum2.EscalatedToInbox != 1 || sum2.Errors != 1 {
		t.Errorf("escalate: EscalatedToInbox=%d Errors=%d, want 1/1",
			sum2.EscalatedToInbox, sum2.Errors)
	}
}

func TestRunMemoryHealthCheck_CancelledContextAbortsSweep(t *testing.T) {
	ev := newHealthEvaluator(t, `{"decision":"ALLOW","reason":"ok","risk":1}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sum, err := routines.RunMemoryHealthCheck(ctx, ev, &fakeHealthPersister{},
		[]routines.MemoryHealthScope{{WorkspaceID: "ws1", CrewID: "cr1"}}, newTestLogger())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if sum.ScannedScopes != 0 {
		t.Errorf("ScannedScopes = %d, want 0", sum.ScannedScopes)
	}
}
