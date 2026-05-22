package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Provider is the pluggable memory backend interface introduced to
// open Crewship to alternative external memory stores while keeping
// the on-disk default as the v1 implementation. Every method MUST be
// safe to call from concurrent goroutines.
//
// Phase 1 (this PR): interface + the LocalDispatcher adapter that
// wraps the existing tools.go path. External-provider reference
// implementations (HTTP-backed long-term memory stores, vector
// recall services, conversation-history platforms) land in PR-F18+.
//
// Auditor framing (PR-F3): "the sidecar /mcp/memory is the right
// place but the contract is missing." Today every external store
// would have to fork the dispatcher to integrate. Provider lets a
// future HTTP-backed or vector-recall implementation slot in behind
// the same wire surface the orchestrator and sidecar already speak,
// without touching call sites.
//
// Wiring posture for this PR is additive only — the existing
// Dispatcher remains the production path; LocalDispatcher proves
// the interface fits without forcing a switchover. PR-F6 will lift
// the call sites once the second provider lands.
type Provider interface {
	// Retain persists content to a memory tier. Returns the
	// canonical id of the stored item for later recall / forget.
	// For the on-disk LocalDispatcher impl, the id is the
	// tier-relative source label (e.g. "AGENT.md",
	// "daily/2026-05-21.md") — stable across reads on the same
	// disk, and the same shape the model already sees in
	// memory.read metadata.
	Retain(ctx context.Context, req RetainRequest) (RetainResult, error)

	// Recall returns ranked snippets matching the query. Tier scope
	// and limit are advisory; provider may return fewer items.
	// Local impl substring-scans; remote providers may do BM25,
	// vector, or hybrid — the wire shape is the same.
	Recall(ctx context.Context, req RecallRequest) (RecallResult, error)

	// Forget deletes one or more items. GDPR cascade (PR-F1) calls
	// this with a data_subject_id filter; per-id deletion is the
	// operator-driven path. Local impl removes the underlying file
	// for an ID; future remote impls translate to provider DELETE.
	Forget(ctx context.Context, req ForgetRequest) (ForgetResult, error)

	// Health is a non-mutating liveness check. The aux-status panel
	// surfaces this so operators can spot a degraded backend.
	// Implementations MUST return promptly (timeout-bounded) — a
	// stuck Health() blocks the operator UI.
	Health(ctx context.Context) HealthStatus
}

// RetainRequest is the input shape for Provider.Retain. Keep flat —
// nested option bags age badly (every new field requires deciding
// which bag it lives in). WorkspaceID + Tier are always present;
// Key is tier-specific (required for daily / peers, ignored
// otherwise to match the dispatcher's resolvePath rules).
//
// Mode mirrors the dispatcher's write modes — "replace" overwrites,
// "append" concatenates. Mode is required so remote providers can
// be explicit about whether they're versioning the prior content;
// a default mode would hide intent at the wire.
type RetainRequest struct {
	WorkspaceID string
	AgentID     string // optional: scopes the write to a specific agent
	CrewID      string // optional: scopes to a crew (CREW tier writes)
	Tier        string // AGENT | CREW | PERSONA | pins | daily | peers | lessons
	Key         string // tier-specific (date for daily, slug for peers)
	Content     string // UTF-8 body to persist
	Mode        string // "replace" | "append"
}

// RetainResult is the output of a successful Retain. ID is the
// canonical handle a caller passes to Forget(byID); for the on-disk
// impl this is the tier source label (stable per agent, not a UUID).
// Bytes is the size persisted, useful for cap-aware callers
// surfacing budget pressure to the operator.
type RetainResult struct {
	ID    string
	Bytes int
}

// RecallRequest scopes a query against a workspace. Tier is
// optional — empty Tier means "all accessible tiers". Limit is
// advisory: providers MAY return fewer than Limit items and SHOULD
// cap at 20 to match the dispatcher's existing schema.
type RecallRequest struct {
	WorkspaceID string
	AgentID     string // optional: limits results to a single agent's tiers
	CrewID      string // optional: limits to crew-shared tiers
	Tier        string // optional scope; "" = all
	Query       string
	Limit       int
}

