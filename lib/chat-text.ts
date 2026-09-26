/** Hide a Codex CLI progress line persisted by older chat streams. */
export function visibleAgentText(content: string): string {
  return content.replace(/^Reading additional input from stdin\.\.\.(?:\r?\n|$)/, "")
}
