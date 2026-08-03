package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/safepath"
)

// AgentContext carries the per-call routing data the dispatcher needs
// to resolve a tier to a concrete filesystem path. Callers (sidecar
// MCP handler, orchestrator adapter wrapper) build this from the run
// request before invoking Dispatch.
type AgentContext struct {
	AgentID        string
	CrewID         string
	WorkspaceID    string
	AgentMemoryDir string // .../crew/agents/{slug}/.memory/
	CrewMemoryDir  string // .../crew/shared/.memory/ (empty for solo agents)
}

// ToolCall is the wire shape of a function-calling invocation from the
// model, decoded once and dispatched. Args is the raw JSON object the
// model produced — the dispatcher unmarshals it per-handler against
// the schema declared in ToolSchemas().
type ToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ToolResult is the wire shape returned to the model as tool_result.
// IsError=true is preferred over returning a Go error because it
// allows the model to recover (retry, adjust args) without crashing
// the run — matches Anthropic + OpenAI tool_result conventions.
type ToolResult struct {
	Content  string         `json:"content"`
	IsError  bool           `json:"is_error,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ToolSchema is the per-tool registration record adapters use to wire
// the tool into the model's tool palette. InputSchema is a raw JSON
// blob (JSON Schema Draft 2020-12) so adapters can pass it verbatim
// to whichever provider API they target (Anthropic tool spec, OpenAI
// function spec, Gemini function declaration, MCP tool descriptor).
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Per-tier caps — single source of truth for the dispatcher. Numbers
// match PRD §6 F1: 4k/4k/8k stays mainstream; daily lowered to 30k
// (was 100k in main); PERSONA.md and per-user peer cards at 1500 B
// (PR-E spec). lessons.md is flock-managed by lesson_writer (PR-Z
// Z.7) and not capped at the tool surface.
const (
	capAgentBytes   = 4000
	capCrewBytes    = 4000
	capPersonaBytes = 1500
	capPinsBytes    = 8000
	capDailyBytes   = 30000
	capPeerBytes    = 1500
	softCapPct      = 0.80

	// maxSearchLimit is the hard upper bound on the number of hits
	// memory.search returns. It is BOTH the clamp applied to the
	// agent-supplied `limit` and the constant used to pre-size the
	// result slice — deriving the slice capacity from the (untrusted)
	// request field would be an unbounded-allocation sink even though
	// the clamp already bounds it in practice. Using the constant keeps
	// the allocation provably O(1) and independent of request input.
	maxSearchLimit = 20
)

// validTiers is the closed enum the dispatcher accepts. Keep in sync
// with the JSON Schema enum in ToolSchemas() — a mismatch would let
// an adapter advertise a tier the dispatcher rejects.
var validTiers = map[string]struct{}{
	"AGENT":   {},
	"CREW":    {},
	"PERSONA": {},
	"pins":    {},
	"daily":   {},
	"peers":   {},
	"lessons": {},
}

// ToolSchemas returns the four memory tools the model can call. The
// returned map is fresh per call (defensive copy of the underlying
// constants) so an adapter can't mutate one schema and have the
// change leak to the next call.
func ToolSchemas() map[string]ToolSchema {
	return map[string]ToolSchema{
		"memory.read": {
			Name: "memory.read",
			Description: "Read the contents of an agent memory file. Returns the file body as text. " +
				"A missing file is normal for a fresh agent — empty content is returned without error.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"tier": {
						"type": "string",
						"enum": ["AGENT", "CREW", "PERSONA", "pins", "daily", "peers", "lessons"],
						"description": "Memory tier to read. AGENT/CREW/PERSONA/pins/lessons map to a single file each; daily and peers require 'key'."
					},
					"key": {
						"type": "string",
						"description": "Required for tier='daily' (e.g. '2026-05-21') and tier='peers' (e.g. user slug). Ignored for other tiers."
					}
				},
				"required": ["tier"],
				"additionalProperties": false
			}`),
		},
		"memory.write": {
			Name: "memory.write",
			Description: "Persist content to an agent memory file (the lessons tier is intentionally NOT writable through this surface — lessons land via the F4.4 negative-learning evaluator which enforces schema + idempotency + locking). Use mode='replace' when reorganizing; " +
				"mode='append' to add new entries. Cap-aware: returns a warning at 80% of cap and a hard " +
				"error at 100% of cap so you must self-curate (drop older entries, summarize) before retrying.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"tier": {
						"type": "string",
						"enum": ["AGENT", "CREW", "PERSONA", "pins", "daily", "peers"]
					},
					"key": {
						"type": "string",
						"description": "Required for tier='daily' / 'peers'. Ignored elsewhere."
					},
					"content": {
						"type": "string",
						"description": "UTF-8 body to write. Subject to per-tier byte caps."
					},
					"mode": {
						"type": "string",
						"enum": ["replace", "append"],
						"description": "replace overwrites the file; append concatenates to existing content."
					}
				},
				"required": ["tier", "content", "mode"],
				"additionalProperties": false
			}`),
		},
		"memory.search": {
			Name: "memory.search",
			Description: "Keyword search across memory tiers. Returns up to 'limit' (max 20) ranked snippets " +
				"with the source file path so you can follow up with memory.read for full context.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"q": {
						"type": "string",
						"description": "Search query. Ask a plain question: the engine drops common function words and matches any remaining word, ranking a chunk that contains the whole phrase highest. File paths are not searched — use 'tier' to scope. FTS5 syntax (AND, OR, NOT, \"phrases\", prefix*) is passed through if you write it."
					},
					"tier": {
						"type": "string",
						"enum": ["AGENT", "CREW", "PERSONA", "pins", "daily", "peers", "lessons"],
						"description": "Optional scope. Omit to search every accessible tier."
					},
					"limit": {
						"type": "integer",
						"minimum": 1,
						"maximum": 20,
						"description": "Maximum number of hits. Values >20 are clamped to 20."
					}
				},
				"required": ["q"],
				"additionalProperties": false
			}`),
		},
		"memory.append_daily": {
			Name: "memory.append_daily",
			Description: "Append a timestamped entry to today's daily log (daily/YYYY-MM-DD.md). " +
				"Convenience wrapper over memory.write for the common case of session-log additions.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"entry": {
						"type": "string",
						"description": "Plain-text entry. The dispatcher adds an ISO 8601 timestamp prefix."
					}
				},
				"required": ["entry"],
				"additionalProperties": false
			}`),
		},
		"conversation.search": {
			Name: "conversation.search",
			Description: "Keyword (BM25) search across YOUR OWN past chat sessions. Returns up to 'limit' " +
				"ranked messages with the source session_id and timestamp so you can recall what was " +
				"discussed or decided in an earlier conversation. Scoped to your agent identity — you " +
				"cannot see another agent's history. Search is from-now-on: only sessions recorded after " +
				"this feature shipped are indexed.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"q": {
						"type": "string",
						"description": "Search query. Plain text; FTS5 operators are treated as literal words."
					},
					"limit": {
						"type": "integer",
						"minimum": 1,
						"maximum": 100,
						"description": "Maximum number of hits. Defaults to 20; values out of range are clamped."
					}
				},
				"required": ["q"],
				"additionalProperties": false
			}`),
		},
	}
}

