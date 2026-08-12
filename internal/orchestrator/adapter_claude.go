package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// claudeCodeAdapter wires Anthropic's `claude` CLI. Production-tested; this
// adapter must remain bit-for-bit compatible with the pre-refactor command
// shape because long-running missions and replay tests pin against it.
type claudeCodeAdapter struct{}

func (claudeCodeAdapter) Name() string { return "CLAUDE_CODE" }

// promptArgMaxBytes is the conservative ceiling, below Linux's per-argv
// MAX_ARG_STRLEN limit of 128 KiB, above which the user message is delivered to
// `claude` over stdin instead of as a positional argument. execve fails with
// E2BIG when any single argv element exceeds 128 KiB; that surfaced as the
// agent exiting 255 with $0.00 when a routine fed a large fetched page into an
// agent_run prompt. The 96 KiB ceiling leaves a 32 KiB safety margin (the
// kernel limit counts the trailing NUL and the message may be multi-byte).
//
// Claude Code's `--print` mode reads the prompt from stdin when no positional
// prompt is supplied and an explicit --output-format is set (we always pass
// --output-format stream-json) — verified against the real CLI (v2.1.x).
const promptArgMaxBytes = 96 * 1024

// DefaultMaxTurns is the adapter-side agent-loop cap for interactive runs when
// AgentRunRequest.MaxTurns is unset. 50 is generous enough for complex
// multi-step tasks without letting a stuck agent burn budget indefinitely.
//
// RoutineMaxTurns is the tighter cap the scheduler stamps onto unattended
// (routine / cron) runs. A background job that gets confused has no human to
// hit stop, so it's exactly where a loose cap quietly runs up the bill — 20
// still covers realistic scheduled work (fetch → reason → act → report).
const (
	DefaultMaxTurns = 50
	RoutineMaxTurns = 20
)

// resolveMaxTurns picks the effective turn cap: an explicit per-run value wins,
// otherwise the interactive default. Kept tiny and pure so the adapter argv
// stays trivially testable.
func resolveMaxTurns(req AgentRunRequest) int {
	if req.MaxTurns > 0 {
		return req.MaxTurns
	}
	return DefaultMaxTurns
}

// claudePromptViaStdin is the shared predicate behind PromptViaStdin and the
// arg-omission branch in BuildCommand so the two never disagree.
func claudePromptViaStdin(req AgentRunRequest) bool {
	return len(req.UserMessage) > promptArgMaxBytes
}

// PromptViaStdin routes oversized prompts through stdin to dodge E2BIG. Normal
// (sub-ceiling) messages keep the historic positional-arg + tmux path so the
// common case is byte-for-byte unchanged.
func (claudeCodeAdapter) PromptViaStdin(req AgentRunRequest) bool {
	return claudePromptViaStdin(req)
}

