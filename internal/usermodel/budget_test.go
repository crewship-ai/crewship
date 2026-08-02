package usermodel

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// blockingProvider hangs until its context is cancelled.
type blockingProvider struct{}

func (b *blockingProvider) Complete(ctx context.Context, _ llm.Request) (*llm.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingProvider) Stream(context.Context, llm.Request, func(llm.StreamEvent) error) (*llm.Response, error) {
	panic("not used")
}
func (b *blockingProvider) Name() string { return "blocking" }

// The aux slot carries a per-call budget the operator can lower
// (`crewship keeper aux set curator --timeout`). #1601 was that exact
// field reaching no evaluator, so every one of them ran on a bound
// nobody had chosen.
//
// Here the consequence is worse than a wrong number. The sweep is ONE
// goroutine walking every operator in every workspace, and the context it
// passes down lives until server shutdown — so a single wedged call
// stalls the whole daily sweep indefinitely, for every workspace behind
// it, with no deadline anywhere in the path to end it.
func TestExtract_BoundsTheModelCallByTheSlotBudget(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	seedTranscript(t, db, []struct{ role, content, author string }{
		{"user", "I run the platform team here.", "u1"},
	})
	ex := New(db,
		func() (llm.Provider, string, time.Duration) { return &blockingProvider{}, "m", 50 * time.Millisecond },
		func(context.Context) Profile { return ProfileStatedTechnical }, quietLogger())

	done := make(chan error, 1)
	go func() {
		_, err := ex.Extract(context.Background(),
			consolidate.UserModelCandidate{WorkspaceID: "ws1", UserID: "u1"}, "")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a wedged model call must surface as an error, not as an empty extraction")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Extract did not return: the slot's per-call budget does not bound the call")
	}
}

// slowProvider answers after a delay, honouring cancellation.
type slowProvider struct {
	delay time.Duration
	reply string
}

func (s *slowProvider) Complete(ctx context.Context, _ llm.Request) (*llm.Response, error) {
	select {
	case <-time.After(s.delay):
		return &llm.Response{Content: s.reply}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *slowProvider) Stream(context.Context, llm.Request, func(llm.StreamEvent) error) (*llm.Response, error) {
	panic("not used")
}
func (s *slowProvider) Name() string { return "slow" }

// A slot reporting no budget gets a USABLE one, not a zero.
//
// The distinction this pins is the one a "does it error" assertion
// misses: `context.WithTimeout(ctx, 0)` is an already-expired context, so
// forwarding a bare zero also produces an error — instantly, on every
// call, for every operator. The feature would be off and look like a
// model that keeps timing out. So the assertion is that the extraction
// SUCCEEDS: the fallback has to be a real deadline, not a nominal one.
func TestExtract_ZeroBudgetGetsAUsableFallbackNotAZero(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	seedTranscript(t, db, []struct{ role, content, author string }{
		{"user", "I run the platform team here.", "u1"},
	})
	prov := &slowProvider{
		delay: 40 * time.Millisecond,
		reply: `{"facts":[{"key":"role","value":"runs the platform team","quote":"I run the platform team here","source":"stated"}]}`,
	}
	ex := New(db,
		func() (llm.Provider, string, time.Duration) { return prov, "m", 0 },
		func(context.Context) Profile { return ProfileStatedTechnical }, quietLogger(),
		WithFallbackCallTimeout(5*time.Second))

	body, err := ex.Extract(context.Background(),
		consolidate.UserModelCandidate{WorkspaceID: "ws1", UserID: "u1"}, "")
	if err != nil {
		t.Fatalf("a zero slot budget was forwarded as the deadline: %v", err)
	}
	if body != "- role: runs the platform team" {
		t.Fatalf("expected the extraction to complete inside the fallback; got %q", body)
	}
}

// …and the fallback is still a bound, not an absence of one.
func TestExtract_ZeroBudgetIsStillBounded(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	seedTranscript(t, db, []struct{ role, content, author string }{
		{"user", "I run the platform team here.", "u1"},
	})
	ex := New(db,
		func() (llm.Provider, string, time.Duration) { return &blockingProvider{}, "m", 0 },
		func(context.Context) Profile { return ProfileStatedTechnical }, quietLogger(),
		WithFallbackCallTimeout(50*time.Millisecond))

	done := make(chan error, 1)
	go func() {
		_, err := ex.Extract(context.Background(),
			consolidate.UserModelCandidate{WorkspaceID: "ws1", UserID: "u1"}, "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the fallback budget to fire")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a zero slot budget was read as no deadline at all")
	}
}
