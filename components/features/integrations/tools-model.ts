// Tools (Composio) for the Harbor 2 lists and app pages.
//
// Access is per agent — that is what the server supports (bind is
// POST /integrations/composio/agents/{id}/bind with the agent's whole app
// list). The mockup's per-crew access is not offered; agents are grouped by
// crew so the table still reads by team.

import { apiFetch } from "@/lib/api-fetch"
import type { AgentBinding, AgentBindingsMap, AgentLite, BindingMode, ConnectedAccount, Tool, ToolsResp } from "./composio/types"

export type AccessLevel = "off" | BindingMode

export interface AccountRow {
  account: ConnectedAccount
  slug: string
  /** Agents that hold this app, with their level. */
  agents: { agent: AgentLite; mode: BindingMode }[]
  signedOut: boolean
}

export function isSignedOut(a: ConnectedAccount): boolean {
  return a.status.toUpperCase() !== "ACTIVE"
}

export function accountRows(accounts: ConnectedAccount[], agents: AgentLite[], bindings: AgentBindingsMap): AccountRow[] {
  return accounts.map((account) => ({
    account,
    slug: account.toolkit.slug,
    signedOut: isSignedOut(account),
    agents: agents.flatMap((agent) => {
      const b = (bindings[agent.id] ?? []).find((x) => x.toolkit === account.toolkit.slug && x.user_id === account.user_id)
      return b ? [{ agent, mode: b.mode }] : []
    }),
  }))
}

const MODE_WORD: Record<BindingMode, string> = { full: "Full", read: "Read-only", custom: "Custom" }

/** "Code review: 2 agents Full · Research: 1 agent Read-only". */
export function accessSummary(row: AccountRow): string {
  if (row.agents.length === 0) return "Not given to any agent"
  const groups = new Map<string, Map<BindingMode, number>>()
  for (const { agent, mode } of row.agents) {
    const crew = agent.crew?.name ?? "No crew"
    const g = groups.get(crew) ?? new Map<BindingMode, number>()
    g.set(mode, (g.get(mode) ?? 0) + 1)
    groups.set(crew, g)
  }
  return [...groups.entries()]
    .map(([crew, modes]) => `${crew}: ${[...modes.entries()].map(([m, n]) => `${n} ${n === 1 ? "agent" : "agents"} ${MODE_WORD[m]}`).join(", ")}`)
    .join(" · ")
}

export type ToolsListView = "connected" | "attention" | "idle"

export function filterAccounts(rows: AccountRow[], view: ToolsListView, search: string, crew: string | null): AccountRow[] {
  const q = search.trim().toLowerCase()
  return rows.filter((r) => {
    if (view === "attention" && !r.signedOut) return false
    if (view === "idle" && r.agents.length > 0) return false
    if (crew && !r.agents.some((a) => (a.agent.crew?.name ?? "") === crew)) return false
    if (q && !`${r.slug} ${r.account.id}`.toLowerCase().includes(q)) return false
    return true
  })
}

/** Agents grouped by crew, for the Access tab. */
export function agentsByCrew(agents: AgentLite[]): { crew: string; agents: AgentLite[] }[] {
  const m = new Map<string, AgentLite[]>()
  for (const a of agents) {
    const k = a.crew?.name ?? "No crew"
    m.set(k, [...(m.get(k) ?? []), a])
  }
  return [...m.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([crew, list]) => ({ crew, agents: list }))
}

export interface BindPayload {
  user_id: string
  apps: { toolkit: string; mode: BindingMode; tools?: string[] }[]
}

export type AccessChange = { kind: "unbind"; toolkit: string } | { kind: "bind"; payload: BindPayload }

/**
 * What to send for one agent × one app. Off removes just that app
 * (DELETE …/bind?toolkit=) — a bind with an empty app list would grant every
 * connected app. Otherwise the agent's whole app list is posted with this app
 * changed; a custom grant keeps its tool list. Null when the agent acts as
 * another Composio user than this account's — that is changed on the agent.
 */
export function accessChange(current: AgentBinding[], toolkit: string, level: AccessLevel, accountUser: string): AccessChange | null {
  if (level === "off") return { kind: "unbind", toolkit }
  const others = current.filter((b) => b.toolkit !== toolkit)
  const user = current[0]?.user_id ?? accountUser
  if (user !== accountUser) return null
  const existing = current.find((b) => b.toolkit === toolkit)
  const apps = others.map((b) => ({ toolkit: b.toolkit, mode: b.mode, ...(b.mode === "custom" ? { tools: b.tools ?? [] } : {}) }))
  apps.push({ toolkit, mode: level, ...(level === "custom" ? { tools: existing?.tools ?? [] } : {}) })
  return { kind: "bind", payload: { user_id: user, apps } }
}

/** Shorter queries would ask every connected app for half its tools. */
export const TOOL_SEARCH_MIN = 2
/** Tools shown per app in the panel search. */
export const TOOL_SEARCH_PER_APP = 5

export interface ToolHit {
  tool: Tool
  slug: string
  /** The connected account whose page opens on Tools. */
  accountId: string
}

/**
 * Tool search across connected apps (N12). The server lists tools per app
 * only (GET …/composio/tools?toolkit=), so each connected app is asked once
 * with the query. An app that fails is named, not hidden behind "no results".
 */
export async function searchToolsAcrossApps(
  workspaceId: string,
  apps: { slug: string; accountId: string }[],
  query: string,
  signal?: AbortSignal,
): Promise<{ hits: ToolHit[]; failed: string[] }> {
  const q = query.trim()
  if (q.length < TOOL_SEARCH_MIN) return { hits: [], failed: [] }
  const bySlug = new Map<string, string>()
  for (const a of apps) if (!bySlug.has(a.slug)) bySlug.set(a.slug, a.accountId)
  const answers = await Promise.all(
    [...bySlug.entries()].map(async ([slug, accountId]) => {
      const params = new URLSearchParams({ workspace_id: workspaceId, toolkit: slug, search: q, limit: String(TOOL_SEARCH_PER_APP) })
      try {
        const r = await apiFetch(`/api/v1/integrations/composio/tools?${params}`, signal ? { signal } : undefined)
        if (!r.ok) throw new Error(`Failed (${r.status})`)
        const j = (await r.json()) as ToolsResp
        return { slug, hits: (j.tools ?? []).map((tool) => ({ tool, slug, accountId })), failed: false }
      } catch (e) {
        if ((e as Error).name === "AbortError") throw e
        return { slug, hits: [] as ToolHit[], failed: true }
      }
    }),
  )
  return { hits: answers.flatMap((a) => a.hits), failed: answers.filter((a) => a.failed).map((a) => a.slug) }
}
