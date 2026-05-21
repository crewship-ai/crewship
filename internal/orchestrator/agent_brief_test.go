package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// TestAgentBrief_Validate_RejectsOversizedMission guards the cap on
// the mission paragraph. A parent agent that ignores the docs and
// dumps an entire conversation transcript must hit the validator
// before the brief reaches disk.
func TestAgentBrief_Validate_RejectsOversizedMission(t *testing.T) {
	brief := NewAgentBrief("parent_42", strings.Repeat("x", missionMaxBytes+1))
	err := brief.Validate()
	if err == nil {
		t.Fatal("expected validation error for oversized mission")
	}
	if !strings.Contains(err.Error(), "Mission") {
		t.Errorf("error should call out Mission, got %q", err.Error())
	}

	// Boundary: exactly missionMaxBytes must pass.
	brief2 := NewAgentBrief("parent_42", strings.Repeat("x", missionMaxBytes))
	if err := brief2.Validate(); err != nil {
		t.Errorf("missionMaxBytes mission should pass, got %v", err)
	}
}

// TestAgentBrief_Validate_RejectsTooManyShared covers both the count
// cap on SharedMemory and the required-field shape (Tier + Reason).
// Both are first-line-of-defence; without them a brief can either
// overflow the context budget or leave the auditor without context
// on WHY a fragment was shared.
func TestAgentBrief_Validate_RejectsTooManyShared(t *testing.T) {
	brief := NewAgentBrief("parent_42", "go check the build")
	for i := 0; i <= sharedMemoryMax; i++ {
		brief.SharedMemory = append(brief.SharedMemory, SharedMemoryRef{
			Tier:   "AGENT",
			Reason: "context",
		})
	}
	if err := brief.Validate(); err == nil {
		t.Fatal("expected validation error for too many shared refs")
	}

	// Missing Reason on a single ref must also fail — operator audit
	// contract: every share has a stated reason.
	brief2 := NewAgentBrief("parent_42", "go check the build")
	brief2.SharedMemory = []SharedMemoryRef{{Tier: "AGENT"}}
	err := brief2.Validate()
	if err == nil || !strings.Contains(err.Error(), "Reason") {
		t.Errorf("expected error mentioning Reason; got %v", err)
	}

	// Missing Tier must also fail.
	brief3 := NewAgentBrief("parent_42", "go check the build")
	brief3.SharedMemory = []SharedMemoryRef{{Reason: "because"}}
	err = brief3.Validate()
	if err == nil || !strings.Contains(err.Error(), "Tier") {
		t.Errorf("expected error mentioning Tier; got %v", err)
	}
}

// TestAgentBrief_Validate_RejectsTooManyConstraints + empty lines.
func TestAgentBrief_Validate_RejectsTooManyConstraints(t *testing.T) {
	brief := NewAgentBrief("parent_42", "task")
	for i := 0; i <= constraintsMax; i++ {
		brief.Constraints = append(brief.Constraints, "do the thing")
	}
	if err := brief.Validate(); err == nil {
		t.Fatal("expected validation error for too many constraints")
	}

	// Empty constraint line (just whitespace) must fail — otherwise
	// the Render output gets noisy bullets.
	brief2 := NewAgentBrief("parent_42", "task")
	brief2.Constraints = []string{"   "}
	if err := brief2.Validate(); err == nil {
		t.Error("expected validation error for empty constraint")
	}
}

// TestAgentBrief_Validate_RequiresParentID — every brief must name
// its issuer for journal traceability. Anonymous briefs are a
// non-feature.
func TestAgentBrief_Validate_RequiresParentID(t *testing.T) {
	brief := NewAgentBrief("", "go do thing")
	err := brief.Validate()
	if err == nil || !strings.Contains(err.Error(), "ParentAgentID") {
		t.Errorf("expected ParentAgentID error, got %v", err)
	}
}