func (claudeCodeAdapter) BuildCommand(req AgentRunRequest) []string {
	// No --bare, on any auth path. It reads like the isolation flag for
	// scripted calls, and Anthropic's docs recommend it as such, but on the
	// real CLI it also REPLACES the built-in tool catalogue with
	// {Bash, Edit, Read} — and --tools can only subtract from that set, never
	// add back. Measured against 2.1.226, same --tools value in every row:
	//
	//	--bare --tools "default"                    -> [Bash Edit Read]
	//	--bare --tools "<the CODING allowlist>"     -> [Bash Edit Read]
	//	--bare --tools "Read,Glob,Grep,ToolSearch"  -> [Read]
	//	          --tools "<the CODING allowlist>"  -> [Bash Edit Glob Grep Read
	//	                                               ToolSearch WebFetch WebSearch Write]
	//
	// So every API-key run — --bare was already dropped for OAuth, whose auth
	// contract it breaks — silently lost Write, Glob, Grep, WebFetch and
	// WebSearch whatever its tool_profile said. A CODING agent that cannot
	// create a file; a MINIMAL reviewer that cannot grep. Nothing errored.
	//
	// What --bare was buying is bought explicitly below instead, and verified
	// on 2.1.226 against a project carrying both a CLAUDE.md and a
	// .claude/settings.json with a SessionStart hook:
	//
	//	--setting-sources ""  the hook did not fire, and the model answered NO
	//	                      when asked whether the repo's CLAUDE.md marker was
	//	                      in its context (with and without --system-prompt)
	//	--strict-mcp-config   only the servers we wrote in .mcp.json load
	//	--tools <allowlist>   keeps the harness catalogue (Task, Workflow, Cron*,
	//	                      TaskCreate, Skill, …) out of the model's context —
	//	                      that catalogue IS present without --bare, so this
	//	                      flag is the one doing that work now
	//
	// --setting-sources "" is therefore load-bearing rather than
	// belt-and-braces: it is what stands between an agent and a cloned
	// repository's hooks. Do not drop it to re-enable project skill discovery
	// without solving hooks separately (#1932).
	cmd := []string{
		"claude", "--print",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--dangerously-skip-permissions",
		"--verbose",
		"--setting-sources", "",
		"--strict-mcp-config",
		"--no-session-persistence",
	}
	if req.LLMModel != "" {
		cmd = append(cmd, "--model", req.LLMModel)
	}
	systemPrompt := crewshipSystemPreamble + req.SystemPrompt
	cmd = append(cmd, "--system-prompt", systemPrompt)
	// Curate the built-in tool surface for EVERY profile (previously only
	// MINIMAL was restricted, so CODING/FULL agents inherited Claude Code's
	// full default catalog — including harness-internal tools like TaskCreate,
	// ToolSearch, Agent, Workflow and Cron* that have no Crewship backing). An
	// agent calling one of those wrote to ephemeral in-process state and could
	// not say where the data went. `--tools` restricts AVAILABILITY (tools not
	// listed are removed from the model's context); MCP tools come from
	// --mcp-config and are unaffected, so crewship-memory + Composio still
	// resolve. The per-profile policy lives in builtinToolAllowlist.
	cmd = append(cmd, "--tools", builtinToolAllowlistCSV(req.ToolProfile))
	// --max-turns caps runaway loops at the Claude side as defense-in-depth
	// alongside Crewship's mission-level paymaster budget and the orchestrator
	// loop guard. Interactive runs default to DefaultMaxTurns; routine runs
	// carry a tighter RoutineMaxTurns via req.MaxTurns (see resolveMaxTurns).
	cmd = append(cmd, "--max-turns", strconv.Itoa(resolveMaxTurns(req)))
	// MCP servers are read from /crew/agents/<slug>/.mcp.json — written by
	// WriteMCPConfig. PR-A F1: the crewship-memory MCP server is now always
	// injected by setupMCPConfig regardless of whether the user/crew
	// declared any other MCP source, so --mcp-config is always set to give
	// the model native memory.read / write / search / append_daily tool
	// calls. Pre-PR-A we gated this on a non-empty MCP source list, which
	// would have stranded memory tools for agents with no other MCP servers.
	cmd = append(cmd, "--mcp-config", fmt.Sprintf("/crew/agents/%s/.mcp.json", req.AgentSlug))
	// Pass the user message as a positional argument guarded by `--` (which
	// stops Claude Code from re-parsing message tokens starting with `-` as
	// flags) — UNLESS it is large enough to risk execve's E2BIG, in which case
	// it is omitted here and delivered over stdin by the orchestrator (see
	// PromptViaStdin). `claude --print` reads the prompt from stdin when no
	// positional prompt is given.
	if !claudePromptViaStdin(req) {
		cmd = append(cmd, "--", req.UserMessage)
	}
	return cmd
}

func (claudeCodeAdapter) UseStreamJSON() bool { return true }

func (claudeCodeAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseClaudeCodeStreamJSON(line, handler)
}

// SetupSystemPrompt drops canonical memory files even though Claude Code
// receives its prompt via --system-prompt, which replaces the default prompt
// outright. Reasoning: --setting-sources "" suppresses CLAUDE.md
// auto-discovery, so nothing reads these files today — but a per-agent toggle
// or an upstream default change would silently drop our memory if they were
// not there. Writing the canonical files unconditionally also means a customer
// SSH-ing into the container sees the same context the agent operates under —
// useful for debugging.
func (claudeCodeAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	if err := writeCanonicalMemoryFiles(ctx, container, containerID, req, workDir, logger); err != nil {
		return fmt.Errorf("claude adapter setup system prompt: %w", err)
	}
	if err := writeAgentSkills(ctx, container, containerID, workDir, req.Skills, logger); err != nil {
		// Skill materialisation is non-fatal — the SKILLS AVAILABLE
		// system-prompt block already gives the model in-context access
		// via the canonical memory files. Native per-CLI skill paths are
		// the discoverability win, not the only route.
		logger.Warn("claude adapter write agent skills failed", "error", err)
	}
	return nil
}

func (claudeCodeAdapter) SupportsMCP() bool { return true }

func (claudeCodeAdapter) WriteMCPConfig(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	if err := writeMCPClaude(ctx, container, containerID, req, workDir, logger); err != nil {
		return fmt.Errorf("claude adapter write MCP config: %w", err)
	}
	return nil
}

