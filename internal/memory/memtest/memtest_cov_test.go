package memtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

func TestBuildRecallRequest_Opts(t *testing.T) {
	t.Parallel()
	req := BuildRecallRequest(
		WithRecallQuery("deploy notes"),
		WithRecallTier("CREW"),
		WithRecallLimit(3),
	)
	if req.Query != "deploy notes" {
		t.Errorf("Query = %q, want %q", req.Query, "deploy notes")
	}
	if req.Tier != "CREW" {
		t.Errorf("Tier = %q, want CREW", req.Tier)
	}
	if req.Limit != 3 {
		t.Errorf("Limit = %d, want 3", req.Limit)
	}
	// Untouched fields keep the defaults.
	if req.WorkspaceID != DefaultWorkspaceID {
		t.Errorf("WorkspaceID = %q, want default %q", req.WorkspaceID, DefaultWorkspaceID)
	}
}

func TestMockProvider_SetRetainResponse(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	m.SetRetainResponse(memory.RetainResult{ID: "custom/id", Bytes: 99})

	got, err := m.Retain(context.Background(), BuildRetainRequest())
	if err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if got.ID != "custom/id" || got.Bytes != 99 {
		t.Errorf("Retain = %+v, want {custom/id 99}", got)
	}
	if calls := m.RetainCalls(); len(calls) != 1 {
		t.Errorf("RetainCalls = %d, want 1", len(calls))
	}
}

func TestMockProvider_SetRecallResponse(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	m.SetRecallResponse(memory.RecallResult{
		Hits:        BuildSnippets(2),
		Quarantined: []string{"persona/poisoned.md"},
	})

	got, err := m.Recall(context.Background(), BuildRecallRequest())
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got.Hits) != 2 {
		t.Errorf("Hits = %d, want 2", len(got.Hits))
	}
	if len(got.Quarantined) != 1 || got.Quarantined[0] != "persona/poisoned.md" {
		t.Errorf("Quarantined = %v, want [persona/poisoned.md]", got.Quarantined)
	}
}

func TestMockProvider_SetForgetResponse(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	m.SetForgetResponse(memory.ForgetResult{Removed: 7})

	got, err := m.Forget(context.Background(), memory.ForgetRequest{
		WorkspaceID: DefaultWorkspaceID,
		ID:          "daily/2026-06-11",
	})
	if err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if got.Removed != 7 {
		t.Errorf("Removed = %d, want 7", got.Removed)
	}
}

func TestMockProvider_SetRetainDelay_HonoursCancelledContext(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	m.SetRetainDelay(5 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := m.Retain(ctx, BuildRetainRequest())
	if err == nil {
		t.Fatal("Retain with cancelled ctx: want error, got nil")
	}
	if err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Retain blocked %v despite cancelled ctx", elapsed)
	}
}

func TestMockProvider_SetForgetDelay_HonoursCancelledContext(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	m.SetForgetDelay(5 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.Forget(ctx, memory.ForgetRequest{WorkspaceID: DefaultWorkspaceID, ID: "x"})
	if err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestMockProvider_SetHealthStatus(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	checked := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	m.SetHealthStatus(memory.HealthStatus{
		OK:        false,
		Message:   "index rebuild in progress",
		CheckedAt: checked,
	})

	got := m.Health(context.Background())
	if got.OK {
		t.Error("Health.OK = true, want false")
	}
	if got.Message != "index rebuild in progress" {
		t.Errorf("Message = %q", got.Message)
	}
	if !got.CheckedAt.Equal(checked) {
		t.Errorf("CheckedAt = %v, want %v", got.CheckedAt, checked)
	}
	if n := m.HealthCalls(); n != 1 {
		t.Errorf("HealthCalls = %d, want 1", n)
	}
}

func TestMockProvider_ForgetCalls_RecordsAndCopies(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	ctx := context.Background()

	if _, err := m.Forget(ctx, memory.ForgetRequest{WorkspaceID: "ws_a", ID: "one"}); err != nil {
		t.Fatalf("Forget 1: %v", err)
	}
	if _, err := m.Forget(ctx, memory.ForgetRequest{WorkspaceID: "ws_a", DataSubjectID: "user-1"}); err != nil {
		t.Fatalf("Forget 2: %v", err)
	}

	calls := m.ForgetCalls()
	if len(calls) != 2 {
		t.Fatalf("ForgetCalls = %d, want 2", len(calls))
	}
	if calls[0].ID != "one" || calls[1].DataSubjectID != "user-1" {
		t.Errorf("recorded calls mismatch: %+v", calls)
	}

	// Defensive copy: mutating the returned slice must not corrupt the
	// mock's internal record.
	calls[0].ID = "tampered"
	again := m.ForgetCalls()
	if again[0].ID != "one" {
		t.Errorf("internal state mutated through returned copy: %+v", again[0])
	}
}

func TestWorkspaceLayout_String(t *testing.T) {
	t.Parallel()
	w := WorkspaceLayout{Root: "/tmp/ws-root"}
	got := w.String()
	if !strings.Contains(got, "/tmp/ws-root") {
		t.Errorf("String() = %q, want it to contain the root path", got)
	}
	if !strings.HasPrefix(got, "WorkspaceLayout{") {
		t.Errorf("String() = %q, want WorkspaceLayout{...} shape", got)
	}
}

func TestMockProvider_DelayElapsesNormally(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	m.SetRecallDelay(5 * time.Millisecond)

	got, err := m.Recall(context.Background(), BuildRecallRequest())
	if err != nil {
		t.Fatalf("Recall after short delay: %v", err)
	}
	if got.Hits == nil {
		t.Error("default Recall result missing non-nil Hits slice")
	}
}
