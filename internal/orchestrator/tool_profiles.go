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
// An unknown or empty profile falls back to CODING, matching the
// `tool_profile TEXT NOT NULL DEFAULT 'CODING'` column default.
func builtinToolAllowlist(profile string) []string {
	switch profile {
	case "MINIMAL":
		// Read-only: inspection, review, grading.
		return []string{"Read", "Glob", "Grep"}
	case "FULL":
		// Everything useful: write/exec + network + notebooks.
		return []string{"Read", "Glob", "Grep", "Write", "Edit", "Bash", "WebFetch", "WebSearch", "NotebookEdit"}
	case "CODING":
		fallthrough
	default:
		// The workhorse default: write/exec + network, no notebooks.
		return []string{"Read", "Glob", "Grep", "Write", "Edit", "Bash", "WebFetch", "WebSearch"}
	}
}

// builtinToolAllowlistCSV renders the allowlist as the comma-separated value
// Claude Code's `--tools` flag expects (e.g. "Read,Glob,Grep").
func builtinToolAllowlistCSV(profile string) string {
	return strings.Join(builtinToolAllowlist(profile), ",")
}