// ConvSearcher is the narrow dependency the dispatcher needs to back the
// conversation.search tool. *conversation.Store satisfies it. Kept as an
// interface so the memory package does not import conversation (avoiding a
// dependency cycle) and so tests can inject a stub.
type ConvSearcher interface {
	Search(ctx context.Context, agentID, query string, limit int) ([]ConvSearchHit, error)
}

// ConvSearchHit is the dispatcher-facing shape of a conversation search
// result. It mirrors conversation.SearchHit; the server adapter converts
// between the two so neither package imports the other.
type ConvSearchHit struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	Role        string `json:"role"`
	Content     string `json:"content"`
	ToolSummary string `json:"tool_summary,omitempty"`
	Timestamp   string `json:"ts"`
}

// Dispatcher routes ToolCall to per-tool handlers. Stateless beyond
// the AgentContext, so callers can share an instance across the
// duration of a single agent turn without coordinating writes.
type Dispatcher struct {
	ctx        AgentContext
	now        func() time.Time
	convSearch ConvSearcher

	// agentIndex / crewIndex are the FTS5 engines memory.search ranks
	// against, one per memory root. Both optional: with neither wired
	// the dispatcher falls back to the substring scan (see
	// handleSearch), which is what a caller with no SQLite gets.
	agentIndex *Engine
	crewIndex  *Engine
}

// DispatcherOption configures a Dispatcher at construction. Variadic so
// existing NewDispatcher(ac) callers are unaffected.
type DispatcherOption func(*Dispatcher)

// WithConvSearcher wires the backend for the conversation.search tool.
// When unset, conversation.search returns a recoverable IsError result
// explaining the tool is unavailable rather than failing the run.
func WithConvSearcher(cs ConvSearcher) DispatcherOption {
	return func(d *Dispatcher) { d.convSearch = cs }
}

// WithSearchIndex wires the FTS5 engines behind memory.search and keeps
// them current on memory.write. agent MUST index AgentMemoryDir and crew
// MUST index CrewMemoryDir — the dispatcher maps an index hit back to a
// tier by resolving the engine's relative `file` under that root, and an
// engine pointed somewhere else resolves to files that fail the memory-
// root containment check and drop out. Either may be nil (a solo agent
// has no crew tier; a sidecar whose SQLite open failed has no engine at
// all), and with both nil memory.search falls back to the substring scan
// so the tool degrades instead of going dark.
func WithSearchIndex(agent, crew *Engine) DispatcherOption {
	return func(d *Dispatcher) {
		d.agentIndex = agent
		d.crewIndex = crew
	}
}

// AdvertisedTools is the ordered catalogue of memory tools the model is
// told it can call. It is the single source of truth for the MCP
// tools/list payload (internal/sidecar/memory_mcp.go) AND for what the
// wake prompt is allowed to name (internal/orchestrator/memory.go) —
// #1651 shipped a prompt telling a woken agent to run
// conversation.search when tools/list did not advertise it and nothing
// wired its backend, so the model was told to call a tool it could not
// see. Anything added here must be dispatchable; anything the prompt
// names must be here. Both directions are tested.
//
// Order is fixed, not map order: adapters that cache the catalogue
// across runs would otherwise re-fetch on every Go map iteration.
//
// conversation.search is deliberately absent. Its handler and schema
// ship (ToolSchemas, handleConversationSearch) but the searchable
// mirror lives in the host database, which the in-container dispatcher
// cannot reach — advertising it would mean advertising a tool that
// answers "not available on this deployment".
func AdvertisedTools() []string {
	return []string{"memory.read", "memory.write", "memory.search", "memory.append_daily"}
}

// NewDispatcher builds a Dispatcher bound to the given AgentContext.
func NewDispatcher(ac AgentContext, opts ...DispatcherOption) *Dispatcher {
	d := &Dispatcher{ctx: ac, now: func() time.Time { return time.Now().UTC() }}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Dispatch is the single entry point. Unknown tool names return
// IsError=true ToolResult (recoverable) instead of a Go error
// (fatal) so the model can correct and retry.
func (d *Dispatcher) Dispatch(ctx context.Context, call ToolCall) (ToolResult, error) {
	switch call.Name {
	case "memory.read":
		return d.handleRead(ctx, call.Args)
	case "memory.write":
		return d.handleWrite(ctx, call.Args)
	case "memory.search":
		return d.handleSearch(ctx, call.Args)
	case "memory.append_daily":
		return d.handleAppendDaily(ctx, call.Args)
	case "conversation.search":
		return d.handleConversationSearch(ctx, call.Args)
	default:
		return ToolResult{
			IsError: true,
			Content: fmt.Sprintf("unknown tool: %q. Available: memory.read, memory.write, memory.search, memory.append_daily, conversation.search.", call.Name),
		}, nil
	}
}

type convSearchArgs struct {
	Q     string `json:"q"`
	Limit int    `json:"limit"`
}

// handleConversationSearch backs the conversation.search tool. It is
// agent-scoped: the agent_id comes from the dispatcher's AgentContext, never
// from the model's args, so an agent can only search its own history. A
// missing searcher (dispatcher built without WithConvSearcher) is a
// recoverable IsError rather than a panic so a run on a build without the
// mirror degrades gracefully.
func (d *Dispatcher) handleConversationSearch(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{IsError: true, Content: "conversation.search: cancelled: " + err.Error()}, nil
	}
	var a convSearchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return ToolResult{IsError: true, Content: "conversation.search: invalid args: " + err.Error()}, nil
	}
	if strings.TrimSpace(a.Q) == "" {
		return ToolResult{IsError: true, Content: "conversation.search: q is required"}, nil
	}
	if d.convSearch == nil {
		return ToolResult{IsError: true, Content: "conversation.search: not available on this deployment"}, nil
	}
	if strings.TrimSpace(d.ctx.AgentID) == "" {
		return ToolResult{IsError: true, Content: "conversation.search: agent identity unavailable"}, nil
	}
	if a.Limit <= 0 || a.Limit > 100 {
		a.Limit = 20
	}

	hits, err := d.convSearch.Search(ctx, d.ctx.AgentID, a.Q, a.Limit)
	if err != nil {
		return ToolResult{IsError: true, Content: "conversation.search: " + err.Error()}, nil
	}
	envelope := map[string]any{"hits": hits, "query": a.Q, "count": len(hits)}
	body, _ := json.MarshalIndent(envelope, "", "  ")
	return ToolResult{Content: string(body)}, nil
}

