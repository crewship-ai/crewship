package server

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// TestServer_OrchestratorWorkspaceProvider_NilRegistry_ReturnsInterfaceNil
// guards the typed-nil-interface gotcha the adapter exists to defuse: a
// nil registry must surface as an interface-nil orchestrator
// .WorkspaceMemoryReader, not a non-nil interface wrapping a nil pointer.
// orchestrator.buildWorkspaceMemoryBlock does a plain `reader == nil`
// check and would skip-then-panic on a wrapped nil.
func TestServer_OrchestratorWorkspaceProvider_NilRegistry_ReturnsInterfaceNil(t *testing.T) {
	t.Parallel()

	a := orchestratorWorkspaceProvider{reg: nil}
	var got orchestrator.WorkspaceMemoryReader = a.For("ws_anything")
	if got != nil {
		t.Errorf("nil registry must yield interface-nil, got %#v", got)
	}
	// Belt-and-braces: reflect-level check that the interface header is
	// fully zero, not just `== nil`-comparing as nil. A typed-nil-
	// interface (e.g. (*memory.WorkspaceMemory)(nil) returned as an
	// orchestrator.WorkspaceMemoryReader) is `!= nil` even though
	// IsNil() reports true.
	if v := reflect.ValueOf(&got).Elem(); !v.IsNil() {
		t.Errorf("interface header should be fully zero, got kind=%v", v.Kind())
	}
}

// TestServer_OrchestratorWorkspaceProvider_MissingWorkspace_ReturnsNil
// covers the second nil path: a real registry that has nothing cached
// for the queried workspace ID must still hand back interface-nil.
// memory.WorkspaceMemoryRegistry.For rejects malformed IDs and returns
// nil for those — we lean on that to exercise the missing-workspace
// branch without standing up a real on-disk FTS5 index.
func TestServer_OrchestratorWorkspaceProvider_MissingWorkspace_ReturnsNil(t *testing.T) {
	t.Parallel()

	reg := memory.NewWorkspaceMemoryRegistry(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	a := orchestratorWorkspaceProvider{reg: reg}

	// Malformed workspace ID is rejected by the registry and returns nil
	// *WorkspaceMemory; the adapter must NOT wrap that into a non-nil
	// interface.
	var got orchestrator.WorkspaceMemoryReader = a.For("../escape")
	if got != nil {
		t.Errorf("missing workspace must yield interface-nil, got %#v", got)
	}
}

// TestServer_OrchestratorWorkspaceProvider_ValidWorkspace_ReturnsReader
// rounds out the happy path: when the registry resolves a real
// *memory.WorkspaceMemory, the adapter must return it as a non-nil
// WorkspaceMemoryReader so orchestrator.buildWorkspaceMemoryBlock can
// call GetContext on it. We pick a benign cuid-shaped ID that survives
// the registry's path-injection guard.
func TestServer_OrchestratorWorkspaceProvider_ValidWorkspace_ReturnsReader(t *testing.T) {
	t.Parallel()

	reg := memory.NewWorkspaceMemoryRegistry(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = reg.Close() })

	a := orchestratorWorkspaceProvider{reg: reg}
	reader := a.For("ws_abcdefghijklmnop")
	if reader == nil {
		t.Fatal("expected non-nil reader for a legitimate workspace id")
	}
	// Sanity: the reader's GetContext must be callable with a budget of
	// zero and return ("", 0) on an empty workspace dir. Anything else
	// means the adapter handed back the wrong type.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, used := reader.GetContext(ctx, 0)
	if body != "" || used != 0 {
		t.Errorf("empty workspace should yield (\"\", 0), got (%q, %d)", body, used)
	}
}

// TestServer_APIWorkspaceProvider_NilRegistry_ReturnsInterfaceNil mirrors
// the orchestrator-side guard for the api package's narrower
// WorkspaceEngineHolder interface. Two adapters, same trap.
func TestServer_APIWorkspaceProvider_NilRegistry_ReturnsInterfaceNil(t *testing.T) {
	t.Parallel()

	a := apiWorkspaceProvider{reg: nil}
	if got := a.For("ws_anything"); got != nil {
		t.Errorf("nil registry must yield interface-nil, got %#v", got)
	}
}

// TestServer_APIWorkspaceProvider_MissingWorkspace_ReturnsNil covers the
// "registry present, workspace absent" branch on the api side.
func TestServer_APIWorkspaceProvider_MissingWorkspace_ReturnsNil(t *testing.T) {
	t.Parallel()

	reg := memory.NewWorkspaceMemoryRegistry(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	a := apiWorkspaceProvider{reg: reg}
	if got := a.For("../escape"); got != nil {
		t.Errorf("missing workspace must yield interface-nil, got %#v", got)
	}
}

// TestServer_APIWorkspaceProvider_ValidWorkspace_ReturnsHolder confirms
// the engine accessor wires through: the holder must expose a non-nil
// *memory.Engine so the hybrid-search handler can run BM25 against it.
func TestServer_APIWorkspaceProvider_ValidWorkspace_ReturnsHolder(t *testing.T) {
	t.Parallel()

	reg := memory.NewWorkspaceMemoryRegistry(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = reg.Close() })

	a := apiWorkspaceProvider{reg: reg}
	holder := a.For("ws_zyxwvutsrqponmlk")
	if holder == nil {
		t.Fatal("expected non-nil engine holder for a legitimate workspace id")
	}
	if holder.Engine() == nil {
		t.Error("engine accessor must yield a non-nil engine")
	}
}

