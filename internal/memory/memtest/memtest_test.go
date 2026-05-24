package memtest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

func TestBuildRetainRequest_Defaults(t *testing.T) {
	t.Parallel()
	req := BuildRetainRequest()
	if req.WorkspaceID != DefaultWorkspaceID {
		t.Errorf("WorkspaceID = %q, want %q", req.WorkspaceID, DefaultWorkspaceID)
	}
	if req.Mode != "replace" {
		t.Errorf("Mode = %q, want \"replace\"", req.Mode)
	}
	if req.Content == "" {
		t.Error("Content should be non-empty by default")
	}
}

func TestBuildRetainRequest_Opts(t *testing.T) {
	t.Parallel()
	req := BuildRetainRequest(
		WithRetainWorkspace("ws_other"),
		WithRetainAgent("agent_other"),
		WithRetainTier("daily"),
		WithRetainKey("2026-05-23"),
		WithRetainContent("custom body"),
		WithRetainMode("append"),
	)
	if req.WorkspaceID != "ws_other" {
		t.Errorf("WorkspaceID override lost: %q", req.WorkspaceID)
	}
	if req.AgentID != "agent_other" {
		t.Errorf("AgentID override lost: %q", req.AgentID)
	}
	if req.Tier != "daily" || req.Key != "2026-05-23" {
		t.Errorf("Tier/Key drift: tier=%q key=%q", req.Tier, req.Key)
	}
	if req.Mode != "append" {
		t.Errorf("Mode override lost: %q", req.Mode)
	}
	if req.Content != "custom body" {
		t.Errorf("Content override lost: %q", req.Content)
	}
}

func TestBuildSnippets_Count(t *testing.T) {
	t.Parallel()
	got := BuildSnippets(5)
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
	for i, s := range got {
		if s.Source == "" {
			t.Errorf("snippet[%d] missing source", i)
		}
		if s.Score != 1.0 {
			t.Errorf("snippet[%d] score = %v, want 1.0", i, s.Score)
		}
	}
}

// TestMockProvider_DefaultSuccess confirms zero-value mock returns
// non-error results from every method — a test that hasn't configured
// failures should see clean success.
func TestMockProvider_DefaultSuccess(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	ctx := context.Background()

	r1, err := m.Retain(ctx, BuildRetainRequest())
	if err != nil || r1.ID == "" {
		t.Errorf("default Retain: err=%v id=%q", err, r1.ID)
	}

	r2, err := m.Recall(ctx, BuildRecallRequest())
	if err != nil {
		t.Errorf("default Recall: err=%v", err)
	}
	if r2.Hits == nil {
		t.Error("default Recall returned nil Hits — should be empty slice")
	}

	r3, err := m.Forget(ctx, memory.ForgetRequest{ID: "x"})
	if err != nil || r3.Removed != 0 {
		t.Errorf("default Forget: err=%v removed=%d", err, r3.Removed)
	}

	h := m.Health(ctx)
	if !h.OK {
		t.Error("default Health should be OK=true")
	}
}

func TestMockProvider_ErrorOverride(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	sentinel := errors.New("upstream wedged")
	m.SetRetainError(sentinel)
	m.SetRecallError(sentinel)
	m.SetForgetError(sentinel)

	if _, err := m.Retain(context.Background(), BuildRetainRequest()); !errors.Is(err, sentinel) {
		t.Errorf("Retain err = %v, want sentinel", err)
	}
	if _, err := m.Recall(context.Background(), BuildRecallRequest()); !errors.Is(err, sentinel) {
		t.Errorf("Recall err = %v, want sentinel", err)
	}
	if _, err := m.Forget(context.Background(), memory.ForgetRequest{}); !errors.Is(err, sentinel) {
		t.Errorf("Forget err = %v, want sentinel", err)
	}
}

