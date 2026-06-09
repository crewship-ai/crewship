package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

func TestBuildPersonaBlock_AgentOverridesCrew(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/alice/.memory/PERSONA.md": "Stay gentle and patient.",
		"/crew/shared/.memory/PERSONA.md":       "Czech language, terse register.",
	})
	o := New(mc, newMemState(), slog.Default())

	got := o.buildPersonaBlock(context.Background(), AgentRunRequest{
		AgentSlug: "alice", ContainerID: "c1", CrewID: "crew1",
		RoleTitle: "Coach", AgentRole: "AGENT",
	})
	if !strings.Contains(got, "agent override") {
		t.Errorf("expected 'agent override' source marker, got %q", got)
	}
	if !strings.Contains(got, "gentle") {
		t.Errorf("expected agent persona content, got %q", got)
	}
	// Agent layer wins outright — crew "Czech" text must NOT appear.
	if strings.Contains(got, "Czech") {
		t.Errorf("agent override leaked crew content: %q", got)
	}
}

func TestBuildPersonaBlock_CrewWhenAgentEmpty(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/shared/.memory/PERSONA.md": "Crew wide tone.",
	})
	o := New(mc, newMemState(), slog.Default())

	got := o.buildPersonaBlock(context.Background(), AgentRunRequest{
		AgentSlug: "alice", ContainerID: "c1", CrewID: "crew1",
		RoleTitle: "Dev", AgentRole: "AGENT",
	})
	if !strings.Contains(got, "crew default") {
		t.Errorf("expected 'crew default' source marker, got %q", got)
	}
	if !strings.Contains(got, "Crew wide tone") {
		t.Errorf("missing crew content, got %q", got)
	}
}

func TestBuildPersonaBlock_DefaultWhenBothEmpty(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{})
	o := New(mc, newMemState(), slog.Default())

	got := o.buildPersonaBlock(context.Background(), AgentRunRequest{
		AgentSlug: "alice", ContainerID: "c1", CrewID: "crew1",
		RoleTitle: "Captain", AgentRole: "LEAD",
	})
	if !strings.Contains(got, "synthesized default") {
		t.Errorf("expected 'synthesized default' source marker, got %q", got)
	}
	if !strings.Contains(got, "Captain") {
		t.Errorf("default should include role title 'Captain', got %q", got)
	}
}

// Without a ContainerID or AgentSlug there's nothing to read from —
// don't emit a noise block.
func TestBuildPersonaBlock_NoContainerReturnsEmpty(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{})
	o := New(mc, newMemState(), slog.Default())
	if got := o.buildPersonaBlock(context.Background(), AgentRunRequest{}); got != "" {
		t.Errorf("expected empty block for unset request; got %q", got)
	}
}

// Critical invariant: peer card injection is keyed on
// OpenedByUserID — without it (system-initiated runs like routine
// dispatch), NO peer card is injected even when one exists on disk.
func TestBuildPeerCardBlock_RequiresOpener(t *testing.T) {
	slug := memory.UserSlug("u1", "ws1")
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/alice/.memory/peers/" + slug + ".md": "u1 notes",
	})
	o := New(mc, newMemState(), slog.Default())

	// No opener → empty.
	if got := o.buildPeerCardBlock(context.Background(), AgentRunRequest{
		AgentSlug: "alice", ContainerID: "c1", WorkspaceID: "ws1",
	}); got != "" {
		t.Errorf("expected empty block without opener; got %q", got)
	}
	// Wrong opener → empty (slug mismatch, file isn't there).
	if got := o.buildPeerCardBlock(context.Background(), AgentRunRequest{
		AgentSlug: "alice", ContainerID: "c1", WorkspaceID: "ws1",
		OpenedByUserID: "u2",
	}); got != "" {
		t.Errorf("expected empty block for opener with no card; got %q", got)
	}
}

func TestBuildPeerCardBlock_InjectsOpenerCardOnly(t *testing.T) {
	slugMatch := memory.UserSlug("u1", "ws1")
	slugOther := memory.UserSlug("u2", "ws1")
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/alice/.memory/peers/" + slugMatch + ".md": "Pavel notes: terse",
		"/crew/agents/alice/.memory/peers/" + slugOther + ".md": "Ivana notes: warm",
	})
	o := New(mc, newMemState(), slog.Default())
	got := o.buildPeerCardBlock(context.Background(), AgentRunRequest{
		AgentSlug: "alice", ContainerID: "c1", WorkspaceID: "ws1",
		OpenedByUserID: "u1",
	})
	if !strings.Contains(got, "[PEER CONTEXT]") {
		t.Errorf("expected [PEER CONTEXT] header; got %q", got)
	}
	if !strings.Contains(got, "Pavel notes") {
		t.Errorf("opener's card not injected; got %q", got)
	}
	// Other user's card MUST NOT appear — no cross-operator gossip.
	if strings.Contains(got, "Ivana notes") {
		t.Errorf("non-opener card leaked into block: %q", got)
	}
}

