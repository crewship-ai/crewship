package orchestrator

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/crewship-ai/crewship/internal/memory"
)

// PR-E F6 — PERSONA + per-user peer card injection.
//
// The runtime reads from inside the container (PERSONA.md / peers/*)
// instead of from the host DB so a) operator edits via memory.write
// land immediately without a sidecar round-trip, and b) the
// container's filesystem is already the source of truth for every
// other memory tier. The host-side persistence (DB indexes,
// audit_log) is owned by the API layer; the orchestrator's job is
// strictly assembly.
//
// PERSONA layering at injection time matches LoadPersona's resolved
// order:
//
//	agent layer (override) → crew layer (default) → DefaultPersona
//
// The peer card lookup is keyed on req.OpenedByUserID — empty for
// non-chat invocations (routine dispatch, system jobs), in which
// case no peer block is emitted. This is a hard rule, not a
// soft-opt: even if other peer cards exist on disk, we never inject
// them unless they're the opener's. Cross-operator gossip is an
// intentional gap, not a missing feature.

// personaContainerPath is the absolute path to the per-agent
// PERSONA.md as the container sees it. Matches the existing
// /crew/agents/{slug}/.memory/ layout used by buildAgentMemoryBlock.
func personaContainerPath(agentSlug string) string {
	return path.Join("/crew", "agents", agentSlug, ".memory", "PERSONA.md")
}

// crewPersonaContainerPath is the crew-default PERSONA.md path. Lives
// under /crew/shared/.memory/ alongside CREW.md.
func crewPersonaContainerPath() string {
	return path.Join("/crew", "shared", ".memory", "PERSONA.md")
}

// peerCardContainerPath is the absolute path to the per-(agent, user)
// peer card inside the container. user_slug is derived host-side
// (memory.UserSlug) so the same hash drives both the disk filename
// and the DB index — derivation must stay byte-identical at both
// sites.
func peerCardContainerPath(agentSlug, userSlug string) string {
	return path.Join("/crew", "agents", agentSlug, ".memory", "peers", userSlug+".md")
}

// userModelContainerPath is the absolute path to the per-(user,
// workspace) operator model inside the container. The model is crew-
// shared (not per-agent), so it lives under /crew/shared/.memory/users
// alongside the crew PERSONA.md — every agent in the crew reads the
// same operator model. user_slug is derived host-side (memory.UserSlug)
// so the hash that names the file is byte-identical to the writer's.
func userModelContainerPath(userSlug string) string {
	return path.Join("/crew", "shared", ".memory", "users", userSlug+".md")
}

// buildPersonaBlock reads PERSONA.md with agent-wins layering and
// renders a [PERSONA] block. Returns "" when no persona is configured
// AND no role title is set (defensive — without role we cannot even
// generate the default). The block is intentionally outside the
// budget machinery: 1.5 KB is small enough that it always fits,
// and PERSONA is high-signal-per-byte (it shapes the response
// register for the whole turn).
func (o *Orchestrator) buildPersonaBlock(ctx context.Context, req AgentRunRequest) string {
	if req.ContainerID == "" || req.AgentSlug == "" {
		return ""
	}
	readCtx, cancel := context.WithTimeout(ctx, memoryReadTimeout)
	defer cancel()

	// Agent layer first — full override semantic. We only fall to
	// crew layer + default when the agent layer is empty or missing.
	agentPersona, _ := o.readContainerFile(readCtx, req.ContainerID, personaContainerPath(req.AgentSlug))
	crewPersona := ""
	if req.CrewID != "" {
		crewPersona, _ = o.readContainerFile(readCtx, req.ContainerID, crewPersonaContainerPath())
	}

	var content, source string
	switch {
	case strings.TrimSpace(agentPersona) != "":
		content = strings.TrimSpace(agentPersona)
		source = "agent override"
	case strings.TrimSpace(crewPersona) != "":
		content = strings.TrimSpace(crewPersona)
		source = "crew default"
	default:
		// Synthesize a minimal default — see memory.DefaultPersona
		// for the same shape. Done host-side (rather than reading
		// from a synthesized file) because there is no file to
		// read, and PERSONA is small enough that the inline format
		// stays under the cap by construction.
		def := memory.DefaultPersona(req.AgentRole, req.RoleTitle)
		if def.Content == "" {
			return ""
		}
		content = def.Content
		source = "synthesized default"
	}
	if content == "" {
		return ""
	}
	return fmt.Sprintf("[PERSONA]\nSource: %s\n%s\n[END PERSONA]\n\n", source, content)
}

// buildPeerCardBlock reads the opener's peer card (and ONLY the
// opener's) and renders a [PEER CONTEXT] block. Returns "" when no
// opener is known, when no card exists for that opener, or when the
// container path is unset. Other users' cards are never injected
// even if they exist on disk — see package doc comment for the
// "no cross-operator gossip" rationale.
func (o *Orchestrator) buildPeerCardBlock(ctx context.Context, req AgentRunRequest) string {
	if req.ContainerID == "" || req.AgentSlug == "" || req.OpenedByUserID == "" {
		return ""
	}
	if req.WorkspaceID == "" {
		// Slug derivation requires workspace_id (cross-workspace
		// isolation guarantee). Bail rather than silently falling
		// back to a workspace-less hash.
		return ""
	}
	slug := memory.UserSlug(req.OpenedByUserID, req.WorkspaceID)
	if slug == "" {
		return ""
	}
	readCtx, cancel := context.WithTimeout(ctx, memoryReadTimeout)
	defer cancel()
	body, _ := o.readContainerFile(readCtx, req.ContainerID, peerCardContainerPath(req.AgentSlug, slug))
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return fmt.Sprintf(
		"[PEER CONTEXT]\nThe operator who opened this session has interacted with you before.\nThe following profile was distilled from prior sessions — treat it as a hint\nabout communication style, not as a fact about the operator's intent.\n%s\n[END PEER CONTEXT]\n\n",
		body,
	)
}

// buildUserModelBlock reads the opener's evolving operator model (and
// ONLY the opener's) from crew-shared memory and renders an [OPERATOR
// MODEL] block. Returns "" when no opener is known, when no model
// exists for that opener, or when the container/workspace is unset.
//
// Unlike the peer card (per agent+user), this model is per (user,
// workspace) — it captures how the operator likes to work across the
// whole crew, accreted and merged over many sessions. It is emitted
// BEFORE [PEER CONTEXT] so the general working-style hint frames the
// per-agent relationship hint that follows. Both are "hint, not fact".
//
// The "no cross-operator gossip" rule applies identically: only the
// session opener's model is ever injected, never another operator's,
// even if it's on disk.
func (o *Orchestrator) buildUserModelBlock(ctx context.Context, req AgentRunRequest) string {
	if req.ContainerID == "" || req.OpenedByUserID == "" {
		return ""
	}
	if req.WorkspaceID == "" {
		// Slug derivation requires workspace_id (cross-workspace
		// isolation guarantee). Bail rather than collapse tenants.
		return ""
	}
	slug := memory.UserSlug(req.OpenedByUserID, req.WorkspaceID)
	if slug == "" {
		return ""
	}
	readCtx, cancel := context.WithTimeout(ctx, memoryReadTimeout)
	defer cancel()
	body, _ := o.readContainerFile(readCtx, req.ContainerID, userModelContainerPath(slug))
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return fmt.Sprintf(
		"[OPERATOR MODEL]\nThis operator has worked with the crew before. The following profile was\ndistilled and merged across prior sessions — treat it as a hint about how\nthey prefer to work, not as a fact about who they are or what they want.\n%s\n[END OPERATOR MODEL]\n\n",
		body,
	)
}
