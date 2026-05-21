package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// PR-E F6 — PERSONA.md as a two-layer surface.
//
// PERSONA.md tells the agent HOW to address the operator. It is not
// long-term knowledge (that's AGENT.md) and not session log content
// (that's daily/*.md). The split layers — crew default + per-agent
// override — exist because operator tone is usually crew-wide ("we
// are blunt and prefer Czech") with per-agent calibration ("Helper
// stays gentle even when the rest of the crew is blunt").
//
// Two physical files:
//
//	/output/_crew_shared/{crew_id}/.memory/PERSONA.md
//	/output/{agent_slug}/.memory/PERSONA.md
//
// Loading order at session start:
//
//	1. Crew layer (always read first)
//	2. Agent layer (read second — if present, REPLACES the crew layer
//	   block; otherwise the crew layer is used as-is)
//	3. If both are empty, the orchestrator falls back to a generated
//	   default of "{role_title} ({agent_role})" — see DefaultPersona.
//
// The agent layer is a full override, not a merge. Merging persona
// text from two sources tends to produce contradictory tone advice
// the model has to reconcile mid-response; full override avoids the
// problem. The crew layer stays visible in the file system so the
// operator can copy-paste and tweak rather than re-author from
// scratch.
//
// # Operator-edited only (Phase 1)
//
// Agents cannot directly write PERSONA.md across any autonomy level.
// Per PRD §6 F6 and policy.ActionPersonaDirectWrite (always
// DecisionRejected), agents instead emit an inbox proposal
// (SuggestPersona) and the operator approves before the file is
// written. The PoliciedWrite helper enforces this — direct calls
// to WritePersona bypass policy by design (used by the API layer
// AFTER policy resolution).

// PersonaCapBytes is the hard cap per PERSONA file (crew or agent).
// Matches tools.go capPersonaBytes — duplicated here so callers
// outside the dispatcher don't have to import the unexported
// constant. Source of truth lives in tools.go; an integration test
// asserts they stay in sync.
const PersonaCapBytes = capPersonaBytes

// PersonaLayer enumerates which layer a write targets. Two values,
// no inheritance, no special "both" enum — callers know which file
// they're touching at the API boundary.
type PersonaLayer string

const (
	// PersonaCrew is the workspace-shared default at
	// /output/_crew_shared/{crew_id}/.memory/PERSONA.md.
	PersonaCrew PersonaLayer = "crew"
	// PersonaAgent is the per-agent override at
	// /output/{agent_slug}/.memory/PERSONA.md.
	PersonaAgent PersonaLayer = "agent"
)

// PersonaPaths resolves the on-disk paths for both layers. AgentDir
// is the per-agent .memory/ root (the same path tools.go uses as
// AgentContext.AgentMemoryDir). CrewDir is the crew shared .memory/
// root; empty if the agent has no crew (solo mode), in which case
// the crew layer is unavailable and only the agent layer is read.
type PersonaPaths struct {
	AgentDir string // .../crew/agents/{slug}/.memory
	CrewDir  string // .../crew/shared/.memory  (empty for solo)
}

// CrewPath returns the absolute path of the crew-layer PERSONA.md,
// or "" if no crew directory is configured.
func (p PersonaPaths) CrewPath() string {
	if p.CrewDir == "" {
		return ""
	}
	return filepath.Join(p.CrewDir, "PERSONA.md")
}

// AgentPath returns the absolute path of the agent-layer PERSONA.md.
func (p PersonaPaths) AgentPath() string {
	return filepath.Join(p.AgentDir, "PERSONA.md")
}

// ResolvedPersona is the result of loading both layers + applying
// the override semantic. Layer reports which layer the returned
// content came from; "default" means both files were empty and the
// caller supplied a generated fallback via DefaultPersona.
type ResolvedPersona struct {
	Content string
	Layer   PersonaLayer
	// FromDefault is true if Content was synthesized via
	// DefaultPersona because neither layer had real content. Lets
	// the orchestrator skip framing the [PERSONA] block when the
	// default would be no-op noise.
	FromDefault bool
}

