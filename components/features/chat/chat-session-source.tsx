"use client"

import { useEffect, useState } from "react"
import { ExternalLink } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { useDrawerStore } from "@/stores/drawer-store"
import type { ChatTreeThread, ChatWorkSource } from "./chat-tree-data"

/** Exact persisted source; never guess a routine from the session's editable title. */
export function ChatSessionSource({ workspaceId, agentId, sessionId, onOpenWork }: { workspaceId: string | null; agentId: string; sessionId: string; onOpenWork?: () => void }) {
  const key = JSON.stringify([workspaceId, agentId, sessionId])
  const [result, setResult] = useState<{ key: string; source: ChatWorkSource | null } | null>(null)
  const data = result?.key === key ? result.source : null
  useEffect(() => {
    if (!workspaceId || !agentId || !sessionId) return
    const controller = new AbortController()
    void apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/chats?workspace_id=${encodeURIComponent(workspaceId)}&chat_id=${encodeURIComponent(sessionId)}&source=1`, { signal: controller.signal }).then(async (res) => {
      if (!res.ok) throw new Error("Source unavailable")
      const rows: ChatTreeThread[] = await res.json()
      if (!controller.signal.aborted) setResult({ key, source: Array.isArray(rows) ? rows.find((row) => row.id === sessionId)?.source ?? null : null })
    }).catch(() => { if (!controller.signal.aborted) setResult({ key, source: null }) })
    return () => controller.abort()
  }, [workspaceId, agentId, sessionId, key])
  useEffect(() => {
    useDrawerStore.setState({ workSource: data && workspaceId ? { workspaceId, agentId, source: data } : null })
  }, [data, workspaceId, agentId, sessionId])
  if (!data || !workspaceId) return null
  return <button type="button" title={`Open ${data.kind}: ${data.name}`} onClick={() => {
    useDrawerStore.setState({ open: true, activeTab: "work", workSource: { workspaceId, agentId, source: data } })
    onOpenWork?.()
  }} className="kit-tap flex max-w-56 items-center gap-1 rounded-md px-2 py-1 text-[10px] text-primary hover:bg-primary/10"><span className="truncate">{data.kind === "routine" ? "Routine" : "Issue"} · {data.name}</span><ExternalLink className="size-3 shrink-0" /></button>
}