type readArgs struct {
	Tier string `json:"tier"`
	Key  string `json:"key"`
}

func (d *Dispatcher) handleRead(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{IsError: true, Content: "memory.read: cancelled: " + err.Error()}, nil
	}
	var a readArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return ToolResult{IsError: true, Content: "memory.read: invalid args: " + err.Error()}, nil
	}
	if _, ok := validTiers[a.Tier]; !ok {
		return ToolResult{IsError: true, Content: fmt.Sprintf("memory.read: unknown tier %q", a.Tier)}, nil
	}
	path, err := d.resolvePath(a.Tier, a.Key)
	if err != nil {
		return ToolResult{IsError: true, Content: "memory.read: " + err.Error()}, nil
	}
	if err := d.assertMemoryFile(path); err != nil {
		return ToolResult{IsError: true, Content: "memory.read: " + err.Error()}, nil
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{IsError: true, Content: "memory.read: cancelled: " + err.Error()}, nil
	}
	// readRegularNoFollow (not os.ReadFile): assertMemoryFile Lstat-rejects a
	// pre-existing symlink, but an agent can race a regular→symlink swap in the
	// TOCTOU window before this read (#1043). O_NOFOLLOW refuses the swapped
	// link and the regular-file check refuses FIFOs/devices — the same
	// primitive the indexer uses.
	data, err := readRegularNoFollow(path)
	if errors.Is(err, os.ErrNotExist) {
		return ToolResult{Content: ""}, nil
	}
	if err != nil {
		return ToolResult{IsError: true, Content: "memory.read: " + err.Error()}, nil
	}
	// PR-A F1: inbound prompt-injection scan. Memory files are written
	// by the same agent that reads them, but external operators / past
	// sessions / future ingestion paths (skill marketplace import,
	// crew-shared CREW.md edited via PR) can land poisoned content.
	// Catching it on the read path means the model never sees the
	// payload even if the file was authored maliciously.
	body := string(data)
	if hit := ScanContent(body); hit != nil {
		label := tierSourceLabel(a.Tier, a.Key)
		placeholder, sha, qerr := Quarantine(d.ctx.AgentMemoryDir, label, body, hit)
		if qerr != nil {
			// If we can't quarantine, surface IsError instead of
			// returning the poisoned body — fail closed.
			return ToolResult{
				IsError: true,
				Content: fmt.Sprintf("memory.read: scan hit %s/%s but quarantine failed: %v", hit.Category, hit.Pattern, qerr),
			}, nil
		}
		return ToolResult{
			Content: placeholder,
			Metadata: map[string]any{
				"quarantined":         true,
				"quarantine_sha256":   sha,
				"quarantine_category": hit.Category,
				"quarantine_pattern":  hit.Pattern,
				"source":              label,
			},
		}, nil
	}
	return ToolResult{
		Content: body,
		Metadata: map[string]any{
			"source": tierSourceLabel(a.Tier, a.Key),
			"bytes":  len(data),
		},
	}, nil
}

type writeArgs struct {
	Tier    string `json:"tier"`
	Key     string `json:"key"`
	Content string `json:"content"`
	Mode    string `json:"mode"`
}