// RecallSnippet is a single hit. Source is the tier-relative label
// (NEVER an absolute filesystem path — see tools.go pathToSourceLabel
// for the rationale: leaking absolute paths discloses the bind-mount
// layout). Score is provider-defined; the local impl returns 1.0 for
// every hit since it does not rank.
type RecallSnippet struct {
	Source  string  `json:"source"`
	Snippet string  `json:"snippet"`
	Line    int     `json:"line,omitempty"`
	Score   float64 `json:"score,omitempty"`
}

// RecallResult envelopes the hits. Hits is always present (may be
// empty). Quarantined surfaces files the scanner rejected; callers
// should display these in the operator UI but NEVER feed them back
// to the model. Mirrors the dispatcher's existing search envelope so
// the wire stays compatible.
type RecallResult struct {
	Hits        []RecallSnippet `json:"hits"`
	Quarantined []string        `json:"quarantined,omitempty"`
}

// ForgetRequest deletes from the provider. Exactly one selector
// MUST be non-empty: ID for per-item deletion, DataSubjectID for
// GDPR cascade (deletes everything attributed to a user across
// every tier).
type ForgetRequest struct {
	WorkspaceID   string
	ID            string // canonical id from RetainResult
	DataSubjectID string // GDPR cascade selector
}

// ForgetResult reports what actually went away. Removed is the
// canonical count for the caller's audit log. Empty Removed with no
// error means the selector matched nothing — not a failure, just a
// no-op (a re-issued DELETE for an already-deleted item is fine).
type ForgetResult struct {
	Removed int
}

