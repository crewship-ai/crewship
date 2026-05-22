package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

// AgentBrief is the curated context a LEAD hands to a freshly hired
// or assigned sub-agent. It replaces the all-or-nothing
// SkipConvHistory boolean with a structured slice the parent
// explicitly chooses to share.
//
// Empty AgentBrief = sub-agent starts with only its PERSONA.md +
// crew CREW.md + its own (probably empty) AGENT.md, exactly like a
// fresh hire. Populated AgentBrief layers the parent's selections
// on top of those tiers, marked as "briefed from parent <id>".
//
// Auditor framing (PR-F7): "PR-D ephemeral agents brzdí na tomto —
// ephemerální agent dostane buď nic, nebo plný kontext leadu."
// Boolean SkipConvHistory was the only knob: either dump the full
// lead conversation into the sub-agent or hand it nothing. AgentBrief
// is the middle option — the lead chooses what to share, the
// sub-agent reads it as just another memory tier.
type AgentBrief struct {
	// Mission is a short (max 500 char) restatement of what the
	// sub-agent is being asked to do. Lands as the first line of
	// the agent's initial system context.
	Mission string

	// SharedMemory is a list of parent-memory references the
	// sub-agent is allowed to read. Each item is { tier, key,
	// reason } — the parent must explain why it's sharing.
	SharedMemory []SharedMemoryRef

	// Constraints is a free-form list of "do" / "don't" lines the
	// parent layers on top of the policy. Each line is appended
	// to the sub-agent's system prompt after PERSONA but before
	// its own working memory.
	Constraints []string

	// ParentAgentID is who issued the brief. Logged in journal +
	// surfaced in the sub-agent's UI so operators can trace.
	ParentAgentID string
}

// SharedMemoryRef points to a single parent-memory entry the
// sub-agent is allowed to read. Tier matches the dispatcher's
// validTiers enum; Key is required for the multi-file tiers
// (daily, peers). Reason is operator-facing — surfaced in the brief
// block so the sub-agent (and the human auditor) can see WHY this
// fragment is in scope.
type SharedMemoryRef struct {
	Tier   string `json:"tier"`
	Key    string `json:"key,omitempty"`
	Reason string `json:"reason"`
}

// Validation caps. Numbers are arbitrary safety ceilings, not
// product constraints — the goal is to make "brief" actually mean
// brief. Defence in depth against a malformed brief (or a parent
// agent that decides to dump everything) overflowing the sub-agent's
// context budget on its first turn.
//
//   - missionMaxBytes (500): one paragraph. Anything larger should
//     live in SharedMemory references, not the brief envelope.
//   - sharedMemoryMax (10): ten parent-tier pointers is already a
//     lot of cross-context for one sub-agent to digest; more than
//     that is usually a sign the lead should be sharing a whole
//     CREW.md tier rather than cherry-picking.
//   - constraintsMax (20): twenty inline "do" / "don't" lines is
//     the upper bound for prompt readability. Past that, the
//     constraints stop reading as instructions and start reading as
//     noise. Policy that needs to be larger belongs in the global
//     policy (PR-B F2), not in a per-brief override.
const (
	missionMaxBytes = 500
	sharedMemoryMax = 10
	constraintsMax  = 20
)

// NewAgentBrief returns a brief with the supplied parent + mission.
// Constraints / SharedMemory default to empty slices so callers can
// append without nil-check ceremony. Caller MUST call Validate
// before passing to ApplyBrief; the constructor does not validate.
func NewAgentBrief(parentAgentID, mission string) AgentBrief {
	return AgentBrief{
		ParentAgentID: parentAgentID,
		Mission:       mission,
		SharedMemory:  []SharedMemoryRef{},
		Constraints:   []string{},
	}
}

