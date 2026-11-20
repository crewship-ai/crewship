/**
 * Which agents' conversations are on screen in this tab right now.
 *
 * A workspace run event carries the agent but not the chat (internal/api/
 * internal_runs.go), so the toast layer cannot tell "the conversation you are
 * reading" from "another run of the same agent". It uses this only to drop a
 * success toast for an agent whose chat is visibly open — the reply is already
 * on screen. Failures still toast: the event cannot prove it is this chat.
 */
const open = new Map<string, number>()

/** Marks the agent's chat as open; call the returned function on unmount. */
export function markAgentChatOpen(agentId: string): () => void {
  open.set(agentId, (open.get(agentId) ?? 0) + 1)
  return () => {
    const n = (open.get(agentId) ?? 1) - 1
    if (n <= 0) open.delete(agentId)
    else open.set(agentId, n)
  }
}

/** True while this agent's chat is mounted and the tab is visible. */
export function isViewingAgentChat(agentId: string | undefined): boolean {
  if (!agentId || !open.has(agentId)) return false
  return typeof document === "undefined" || document.visibilityState !== "hidden"
}
