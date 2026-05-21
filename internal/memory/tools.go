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
			Description: "Persist content to an agent memory file. Use mode='replace' when reorganizing; " +
				"mode='append' to add new entries. Cap-aware: returns a warning at 80% of cap and a hard " +
				"error at 100% of cap so you must self-curate (drop older entries, summarize) before retrying.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"tier": {
						"type": "string",
						"enum": ["AGENT", "CREW", "PERSONA", "pins", "daily", "peers", "lessons"]
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
						"description": "Search query. Plain text; the engine handles tokenisation."
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
	}
}

// Dispatcher routes ToolCall to per-tool handlers. Stateless beyond
// the AgentContext, so callers can share an instance across the
// duration of a single agent turn without coordinating writes.
type Dispatcher struct {
	ctx AgentContext
	now func() time.Time
}

// NewDispatcher builds a Dispatcher bound to the given AgentContext.
func NewDispatcher(ac AgentContext) *Dispatcher {
	return &Dispatcher{ctx: ac, now: func() time.Time { return time.Now().UTC() }}
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
	default:
		return ToolResult{
			IsError: true,
			Content: fmt.Sprintf("unknown tool: %q. Available: memory.read, memory.write, memory.search, memory.append_daily.", call.Name),
		}, nil
	}
}

type readArgs struct {
	Tier string `json:"tier"`
	Key  string `json:"key"`
}

func (d *Dispatcher) handleRead(_ context.Context, raw json.RawMessage) (ToolResult, error) {
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
	data, err := os.ReadFile(path)
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

func (d *Dispatcher) handleWrite(_ context.Context, raw json.RawMessage) (ToolResult, error) {
	var a writeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return ToolResult{IsError: true, Content: "memory.write: invalid args: " + err.Error()}, nil
	}
	if _, ok := validTiers[a.Tier]; !ok {
		return ToolResult{IsError: true, Content: fmt.Sprintf("memory.write: unknown tier %q", a.Tier)}, nil
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

	// Serialise the read-modify-write window so two concurrent appends
	// can't each pass the cap check against the same pre-existing size
	// and then sequentially write past the cap. Same lock primitive
	// the lesson writer uses (writer.go FileLock / flock).
	lk := NewFileLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		return ToolResult{IsError: true, Content: "memory.write: lock: " + err.Error()}, nil
	}
	defer func() { _ = lk.Unlock() }()

	var data []byte
	var existing int
	if a.Mode == "append" {
		old, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return ToolResult{IsError: true, Content: "memory.write: " + err.Error()}, nil
		}
		existing = len(old)
		data = append(old, []byte(a.Content)...)
	} else {
		data = []byte(a.Content)
	}

	if cap > 0 && len(data) > cap {
		return ToolResult{
			IsError: true,
			Content: fmt.Sprintf(
				"memory.write: cap exceeded for tier=%s. Final would be %d bytes; cap is %d. "+
					"Use mode='replace' (shrinks the file) or drop older entries before retrying.",
				a.Tier, len(data), cap),
			Metadata: map[string]any{
				"tier":           a.Tier,
				"cap_bytes":      cap,
				"projected_size": len(data),
				"current_size":   existing,
			},
		}, nil
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return ToolResult{IsError: true, Content: "memory.write: " + err.Error()}, nil
	}

	res := ToolResult{
		Content: fmt.Sprintf("ok: %d bytes written to %s", len(data), a.Tier),
		Metadata: map[string]any{
			"source":        tierSourceLabel(a.Tier, a.Key),
			"bytes_written": len(data),
			"cap_bytes":     cap,
			"cap_pct":       capPct(len(data), cap),
		},
	}
	if cap > 0 && float64(len(data)) >= float64(cap)*softCapPct {
		res.Content += fmt.Sprintf(
			". warning: approaching cap (%d of %d bytes, %d%%). "+
				"Consider mode='replace' with consolidated content soon to avoid the next append being rejected.",
			len(data), cap, capPct(len(data), cap))
	}
	return res, nil
}

type searchArgs struct {
	Q     string `json:"q"`
	Tier  string `json:"tier"`
	Limit int    `json:"limit"`
}

// handleSearch is a minimal substring search over the resolved tier
// files. Replaces the curl /memory/search surface removed in PR-Z
// Z.1. A follow-up commit will plumb this through the FTS5 engine
// for keyword + semantic recall; the present implementation keeps
// the wire contract stable while we get adapter wiring landed.
func (d *Dispatcher) handleSearch(_ context.Context, raw json.RawMessage) (ToolResult, error) {
	var a searchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return ToolResult{IsError: true, Content: "memory.search: invalid args: " + err.Error()}, nil
	}
	if strings.TrimSpace(a.Q) == "" {
		return ToolResult{IsError: true, Content: "memory.search: q is required"}, nil
	}
	if a.Limit <= 0 || a.Limit > 20 {
		a.Limit = 20
	}
	if a.Tier != "" {
		if _, ok := validTiers[a.Tier]; !ok {
			return ToolResult{IsError: true, Content: fmt.Sprintf("memory.search: unknown tier %q", a.Tier)}, nil
		}
	}

	files := d.candidateFiles(a.Tier)
	type hit struct {
		Path    string `json:"path"`
		Snippet string `json:"snippet"`
		Line    int    `json:"line"`
	}
	hits := make([]hit, 0, a.Limit)
	needle := strings.ToLower(a.Q)
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(strings.ToLower(line), needle) {
				hits = append(hits, hit{Path: p, Snippet: line, Line: i + 1})
				if len(hits) >= a.Limit {
					break
				}
			}
		}
		if len(hits) >= a.Limit {
			break
		}
	}

	body, _ := json.MarshalIndent(map[string]any{"hits": hits, "query": a.Q}, "", "  ")
	return ToolResult{Content: string(body)}, nil
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
		if strings.ContainsAny(key, `/\`) || strings.Contains(key, "..") {
			return "", fmt.Errorf("invalid daily key %q", key)
		}
		return filepath.Join(d.ctx.AgentMemoryDir, "daily", key+".md"), nil
	case "peers":
		if key == "" {
			return "", errors.New("peers tier requires 'key' (user slug)")
		}
		if strings.ContainsAny(key, `/\`) || strings.Contains(key, "..") {
			return "", fmt.Errorf("invalid peer key %q", key)
		}
		return filepath.Join(d.ctx.AgentMemoryDir, "peers", key+".md"), nil
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

func (d *Dispatcher) candidateFiles(tier string) []string {
	var paths []string
	addIfExists := func(p string) {
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
				paths = append(paths, filepath.Join(dir, e.Name()))
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
