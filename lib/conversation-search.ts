import { apiFetch } from "@/lib/api-fetch"

/**
 * One matched conversation message, as POST /api/v1/conversations/search
 * returns it. agent_slug / agent_name are filled in by the API handler: the
 * FTS mirror stores only an agent id, and every caller needs to say who said
 * it and to link to the thread.
 */
export interface ConversationHit {
  id: string
  session_id: string
  agent_id: string
  agent_slug?: string
  agent_name?: string
  role: string
  content: string
  tool_summary?: string
  ts: string
}

/**
 * The shortest query worth sending. One character matches most of a
 * workspace's history and ranks it by nothing useful; it is also the
 * keystroke a user is most likely to still be typing.
 */
export const CONVERSATION_SEARCH_MIN_QUERY = 2

/** Debounce window for search-as-you-type, in ms. */
export const CONVERSATION_SEARCH_DEBOUNCE_MS = 200

/** How many hits one search asks for. The server's own ceiling is 100. */
export const CONVERSATION_SEARCH_LIMIT = 8

/**
 * The URL a hit opens: the SAME shape chat notifications deep-link to
 * (internal/chatnotify/notify.go builds `/chat/<slug>?session=<chatId>`), so
 * the palette and the bell can never drift into two different links to one
 * thread. A hit whose agent slug did not resolve has no thread URL — the
 * caller drops the row rather than navigating somewhere that 404s.
 */
export function conversationHitHref(hit: ConversationHit): string | null {
  if (!hit.agent_slug || !hit.session_id) return null
  return `/chat/${encodeURIComponent(hit.agent_slug)}?session=${encodeURIComponent(hit.session_id)}`
}

/** The one-line preview a row shows. */
export function conversationHitSnippet(hit: ConversationHit): string {
  const raw = (hit.content || hit.tool_summary || "").replace(/\s+/g, " ").trim()
  return raw.length > 140 ? `${raw.slice(0, 137)}…` : raw
}

/**
 * Search conversation history across the caller's workspace.
 *
 * No agent_id: the endpoint's default scope is every agent in the workspace,
 * which is what ⌘K means — the user is searching everything they can see and
 * has no agent in mind to name.
 *
 * Failure is silent BY DESIGN and returns []: this search runs on keystrokes
 * nobody explicitly submitted, and the endpoint is optional (503 when the
 * conversation mirror is not configured). A toast for it would punish the
 * user for typing. An aborted request is likewise not an error — it means a
 * newer keystroke replaced it.
 */
export async function searchConversations(
  query: string,
  opts: { workspaceId: string; signal?: AbortSignal; limit?: number },
): Promise<ConversationHit[]> {
  const trimmed = query.trim()
  if (trimmed.length < CONVERSATION_SEARCH_MIN_QUERY || !opts.workspaceId) return []

  try {
    const res = await apiFetch(
      `/api/v1/conversations/search?workspace_id=${encodeURIComponent(opts.workspaceId)}`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ query: trimmed, limit: opts.limit ?? CONVERSATION_SEARCH_LIMIT }),
        signal: opts.signal,
      },
    )
    if (!res.ok) return []
    const data = (await res.json()) as { hits?: ConversationHit[] } | null
    return Array.isArray(data?.hits) ? data.hits : []
  } catch {
    return []
  }
}
