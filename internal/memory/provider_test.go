package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalDispatcher_Retain_PersistsToDisk asserts that a Retain
// through the Provider interface writes the same bytes the legacy
// dispatcher would have written via memory.write. The on-disk file
// is the contract: any provider implementation MUST leave the
// content readable by the existing dispatcher (so a future operator
// `crewship memory cat AGENT.md` still works after a Mem0 round
// trip — well, modulo that Mem0 wouldn't write to disk; but the
// LocalDispatcher must.)
func TestLocalDispatcher_Retain_PersistsToDisk(t *testing.T) {
	ac := testAgentCtx(t)
	p := NewLocalDispatcher(ac)

	res, err := p.Retain(context.Background(), RetainRequest{
		WorkspaceID: ac.WorkspaceID,
		AgentID:     ac.AgentID,
		Tier:        "AGENT",
		Content:     "first fact\n",
		Mode:        "replace",
	})
	if err != nil {
		t.Fatalf("retain: %v", err)
	}
	if res.ID != "AGENT.md" {
		t.Errorf("expected ID=AGENT.md, got %q", res.ID)
	}

	data, err := os.ReadFile(filepath.Join(ac.AgentMemoryDir, "AGENT.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "first fact\n" {
		t.Errorf("file contents wrong: %q", string(data))
	}
}

// TestLocalDispatcher_Recall_DelegatesToHandleSearch verifies that a
// Recall hits the same substring path memory.search uses today.
// Confirms the envelope decode survives the round-trip — a future
// Provider impl must produce the same RecallResult shape.
func TestLocalDispatcher_Recall_DelegatesToHandleSearch(t *testing.T) {
	ac := testAgentCtx(t)
	if err := os.WriteFile(filepath.Join(ac.AgentMemoryDir, "AGENT.md"),
		[]byte("alpha beta gamma\nsecond line with needle here\nthird line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewLocalDispatcher(ac)
	out, err := p.Recall(context.Background(), RecallRequest{
		WorkspaceID: ac.WorkspaceID,
		Query:       "needle",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(out.Hits) == 0 {
		t.Fatalf("expected at least one hit for 'needle'")
	}
	found := false
	for _, h := range out.Hits {
		if strings.Contains(h.Snippet, "needle") {
			found = true
			if h.Source != "AGENT.md" {
				t.Errorf("expected source=AGENT.md, got %q", h.Source)
			}
		}
	}
	if !found {
		t.Errorf("expected at least one hit with 'needle' in snippet; got %+v", out.Hits)
	}
}

// TestLocalDispatcher_Forget_RemovesFile asserts a per-ID delete
// removes the underlying file. The follow-up read returns empty
// (matching the dispatcher's missing-file-is-not-an-error contract)
// rather than failing — a downstream call site must be able to
// distinguish "deleted" from "errored" by reading nothing.
func TestLocalDispatcher_Forget_RemovesFile(t *testing.T) {
	ac := testAgentCtx(t)
	target := filepath.Join(ac.AgentMemoryDir, "AGENT.md")
	if err := os.WriteFile(target, []byte("doomed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewLocalDispatcher(ac)
	out, err := p.Forget(context.Background(), ForgetRequest{
		WorkspaceID: ac.WorkspaceID,
		ID:          "AGENT.md",
	})
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if out.Removed != 1 {
		t.Errorf("expected Removed=1, got %d", out.Removed)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("expected file removed, stat err=%v", err)
	}

	// Re-forget the same ID — must be a no-op (Removed=0), not an
	// error. Operators replaying a journal of deletes can't be
	// punished for idempotency.
	out2, err := p.Forget(context.Background(), ForgetRequest{
		WorkspaceID: ac.WorkspaceID,
		ID:          "AGENT.md",
	})
	if err != nil {
		t.Fatalf("forget second pass: %v", err)
	}
	if out2.Removed != 0 {
		t.Errorf("re-forget should be no-op, got Removed=%d", out2.Removed)
	}
}

// TestLocalDispatcher_Forget_RejectsCascadeSelector — the local
// impl does NOT do GDPR cascade (PR-F1 owns it at the API layer).
// Asking for a DataSubjectID-only delete must fail loudly so the
// caller knows to use the cascade endpoint instead.
func TestLocalDispatcher_Forget_RejectsCascadeSelector(t *testing.T) {
	ac := testAgentCtx(t)
	p := NewLocalDispatcher(ac)
	_, err := p.Forget(context.Background(), ForgetRequest{
		WorkspaceID:   ac.WorkspaceID,
		DataSubjectID: "user_42",
	})
	if err == nil {
		t.Fatal("expected error for cascade-only selector, got nil")
	}
	if !strings.Contains(err.Error(), "cascade") {
		t.Errorf("error message should mention cascade, got %q", err.Error())
	}
}

// TestLocalDispatcher_Health_ReturnsOKWhenDirWritable covers the
// happy-path liveness probe. A freshly-created AgentMemoryDir is
// always writable; the probe must succeed without leaving debris.
func TestLocalDispatcher_Health_ReturnsOKWhenDirWritable(t *testing.T) {
	ac := testAgentCtx(t)
	p := NewLocalDispatcher(ac)
	st := p.Health(context.Background())
	if !st.OK {
		t.Fatalf("expected OK=true on freshly-created dirs, got OK=false msg=%q", st.Message)
	}
	if st.CheckedAt.IsZero() {
		t.Errorf("expected non-zero CheckedAt")
	}
	// Probe file must NOT linger — operators should not see
	// ".crewship-health-probe" in their memory dir after a probe.
	probe := filepath.Join(ac.AgentMemoryDir, ".crewship-health-probe")
	if _, err := os.Stat(probe); err == nil {
		t.Errorf("probe file should be cleaned up, but %s still exists", probe)
	}
}

// TestLocalDispatcher_Health_DetectsUnwritableDir asserts the
// negative path. We can't reliably chmod 0500 inside CI's sandbox,
// so the easiest reproducible failure is pointing at a non-existent
// dir — same observable signal to the operator ("backend down").
func TestLocalDispatcher_Health_DetectsUnwritableDir(t *testing.T) {
	ac := AgentContext{
		AgentID:        "agent-1",
		WorkspaceID:    "ws-1",
		AgentMemoryDir: filepath.Join(t.TempDir(), "does-not-exist"),
	}
	p := NewLocalDispatcher(ac)
	st := p.Health(context.Background())
	if st.OK {
		t.Fatalf("expected OK=false for non-existent dir, got OK=true")
	}
	if st.Message == "" {
		t.Errorf("expected explanatory Message on degraded health")
	}
}

// TestSplitTierLabel covers the round-trip used by Forget. Each
// label produced by tierSourceLabel must split cleanly back into
// (tier, key); otherwise Forget can't resolve a Retain'd ID to its
// path.
func TestSplitTierLabel(t *testing.T) {
	cases := []struct {
		label    string
		wantTier string
		wantKey  string
	}{
		{"AGENT.md", "AGENT", ""},
		{"CREW.md", "CREW", ""},
		{"PERSONA.md", "PERSONA", ""},
		{"pins.md", "pins", ""},
		{"lessons.md", "lessons", ""},
		{"daily/2026-05-21.md", "daily", "2026-05-21"},
		{"peers/jane.md", "peers", "jane"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			gotTier, gotKey := splitTierLabel(c.label)
			if gotTier != c.wantTier || gotKey != c.wantKey {
				t.Errorf("splitTierLabel(%q) = (%q, %q); want (%q, %q)",
					c.label, gotTier, gotKey, c.wantTier, c.wantKey)
			}
		})
	}
}
