import { apiFetch } from "@/lib/api-fetch"
import { CONVERSATION_SEARCH_MIN_QUERY, conversationHitHref, type ConversationHit } from "@/lib/conversation-search"

/**
 * Chat search (CH-25). Talks to the EXISTING POST /api/v1/conversations/search,
 * which understands only `query`, `agent_id` and `limit` and returns a flat,
 * uncursored page. Everything richer (From / Has / Date, This chat) is applied
 * to that page on the client, and because a client filter over a full page can
 * hide matches that the server never sent, such a result is reported as
 * "partial" and never as an empty list. A full page with nothing narrowed
 * locally is "truncated": there is no cursor, so more matches may exist.
 * See coord FE-REQ-F6-1.
 */

export type ChatSearchScope = "this" | "all" | "files"
export type ChatSearchStatus = "idle" | "complete" | "partial" | "truncated" | "unavailable" | "error"
export type ChatSearchHas = "link" | "code" | "tool"

export interface ChatSearchFilters {
  from?: string
  in?: string
  has?: ChatSearchHas[]
  after?: string
  before?: string
}
export interface ParsedChatSearch { text: string; filters: ChatSearchFilters; invalid: string[] }
export type ChatSearchRoster = { id: string; slug: string; name: string }[]

export const CHAT_SEARCH_PAGE = 50
/** The server searches at most this many agents per workspace-wide query (maxWorkspaceSearchAgents); it says nothing when the cap applied. */
export const WORKSPACE_SEARCH_AGENT_CAP = 400
const MAX_TEXT = 200
const MAX_TOKENS = 64
const MAX_HAS = 3
const HAS_VALUES: readonly string[] = ["link", "code", "tool"]
const DATE_RE = /^\d{4}-\d{2}-\d{2}$/
const OPERATORS = ["from", "in", "has", "after", "before"] as const

/** Bounded, regex-free operator parser: `from:ada in:bob has:link after:2026-10-01 text`. */
export function parseChatSearchQuery(raw: string): ParsedChatSearch {
  const filters: ChatSearchFilters = {}
  const invalid: string[] = []
  const words: string[] = []
  for (const token of raw.slice(0, 2000).split(/\s+/).filter(Boolean).slice(0, MAX_TOKENS)) {
    const colon = token.indexOf(":")
    const op = colon > 0 ? token.slice(0, colon).toLowerCase() : ""
    const value = colon > 0 ? token.slice(colon + 1) : ""
    if (!(OPERATORS as readonly string[]).includes(op)) { words.push(token); continue }
    if (!value) { invalid.push(token); continue }
    if (op === "has") {
      if (!HAS_VALUES.includes(value.toLowerCase())) { invalid.push(token); continue }
      const next = [...(filters.has ?? []), value.toLowerCase() as ChatSearchHas]
      filters.has = [...new Set(next)].slice(0, MAX_HAS)
    } else if (op === "after" || op === "before") {
      if (!DATE_RE.test(value) || Number.isNaN(Date.parse(`${value}T00:00:00Z`))) { invalid.push(token); continue }
      filters[op as "after" | "before"] = value
    } else filters[op as "from" | "in"] = value
  }
  return { text: words.join(" ").slice(0, MAX_TEXT), filters, invalid }
}

const FROM_ME = ["me", "user", "you"]
const FROM_ME_REASON = "needs message author from the server"
const FROM_AGENT = ["agent", "assistant"]
const isAgentRole = (role: string) => FROM_AGENT.includes(role.toLowerCase())

const norm = (value: string | undefined) => (value ?? "").trim().toLowerCase()

function hasLink(h: ConversationHit) { return /https?:\/\/\S+/i.test(h.content) }
function hasCode(h: ConversationHit) { return h.content.includes("```") }