// TestServer_ApprovalGateAdapter_ModeMapping_RoundTrip locks the string→
// harbormaster.Mode translation: orchestrator hands us "async" / "sync" /
// (anything else); the adapter must hand harbormaster the matching const.
// We assert end-to-end via the Decision shape: with a non-matching tool
// the gate short-circuits to {NotGated:true, Approved:true} regardless
// of mode, so a clean round-trip ApprovalDecision{Required:false,
// Approved:true} proves the mapping reached Gate without exploding on
// an unknown Mode.
func TestServer_ApprovalGateAdapter_ModeMapping_RoundTrip(t *testing.T) {
	t.Parallel()

	adapter := newApprovalGateAdapter(nil, nil)
	cases := []struct {
		name string
		mode string
	}{
		{"empty defaults to none", ""},
		{"explicit none", "none"},
		{"async maps to ModeAsync", "async"},
		{"sync maps to ModeSync", "sync"},
		{"unknown defaults to none", "garbage-value"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dec, err := adapter.Check(context.Background(), orchestrator.ApprovalCheckInput{
				WorkspaceID: "ws_round_trip",
				CrewID:      "crew_x",
				AgentID:     "agent_x",
				Tool:        "echo",
				Args:        map[string]any{"msg": "hi"},
				Mode:        tc.mode,
				UserID:      "user_x",
			})
			if err != nil {
				t.Fatalf("Check returned err: %v", err)
			}
			if dec.Required {
				t.Errorf("benign tool must NOT be Required (NotGated path), got %+v", dec)
			}
			if !dec.Approved {
				t.Errorf("benign tool must be Approved when NotGated, got %+v", dec)
			}
			if dec.Denied || dec.Pending {
				t.Errorf("benign tool must not be Denied/Pending, got %+v", dec)
			}
		})
	}
}

// TestServer_EpisodicRecallAdapter_NilEmbedder_ReturnsEmpty locks the
// documented graceful-degrade contract: a nil embedder means Ollama is
// unreachable, and Recall must silently return ("", nil) so the
// orchestrator drops the [EPISODIC RECALL] block without failing the
// run.
func TestServer_EpisodicRecallAdapter_NilEmbedder_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	adapter := newEpisodicRecallAdapter(nil, nil)
	got, err := adapter.Recall(context.Background(), orchestrator.EpisodicRecallInput{
		WorkspaceID: "ws_x",
		CrewID:      "crew_x",
		AgentID:     "agent_x",
		Role:        "AGENT",
		Query:       "anything",
		MaxChars:    1000,
	})
	if err != nil {
		t.Errorf("nil embedder must produce nil error, got %v", err)
	}
	if got != "" {
		t.Errorf("nil embedder must produce empty recall, got %q", got)
	}
}

// TestServer_PresenceAdapter_InvalidStatus_LogsAndSwallows pins the
// "best-effort presence" contract: an upstream DB error (here forced by
// passing an invalid status that fails presence.Snapshot.Validate before
// any DB call) must be logged and SWALLOWED — Track always returns nil
// so a roster failure can't abort an agent run.
func TestServer_PresenceAdapter_InvalidStatus_LogsAndSwallows(t *testing.T) {
	t.Parallel()

	logger, buf := jsonLogBuf()
	// nil DB + nil journal: the invalid status fails Validate inside
	// presence.Upsert before either is dereferenced, so we exercise the
	// error path without standing up a real schema.
	adapter := newPresenceAdapter((*sql.DB)(nil), nil, logger)

	err := adapter.Track(context.Background(), orchestrator.PresenceInput{
		WorkspaceID: "ws_x",
		CrewID:      "crew_x",
		AgentID:     "agent_x",
		Status:      "not-a-real-status",
	})
	if err != nil {
		t.Errorf("Track must swallow upstream errors, got %v", err)
	}
	if !strings.Contains(buf.String(), "presence track failed") {
		t.Errorf("expected warning log for failed track, got: %s", buf.String())
	}
}

// TestServer_PresenceAdapter_NilLogger_DoesNotPanic guards against a
// regression where the logger nil-check in the error branch is dropped:
// the adapter's constructor accepts a nil logger and the error path
// must not panic on Warn-of-nil.
func TestServer_PresenceAdapter_NilLogger_DoesNotPanic(t *testing.T) {
	t.Parallel()

	adapter := newPresenceAdapter((*sql.DB)(nil), nil, nil)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Track with nil logger panicked: %v", r)
		}
	}()
	err := adapter.Track(context.Background(), orchestrator.PresenceInput{
		WorkspaceID: "ws_x",
		AgentID:     "agent_x",
		Status:      "not-a-real-status",
	})
	if err != nil {
		t.Errorf("Track must swallow upstream errors, got %v", err)
	}
}