// LoadPersona reads both layers and returns the effective persona
// for the agent. The override rule is: agent layer wins outright
// when non-empty; otherwise crew layer wins; otherwise an empty
// result (caller decides whether to fall back via DefaultPersona).
//
// Missing files are normal, not errors — the function only returns
// an error for genuine IO failures (permission denied, corrupt
// directory). Read-side prompt-injection scanning is the orchestrator
// or sidecar's responsibility (they call ScanContent on the result);
// this function returns raw file bytes.
func LoadPersona(p PersonaPaths) (ResolvedPersona, error) {
	agent, aerr := readPersonaFile(p.AgentPath())
	if aerr != nil {
		return ResolvedPersona{}, fmt.Errorf("load persona (agent layer): %w", aerr)
	}
	if strings.TrimSpace(agent) != "" {
		return ResolvedPersona{Content: agent, Layer: PersonaAgent}, nil
	}
	if crewPath := p.CrewPath(); crewPath != "" {
		crew, cerr := readPersonaFile(crewPath)
		if cerr != nil {
			return ResolvedPersona{}, fmt.Errorf("load persona (crew layer): %w", cerr)
		}
		if strings.TrimSpace(crew) != "" {
			return ResolvedPersona{Content: crew, Layer: PersonaCrew}, nil
		}
	}
	return ResolvedPersona{}, nil
}

// DefaultPersona synthesizes a minimal "you are {role}" persona
// when both layers are empty. agentRole is the runtime role bucket
// (AGENT / LEAD), roleTitle is the human-facing title. The
// synthesized text is intentionally short (under 200 bytes) so it
// doesn't crowd the system prompt for crews that haven't bothered
// to author a persona yet.
func DefaultPersona(agentRole, roleTitle string) ResolvedPersona {
	role := strings.TrimSpace(roleTitle)
	if role == "" {
		role = strings.TrimSpace(agentRole)
	}
	if role == "" {
		role = "Agent"
	}
	bucket := strings.TrimSpace(agentRole)
	if bucket == "" {
		bucket = "AGENT"
	}
	return ResolvedPersona{
		Content:     fmt.Sprintf("You are the %s (role: %s). Respond clearly and stay on-task.", role, bucket),
		Layer:       PersonaCrew, // default is treated as a "soft crew" floor
		FromDefault: true,
	}
}

// WritePersona persists content to the given layer. Caps enforced
// per PersonaCapBytes — content larger than the cap is rejected
// before any IO so a partial write can't leave the file half-truncated.
// Empty content is also rejected; callers wanting to clear a layer
// should use ResetPersona which removes the file entirely.
//
// Writes are flock-protected via the same FileLock primitive the
// memory.write dispatcher uses, so a routine sweep and an API PATCH
// landing simultaneously can't race past the cap check.
//
// This function bypasses policy. The API layer is responsible for
// calling policy.Resolver.Resolve(crewID).DecideAction(
// ActionPersonaSuggest) FIRST and only invoking WritePersona once
// the operator has approved. Calling this from an agent path is a
// bug — agents must go through SuggestPersona (inbox proposal).
func WritePersona(p PersonaPaths, layer PersonaLayer, content string) error {
	if strings.TrimSpace(content) == "" {
		return errors.New("persona: empty content rejected (use ResetPersona to clear)")
	}
	if len(content) > PersonaCapBytes {
		return fmt.Errorf("persona: content %d bytes exceeds cap %d", len(content), PersonaCapBytes)
	}
	path, err := resolvePersonaPath(p, layer)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("persona: mkdir: %w", err)
	}
	lk := NewFileLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		return fmt.Errorf("persona: lock: %w", err)
	}
	defer func() { _ = lk.Unlock() }()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("persona: write: %w", err)
	}
	return nil
}

