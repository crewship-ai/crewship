package orchestrator

import "strings"

// builtinToolAllowlist is the single source of truth for which CLI built-in
// tools an agent may use, keyed by its tool_profile (FULL | CODING | MINIMAL).
//
// Why an allowlist (not a denylist): a headless agent driven by Claude Code
// otherwise inherits the CLI's ENTIRE default tool catalog, including
// harness-internal tools — TaskCreate/TaskUpdate/TaskList/TaskGet/TaskStop,
// TodoWrite, ToolSearch, Agent, Workflow, Cron*, ScheduleWakeup, RemoteTrigger,
// EnterPlanMode, AskUserQuestion, Artifact, … — that have NO Crewship backing.
// An agent that reaches for one (e.g. TaskCreate to "create a task") writes to
// ephemeral in-process CLI state that persists nowhere the user can see, then
// can't explain where the data went. An allowlist removes those tools from the
// model's context entirely; a denylist would let any newly-added CLI builtin
// leak back in on the next upgrade.
//
// This governs ONLY the CLI's built-in tools. MCP tools (crewship-memory,
// Composio apps, …) come from the agent's .mcp.json and are unaffected — and
// the real Crewship capabilities (missions, issues, pipelines, keeper, peer
// messaging) are sidecar HTTP endpoints the system prompt advertises, not CLI
// tools. The escalation ladder is: read-only → +write/exec → +network/notebooks.
//
// ToolSearch is included in EVERY profile on purpose. Claude Code defers MCP
// tools by default (tool search enabled) and the model discovers them via the
// built-in ToolSearch tool; drop ToolSearch and the agent can no longer see or
// call ANY MCP tool — crewship-memory, Composio/YouTube, everything. Verified:
// with ToolSearch in --tools, MCP tools load on demand while a builtin NOT in
// the allowlist (e.g. TaskCreate) stays NOT_FOUND even via ToolSearch — so the
// allowlist still removes the dead harness tools. Keeping deferral also scales
// to large MCP catalogs (e.g. GitHub's ~846 tools) without bloating context.
//
// An unknown or empty profile falls back to CODING, matching the
// `tool_profile TEXT NOT NULL DEFAULT 'CODING'` column default.
func builtinToolAllowlist(profile string) []string {
	switch profile {
	case "MINIMAL":
		// Read-only: inspection, review, grading (+ MCP discovery).
		return []string{"Read", "Glob", "Grep", "ToolSearch"}
	case "FULL":
		// Everything useful: write/exec + network + notebooks (+ MCP discovery).
		return []string{"Read", "Glob", "Grep", "Write", "Edit", "Bash", "WebFetch", "WebSearch", "NotebookEdit", "ToolSearch"}
	case "CODING":
		fallthrough
	default:
		// The workhorse default: write/exec + network (+ MCP discovery), no notebooks.
		return []string{"Read", "Glob", "Grep", "Write", "Edit", "Bash", "WebFetch", "WebSearch", "ToolSearch"}
	}
}

// builtinToolAllowlistCSV renders the allowlist as the comma-separated value
// Claude Code's `--tools` flag expects (e.g. "Read,Glob,Grep").
func builtinToolAllowlistCSV(profile string) string {
	return strings.Join(builtinToolAllowlist(profile), ",")
}
