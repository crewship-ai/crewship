"use client"

import { useEffect, useMemo, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { readTotalCount } from "@/hooks/use-paged-list"
import type { ChatTreeAgent, ChatTreeThread } from "./chat-tree-data"
import { scopeKindParam, classifyThread, type ChatScope } from "./chat-kind"
import type { ConversationRow } from "./conversations-sidebar"

/** Server-side title search, independent of the initial twelve-agent/ten-chat page. */
export function useSidebarSessionSearch(workspaceId: string | undefined, agents: ChatTreeAgent[], query: string, scope: ChatScope) {
  const q = query.trim()
  const rosterKey = JSON.stringify(agents.map(({ id, name }) => [id, name]))
  const roster: [string, string][] = useMemo(() => JSON.parse(rosterKey), [rosterKey])
  const identity = JSON.stringify([workspaceId, q, scope, rosterKey])
  const [request, setRequest] = useState({ identity: "", page: 0, retry: 0 })
  const page = request.identity === identity ? request.page : 0
  const [state, setState] = useState<{ identity: string; rows: { agentId: string; thread: ChatTreeThread }[]; more: boolean; error: boolean; loading: boolean }>({ identity: "", rows: [], more: false, error: false, loading: false })
  useEffect(() => {
    if (!workspaceId || !q) return
    const controller = new AbortController()
    const timer = setTimeout(() => {
      setState((old) => ({ identity, rows: old.identity === identity ? old.rows : [], more: false, error: false, loading: true }))
      void (async () => {
        const found: { agentId: string; thread: ChatTreeThread }[] = []
        let more = false
        // Limit concurrent HTTP requests while still searching every accessible agent.
        for (let start = 0; start < roster.length; start += 4) {
          if (controller.signal.aborted) return
          await Promise.all(roster.slice(start, start + 4).map(async ([id, name]) => {
            const params = new URLSearchParams({ workspace_id: workspaceId, limit: "100", offset: String(page * 100), source: "1" })
            if (!name.toLowerCase().includes(q.toLowerCase())) params.set("q", q)
            const kind = scopeKindParam(scope)
            if (kind) params.set("kind", kind)
            const res = await apiFetch(`/api/v1/agents/${encodeURIComponent(id)}/chats?${params}`, { signal: controller.signal })
            if (!res.ok) throw new Error("Session search unavailable")
            const rows: ChatTreeThread[] = await res.json()
            if (!Array.isArray(rows)) throw new Error("Invalid session search response")
            found.push(...rows.map((thread) => ({ agentId: id, thread })))
            const total = readTotalCount(res.headers)
            more ||= total === null ? rows.length === 100 : (page + 1) * 100 < total
          }))
        }
        if (!controller.signal.aborted) setState((old) => {
          const merged = new Map((page > 0 && old.identity === identity ? old.rows : []).map((row) => [row.thread.id, row]))
          for (const row of found) merged.set(row.thread.id, row)
          return { identity, rows: [...merged.values()], more, error: false, loading: false }
        })
      })().catch(() => { if (!controller.signal.aborted) setState((old) => ({ ...old, identity, error: true, loading: false })) })
    }, 250)
    return () => { clearTimeout(timer); controller.abort() }
  }, [workspaceId, q, scope, identity, roster, page, request.retry])
  const current = state.identity === identity
  const rows: ConversationRow[] = current ? state.rows.flatMap(({ agentId, thread }) => {
    const agent = agents.find((item) => item.id === agentId)
    return agent ? [{ agent, thread, kind: classifyThread(thread), at: Date.parse(thread.last_activity_at || thread.started_at) }] : []
  }) : []
  rows.sort((a, b) => b.at - a.at)
  return { rows, loading: !!q && (!current || state.loading), error: current && state.error, more: current && state.more, loadMore: () => setRequest({ identity, page: page + 1, retry: 0 }), retry: () => setRequest((old) => ({ identity, page, retry: old.retry + 1 })) }
}
