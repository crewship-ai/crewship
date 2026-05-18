package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// cli_adapter.go — unknownAdapter safety-net contract.
//
// unknownAdapter is the fallback getAdapter returns for any CLI string
// it does not recognise. The source contract is explicit: "a malformed
// agent record cannot crash the orchestrator." That promise rests on
// every method being a safe no-op. These tests pin each method so a
// regression that introduced a panic or non-nil error would surface
// here before the production blast-radius of a 500 mid-run.
// ---------------------------------------------------------------------------

func TestUnknownAdapter_NameIsEmptyString(t *testing.T) {
	// Empty name is the registry sentinel — getAdapter never matches
	// against "" so unknownAdapter cannot be retrieved by name lookup;
	// it's only ever returned as the fallback. Pin that invariant.
	if got := (unknownAdapter{}).Name(); got != "" {
		t.Errorf("Name() = %q, want \"\" (registry-untargetable sentinel)", got)
	}
	if a, ok := adapterRegistry[""]; ok {
		t.Errorf("adapterRegistry[\"\"] = %v; the empty-name sentinel must not appear in the registry", a)
	}
}

func TestUnknownAdapter_BuildCommand_EmitsBareClaudePrint(t *testing.T) {
	// Source comment: "produces a minimal `claude --print <msg>` command"
	// — enough to be runnable for debugging, not enough to be useful in
	// production. Pin that exact shape; a regression that emits a
	// different binary name (or no name) would break the failover
	// debug path.
	req := AgentRunRequest{UserMessage: "diagnostic ping"}
	got := (unknownAdapter{}).BuildCommand(req)
	if len(got) != 3 {
		t.Fatalf("argv len = %d, want 3 (binary + flag + msg)", len(got))
	}
	if got[0] != "claude" {
		t.Errorf("argv[0] = %q, want \"claude\"", got[0])
	}
	if got[1] != "--print" {
		t.Errorf("argv[1] = %q, want \"--print\"", got[1])
	}
	if got[2] != "diagnostic ping" {
		t.Errorf("argv[2] = %q, want \"diagnostic ping\"", got[2])
	}
}

func TestUnknownAdapter_BuildCommand_EmptyMessage(t *testing.T) {
	// An empty UserMessage must still produce a valid 3-element argv —
	// callers exec on it directly, a short slice would index-out-of-bounds.
	got := (unknownAdapter{}).BuildCommand(AgentRunRequest{})
	if len(got) != 3 {
		t.Fatalf("argv len with empty message = %d, want 3", len(got))
	}
	if got[2] != "" {
		t.Errorf("argv[2] = %q, want \"\" for empty UserMessage", got[2])
	}
}

func TestUnknownAdapter_UseStreamJSON_False(t *testing.T) {
	// unknownAdapter never produces structured output; streamOutput
	// reads this to decide whether to call ParseStreamLine — a true
	// here would route bare stdout into a never-implemented parser.
	if (unknownAdapter{}).UseStreamJSON() {
		t.Error("UseStreamJSON() = true, want false")
	}
}

func TestUnknownAdapter_ParseStreamLine_NoOp_NeverCallsHandler(t *testing.T) {
	// ParseStreamLine is documented as a no-op for unknownAdapter
	// (UseStreamJSON is false anyway, but defense in depth). Verify
	// it doesn't panic on a variety of inputs AND doesn't invoke the
	// handler — a regression that started emitting events would race
	// with the empty event stream consumers expect.
	called := false
	handler := func(_ AgentEvent) { called = true }

	for _, in := range [][]byte{
		nil,
		{},
		[]byte("plain text"),
		[]byte(`{"type":"text","content":"hi"}`),
		[]byte(strings.Repeat("x", 4096)),
	} {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("ParseStreamLine panicked on input %q: %v", in, r)
			}
		}()
		(unknownAdapter{}).ParseStreamLine(in, handler)
	}
	if called {
		t.Error("handler was invoked; ParseStreamLine is documented as a no-op")
	}
}