func (d *Dispatcher) handleWrite(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{IsError: true, Content: "memory.write: cancelled: " + err.Error()}, nil
	}
	var a writeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return ToolResult{IsError: true, Content: "memory.write: invalid args: " + err.Error()}, nil
	}
	if _, ok := validTiers[a.Tier]; !ok {
		return ToolResult{IsError: true, Content: fmt.Sprintf("memory.write: unknown tier %q", a.Tier)}, nil
	}
	// Security gate: lessons tier MUST flow through
	// consolidate.WriteLesson (PR-Z Z.7 + F4.4) which enforces YAML
	// schema, idempotency-by-ID, atomic-rename, and flock. Allowing
	// the raw dispatcher write here would let an agent (a) bypass
	// cap validation because capForTier("lessons") returns 0
	// = "no cap", (b) bypass the kind/source closed-enum guard so
	// downstream filters silently drop entries, (c) bypass the
	// idempotency key and accumulate duplicates, and (d) corrupt
	// the file with freeform text ReadLessons cannot parse. The
	// right surface is the F4.4 negative-learning endpoint, which
	// routes through WriteLesson with the policy + self_learning
	// gates intact. Auditor flagged this as a persistence attack
	// vector in the 2026-05-21 pre-launch audit; the tombstone
	// stays here until any agent-author surface needs lessons
	// writes (none does today; consolidator is the only writer).
	if a.Tier == "lessons" {
		return ToolResult{
			IsError: true,
			Content: "memory.write: lessons tier is read-only via this surface; submit a lesson through the F4.4 negative-learning evaluator (consolidate.WriteLesson enforces schema + idempotency + locking that this dispatcher does not).",
		}, nil
	}
	if a.Mode != "replace" && a.Mode != "append" {
		return ToolResult{IsError: true, Content: "memory.write: mode must be 'replace' or 'append'"}, nil
	}
	if a.Content == "" {
		return ToolResult{IsError: true, Content: "memory.write: empty content rejected"}, nil
	}
	cap, err := capForTier(a.Tier)
	if err != nil {
		return ToolResult{IsError: true, Content: "memory.write: " + err.Error()}, nil
	}
	path, err := d.resolvePath(a.Tier, a.Key)
	if err != nil {
		return ToolResult{IsError: true, Content: "memory.write: " + err.Error()}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ToolResult{IsError: true, Content: "memory.write: mkdir: " + err.Error()}, nil
	}
	// Reject if the resolved path is a symlink or escapes the memory
	// roots — guards against a pre-existing AGENT.md / daily/*.md
	// symlink that would otherwise let os.WriteFile overwrite an
	// arbitrary path the process can reach.
	if err := d.assertMemoryFile(path); err != nil {
		return ToolResult{IsError: true, Content: "memory.write: " + err.Error()}, nil
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{IsError: true, Content: "memory.write: cancelled: " + err.Error()}, nil
	}

	// Serialise the read-modify-write window so two concurrent appends
	// can't each pass the cap check against the same pre-existing size
	// and then sequentially write past the cap. Same lock primitive
	// the lesson writer uses (writer.go FileLock / flock).
	lk := NewFileLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		return ToolResult{IsError: true, Content: "memory.write: lock: " + err.Error()}, nil
	}
	defer func() { _ = lk.Unlock() }()

	if err := ctx.Err(); err != nil {
		return ToolResult{IsError: true, Content: "memory.write: cancelled: " + err.Error()}, nil
	}
	// Re-check symlink containment after acquiring the lock — a writer
	// could have raced us between resolvePath and Lock() to swap the
	// file for a symlink. The lock now serialises further races.
	if err := d.assertMemoryFile(path); err != nil {
		return ToolResult{IsError: true, Content: "memory.write: " + err.Error()}, nil
	}

	// Read the current on-disk body once for both modes. append uses it
	// as the prefix; replace discards it for the new content but still
	// surfaces it as current_entries in the overflow guidance below (PR
	// #6) so the agent can consolidate the existing file in-turn. The
	// store stays a pure bounded store — this read is only for the
	// guidance payload, it never widens the cap.
	// readRegularNoFollow (not os.ReadFile): same TOCTOU no-follow guard as the
	// read path (#1043) — append re-reads the on-disk body after taking the
	// lock, and must refuse a raced symlink/FIFO swap rather than follow it.
	old, err := readRegularNoFollow(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ToolResult{IsError: true, Content: "memory.write: " + err.Error()}, nil
	}
	currentBody := string(old)

	var data []byte
	existing := len(old)
	if a.Mode == "append" {
		data = append(old, []byte(a.Content)...)
	} else {
		data = []byte(a.Content)
	}

	// Audit A10.1 / P1-A: write-path scanner symmetry. handleRead has
	// scanned every byte returned to the model since PR-A F1, but the
	// write-path historically trusted the agent that called memory.write.
	// That single-layer defence collapses in two scenarios the audit
	// LIVE-verified:
	//
	//  1. Indirect injection -- an agent ingests poison via tool returns
	//     (web fetch, file read, peer query) and persists the literal
	//     bytes via memory.write. The next read trips the read-path
	//     scanner, but the poison sits on disk in the meantime and is
	//     visible to any other reader (audit_watcher, fts5 indexer,
	//     operator viewing the file).
	//
	//  2. Confused-deputy -- a future code path reads memory files via
	//     a route that bypasses the dispatcher (raw filesystem walk,
	//     debug endpoint, backup roundtrip) and serves the poison.
	//
	// Scanning at the write step means poison never lands on disk in the
	// first place. Same Quarantine helper as the read path -- the
	// original payload lands under .quarantine/<sha>.md for operator
	// review, the caller gets an IsError result with the category +
	// pattern so the agent can see why the write was rejected and
	// (hopefully) self-correct.
	if hit := ScanContent(string(data)); hit != nil {
		label := tierSourceLabel(a.Tier, a.Key)
		_, sha, qerr := Quarantine(d.ctx.AgentMemoryDir, label, string(data), hit)
		if qerr != nil {
			// Quarantine write itself failed -- still refuse the
			// memory.write so poison doesn't reach disk, surface the
			// dual failure to the caller.
			return ToolResult{
				IsError: true,
				Content: fmt.Sprintf("memory.write: scan hit %s/%s; quarantine also failed: %v", hit.Category, hit.Pattern, qerr),
			}, nil
		}
		return ToolResult{
			IsError: true,
			Content: fmt.Sprintf("memory.write: rejected — scanner caught %s/%s. "+
				"Original content moved to .quarantine/%s.md for operator review. "+
				"Rewrite the content without the offending pattern before retrying.",
				hit.Category, hit.Pattern, sha),
			Metadata: map[string]any{
				"quarantined":         true,
				"quarantine_sha256":   sha,
				"quarantine_category": hit.Category,
				"quarantine_pattern":  hit.Pattern,
				"source":              label,
				"tier":                a.Tier,
			},
		}, nil
	}

	if cap > 0 && len(data) > cap {
		// PR #6: instead of a bare rejection, hand the agent everything
		// it needs to fix this within the SAME turn — the current file
		// body (current_entries) plus a usage string — and tell it to
		// consolidate that body and retry the write now, rather than
		// abandoning the write. The store does not consolidate for the
		// agent; it just surfaces the material so the agent can.
		return ToolResult{
			IsError: true,
			Content: fmt.Sprintf(
				"memory.write: cap exceeded for tier=%s. Final would be %d bytes; cap is %d (%s). "+
					"Consolidate the current entries shown in metadata.current_entries — merge "+
					"duplicates, drop stale lines, summarize — then retry this write in this turn "+
					"with mode='replace' carrying the consolidated body.",
				a.Tier, len(data), cap, capUsage(existing, cap)),
			Metadata: map[string]any{
				"tier":            a.Tier,
				"cap_bytes":       cap,
				"projected_size":  len(data),
				"current_size":    existing,
				"current_entries": currentBody,
				"usage":           capUsage(existing, cap),
			},
		}, nil
	}

	// 2a: durable, atomic persist — a memory.write that returns success
	// MUST be on stable storage (fsync'd) and never leave a torn file. The
	// old os.WriteFile only reached the page cache and truncated in place,
	// so "ok" could be returned for a write a crash would lose, and an
	// interrupted write corrupted the file. writeFileDurable fails closed:
	// on any error the prior content is intact and we return is_error, so
	// the model (which surfaces is_error — verified on dev2) reports the
	// failure instead of a false "DONE".
	if err := writeFileDurable(path, data, 0o644); err != nil {
		return ToolResult{IsError: true, Content: "memory.write: " + err.Error()}, nil
	}

	label := d.pathToSourceLabel(path)
	res := ToolResult{
		Content: fmt.Sprintf("ok: %d bytes written to %s", len(data), a.Tier),
		Metadata: map[string]any{
			"source":        tierSourceLabel(a.Tier, a.Key),
			"bytes_written": len(data),
			"cap_bytes":     cap,
			"cap_pct":       capPct(len(data), cap),
		},
	}

	// Keep the search index in step with the file we just wrote. Nothing
	// else in the container does: memory.StartWatcher has no production
	// caller, the sidecar's post-write ReindexPath only runs on the
	// legacy HTTP route, and the crew ticker is 60s and crew-only. With
	// memory.search ranking off the index (#1651), skipping this would
	// make an agent's own notes unfindable for the rest of the session —
	// precisely the recall the [MEMORY GAP] block sends it to look for.
	// Incremental (O(this file), not O(corpus)) and best-effort: a failed
	// reindex costs searchability of one file until the next boot, never
	// the write, so it is reported in metadata rather than turned into an
	// error the model has to handle.
	if idx := d.indexForTier(a.Tier); idx != nil {
		if _, err := idx.ReindexPath(ctx, label); err != nil {
			res.Metadata["search_index_updated"] = false
		} else {
			res.Metadata["search_index_updated"] = true
		}
	}
	if cap > 0 && float64(len(data)) >= float64(cap)*softCapPct {
		// PR #6 parity with the hard-error branch: the write succeeded,
		// but it's close enough to the cap that the NEXT append will be
		// rejected. Surface the just-written body as current_entries +
		// usage and steer the agent to consolidate it and re-write the
		// consolidated form in this same turn, while it still has the
		// content in context — don't make it wait for the hard error.
		res.Content += fmt.Sprintf(
			". warning: approaching cap (%s). Consolidate the entries in "+
				"metadata.current_entries — merge duplicates, drop stale lines, "+
				"summarize — and rewrite the consolidated body with mode='replace' "+
				"in this turn to avoid the next append being rejected.",
			capUsage(len(data), cap))
		res.Metadata["current_entries"] = string(data)
		res.Metadata["usage"] = capUsage(len(data), cap)
	}
	return res, nil
}