// Validate enforces the safety caps documented on the constants
// above. Returns the first violation encountered (not an aggregated
// error list) — the caller's path is "fix and retry", not "render a
// validation form".
func (b *AgentBrief) Validate() error {
	if b == nil {
		return errors.New("agent_brief: nil")
	}
	if b.ParentAgentID == "" {
		return errors.New("agent_brief: ParentAgentID is required (journal traceability)")
	}
	if len(b.Mission) > missionMaxBytes {
		return fmt.Errorf("agent_brief: Mission length %d exceeds cap %d", len(b.Mission), missionMaxBytes)
	}
	if len(b.SharedMemory) > sharedMemoryMax {
		return fmt.Errorf("agent_brief: SharedMemory count %d exceeds cap %d", len(b.SharedMemory), sharedMemoryMax)
	}
	if len(b.Constraints) > constraintsMax {
		return fmt.Errorf("agent_brief: Constraints count %d exceeds cap %d", len(b.Constraints), constraintsMax)
	}
	for i, ref := range b.SharedMemory {
		if ref.Tier == "" {
			return fmt.Errorf("agent_brief: SharedMemory[%d].Tier is required", i)
		}
		if ref.Reason == "" {
			return fmt.Errorf("agent_brief: SharedMemory[%d].Reason is required (operator audit)", i)
		}
		// Multi-file tiers (daily, peers) name a specific file via Key
		// — without one, the reference is ambiguous ("which daily?
		// which peer?") and the dispatcher would return the wrong
		// content. The doc comment on SharedMemoryRef already says
		// Key is required for these tiers; enforce it here so a
		// caller that forgets Key gets a clear validation error
		// instead of a runtime mis-resolve. CodeRabbit round-11 catch.
		if (ref.Tier == "daily" || ref.Tier == "peers") && ref.Key == "" {
			return fmt.Errorf("agent_brief: SharedMemory[%d].Key is required for tier %q (multi-file tier)", i, ref.Tier)
		}
	}
	for i, line := range b.Constraints {
		if strings.TrimSpace(line) == "" {
			return fmt.Errorf("agent_brief: Constraints[%d] is empty", i)
		}
	}
	return nil
}

