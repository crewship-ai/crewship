package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newLocalDispatcherForTest(t *testing.T) (*LocalDispatcher, AgentContext) {
	t.Helper()
	ac := testAgentCtx(t)
	return NewLocalDispatcher(ac), ac
}

// ── nil / uninitialized receivers ────────────────────────────────────

func TestLocalDispatcher_UninitializedReceivers(t *testing.T) {
	for name, l := range map[string]*LocalDispatcher{
		"nil pointer":    nil,
		"zero value &{}": {},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := l.Retain(ctx, RetainRequest{Tier: "AGENT", Content: "x", Mode: "replace"}); err == nil || !strings.Contains(err.Error(), "not initialized") {
				t.Errorf("Retain err = %v, want not-initialized", err)
			}
			if _, err := l.Recall(ctx, RecallRequest{Query: "x"}); err == nil || !strings.Contains(err.Error(), "not initialized") {
				t.Errorf("Recall err = %v, want not-initialized", err)
			}
			if _, err := l.Forget(ctx, ForgetRequest{ID: "AGENT.md"}); err == nil || !strings.Contains(err.Error(), "not initialized") {
				t.Errorf("Forget err = %v, want not-initialized", err)
			}
			hs := l.Health(ctx)
			if hs.OK || hs.Message != "not initialized" {
				t.Errorf("Health = %+v, want not-initialized", hs)
			}
			if hs.CheckedAt.IsZero() {
				t.Errorf("Health.CheckedAt must be set even on failure")
			}
		})
	}
}

// ── validateScope ────────────────────────────────────────────────────

func TestLocalDispatcher_ValidateScope_Mismatches(t *testing.T) {
	l, _ := newLocalDispatcherForTest(t) // bound to ws-1 / agent-1 / crew-1
	cases := []struct {
		name            string
		ws, agent, crew string
		wantErrContains string
	}{
		{name: "workspace mismatch", ws: "ws-other", wantErrContains: "workspace mismatch"},
		{name: "agent mismatch", agent: "agent-other", wantErrContains: "agent mismatch"},
		{name: "crew mismatch", crew: "crew-other", wantErrContains: "crew mismatch"},
		{name: "all empty is no-override", wantErrContains: ""},
		{name: "exact match passes", ws: "ws-1", agent: "agent-1", crew: "crew-1", wantErrContains: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := l.validateScope(tc.ws, tc.agent, tc.crew)
			if tc.wantErrContains == "" {
				if err != nil {
					t.Errorf("validateScope: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("validateScope err = %v, want containing %q", err, tc.wantErrContains)
			}
		})
	}
}

func TestLocalDispatcher_ScopeMismatch_BlocksAllVerbs(t *testing.T) {
	l, ac := newLocalDispatcherForTest(t)
	if _, err := l.Retain(context.Background(), RetainRequest{WorkspaceID: "ws-evil", Tier: "AGENT", Content: "x", Mode: "replace"}); err == nil || !strings.Contains(err.Error(), "workspace mismatch") {
		t.Errorf("Retain scope err = %v", err)
	}
	if _, err := l.Recall(context.Background(), RecallRequest{AgentID: "agent-evil", Query: "x"}); err == nil || !strings.Contains(err.Error(), "agent mismatch") {
		t.Errorf("Recall scope err = %v", err)
	}
	if _, err := l.Forget(context.Background(), ForgetRequest{WorkspaceID: "ws-evil", ID: "AGENT.md"}); err == nil || !strings.Contains(err.Error(), "workspace mismatch") {
		t.Errorf("Forget scope err = %v", err)
	}
	// Nothing was written by the rejected Retain.
	if _, err := os.Stat(filepath.Join(ac.AgentMemoryDir, "AGENT.md")); !os.IsNotExist(err) {
		t.Errorf("scope-rejected Retain must not write AGENT.md")
	}
}

// ── Retain / Recall dispatcher-error propagation ─────────────────────

func TestLocalDispatcher_Retain_DispatcherIsError_Surfaced(t *testing.T) {
	l, _ := newLocalDispatcherForTest(t)
	_, err := l.Retain(context.Background(), RetainRequest{Tier: "lessons", Content: "x", Mode: "replace"})
	if err == nil || !strings.Contains(err.Error(), "retain:") || !strings.Contains(err.Error(), "lessons tier is read-only") {
		t.Fatalf("Retain lessons err = %v, want lessons read-only rejection", err)
	}
}

func TestLocalDispatcher_Recall_DispatcherIsError_Surfaced(t *testing.T) {
	l, _ := newLocalDispatcherForTest(t)
	_, err := l.Recall(context.Background(), RecallRequest{Query: "   "})
	if err == nil || !strings.Contains(err.Error(), "recall:") || !strings.Contains(err.Error(), "q is required") {
		t.Fatalf("Recall blank-q err = %v", err)
	}
}

func TestLocalDispatcher_Recall_SurfacesQuarantinedSources(t *testing.T) {
	l, ac := newLocalDispatcherForTest(t)
	if err := os.WriteFile(filepath.Join(ac.AgentMemoryDir, "AGENT.md"), []byte(poisonBody), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := l.Recall(context.Background(), RecallRequest{Query: "exfiltrate"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(res.Hits) != 0 {
		t.Errorf("poisoned file contributed %d hits", len(res.Hits))
	}
	if len(res.Quarantined) != 1 || res.Quarantined[0] != "AGENT.md" {
		t.Errorf("Quarantined = %v, want [AGENT.md]", res.Quarantined)
	}
}

// ── Forget selector + path branches ──────────────────────────────────

func TestLocalDispatcher_Forget_SelectorContract(t *testing.T) {
	l, _ := newLocalDispatcherForTest(t)
	cases := []struct {
		name string
		req  ForgetRequest
		want string
	}{
		{name: "neither selector", req: ForgetRequest{}, want: "exactly one of ID or DataSubjectID"},
		{name: "both selectors", req: ForgetRequest{ID: "AGENT.md", DataSubjectID: "u1"}, want: "not both"},
		{name: "cascade unimplemented", req: ForgetRequest{DataSubjectID: "u1"}, want: "cascade not implemented"},
		{name: "unknown label shape", req: ForgetRequest{ID: "what/is/this"}, want: "forget: resolve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := l.Forget(context.Background(), tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Forget(%+v) err = %v, want containing %q", tc.req, err, tc.want)
			}
		})
	}
}

func TestLocalDispatcher_Forget_MissingFile_RemovedZero(t *testing.T) {
	l, _ := newLocalDispatcherForTest(t)
	res, err := l.Forget(context.Background(), ForgetRequest{ID: "pins.md"})
	if err != nil {
		t.Fatalf("Forget on missing file must be a no-op: %v", err)
	}
	if res.Removed != 0 {
		t.Errorf("Removed = %d, want 0", res.Removed)
	}
}

func TestLocalDispatcher_Forget_SymlinkRefused(t *testing.T) {
	l, ac := newLocalDispatcherForTest(t)
	target := filepath.Join(t.TempDir(), "host.md")
	if err := os.WriteFile(target, []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(ac.AgentMemoryDir, "pins.md")); err != nil {
		t.Fatal(err)
	}
	_, err := l.Forget(context.Background(), ForgetRequest{ID: "pins.md"})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Forget symlink err = %v, want symlink refusal", err)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Errorf("symlink target deleted: %v", statErr)
	}
}