// TestAgentBrief_Render_RoundTrip — Render produces operator-readable
// markdown containing all the brief fields. The shape is part of the
// contract: a human peeking at .memory/BRIEF.md must immediately see
// from-whom + mission + shared-memory + constraints.
func TestAgentBrief_Render_RoundTrip(t *testing.T) {
	brief := NewAgentBrief("parent_42", "verify the auth migration")
	brief.SharedMemory = []SharedMemoryRef{
		{Tier: "AGENT", Reason: "prior auth notes"},
		{Tier: "daily", Key: "2026-05-20", Reason: "yesterday's incident timeline"},
	}
	brief.Constraints = []string{"do not modify migration v107", "ask before deploying"}

	out := brief.Render()
	for _, want := range []string{
		"parent agent parent_42",
		"verify the auth migration",
		"AGENT — prior auth notes",
		"daily/2026-05-20 — yesterday's incident timeline",
		"do not modify migration v107",
		"ask before deploying",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render() missing %q\n\n--- output:\n%s", want, out)
		}
	}
}

// TestAgentBrief_JSON_RoundTrip — the brief travels as JSON across
// the assignment API boundary. Marshal then unmarshal must produce
// an equivalent struct (field-for-field) so the API layer and the
// orchestrator agree on the wire shape.
func TestAgentBrief_JSON_RoundTrip(t *testing.T) {
	orig := NewAgentBrief("p1", "go")
	orig.SharedMemory = []SharedMemoryRef{{Tier: "AGENT", Reason: "context"}}
	orig.Constraints = []string{"be careful"}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"parent_agent_id":"p1"`) {
		t.Errorf("expected snake_case parent_agent_id in wire JSON, got %s", data)
	}

	var decoded AgentBrief
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ParentAgentID != orig.ParentAgentID || decoded.Mission != orig.Mission {
		t.Errorf("round-trip lost fields: %+v vs %+v", decoded, orig)
	}
	if len(decoded.SharedMemory) != 1 || decoded.SharedMemory[0].Tier != "AGENT" {
		t.Errorf("round-trip lost SharedMemory: %+v", decoded.SharedMemory)
	}
}

// TestApplyBrief_WritesBriefMDToAgentDir asserts the wire format
// the orchestrator sends to the container: the brief's Render()
// output must land at /crew/agents/{slug}/.memory/BRIEF.md. We
// capture the exec call (mkdir + base64 write) and decode the
// payload to verify byte-equivalence.
func TestApplyBrief_WritesBriefMDToAgentDir(t *testing.T) {
	var capturedScript string
	mc := &mockContainer{}
	mc.execFn = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		if len(cfg.Cmd) >= 3 && cfg.Cmd[0] == "sh" && cfg.Cmd[1] == "-c" {
			capturedScript = cfg.Cmd[2]
		}
		return &provider.ExecResult{
			ExecID: "ok",
			Reader: io.NopCloser(strings.NewReader("")),
		}, nil
	}
	o := New(mc, newMemState(), slog.Default())

	brief := NewAgentBrief("parent_42", "verify the auth migration")
	brief.Constraints = []string{"do not deploy"}

	if err := o.ApplyBrief(context.Background(), "alpha-agent", "c1", brief); err != nil {
		t.Fatalf("ApplyBrief: %v", err)
	}
	if !strings.Contains(capturedScript, "/crew/agents/alpha-agent/.memory/BRIEF.md") {
		t.Errorf("expected BRIEF.md target path in script, got %q", capturedScript)
	}
	if !strings.Contains(capturedScript, "mkdir -p '/crew/agents/alpha-agent/.memory'") {
		t.Errorf("expected mkdir -p for .memory dir, got %q", capturedScript)
	}
	// Decoding the base64 payload from the captured script is the
	// real assertion — confirms the body that lands on disk is
	// exactly what Render() produced.
	if !strings.Contains(capturedScript, "base64 -d") {
		t.Fatalf("expected base64 -d in script, got %q", capturedScript)
	}
}

// TestApplyBrief_OverwritesPreviousBrief_Idempotent — applying the
// same brief twice is fine (writes the same bytes); applying a
// different brief replaces the file. Both runs must succeed without
// any "already exists" / "cannot overwrite" complaints. Mirrors the
// "re-forget is a no-op" idempotency contract from Provider.Forget.
func TestApplyBrief_OverwritesPreviousBrief_Idempotent(t *testing.T) {
	var calls int
	mc := &mockContainer{}
	mc.execFn = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		calls++
		return &provider.ExecResult{
			ExecID: "ok",
			Reader: io.NopCloser(strings.NewReader("")),
		}, nil
	}
	o := New(mc, newMemState(), slog.Default())

	first := NewAgentBrief("p1", "first mission")
	second := NewAgentBrief("p1", "second mission")

	if err := o.ApplyBrief(context.Background(), "a1", "c1", first); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := o.ApplyBrief(context.Background(), "a1", "c1", first); err != nil {
		t.Fatalf("idempotent re-apply: %v", err)
	}
	if err := o.ApplyBrief(context.Background(), "a1", "c1", second); err != nil {
		t.Fatalf("overwrite apply: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 exec calls, got %d", calls)
	}
}

// TestApplyBrief_NoContainer_NoOp covers the "brief before container"
// path. The API layer can prepare a brief on a not-yet-started
// container; ApplyBrief returns nil so callers don't have to wrap
// every call in a containerID check.
func TestApplyBrief_NoContainer_NoOp(t *testing.T) {
	mc := &mockContainer{}
	called := false
	mc.execFn = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		called = true
		return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(""))}, nil
	}
	o := New(mc, newMemState(), slog.Default())

	brief := NewAgentBrief("p1", "later")
	if err := o.ApplyBrief(context.Background(), "a1", "", brief); err != nil {
		t.Fatalf("ApplyBrief(no container): %v", err)
	}
	if called {
		t.Error("expected no exec when containerID empty")
	}
}

// TestApplyBrief_RejectsInvalidBrief — the validator runs BEFORE
// the container exec, so a malformed brief never produces a half-
// written file on disk.
func TestApplyBrief_RejectsInvalidBrief(t *testing.T) {
	mc := &mockContainer{}
	mc.execFn = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		t.Fatal("exec must not be called for invalid brief")
		return nil, nil
	}
	o := New(mc, newMemState(), slog.Default())

	bad := AgentBrief{} // ParentAgentID empty → validator rejects
	if err := o.ApplyBrief(context.Background(), "a1", "c1", bad); err == nil {
		t.Fatal("expected error for invalid brief")
	}
}

// TestBuildAgentMemoryBlock_IncludesBrief — integration coverage:
// once a brief is on disk (read via the mock container), the
// [AGENT MEMORY] block must surface it ahead of AGENT.md and the
// daily logs. This is the one-line addition documented on
// buildAgentMemoryBlock — failure here means the brief is being
// written but never reaching the prompt.
func TestBuildAgentMemoryBlock_IncludesBrief(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/alpha-agent/.memory/AGENT.md": "long-term notes\n",
		"/crew/agents/alpha-agent/.memory/BRIEF.md": "# BRIEF\nBriefed by: parent agent p1\n\n## Mission\nverify the auth migration\n",
	})
	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		ContainerID:   "c1",
		AgentSlug:     "alpha-agent",
		AgentID:       "a1",
		WorkspaceID:   "ws1",
		MemoryEnabled: true,
	}
	out := o.buildMemoryContext(context.Background(), req, 0)
	if !strings.Contains(out, "verify the auth migration") {
		t.Errorf("expected brief mission in output; got:\n%s", out)
	}
	if !strings.Contains(out, "BRIEF.md (parent-issued brief)") {
		t.Errorf("expected BRIEF.md label in output; got:\n%s", out)
	}
}