// HealthStatus is the non-mutating liveness signal. OK=true means
// the provider is fully operational. OK=false + Message explains
// why; aux-status surfaces this verbatim to the operator. CheckedAt
// lets the panel show "last successful check N seconds ago" without
// piggy-backing on the framework's clock.
type HealthStatus struct {
	OK        bool      `json:"ok"`
	Message   string    `json:"message,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// LocalDispatcher is a Provider implementation that wraps the
// existing *Dispatcher in tools.go. It proves the Provider interface
// fits the current code without breaking anything — every Retain /
// Recall / Forget call delegates to the same handlers the live
// system uses today.
//
// Concurrency: thread-safe per the underlying Dispatcher contract
// (write paths use FileLock; read paths are stateless).
//
// Wiring: callers stay on *Dispatcher in production for PR-F3.
// LocalDispatcher is the reference impl that lets us land the
// Provider abstraction without a risky cutover. PR-F6+ will lift
// orchestrator/sidecar call sites to Provider once the second impl
// (an HTTP-backed external store) is ready to choose between.
type LocalDispatcher struct {
	d *Dispatcher
}

// NewLocalDispatcher constructs the on-disk Provider impl backed by
// the supplied AgentContext. Same context the existing Dispatcher
// uses — callers can share an AgentContext across both surfaces.
func NewLocalDispatcher(ac AgentContext) *LocalDispatcher {
	return &LocalDispatcher{d: NewDispatcher(ac)}
}

// Retain delegates to the underlying memory.write handler. Returns
// the resolved tier source label as ID so a follow-up Forget(byID)
// can resolve it back to a path without state.
func (l *LocalDispatcher) Retain(ctx context.Context, req RetainRequest) (RetainResult, error) {
	if l == nil || l.d == nil {
		return RetainResult{}, errors.New("local dispatcher: not initialized")
	}
	args, err := json.Marshal(writeArgs{
		Tier:    req.Tier,
		Key:     req.Key,
		Content: req.Content,
		Mode:    req.Mode,
	})
	if err != nil {
		return RetainResult{}, fmt.Errorf("retain: marshal args: %w", err)
	}
	res, err := l.d.Dispatch(ctx, ToolCall{Name: "memory.write", Args: args})
	if err != nil {
		return RetainResult{}, fmt.Errorf("retain: dispatch: %w", err)
	}
	if res.IsError {
		return RetainResult{}, fmt.Errorf("retain: %s", res.Content)
	}
	id := tierSourceLabel(req.Tier, req.Key)
	bytes := 0
	if res.Metadata != nil {
		// Numeric metadata values may arrive as either int (in-process
		// dispatcher path) or float64 (round-tripped through JSON via
		// the sidecar MCP path). Accept both — a single type assertion
		// to int would silently fail in the JSON case and leave the
		// caller with bytes=0 (auditor catch, 2026-05-21).
		switch v := res.Metadata["bytes_written"].(type) {
		case int:
			bytes = v
		case int64:
			bytes = int(v)
		case float64:
			bytes = int(v)
		}
	}
	return RetainResult{ID: id, Bytes: bytes}, nil
}

// Recall delegates to the memory.search handler. The dispatcher's
// existing search returns a JSON envelope; we decode it back into
// the Provider's typed shape so callers don't have to re-parse.
func (l *LocalDispatcher) Recall(ctx context.Context, req RecallRequest) (RecallResult, error) {
	if l == nil || l.d == nil {
		return RecallResult{}, errors.New("local dispatcher: not initialized")
	}
	args, err := json.Marshal(searchArgs{
		Q:     req.Query,
		Tier:  req.Tier,
		Limit: req.Limit,
	})
	if err != nil {
		return RecallResult{}, fmt.Errorf("recall: marshal args: %w", err)
	}
	res, err := l.d.Dispatch(ctx, ToolCall{Name: "memory.search", Args: args})
	if err != nil {
		return RecallResult{}, fmt.Errorf("recall: dispatch: %w", err)
	}
	if res.IsError {
		return RecallResult{}, fmt.Errorf("recall: %s", res.Content)
	}
	// Decode the envelope the dispatcher produced. Shape:
	// { "hits": [{source, snippet, line}], "quarantined": [...] }
	var envelope struct {
		Hits []struct {
			Source  string `json:"source"`
			Snippet string `json:"snippet"`
			Line    int    `json:"line"`
		} `json:"hits"`
		Quarantined []struct {
			Source string `json:"source"`
		} `json:"quarantined"`
	}
	if err := json.Unmarshal([]byte(res.Content), &envelope); err != nil {
		return RecallResult{}, fmt.Errorf("recall: decode envelope: %w", err)
	}
	out := RecallResult{Hits: make([]RecallSnippet, 0, len(envelope.Hits))}
	for _, h := range envelope.Hits {
		out.Hits = append(out.Hits, RecallSnippet{
			Source:  h.Source,
			Snippet: h.Snippet,
			Line:    h.Line,
			Score:   1.0,
		})
	}
	for _, q := range envelope.Quarantined {
		out.Quarantined = append(out.Quarantined, q.Source)
	}
	return out, nil
}

// Forget removes one item by ID. The local impl resolves the ID
// (tier source label) back to its filesystem path under the agent
// or crew memory dir and unlinks it. A no-match is NOT an error —
// returning Removed=0 matches the contract documented on
// ForgetRequest. DataSubjectID cascade is not implemented in the
// local impl (PR-F1 handles GDPR cascade separately at the API
// layer); a non-empty DataSubjectID with an empty ID returns an
// error so callers know to use the cascade endpoint instead.
func (l *LocalDispatcher) Forget(ctx context.Context, req ForgetRequest) (ForgetResult, error) {
	if l == nil || l.d == nil {
		return ForgetResult{}, errors.New("local dispatcher: not initialized")
	}
	// Exactly-one-selector contract: the doc on ForgetRequest says the
	// caller picks per-id (ID set, DataSubjectID empty) OR cascade
	// (DataSubjectID set, ID empty), never both. Earlier code accepted
	// both and silently proceeded with the ID branch, leaving a
	// cascade-intent silently downgraded to a single-row delete —
	// auditable governance hole. Now we reject the both-set case
	// explicitly so a malformed caller fails loud. CodeRabbit
	// round-9 catch.
	if req.ID == "" && req.DataSubjectID == "" {
		return ForgetResult{}, errors.New("forget: must set exactly one of ID or DataSubjectID")
	}
	if req.ID != "" && req.DataSubjectID != "" {
		return ForgetResult{}, errors.New("forget: must set exactly one of ID or DataSubjectID, not both (mixing per-id and cascade selectors is ambiguous)")
	}
	if req.ID == "" && req.DataSubjectID != "" {
		return ForgetResult{}, errors.New("forget: DataSubjectID cascade not implemented in local provider; use the PR-F1 API endpoint")
	}
	// Resolve the ID back to an absolute path. ID is a tier-relative
	// label produced by tierSourceLabel — split it into tier + key
	// and reuse the dispatcher's resolvePath.
	tier, key := splitTierLabel(req.ID)
	path, err := l.d.resolvePath(tier, key)
	if err != nil {
		return ForgetResult{}, fmt.Errorf("forget: resolve %q: %w", req.ID, err)
	}
	// Defence in depth: never delete a symlink (would follow into
	// arbitrary host paths). The dispatcher's assertMemoryFile
	// covers this for read/write; forget needs the same check.
	if err := l.d.assertMemoryFile(path); err != nil {
		return ForgetResult{}, fmt.Errorf("forget: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ForgetResult{}, err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ForgetResult{Removed: 0}, nil
		}
		return ForgetResult{}, fmt.Errorf("forget: remove %q: %w", req.ID, err)
	}
	return ForgetResult{Removed: 1}, nil
}

// Health probes the agent memory dir for writability. A backend
// that can't write is degraded — surface it loudly to the operator
// rather than silently dropping subsequent Retain calls. CrewDir is
// optional (solo agents don't have one); if set, it's also probed.
func (l *LocalDispatcher) Health(ctx context.Context) HealthStatus {
	now := time.Now().UTC()
	if l == nil || l.d == nil {
		return HealthStatus{OK: false, Message: "not initialized", CheckedAt: now}
	}
	if err := ctx.Err(); err != nil {
		return HealthStatus{OK: false, Message: "context cancelled: " + err.Error(), CheckedAt: now}
	}
	if l.d.ctx.AgentMemoryDir == "" {
		return HealthStatus{OK: false, Message: "agent memory dir unset", CheckedAt: now}
	}
	for _, dir := range []string{l.d.ctx.AgentMemoryDir, l.d.ctx.CrewMemoryDir} {
		if dir == "" {
			continue
		}
		info, err := os.Stat(dir)
		if err != nil {
			return HealthStatus{OK: false, Message: fmt.Sprintf("stat %s: %v", dir, err), CheckedAt: now}
		}
		if !info.IsDir() {
			return HealthStatus{OK: false, Message: fmt.Sprintf("%s is not a directory", dir), CheckedAt: now}
		}
		// Probe write access by creating + removing a temp file. The
		// alternative (parsing unix mode bits) is unreliable across
		// bind mounts where the apparent permissions don't reflect
		// what the kernel will actually allow.
		probe := filepath.Join(dir, ".crewship-health-probe")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
			return HealthStatus{OK: false, Message: fmt.Sprintf("probe write %s: %v", dir, err), CheckedAt: now}
		}
		_ = os.Remove(probe)
	}
	return HealthStatus{OK: true, CheckedAt: now}
}

// splitTierLabel reverses tierSourceLabel for Forget's resolvePath
// call. Labels are flat strings ("AGENT.md", "CREW.md",
// "PERSONA.md", "pins.md", "lessons.md", "daily/YYYY-MM-DD.md",
// "peers/<slug>.md"). Returns (tier, key) where key is "" for the
// single-file tiers.
func splitTierLabel(label string) (tier, key string) {
	switch label {
	case "AGENT.md":
		return "AGENT", ""
	case "CREW.md":
		return "CREW", ""
	case "PERSONA.md":
		return "PERSONA", ""
	case "pins.md":
		return "pins", ""
	case "lessons.md":
		return "lessons", ""
	}
	if strings.HasPrefix(label, "daily/") && strings.HasSuffix(label, ".md") {
		return "daily", strings.TrimSuffix(strings.TrimPrefix(label, "daily/"), ".md")
	}
	if strings.HasPrefix(label, "peers/") && strings.HasSuffix(label, ".md") {
		return "peers", strings.TrimSuffix(strings.TrimPrefix(label, "peers/"), ".md")
	}
	// Unknown shape — fall back to "" tier so resolvePath rejects it
	// cleanly rather than silently producing a host path.
	return "", ""
}