// Render serialises the brief to the markdown blob that lands in
// .memory/BRIEF.md. The shape is deliberately operator-readable —
// a human peeking at the file should immediately see "from whom",
// "to do what", "with which shared context", "under which rules".
//
// Header line carries the parent id so the journal-level audit trail
// (parent.spawn + sub-agent.brief.read) can be cross-referenced
// without re-parsing the body.
func (b *AgentBrief) Render() string {
	var sb strings.Builder
	sb.WriteString("# BRIEF\n")
	sb.WriteString(fmt.Sprintf("Briefed by: parent agent %s\n\n", b.ParentAgentID))
	if b.Mission != "" {
		sb.WriteString("## Mission\n")
		sb.WriteString(strings.TrimSpace(b.Mission))
		sb.WriteString("\n\n")
	}
	if len(b.SharedMemory) > 0 {
		sb.WriteString("## Shared memory (read-allow)\n")
		for _, ref := range b.SharedMemory {
			if ref.Key != "" {
				sb.WriteString(fmt.Sprintf("- %s/%s — %s\n", ref.Tier, ref.Key, ref.Reason))
			} else {
				sb.WriteString(fmt.Sprintf("- %s — %s\n", ref.Tier, ref.Reason))
			}
		}
		sb.WriteString("\n")
	}
	if len(b.Constraints) > 0 {
		sb.WriteString("## Constraints\n")
		for _, line := range b.Constraints {
			sb.WriteString("- ")
			sb.WriteString(strings.TrimSpace(line))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// agentBriefJSON is the wire shape used by the API and the journal
// (sub-agent.brief.applied event payload). Hand-rolled so the
// on-wire field names stay snake_case while Go fields stay
// PascalCase — flipping a struct tag would silently break both
// surfaces at once.
type agentBriefJSON struct {
	Mission       string            `json:"mission"`
	SharedMemory  []SharedMemoryRef `json:"shared_memory,omitempty"`
	Constraints   []string          `json:"constraints,omitempty"`
	ParentAgentID string            `json:"parent_agent_id"`
}

// MarshalJSON emits the stable wire shape used by the API and the
// journal.
func (b AgentBrief) MarshalJSON() ([]byte, error) {
	return json.Marshal(agentBriefJSON{
		Mission:       b.Mission,
		SharedMemory:  b.SharedMemory,
		Constraints:   b.Constraints,
		ParentAgentID: b.ParentAgentID,
	})
}

// UnmarshalJSON parses the wire shape. Both empty and missing
// collection fields decode to a nil slice — Validate() and Render()
// already tolerate that.
func (b *AgentBrief) UnmarshalJSON(data []byte) error {
	var raw agentBriefJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	b.Mission = raw.Mission
	b.SharedMemory = raw.SharedMemory
	b.Constraints = raw.Constraints
	b.ParentAgentID = raw.ParentAgentID
	return nil
}

// briefContainerPath is the absolute path BRIEF.md lives at inside
// the agent's container — sibling of AGENT.md / PERSONA.md so the
// existing memory-tier conventions apply unchanged. Idempotent:
// re-writing the same brief replaces the previous one byte-for-byte;
// re-writing a different brief overwrites it (no versioning at the
// orchestrator layer — versioning lives in the API + journal).
func briefContainerPath(agentSlug string) string {
	return path.Join("/crew", "agents", agentSlug, ".memory", "BRIEF.md")
}

// ApplyBrief writes the brief to the sub-agent's container at
// .memory/BRIEF.md. The prompt assembly in buildAgentMemoryBlock
// reads BRIEF.md on the next turn and frames it as a [BRIEF] block
// (see memory.go).
//
// The brief is validated before the write — an invalid brief fails
// loudly here rather than producing a malformed file on disk that
// the agent reads next turn.
//
// Containerless usage (tests + future host-side adapters): if
// containerID is empty, ApplyBrief is a no-op and returns nil so
// callers can pre-stage a brief on a not-yet-started container by
// writing to the host AgentMemoryDir first (handled by the PR-F1
// API layer, not here). This matches the read-side tolerance:
// buildAgentMemoryBlock's container reads return "" for unknown
// container ids instead of erroring.
func (o *Orchestrator) ApplyBrief(ctx context.Context, agentSlug, containerID string, brief AgentBrief) error {
	if err := brief.Validate(); err != nil {
		return err
	}
	if agentSlug == "" {
		return errors.New("apply_brief: agentSlug is required")
	}
	if containerID == "" {
		// No container yet — caller will pre-stage on first
		// run via the API layer's pre-start hook. Returning nil
		// here keeps callers from having to special-case the
		// "brief before container" path themselves.
		return nil
	}

	body := brief.Render()
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	target := briefContainerPath(agentSlug)
	dir := path.Dir(target)

	// Two-step: mkdir -p the .memory dir (container may not have it
	// yet on a fresh hire), then write the brief. Using base64 +
	// `printf | base64 -d` mirrors the same shell-safe pattern the
	// args/env writer in orchestrator_exec_env.go already uses —
	// the brief body can contain quotes, backticks, $-substitutions,
	// or anything else a parent agent decides to put in the Mission.
	//
	// SECURITY round-8: script is now a CONSTANT and the variable
	// values (dir, encoded, target) are passed as positional args
	// $1/$2/$3. An earlier version interpolated them into the script
	// string via fmt.Sprintf which would let a single-quote in the
	// path break out of the intended command — e.g. a crew slug
	// containing `'; rm -rf /tmp; '` would inject. CodeRabbit catch.
	const script = `mkdir -p "$1" && printf '%s' "$2" | base64 -d > "$3"`
	res, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script, "apply_brief", dir, encoded, target},
		User:        "1001:1001",
	})
	if err != nil {
		return fmt.Errorf("apply_brief: exec: %w", err)
	}
	// Drain + close so the exec session shuts down cleanly. The
	// reader content is uninteresting (sh writes nothing on
	// success) but leaking the FD leaves the docker exec session
	// dangling.
	_, _ = io.Copy(io.Discard, res.Reader)
	_ = res.Reader.Close()
	return nil
}