func TestMockProvider_DelayHonoursContext(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	m.SetRecallDelay(2 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := m.Recall(ctx, BuildRecallRequest())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected ctx.Err(), got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
	if elapsed >= time.Second {
		t.Errorf("Recall waited %v — should have returned at deadline", elapsed)
	}
}

func TestMockProvider_CallRecording(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, _ = m.Retain(ctx, BuildRetainRequest(WithRetainKey(string(rune('a'+i)))))
	}
	_, _ = m.Recall(ctx, BuildRecallRequest())
	_ = m.Health(ctx)
	_ = m.Health(ctx)

	if got := len(m.RetainCalls()); got != 3 {
		t.Errorf("RetainCalls len = %d, want 3", got)
	}
	if got := len(m.RecallCalls()); got != 1 {
		t.Errorf("RecallCalls len = %d, want 1", got)
	}
	if got := m.HealthCalls(); got != 2 {
		t.Errorf("HealthCalls = %d, want 2", got)
	}
}

// TestMockProvider_ConcurrentSafe exercises the mutex protecting the
// call recorder. Without locking, the race detector flags this.
func TestMockProvider_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	ctx := context.Background()

	var wg sync.WaitGroup
	const n = 20
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Retain(ctx, BuildRetainRequest())
		}()
	}
	wg.Wait()

	if got := len(m.RetainCalls()); got != n {
		t.Errorf("RetainCalls len = %d, want %d (lost call?)", got, n)
	}
}

func TestMockProvider_Reset(t *testing.T) {
	t.Parallel()
	m := NewMockProvider()
	m.SetRetainError(errors.New("x"))
	_, _ = m.Retain(context.Background(), BuildRetainRequest())
	m.Reset()

	if _, err := m.Retain(context.Background(), BuildRetainRequest()); err != nil {
		t.Errorf("post-Reset Retain err = %v, want nil (default success)", err)
	}
	if got := len(m.RetainCalls()); got != 1 {
		t.Errorf("post-Reset RetainCalls len = %d, want 1 (one call AFTER reset)", got)
	}
}

func TestEdgeCases_CatalogShape(t *testing.T) {
	t.Parallel()
	if len(EdgeCases) == 0 {
		t.Fatal("EdgeCases catalog is empty")
	}
	seen := make(map[string]bool)
	for i, ec := range EdgeCases {
		if ec.Slug == "" {
			t.Errorf("EdgeCases[%d] missing slug", i)
		}
		if seen[ec.Slug] {
			t.Errorf("EdgeCases[%d] duplicate slug %q", i, ec.Slug)
		}
		seen[ec.Slug] = true
		if !strings.HasPrefix(ec.Slug, "edge_case_") {
			t.Errorf("EdgeCases[%d] slug %q must use edge_case_ prefix for grep-ability", i, ec.Slug)
		}
		if ec.Description == "" {
			t.Errorf("EdgeCases[%d] %q missing description", i, ec.Slug)
		}
		if ec.Recipe == "" {
			t.Errorf("EdgeCases[%d] %q missing recipe", i, ec.Slug)
		}
	}
}

func TestEdgeCaseBySlug_Roundtrip(t *testing.T) {
	t.Parallel()
	for _, slug := range EdgeCaseSlugs() {
		ec := EdgeCaseBySlug(slug)
		if ec == nil {
			t.Errorf("EdgeCaseBySlug(%q) = nil, but slug came from EdgeCaseSlugs", slug)
			continue
		}
		if ec.Slug != slug {
			t.Errorf("EdgeCaseBySlug(%q).Slug = %q, want %q", slug, ec.Slug, slug)
		}
	}
	if got := EdgeCaseBySlug("nonexistent"); got != nil {
		t.Errorf("EdgeCaseBySlug(\"nonexistent\") = %+v, want nil", got)
	}
}

func TestDescribeEdgeCases_ListsEverything(t *testing.T) {
	t.Parallel()
	got := DescribeEdgeCases()
	for _, ec := range EdgeCases {
		if !strings.Contains(got, ec.Slug) {
			t.Errorf("DescribeEdgeCases missing slug %q", ec.Slug)
		}
	}
}