// --- [OPERATOR MODEL] block (PR #10) ---

// The operator model is keyed on OpenedByUserID + WorkspaceID and read
// from the crew-shared memory (/crew/shared/.memory/users/{slug}.md).
func TestBuildUserModelBlock_InjectsOpenerModel(t *testing.T) {
	slug := memory.UserSlug("u1", "ws1")
	mc := mockContainerForMemory(map[string]string{
		"/crew/shared/.memory/users/" + slug + ".md": "- tone: terse, technical",
	})
	o := New(mc, newMemState(), slog.Default())
	got := o.buildUserModelBlock(context.Background(), AgentRunRequest{
		AgentSlug: "alice", ContainerID: "c1", WorkspaceID: "ws1",
		OpenedByUserID: "u1",
	})
	if !strings.Contains(got, "[OPERATOR MODEL]") {
		t.Errorf("expected [OPERATOR MODEL] header; got %q", got)
	}
	if !strings.Contains(got, "terse, technical") {
		t.Errorf("opener's model not injected; got %q", got)
	}
	// "hint not fact" framing must be present.
	if !strings.Contains(strings.ToLower(got), "hint") {
		t.Errorf("expected 'hint not fact' framing; got %q", got)
	}
}

// No opener → no block, even when a model exists on disk (system runs).
func TestBuildUserModelBlock_RequiresOpener(t *testing.T) {
	slug := memory.UserSlug("u1", "ws1")
	mc := mockContainerForMemory(map[string]string{
		"/crew/shared/.memory/users/" + slug + ".md": "- tone: terse",
	})
	o := New(mc, newMemState(), slog.Default())
	if got := o.buildUserModelBlock(context.Background(), AgentRunRequest{
		AgentSlug: "alice", ContainerID: "c1", WorkspaceID: "ws1",
	}); got != "" {
		t.Errorf("expected empty block without opener; got %q", got)
	}
}

// Missing WorkspaceID → empty (slug derivation fails closed), guarding
// the cross-tenant leak.
func TestBuildUserModelBlock_RequiresWorkspace(t *testing.T) {
	slug := memory.UserSlug("u1", "ws1")
	mc := mockContainerForMemory(map[string]string{
		"/crew/shared/.memory/users/" + slug + ".md": "- tone: terse",
	})
	o := New(mc, newMemState(), slog.Default())
	if got := o.buildUserModelBlock(context.Background(), AgentRunRequest{
		AgentSlug: "alice", ContainerID: "c1", OpenedByUserID: "u1",
	}); got != "" {
		t.Errorf("expected empty when WorkspaceID is unset; got %q", got)
	}
}

// No model on disk for the opener → empty block (fresh operator).
func TestBuildUserModelBlock_NoModelEmpty(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{})
	o := New(mc, newMemState(), slog.Default())
	if got := o.buildUserModelBlock(context.Background(), AgentRunRequest{
		AgentSlug: "alice", ContainerID: "c1", WorkspaceID: "ws1",
		OpenedByUserID: "u1",
	}); got != "" {
		t.Errorf("expected empty block for opener with no model; got %q", got)
	}
}

// Workspace component is load-bearing in the slug derivation. If a
// future bug drops the workspace_id, this test catches the
// cross-tenant card leak before it ships.
func TestBuildPeerCardBlock_RequiresWorkspace(t *testing.T) {
	slug := memory.UserSlug("u1", "ws1")
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/alice/.memory/peers/" + slug + ".md": "u1 notes",
	})
	o := New(mc, newMemState(), slog.Default())
	if got := o.buildPeerCardBlock(context.Background(), AgentRunRequest{
		AgentSlug: "alice", ContainerID: "c1",
		OpenedByUserID: "u1", // missing WorkspaceID
	}); got != "" {
		t.Errorf("expected empty when WorkspaceID is unset; got %q", got)
	}
}