type searchArgs struct {
	Q     string `json:"q"`
	Tier  string `json:"tier"`
	Limit int    `json:"limit"`
}

// searchHit is one entry in the memory.search result envelope. Source
// is the tier label ("AGENT.md", "daily/2026-05-21.md"), never the
// absolute container path — leaking `/output/agent_xxx/.memory/...`
// discloses the bind-mount layout and is symmetric to the read/write
// metadata fix. Score is populated only on the ranked (FTS5) path; the
// substring fallback has no ranking signal to report.
type searchHit struct {
	Source  string  `json:"source"`
	Snippet string  `json:"snippet"`
	Line    int     `json:"line"`
	Score   float64 `json:"score,omitempty"`
}

// quarantineNote reports a file the injection scanner rejected. It
// replaces that file's hits entirely — see searchScan / searchIndexed.
type quarantineNote struct {
	Source      string `json:"source"`
	Category    string `json:"quarantine_category"`
	Pattern     string `json:"quarantine_pattern"`
	SHA256      string `json:"quarantine_sha256"`
	Placeholder string `json:"placeholder"`
}

// errSearchCancelled is the sentinel the two search paths return when
// the caller's context goes away mid-walk, so handleSearch can render
// the one cancellation message both paths share.
var errSearchCancelled = errors.New("cancelled")

// handleSearch answers the memory.search tool call.
//
// Ranked path (production): when an FTS5 engine is wired
// (WithSearchIndex) the query runs through memory.HybridSearch, the
// same RRF primitive the HTTP /api/v1/memory/search/hybrid handler
// uses. Until #1651 this function walked candidateFiles(tier) and
// matched lowercase substrings while the FTS5 index the sidecar builds
// at boot — and rebuilds on every write — sat unread, so the
// [MEMORY GAP] block woke an agent, told it to search, and handed it a
// grep that could not match two words in the wrong order.
//
// Fallback path: with no engine wired (LocalDispatcher, a sidecar whose
// SQLite open failed, tests) the substring scan still answers, so the
// tool degrades rather than going dark.
//
// The episodic half of HybridSearch (vec+BM25 over journal_entries)
// stays unwired here on purpose: it needs a *sql.DB the in-container
// dispatcher does not have. HybridSearch documents nil db/embedder as
// the FTS-only fallback, so passing them costs nothing and the seam is
// ready for a caller that does have a database.
//
// Two security properties hold on BOTH paths:
//   - Hits carry the tier label, never the absolute path.
//   - Every file that would contribute a snippet is run through
//     ScanContent FIRST. Injection-positive files are quarantined and
//     surfaced in a separate `quarantined` array instead of
//     contributing raw snippets to `hits`, keeping search consistent
//     with the read-path fail-closed contract: a poisoned file can
//     never return its payload to the model via the search tool.
func (d *Dispatcher) handleSearch(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{IsError: true, Content: "memory.search: cancelled: " + err.Error()}, nil
	}
	var a searchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return ToolResult{IsError: true, Content: "memory.search: invalid args: " + err.Error()}, nil
	}
	if strings.TrimSpace(a.Q) == "" {
		return ToolResult{IsError: true, Content: "memory.search: q is required"}, nil
	}
	if a.Limit <= 0 || a.Limit > maxSearchLimit {
		a.Limit = maxSearchLimit
	}
	if a.Tier != "" {
		if _, ok := validTiers[a.Tier]; !ok {
			return ToolResult{IsError: true, Content: fmt.Sprintf("memory.search: unknown tier %q", a.Tier)}, nil
		}
	}

	var (
		hits        []searchHit
		quarantined []quarantineNote
		err         error
	)
	if d.agentIndex != nil || d.crewIndex != nil {
		hits, quarantined, err = d.searchIndexed(ctx, a)
	} else {
		hits, quarantined, err = d.searchScan(ctx, a)
	}
	if err != nil {
		return ToolResult{IsError: true, Content: "memory.search: cancelled: " + err.Error()}, nil
	}

	envelope := map[string]any{"hits": hits, "query": a.Q}
	if len(quarantined) > 0 {
		envelope["quarantined"] = quarantined
	}
	body, _ := json.MarshalIndent(envelope, "", "  ")
	return ToolResult{Content: string(body)}, nil
}