func TestLocalDispatcher_Forget_CancelledContext(t *testing.T) {
	l, ac := newLocalDispatcherForTest(t)
	if err := os.WriteFile(filepath.Join(ac.AgentMemoryDir, "AGENT.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := l.Forget(ctx, ForgetRequest{ID: "AGENT.md"})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("Forget cancelled err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(ac.AgentMemoryDir, "AGENT.md")); statErr != nil {
		t.Errorf("file removed despite cancellation: %v", statErr)
	}
}

func TestLocalDispatcher_Forget_RemoveFailure_Surfaced(t *testing.T) {
	l, ac := newLocalDispatcherForTest(t)
	peers := filepath.Join(ac.AgentMemoryDir, "peers")
	if err := os.MkdirAll(peers, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peers, "user-1.md"), []byte("card"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(peers, 0o555); err != nil { // unlink needs write on parent
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(peers, 0o755) })
	_, err := l.Forget(context.Background(), ForgetRequest{ID: "peers/user-1.md"})
	if err == nil || !strings.Contains(err.Error(), "forget: remove") {
		t.Fatalf("Forget EACCES err = %v, want remove error", err)
	}
}

// ── Health degraded branches ─────────────────────────────────────────

func TestLocalDispatcher_Health_DegradedBranches(t *testing.T) {
	t.Run("cancelled context", func(t *testing.T) {
		l, _ := newLocalDispatcherForTest(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		hs := l.Health(ctx)
		if hs.OK || !strings.Contains(hs.Message, "context cancelled") {
			t.Errorf("Health = %+v, want context-cancelled", hs)
		}
	})
	t.Run("agent dir unset", func(t *testing.T) {
		l := NewLocalDispatcher(AgentContext{})
		hs := l.Health(context.Background())
		if hs.OK || !strings.Contains(hs.Message, "agent memory dir unset") {
			t.Errorf("Health = %+v, want dir-unset", hs)
		}
	})
	t.Run("agent dir missing", func(t *testing.T) {
		l := NewLocalDispatcher(AgentContext{AgentMemoryDir: filepath.Join(t.TempDir(), "ghost")})
		hs := l.Health(context.Background())
		if hs.OK || !strings.Contains(hs.Message, "stat") {
			t.Errorf("Health = %+v, want stat error", hs)
		}
	})
	t.Run("agent dir is a file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "flat")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		l := NewLocalDispatcher(AgentContext{AgentMemoryDir: file})
		hs := l.Health(context.Background())
		if hs.OK || !strings.Contains(hs.Message, "not a directory") {
			t.Errorf("Health = %+v, want not-a-directory", hs)
		}
	})
	t.Run("solo agent without crew dir is OK", func(t *testing.T) {
		dir := t.TempDir()
		l := NewLocalDispatcher(AgentContext{AgentMemoryDir: dir}) // CrewMemoryDir empty → skipped
		hs := l.Health(context.Background())
		if !hs.OK || hs.Message != "" {
			t.Errorf("Health = %+v, want OK for solo agent", hs)
		}
	})
	t.Run("crew dir unwritable", func(t *testing.T) {
		ac := testAgentCtx(t)
		if err := os.Chmod(ac.CrewMemoryDir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(ac.CrewMemoryDir, 0o755) })
		l := NewLocalDispatcher(ac)
		hs := l.Health(context.Background())
		if hs.OK || !strings.Contains(hs.Message, "probe write") {
			t.Errorf("Health = %+v, want probe-write failure", hs)
		}
	})
}

// ── splitTierLabel fallback ──────────────────────────────────────────

func TestSplitTierLabel_UnknownShape_EmptyPair(t *testing.T) {
	for _, label := range []string{"", "mystery.md", "daily/nested/too-deep", "peers.md/x"} {
		tier, key := splitTierLabel(label)
		if tier != "" || key != "" {
			t.Errorf("splitTierLabel(%q) = (%q,%q), want empty pair", label, tier, key)
		}
	}
}