// ResetPersona deletes the per-layer file. Idempotent — a missing
// file is treated as already-reset. Crew vs agent layer reset is
// distinct: clearing the agent layer falls back to the crew layer
// on the next read; clearing the crew layer falls back to whatever
// DefaultPersona generates.
func ResetPersona(p PersonaPaths, layer PersonaLayer) error {
	path, err := resolvePersonaPath(p, layer)
	if err != nil {
		return err
	}
	lk := NewFileLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		return fmt.Errorf("persona: lock: %w", err)
	}
	defer func() { _ = lk.Unlock() }()
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("persona: remove: %w", err)
	}
	return nil
}

// BackfillFromLegacy migrates the pre-PR-E agents.system_prompt_legacy
// value into PERSONA.md the first time PERSONA is touched for an
// agent. Idempotent on the source side (caller should NULL-out the
// legacy column after a successful backfill so a subsequent restart
// doesn't re-import). Skipped entirely if the agent already has a
// non-empty PERSONA.md — the operator's deliberate authoring wins
// over an automated migration.
//
// Returns true if a write happened, false if the migration was a
// no-op (either no legacy value or agent layer already populated).
func BackfillFromLegacy(p PersonaPaths, legacy string) (wrote bool, err error) {
	trimmed := strings.TrimSpace(legacy)
	if trimmed == "" {
		return false, nil
	}
	// Atomic check-and-write: hold the same lock WritePersona uses
	// across the existence check + write so a concurrent operator
	// edit between the read and the write isn't silently clobbered.
	// Without this, two goroutines (e.g. orchestrator startup +
	// migration sweep) can both observe an empty file and race to
	// write different content, with the later writer winning
	// non-deterministically.
	if p.AgentDir == "" {
		return false, errors.New("persona backfill: AgentDir required")
	}
	path := p.AgentPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("persona backfill: mkdir: %w", err)
	}
	lk := NewFileLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		return false, fmt.Errorf("persona backfill: lock: %w", err)
	}
	defer func() { _ = lk.Unlock() }()

	existing, rerr := readPersonaFile(path)
	if rerr != nil {
		return false, fmt.Errorf("persona backfill: %w", rerr)
	}
	if strings.TrimSpace(existing) != "" {
		return false, nil
	}
	// Truncate to cap if the legacy prompt is too long. We don't
	// reject the backfill in that case — losing 100% of the legacy
	// value to preserve "the file MUST fit the cap" invariant is
	// worse than landing the first PersonaCapBytes worth of
	// content. The operator can re-edit afterwards.
	body := trimmed
	if len(body) > PersonaCapBytes {
		body = body[:PersonaCapBytes]
	}
	// Inline write under the held lock — calling WritePersona would
	// try to re-acquire the same flock and deadlock (or fail-fast
	// with EAGAIN, depending on FileLock semantics).
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return false, fmt.Errorf("persona backfill: write: %w", err)
	}
	return true, nil
}

// resolvePersonaPath maps a PersonaLayer to the concrete file path.
// Returns an explicit error for crew-layer requests when no crew
// directory is configured (solo agent) — silently writing to a
// disabled path would create cross-agent confusion.
func resolvePersonaPath(p PersonaPaths, layer PersonaLayer) (string, error) {
	switch layer {
	case PersonaAgent:
		if p.AgentDir == "" {
			return "", errors.New("persona: agent layer requires AgentDir")
		}
		return p.AgentPath(), nil
	case PersonaCrew:
		if p.CrewDir == "" {
			return "", errors.New("persona: crew layer unavailable for solo agent")
		}
		return p.CrewPath(), nil
	default:
		return "", fmt.Errorf("persona: unknown layer %q", layer)
	}
}

// readPersonaFile is a small wrapper over os.ReadFile that treats
// ENOENT as "" (no error). Lets callers reason about "file content"
// vs "real IO failure" without a fs.ErrNotExist check at every call
// site.
func readPersonaFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}