// searchIndexed ranks the query with memory.HybridSearch over the wired
// FTS5 engines and maps the winners back onto the tool's envelope.
//
// Two mapping jobs the index cannot do for us:
//
//   - Tier. An engine indexes a directory, not a tier, so the `tier`
//     argument is applied here by resolving each hit's relative file
//     back to the tier that owns it. A file under the memory root that
//     belongs to no tier (a stray note some other tool dropped in) is
//     dropped: the index walks every .md it finds, which is a wider set
//     than candidateFiles ever exposed to the model.
//   - Freshness of the fail-closed scan. The indexed chunk is a copy
//     taken at index time; the quarantine decision has to be made
//     against what is on disk NOW, so each source file is read (once)
//     and scanned before any of its chunks are allowed through.
func (d *Dispatcher) searchIndexed(ctx context.Context, a searchArgs) ([]searchHit, []quarantineNote, error) {
	type indexSource struct {
		engine *Engine
		root   string
		crew   bool
	}
	var sources []indexSource
	if d.agentIndex != nil && d.ctx.AgentMemoryDir != "" {
		sources = append(sources, indexSource{engine: d.agentIndex, root: d.ctx.AgentMemoryDir})
	}
	if d.crewIndex != nil && d.ctx.CrewMemoryDir != "" {
		sources = append(sources, indexSource{engine: d.crewIndex, root: d.ctx.CrewMemoryDir, crew: true})
	}

	// Pre-size from the constant, never from a.Limit: a.Limit is
	// request-controlled and feeding it to make() is an uncontrolled-
	// allocation sink. maxSearchLimit is the same value a.Limit is
	// clamped to above, so behaviour is unchanged.
	hits := make([]searchHit, 0, maxSearchLimit)
	var quarantined []quarantineNote

	// Over-fetch from the engines. Tier filtering and quarantine
	// suppression both remove rows AFTER ranking, so asking for exactly
	// a.Limit would under-return on a scoped query. The multiplier is a
	// constant applied to the already-clamped limit and the engine caps
	// at 50 of its own accord, so this stays bounded — and it is a SQL
	// LIMIT, not a slice capacity.
	fetch := a.Limit * indexOverfetch
	if fetch > maxIndexFetch {
		fetch = maxIndexFetch
	}

	// One read + one scan per distinct file, shared across all of that
	// file's chunks and across both engines.
	seen := make(map[string]searchFileState)

	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return nil, nil, errSearchCancelled
		}
		ranked, err := HybridSearch(ctx, src.engine, nil, nil, HybridQuery{
			WorkspaceID: d.ctx.WorkspaceID,
			AgentID:     d.ctx.AgentID,
			CrewID:      d.ctx.CrewID,
			Text:        a.Q,
			Limit:       fetch,
		})
		if err != nil {
			// HybridSearch swallows single-engine failures by design;
			// an error here is the whole call failing, and the other
			// engine may still have something to say.
			continue
		}
		for _, r := range ranked {
			if err := ctx.Err(); err != nil {
				return nil, nil, errSearchCancelled
			}
			if r.FTS == nil {
				// Episodic lane — not wired in-container, and its hits
				// carry no memory-tier identity to render.
				continue
			}
			rel := filepath.ToSlash(r.FTS.File)
			tier, ok := tierForRelPath(rel, src.crew)
			if !ok {
				continue
			}
			if a.Tier != "" && tier != a.Tier {
				continue
			}

			key := src.root + "\x00" + rel
			st, cached := seen[key]
			if !cached {
				st = d.readForSearch(src.root, rel)
				if st.poisoned {
					note, _ := d.quarantineNoteFor(rel, st.body)
					quarantined = append(quarantined, note)
				}
				seen[key] = st
			}
			if !st.readable || st.poisoned {
				continue
			}

			hits = append(hits, searchHit{
				Source:  rel,
				Snippet: r.FTS.Snippet,
				Line:    lineOfSnippet(st.body, r.FTS.Snippet),
				Score:   r.Score,
			})
		}
	}

	// Merge the per-engine lists on their RRF scores so the agent's and
	// the crew's best hits interleave by rank. Truncating per engine
	// instead would let a run of mediocre agent-tier chunks push the
	// crew's top hit out of a limit-sized result. Stable, so equal
	// scores keep agent-before-crew order.
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > a.Limit {
		hits = hits[:a.Limit]
	}
	return hits, quarantined, nil
}

// indexOverfetch / maxIndexFetch bound how many ranked rows the indexed
// path pulls per engine before tier + quarantine filtering. 50 is the
// engine's own ceiling (search.go), so asking for more is wasted work.
const (
	indexOverfetch = 3
	maxIndexFetch  = 50
)

// searchFileState is one indexed file as the search path sees it now:
// its current on-disk body, whether it could be read at all, and the
// scanner's verdict on it.
type searchFileState struct {
	body     string
	poisoned bool
	readable bool
}

// readForSearch reads one indexed file for the search path and reports
// whether it is readable and whether the injection scanner rejects it.
// The containment check runs first: the relative path comes out of the
// index, and an engine pointed at the wrong directory (or a file that
// became a symlink after it was indexed) must not turn into a read
// primitive.
func (d *Dispatcher) readForSearch(root, rel string) (st searchFileState) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := d.assertMemoryFile(p); err != nil {
		return st
	}
	data, err := readRegularNoFollow(p)
	if err != nil {
		return st
	}
	st.readable = true
	st.body = string(data)
	st.poisoned = ScanContent(st.body) != nil
	return st
}