/**
 * The hit carries only a role, not an author id, and group agent chats return
 * other people's messages too, so "mine" cannot be told apart: from:me/user/you
 * are unsupported. Supported: agent/assistant (role assistant; the backend joins
 * chats on agent_id, so those hits belong to the chat's agent) and an agent's
 * name or slug (that agent's own replies). The roster is resolved first, so an
 * agent named "Agent" or "User" is selectable.
 */
function matchesFrom(h: ConversationHit, from: string, roster: ChatSearchRoster) {
  if (!isAgentRole(h.role)) return false
  const named = roster.some((a) => norm(a.slug) === from || norm(a.name) === from)
  if (!named && FROM_AGENT.includes(from)) return true
  return norm(h.agent_slug) === from || norm(h.agent_name) === from
}

/** The filters the endpoint cannot do. Pure; input is not mutated. */
export function applyClientFilters(hits: ConversationHit[], f: ChatSearchFilters, roster: ChatSearchRoster = []): ConversationHit[] {
  const from = norm(f.from)
  const after = f.after ? Date.parse(`${f.after}T00:00:00Z`) : null
  const before = f.before ? Date.parse(`${f.before}T00:00:00Z`) : null
  return hits.filter((h) => {
    if (from && !matchesFrom(h, from, roster)) return false
    for (const has of f.has ?? []) {
      if (has === "link" && !hasLink(h)) return false
      if (has === "code" && !hasCode(h)) return false
      if (has === "tool" && !h.tool_summary) return false
    }
    const ts = Date.parse(h.ts)
    if (after !== null && !(ts >= after)) return false
    if (before !== null && !(ts < before)) return false
    return true
  })
}

export interface ChatSearchResult {
  status: ChatSearchStatus
  hits: ConversationHit[]
  /** Human-readable reason shown next to partial / truncated / unavailable / error. */
  note?: string
}