func TestUnknownAdapter_ParseStreamLine_NilHandler_DoesNotPanic(t *testing.T) {
	// Documented no-op must accept a nil handler — a caller mistake
	// must not crash the orchestrator.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParseStreamLine(nil handler) panicked: %v", r)
		}
	}()
	(unknownAdapter{}).ParseStreamLine([]byte("anything"), nil)
}

func TestUnknownAdapter_SetupSystemPrompt_NilReturn(t *testing.T) {
	// Source: "CLIs that take the system prompt via a command-line flag
	// (Claude Code, Gemini) return nil here." unknownAdapter inherits
	// the no-op contract — and must accept a nil container without
	// panicking (the orchestrator's safety net should hold even in
	// degraded environments).
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	err := (unknownAdapter{}).SetupSystemPrompt(
		context.Background(),
		nil, // container: deliberately nil
		"",
		AgentRunRequest{},
		"/work",
		logger,
	)
	if err != nil {
		t.Errorf("err = %v, want nil (documented no-op)", err)
	}
}

func TestUnknownAdapter_SupportsMCP_False(t *testing.T) {
	// orchestrator_run.go skips WriteMCPConfig when SupportsMCP() is
	// false — pin the false return so a malformed CLI string never
	// accidentally triggers MCP wiring against a no-op writer.
	if (unknownAdapter{}).SupportsMCP() {
		t.Error("SupportsMCP() = true, want false")
	}
}

func TestUnknownAdapter_WriteMCPConfig_NilReturn(t *testing.T) {
	// Safety-net no-op: even when reached defensively, must return nil
	// and not touch the (possibly nil) container.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	err := (unknownAdapter{}).WriteMCPConfig(
		context.Background(),
		nil,
		"",
		AgentRunRequest{},
		"/work",
		logger,
	)
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

// ---- getAdapter safety net ----

func TestGetAdapter_UnknownNameReturnsUnknownAdapter(t *testing.T) {
	// The "unknown defaults to safe" contract is mentioned in the source
	// (already covered by failover_test.go for the BuildCommand half).
	// Pin the full type-identity result here so callers can rely on
	// `if a == (unknownAdapter{}) { ... }` checks without surprise.
	got := getAdapter("DOES_NOT_EXIST")
	if _, ok := got.(unknownAdapter); !ok {
		t.Errorf("getAdapter(\"DOES_NOT_EXIST\") = %T, want unknownAdapter", got)
	}
}

func TestGetAdapter_EmptyStringReturnsUnknownAdapter(t *testing.T) {
	// Empty name → fallback; otherwise a registry lookup against
	// adapterRegistry[""] would hit the empty-name sentinel an adapter
	// might register by accident.
	got := getAdapter("")
	if _, ok := got.(unknownAdapter); !ok {
		t.Errorf("getAdapter(\"\") = %T, want unknownAdapter", got)
	}
}

func TestAdapterRegistry_AllInitAdaptersRegistered(t *testing.T) {
	// Pin which adapters init() registers. A regression that drops an
	// adapter (e.g. removing one mid-refactor) would surface here AND
	// would also break getAdapter for the corresponding agent.CLIAdapter
	// values in production. The list mirrors init() in this file.
	want := []string{"CLAUDE_CODE", "CODEX_CLI", "GEMINI_CLI", "OPENCODE", "CURSOR_CLI", "FACTORY_DROID"}
	for _, name := range want {
		t.Run(name, func(t *testing.T) {
			a := getAdapter(name)
			if _, ok := a.(unknownAdapter); ok {
				t.Errorf("getAdapter(%q) fell through to unknownAdapter; init() should have registered it", name)
			}
			if a.Name() != name {
				t.Errorf("adapter.Name() = %q, want %q (registry key must match Name())", a.Name(), name)
			}
		})
	}
}