// quarantineNoteFor moves a scanner-positive file into .quarantine and
// builds the note that replaces its hits. A failed quarantine write
// still yields a note (minus the payload-derived fields) because
// suppressing the hits matters more than recording why.
func (d *Dispatcher) quarantineNoteFor(label, body string) (quarantineNote, error) {
	hit := ScanContent(body)
	if hit == nil {
		return quarantineNote{}, nil
	}
	placeholder, sha, err := Quarantine(d.ctx.AgentMemoryDir, label, body, hit)
	if err != nil {
		return quarantineNote{Source: label, Category: hit.Category, Pattern: hit.Pattern}, err
	}
	return quarantineNote{
		Source:      label,
		Category:    hit.Category,
		Pattern:     hit.Pattern,
		SHA256:      sha,
		Placeholder: placeholder,
	}, nil
}

// tierForRelPath maps an index-relative file path back to the tier that
// owns it, mirroring candidateFiles in reverse. crew=true means the path
// came from the crew engine, where CREW.md is the only tier that exists.
// A path matching nothing returns ok=false and is dropped from results.
func tierForRelPath(rel string, crew bool) (string, bool) {
	if crew {
		if rel == "CREW.md" {
			return "CREW", true
		}
		return "", false
	}
	switch rel {
	case "AGENT.md":
		return "AGENT", true
	case "PERSONA.md":
		return "PERSONA", true
	case "pins.md":
		return "pins", true
	case "lessons.md":
		return "lessons", true
	}
	if !strings.HasSuffix(rel, ".md") {
		return "", false
	}
	for _, t := range []string{"daily", "peers"} {
		name, ok := strings.CutPrefix(rel, t+"/")
		// One level deep only: the tiers are flat directories, and a
		// nested path would not resolve through resolvePath either.
		if ok && name != "" && !strings.Contains(name, "/") {
			return t, true
		}
	}
	return "", false
}

// indexForTier returns the engine that indexes the tier's file, or nil
// when no engine is wired for it. CREW lives in the crew root, every
// other tier in the agent root.
func (d *Dispatcher) indexForTier(tier string) *Engine {
	if tier == "CREW" {
		return d.crewIndex
	}
	return d.agentIndex
}

// lineOfSnippet locates a chunk's first line in the file body so a hit
// can still say WHERE it came from. The FTS5 index stores chunk text
// without line numbers (engine.Search returns LineStart=0), and the
// model uses this to follow up with memory.read. Returns 0 when the
// line cannot be located — the field is advisory, never a path.
func lineOfSnippet(body, snippet string) int {
	first := ""
	for _, l := range strings.Split(snippet, "\n") {
		if strings.TrimSpace(l) != "" {
			first = l
			break
		}
	}
	if first == "" {
		return 0
	}
	for i, l := range strings.Split(body, "\n") {
		if l == first {
			return i + 1
		}
	}
	return 0
}