export async function runChatSearch(opts: {
  raw: string
  scope: Exclude<ChatSearchScope, "files">
  workspaceId: string
  roster: ChatSearchRoster
  current?: { agentId: string; sessionId: string } | null
  signal?: AbortSignal
}): Promise<ChatSearchResult> {
  const parsed = parseChatSearchQuery(opts.raw)
  if (parsed.invalid.length) return { status: "error", hits: [], note: `Unsupported filter: ${parsed.invalid.join(", ")}` }
  if (parsed.text.length < CONVERSATION_SEARCH_MIN_QUERY || !opts.workspaceId) return { status: "idle", hits: [] }

  const from = norm(parsed.filters.from)
  if (from) {
    const named = opts.roster.some((a) => norm(a.slug) === from || norm(a.name) === from)
    if (!named && FROM_ME.includes(from)) return { status: "error", hits: [], note: `from:${parsed.filters.from} is not supported (${FROM_ME_REASON}). Use from:agent or an agent's name.` }
    if (!named && !FROM_AGENT.includes(from)) return { status: "error", hits: [], note: `from:${parsed.filters.from} is not supported. Use from:agent or an agent's name.` }
  }

  let agentId: string | undefined
  if (opts.scope === "this" && parsed.filters.in) return { status: "error", hits: [], note: "in: cannot be combined with This chat. Switch to All chats to search one agent." }
  if (opts.scope === "this") {
    if (!opts.current?.sessionId) return { status: "unavailable", hits: [], note: "Search inside this chat is not available for this conversation yet." }
    agentId = opts.current.agentId
  } else if (parsed.filters.in) {
    const key = norm(parsed.filters.in)
    const agent = opts.roster.find((a) => norm(a.slug) === key || norm(a.name) === key)
    if (!agent) return { status: "error", hits: [], note: `No agent named "${parsed.filters.in}" you can search.` }
    agentId = agent.id
  }

  try {
    const res = await apiFetch(`/api/v1/conversations/search?workspace_id=${encodeURIComponent(opts.workspaceId)}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ query: parsed.text, limit: CHAT_SEARCH_PAGE, ...(agentId ? { agent_id: agentId } : {}) }),
      signal: opts.signal,
    })
    if (res.status === 503) return { status: "unavailable", hits: [], note: "Search is not configured on this server." }
    if (!res.ok) return { status: "error", hits: [], note: `Search failed (${res.status}).` }
    const data = (await res.json()) as { hits?: ConversationHit[] } | null
    const page = Array.isArray(data?.hits) ? data.hits : []
    let hits = page
    if (opts.scope === "this") hits = hits.filter((h) => h.session_id === opts.current!.sessionId)
    const before = hits.length
    hits = applyClientFilters(hits, parsed.filters, opts.roster)
    const narrowedLocally = opts.scope === "this" || before !== hits.length || Object.keys(parsed.filters).some((k) => k !== "in")
    const fullPage = page.length >= CHAT_SEARCH_PAGE
    const capNote = opts.scope === "all" && !agentId && opts.roster.length >= WORKSPACE_SEARCH_AGENT_CAP
      ? ` Only the first ${WORKSPACE_SEARCH_AGENT_CAP} agents of a workspace are searched; chats of other agents may be missing.`
      : ""
    if (fullPage && narrowedLocally) {
      return { status: "partial", hits, note: `Filters were applied to the ${CHAT_SEARCH_PAGE} best matches only. More matches may exist.${capNote}` }
    }
    if (fullPage) return { status: "truncated", hits, note: `First ${CHAT_SEARCH_PAGE} results, more may exist.${capNote}` }
    if (capNote) return { status: "partial", hits, note: capNote.trim() }
    return { status: "complete", hits }
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") return { status: "idle", hits: [] }
    return { status: "error", hits: [], note: "Search could not be reached." }
  }
}

/** The exact-message link: the thread link plus the message id. */
export function hitMessageHref(hit: ConversationHit): string | null {
  const base = conversationHitHref(hit)
  return base && hit.id ? `${base}&message=${encodeURIComponent(hit.id)}` : null
}

const UNSAFE = /[\u0000-\u0008\u000b-\u001f\u007f‎‏‪-‮⁦-⁩]/g

const SNIPPET_MAX = 140
const SNIPPET_LEAD = 40

/**
 * Snippet as plain-text segments, windowed around the first match so a match
 * deep in a long message is still shown and highlighted. The UI renders the
 * segments as text nodes, never HTML.
 */
export function snippetSegments(content: string, text: string): { text: string; match: boolean }[] {
  const full = content.replace(UNSAFE, "").replace(/\s+/g, " ").trim()
  if (!full) return []
  const terms = [...new Set(text.toLowerCase().split(/\s+/).filter((t) => t.length >= 2))]
  const fullLower = full.toLowerCase()
  const first = terms.map((t) => fullLower.indexOf(t)).filter((i) => i >= 0).sort((a, b) => a - b)[0]
  const start = first === undefined ? 0 : Math.max(0, first - SNIPPET_LEAD)
  const end = Math.min(full.length, start + SNIPPET_MAX)
  const clean = `${start > 0 ? "\u2026" : ""}${full.slice(start, end)}${end < full.length ? "\u2026" : ""}`
  if (!terms.length) return [{ text: clean, match: false }]
  const lower = clean.toLowerCase()
  const marks: [number, number][] = []
  for (const term of terms) {
    let i = lower.indexOf(term)
    while (i >= 0 && marks.length < 50) { marks.push([i, i + term.length]); i = lower.indexOf(term, i + term.length) }
  }
  marks.sort((a, b) => a[0] - b[0])
  const out: { text: string; match: boolean }[] = []
  let pos = 0
  for (const [s, e] of marks) {
    if (e <= pos) continue
    const from = Math.max(s, pos)
    if (from > pos) out.push({ text: clean.slice(pos, from), match: false })
    out.push({ text: clean.slice(from, e), match: true })
    pos = e
  }
  if (pos < clean.length) out.push({ text: clean.slice(pos), match: false })
  return out
}