// parseClaudeCodeStreamJSON parses one line of Claude Code stream-json output
// and emits zero-or-more AgentEvents. Extracted from Orchestrator.handleStreamJSONLine
// so the adapter is stateless and easy to unit-test without a full Orchestrator.
func parseClaudeCodeStreamJSON(line []byte, handler EventHandler) {
	if handler == nil {
		return
	}

	// A stream line is ONE JSON object, so treating any decode error as a
	// line-level failure is what made a single mistyped field cost the whole
	// envelope — on a result line that is no cost, no usage, and no is_error for
	// inBandFailure to key on, i.e. a run that failed to authenticate recorded
	// COMPLETED. Making one more field tolerant each time it happens is
	// whack-a-mole against a shape we do not control; this is the fix in one
	// place, and it covers the fields nobody has thought of yet.
	//
	// encoding/json already does the work: "If the JSON value is not appropriate
	// for a given target type … Unmarshal skips that field and completes the
	// unmarshaling as best it can", returning the earliest such error at the end.
	// So on a type error `msg` is ALREADY populated with every field that did
	// match — the error is a report about one field, not a verdict on the line,
	// and the old code threw the decoded envelope away on the strength of it.
	// Nothing is added to the happy path: a clean line never reaches this branch.
	//
	// msg.Type is the discriminator for everything below, so it is also the
	// honest test of whether anything survived. Empty means the failure was
	// structural rather than per-field — invalid JSON (checkValid rejects the
	// line before any decoding), a JSON array or scalar, or a `type` that is not
	// a string — and then the raw line still belongs in the transcript, because
	// a CLI that started writing plain-text diagnostics to stdout must show up
	// as visibly wrong rather than as silence.
	var msg streamJSONMessage
	err := json.Unmarshal(line, &msg)
	if err != nil && msg.Type == "" {
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
		return
	}

	// The isolation above has a second edge, and it is the price of narrowing
	// it: a line that ROUTED is no longer dumped as raw text, so the fields json
	// skipped on the way through are gone with nothing said about them. `result`
	// is the sharp case — it is the CLI's final user-facing message and
	// inBandFailure quotes it, so a release that emitted it as content blocks
	// (the shape Anthropic uses for message content everywhere else) would show
	// an operator "agent reported a failed run (api_error 401):" and nothing
	// else, indistinguishable from a CLI that sent no message at all.
	//
	// So the line says what it lost. json's own error already names the field
	// and the type it choked on, which is the whole diagnostic; the verbatim
	// line remains recoverable from the run's exec.output_chunk entry, which
	// captures raw stdout. Stamped on the events rather than logged because this
	// is a pure function with no logger, and because a log line is not attached
	// to the run someone is triaging — event metadata rides into the journal
	// next to the envelope it belongs to.
	//
	// Deliberately NOT a raw-text event as well: an assistant line carries every
	// tool_use of a turn, so re-emitting the line on any partial decode would
	// dump JSON into the chat for a mistyped tool id.
	if note := decodeNote(err, msg); note != "" {
		inner := handler
		handler = func(e AgentEvent) {
			meta, _ := e.Metadata.(map[string]interface{})
			if meta == nil {
				meta = map[string]interface{}{}
			}
			meta["decode_error"] = note
			e.Metadata = meta
			inner(e)
		}
	}

	// Claude Code wraps content in message.content; promote it when top-level is
	// empty. Same rule as the line-level decode above, and for the same reason:
	// encoding/json skips the block whose shape moved and decodes the ones either
	// side of it, so requiring err == nil here would put the hole back exactly
	// where the tool calls live — an assistant line carries every tool_use of a
	// turn, and one numeric id would cost all of them. What survived is what
	// counts, so the error is discarded and the length is the test.
	if len(msg.Content) == 0 && len(msg.Message) > 0 {
		var nested nestedMessage
		_ = json.Unmarshal(msg.Message, &nested)
		if len(nested.Content) > 0 {
			msg.Content = nested.Content
		}
	}

	// parentID is non-empty when this line came from a nested subagent (Task
	// tool). tagSubagent stamps it onto an event's metadata so the UI can scope
	// the activity under its parent.
	parentID := msg.ParentToolUseID
	tagSubagent := func(meta map[string]interface{}) map[string]interface{} {
		if parentID == "" {
			return meta
		}
		if meta == nil {
			meta = map[string]interface{}{}
		}
		meta["parent_tool_use_id"] = parentID
		meta["subagent"] = true
		return meta
	}

	switch msg.Type {
	case "stream_event":
		// Token-level streaming (when --include-partial-messages is used).
		if msg.Event != nil && msg.Event.Delta != nil {
			switch msg.Event.Delta.Type {
			case "text_delta":
				handler(AgentEvent{Type: "text", Content: msg.Event.Delta.Text, Metadata: tagSubagent(nil), Timestamp: time.Now()})
			case "thinking_delta":
				handler(AgentEvent{
					Type:      "thinking",
					Content:   msg.Event.Delta.Thinking,
					Metadata:  tagSubagent(map[string]interface{}{"streaming": true}),
					Timestamp: time.Now(),
				})
			}
		}

	case "assistant":
		// With --include-partial-messages on (always), text and thinking
		// were streamed via stream_event already — only emit tool blocks
		// here so we don't duplicate the visible text.
		for _, block := range msg.Content {
			switch block.Type {
			case "thinking", "text":
				// Already delivered via deltas — skip.
			case "tool_use":
				name := block.Name
				if name == "" {
					name = "tool"
				}
				handler(AgentEvent{
					Type:    "tool_call",
					Content: name,
					Metadata: tagSubagent(map[string]interface{}{
						"tool_name": name,
						"tool_id":   block.ID,
						"input":     block.Input,
					}),
					Timestamp: time.Now(),
				})
			case "tool_result":
				emitToolResultBlock(block, parentID, handler)
			case "image":
				emitImageBlock(block, parentID, handler)
			}
		}

	case "tool", "user":
		for _, block := range msg.Content {
			switch block.Type {
			case "tool_result":
				emitToolResultBlock(block, parentID, handler)
			case "image":
				emitImageBlock(block, parentID, handler)
			}
		}

	case "result":
		meta := map[string]interface{}{
			"subtype":         msg.Subtype.String(),
			"duration_ms":     msg.DurationMs,
			"duration_api_ms": msg.DurationAPI,
			"total_cost_usd":  float64(msg.TotalCostUSD),
			"num_turns":       int(msg.NumTurns),
			"is_error":        bool(msg.IsError),
		}
		if len(msg.Usage) > 0 {
			var usage map[string]interface{}
			if json.Unmarshal(msg.Usage, &usage) == nil {
				meta["usage"] = usage
			}
		}
		if len(msg.ModelUsage) > 0 {
			var mu map[string]interface{}
			if json.Unmarshal(msg.ModelUsage, &mu) == nil {
				meta["model_usage"] = mu
			}
		}
		if len(msg.Errors) > 0 {
			meta["errors"] = msg.Errors
		}
		// terminal_reason names WHY the turn ended, and it is not derivable
		// from subtype: a hard auth failure arrives as subtype "success" with
		// is_error true and terminal_reason "api_error". api_error_status
		// separates a bad credential (401) from a busy API (529), which is the
		// difference between "fix the credential" and "retry".
		if msg.TerminalReason != "" {
			meta["terminal_reason"] = string(msg.TerminalReason)
		}
		if msg.APIErrorStatus > 0 {
			meta["api_error_status"] = int(msg.APIErrorStatus)
		}
		if msg.StopReason != "" {
			meta["stop_reason"] = string(msg.StopReason)
		}
		if msg.SessionID != "" {
			meta["session_id"] = msg.SessionID
		}
		// A run blocked by permissions otherwise looks like a run that chose
		// not to act — the model simply reports it could not do the thing.
		if len(msg.PermissionDenials) > 0 {
			meta["permission_denials"] = json.RawMessage(append([]byte{}, msg.PermissionDenials...))
		}
		handler(AgentEvent{
			Type:      "result",
			Content:   msg.Result,
			Metadata:  meta,
			Timestamp: time.Now(),
		})

	case "system":
		subtype := msg.Subtype.String()
		meta := map[string]interface{}{
			"subtype": subtype,
		}
		switch subtype {
		case "init":
			if msg.Model != "" {
				meta["model"] = msg.Model
			}
			if len(msg.Tools) > 0 {
				meta["tools"] = msg.Tools
			}
			if msg.CWD != "" {
				meta["cwd"] = msg.CWD
			}
			if len(msg.MCPSrvrs) > 0 {
				var servers []json.RawMessage
				if json.Unmarshal(msg.MCPSrvrs, &servers) == nil {
					meta["mcp_servers"] = servers
				}
			}
			// Session provenance. claude_code_version is the one that pays for
			// the rest: the adapter is validated against a pinned npm version
			// while agent containers install the `claude-code:2` devcontainer
			// feature — latest — so the two drift apart silently, and a
			// capability can go missing for a hundred releases before anyone
			// notices (#1932). Record what actually answered.
			//
			// apiKeySource says which auth path resolved, i.e. whether the
			// credential we mounted is the one in use. permissionMode is proof
			// --dangerously-skip-permissions took. capabilities is the CLI's
			// own list of protocol behaviours it implements — feature-detect on
			// it instead of comparing version strings.
			if msg.ClaudeCodeVersion != "" {
				meta["claude_code_version"] = msg.ClaudeCodeVersion
			}
			if msg.SessionID != "" {
				meta["session_id"] = msg.SessionID
			}
			if msg.APIKeySource != "" {
				meta["apiKeySource"] = msg.APIKeySource
			}
			if msg.PermissionMode != "" {
				meta["permissionMode"] = msg.PermissionMode
			}
			if len(msg.Capabilities) > 0 {
				meta["capabilities"] = []string(msg.Capabilities)
			}
			// Whether the SKILL.md files we materialise were actually
			// discovered. Today they are not — project-level skill discovery
			// is off under --setting-sources "" — and this field is the
			// per-run evidence of it.
			if len(msg.Skills) > 0 {
				meta["skills"] = json.RawMessage(append([]byte{}, msg.Skills...))
			}
			// A --mcp-config entry that fails validation is skipped and the run
			// continues, exiting 0. An agent that lost crewship-memory that way
			// looks healthy; this array is the only place it is reported.
			// v2.1.219+ — absent when nothing was skipped, so a gate can fail
			// on a non-empty array.
			if len(msg.MCPServerErrors) > 0 {
				meta["mcp_server_errors"] = json.RawMessage(append([]byte{}, msg.MCPServerErrors...))
			}
			// v2.1.111+ ships plugins + plugin_errors so operators can see
			// when a plugin fails to load at session start. Plugin discovery
			// stays off under --setting-sources "", but a per-agent opt-out
			// would benefit from this visibility.
			if len(msg.Plugins) > 0 {
				var plugins json.RawMessage
				meta["plugins"] = json.RawMessage(append([]byte{}, msg.Plugins...))
				_ = plugins
			}
			if len(msg.PluginErrors) > 0 {
				meta["plugin_errors"] = json.RawMessage(append([]byte{}, msg.PluginErrors...))
			}
		case "api_retry":
			// Anthropic auth/rate/billing/server retry envelope. Capture all
			// fields so backoff investigations have ground truth; Crow's
			// Nest can render a "retrying" banner without polling logs.
			if msg.Attempt > 0 {
				meta["attempt"] = msg.Attempt
			}
			if msg.MaxRetries > 0 {
				meta["max_retries"] = msg.MaxRetries
			}
			if msg.RetryDelayMs > 0 {
				meta["retry_delay_ms"] = msg.RetryDelayMs
			}
			if msg.ErrorStatus > 0 {
				meta["error_status"] = msg.ErrorStatus
			}
			if msg.ErrorMessage != "" {
				meta["error"] = msg.ErrorMessage
			}
		}
		handler(AgentEvent{
			Type:      "system",
			Content:   subtype,
			Metadata:  meta,
			Timestamp: time.Now(),
		})

	default:
		for _, block := range msg.Content {
			if block.Text != "" {
				handler(AgentEvent{Type: "text", Content: block.Text, Timestamp: time.Now()})
			}
		}
	}
}

// decodeNote summarises, in one string, what this line lost while decoding —
// nothing more than a description, and empty for every healthy line so the
// marker keeps meaning something.
//
// It has two sources, and the second is the reason it is a function rather than
// err.Error(). json reports a type error for a STRICT field, so a strict field
// we could not read names itself. The tolerant types never error — that is the
// point of them — so they can only report through their own state, and the only
// one that does is tolerantSubtype, because it is the only one whose zero value
// costs more than its own field. The remaining tolerant fields reduce a known
// value ("401" is 401, "true" is true) or drop a pass-through list, which is a
// loss the run can absorb.
func decodeNote(err error, msg streamJSONMessage) string {
	notes := make([]string, 0, 2)
	if err != nil {
		notes = append(notes, err.Error())
	}
	if msg.Subtype.unreadable {
		// Named explicitly: json never saw a type error here, so nothing else
		// in the note would mention the field that routed the whole envelope.
		notes = append(notes, "subtype was not a readable label, so this envelope routed on an empty discriminator")
	}
	return strings.Join(notes, "; ")
}