// searchScan is the unranked fallback: a lowercase substring match over
// the tier's candidate files, in path order. It is what every caller
// without an FTS5 engine gets, and it is the reason memory.search kept
// working at all before #1651 — but it cannot match terms in a
// different order than the file wrote them, which is most of what an
// agent asks for after a week away.
func (d *Dispatcher) searchScan(ctx context.Context, a searchArgs) ([]searchHit, []quarantineNote, error) {
	files := d.candidateFiles(a.Tier)
	// Pre-size from the constant, never from a.Limit: a.Limit is
	// request-controlled and feeding it to make() is an uncontrolled-
	// allocation sink. maxSearchLimit is the same value a.Limit is
	// clamped to above, so behaviour is unchanged.
	hits := make([]searchHit, 0, maxSearchLimit)
	var quarantined []quarantineNote
	needle := strings.ToLower(a.Q)
	for _, p := range files {
		if err := ctx.Err(); err != nil {
			return nil, nil, errSearchCancelled
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		body := string(data)
		label := d.pathToSourceLabel(p)
		// Fail-closed: a scan hit on the file means none of its lines
		// can flow into `hits`. Quarantine the content and record a
		// placeholder in the response instead — symmetric with the
		// read path.
		if ScanContent(body) != nil {
			note, _ := d.quarantineNoteFor(label, body)
			quarantined = append(quarantined, note)
			continue
		}
		for i, line := range strings.Split(body, "\n") {
			if strings.Contains(strings.ToLower(line), needle) {
				hits = append(hits, searchHit{Source: label, Snippet: line, Line: i + 1})
				if len(hits) >= a.Limit {
					break
				}
			}
		}
		if len(hits) >= a.Limit {
			break
		}
	}
	return hits, quarantined, nil
}

// pathToSourceLabel maps an absolute candidateFiles path back to the
// tier+key label exposed to the model. Mirrors candidateFiles' own
// path construction in reverse so search hits never carry absolute
// bind-mount paths (which would disclose container/host topology).
func (d *Dispatcher) pathToSourceLabel(p string) string {
	if d.ctx.AgentMemoryDir != "" {
		if rel, err := filepath.Rel(d.ctx.AgentMemoryDir, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	if d.ctx.CrewMemoryDir != "" {
		if rel, err := filepath.Rel(d.ctx.CrewMemoryDir, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(p)
}

type appendDailyArgs struct {
	Entry string `json:"entry"`
}

func (d *Dispatcher) handleAppendDaily(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var a appendDailyArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return ToolResult{IsError: true, Content: "memory.append_daily: invalid args: " + err.Error()}, nil
	}
	if strings.TrimSpace(a.Entry) == "" {
		return ToolResult{IsError: true, Content: "memory.append_daily: entry is required"}, nil
	}
	today := d.now().Format("2006-01-02")
	stamp := d.now().Format(time.RFC3339)
	line := fmt.Sprintf("- %s — %s\n", stamp, a.Entry)
	inner, _ := json.Marshal(writeArgs{
		Tier:    "daily",
		Key:     today,
		Content: line,
		Mode:    "append",
	})
	return d.handleWrite(ctx, inner)
}

func (d *Dispatcher) resolvePath(tier, key string) (string, error) {
	switch tier {
	case "AGENT":
		return filepath.Join(d.ctx.AgentMemoryDir, "AGENT.md"), nil
	case "CREW":
		if d.ctx.CrewMemoryDir == "" {
			return "", errors.New("crew tier unavailable for solo agent (no crew memory dir)")
		}
		return filepath.Join(d.ctx.CrewMemoryDir, "CREW.md"), nil
	case "PERSONA":
		return filepath.Join(d.ctx.AgentMemoryDir, "PERSONA.md"), nil
	case "pins":
		return filepath.Join(d.ctx.AgentMemoryDir, "pins.md"), nil
	case "lessons":
		return filepath.Join(d.ctx.AgentMemoryDir, "lessons.md"), nil
	case "daily":
		if key == "" {
			key = d.now().Format("2006-01-02")
		}
		// key is one path segment, so it goes through the component form
		// of the guard: safepath.JoinUnder validates "daily" and the
		// key'd filename and confines the join under AgentMemoryDir
		// (rejecting residual traversal / NUL / separator smuggling in
		// one place). The ContainsAny check above is the tier's own key
		// policy — it also refuses ".." *inside* a name, which is
		// stricter than a path guard needs to be and is kept as-is.
		if strings.ContainsAny(key, `/\`) || strings.Contains(key, "..") {
			return "", fmt.Errorf("invalid daily key %q", key)
		}
		p, err := safepath.JoinUnder(d.ctx.AgentMemoryDir, "daily", key+".md")
		if err != nil {
			return "", fmt.Errorf("invalid daily key %q: %w", key, err)
		}
		return p, nil
	case "peers":
		if key == "" {
			return "", errors.New("peers tier requires 'key' (user slug)")
		}
		if strings.ContainsAny(key, `/\`) || strings.Contains(key, "..") {
			return "", fmt.Errorf("invalid peer key %q", key)
		}
		p, err := safepath.JoinUnder(d.ctx.AgentMemoryDir, "peers", key+".md")
		if err != nil {
			return "", fmt.Errorf("invalid peer key %q: %w", key, err)
		}
		return p, nil
	default:
		return "", fmt.Errorf("unknown tier %q", tier)
	}
}

func capForTier(tier string) (int, error) {
	switch tier {
	case "AGENT":
		return capAgentBytes, nil
	case "CREW":
		return capCrewBytes, nil
	case "PERSONA":
		return capPersonaBytes, nil
	case "pins":
		return capPinsBytes, nil
	case "daily":
		return capDailyBytes, nil
	case "peers":
		return capPeerBytes, nil
	case "lessons":
		return 0, nil
	default:
		return 0, fmt.Errorf("unknown tier %q", tier)
	}
}

func capPct(size, c int) int {
	if c == 0 {
		return 0
	}
	return (size * 100) / c
}

// capUsage renders a compact "<size> of <cap> bytes, <pct>%" usage
// string for the PR #6 overflow / soft-cap guidance. Single source so
// the message body and the metadata["usage"] field never drift.
func capUsage(size, c int) string {
	return fmt.Sprintf("%d of %d bytes, %d%%", size, c, capPct(size, c))
}

// assertMemoryFile rejects two attack surfaces against a candidate
// path:
//
//  1. Symlinks. `os.ReadFile` / `os.WriteFile` follow them, so an
//     `AGENT.md` symlink pre-planted inside `.memory` could read or
//     overwrite an arbitrary host path. `os.Lstat` + ModeSymlink
//     check refuses the file before the read/write syscall.
//  2. Path escape. Even without a symlink, a resolvePath bug or a
//     future tier addition could route outside the configured
//     memory roots. filepath.EvalSymlinks on the parent directory
//     normalises any traversal, then a Rel containment check
//     pins the final path inside AgentMemoryDir or CrewMemoryDir.
//
// A non-existent file is fine — handleRead returns empty content
// and handleWrite is about to create it. Only existing symlinks or
// out-of-root paths get rejected.
// The check itself lives in confine.go as AssertInsideRoot so every
// door into a memory tree runs the same code — the dispatcher, the
// portability importer, and anything added later. The dispatcher's own
// contribution is that it has TWO roots and a path is legitimate under
// either.
func (d *Dispatcher) assertMemoryFile(path string) error {
	var lastErr error
	for _, root := range []string{d.ctx.AgentMemoryDir, d.ctx.CrewMemoryDir} {
		if root == "" {
			continue
		}
		if err := AssertInsideRoot(root, path); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("path escapes memory root: %s", filepath.Base(path))
}

// isInsideMemoryRoot returns true when canon resolves under either of
// the dispatcher's configured roots. Caller must pass a path already
// run through EvalSymlinks (on the parent) so Rel works against the
// canonical form, not a traversed one.
func (d *Dispatcher) isInsideMemoryRoot(canon string) bool {
	for _, root := range []string{d.ctx.AgentMemoryDir, d.ctx.CrewMemoryDir} {
		if root == "" {
			continue
		}
		canonRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if isUnder(canonRoot, canon) {
			return true
		}
	}
	return false
}

func (d *Dispatcher) candidateFiles(tier string) []string {
	var paths []string
	addIfExists := func(p string) {
		if d.assertMemoryFile(p) != nil {
			return
		}
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	addDir := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				p := filepath.Join(dir, e.Name())
				if d.assertMemoryFile(p) != nil {
					continue
				}
				paths = append(paths, p)
			}
		}
	}

	if tier == "" || tier == "AGENT" {
		addIfExists(filepath.Join(d.ctx.AgentMemoryDir, "AGENT.md"))
	}
	if (tier == "" || tier == "CREW") && d.ctx.CrewMemoryDir != "" {
		addIfExists(filepath.Join(d.ctx.CrewMemoryDir, "CREW.md"))
	}
	if tier == "" || tier == "PERSONA" {
		addIfExists(filepath.Join(d.ctx.AgentMemoryDir, "PERSONA.md"))
	}
	if tier == "" || tier == "pins" {
		addIfExists(filepath.Join(d.ctx.AgentMemoryDir, "pins.md"))
	}
	if tier == "" || tier == "lessons" {
		addIfExists(filepath.Join(d.ctx.AgentMemoryDir, "lessons.md"))
	}
	if tier == "" || tier == "daily" {
		addDir(filepath.Join(d.ctx.AgentMemoryDir, "daily"))
	}
	if tier == "" || tier == "peers" {
		addDir(filepath.Join(d.ctx.AgentMemoryDir, "peers"))
	}
	sort.Strings(paths)
	return paths
}
