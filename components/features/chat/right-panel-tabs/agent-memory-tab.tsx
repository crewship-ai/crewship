"use client"

import { useCallback, useState } from "react"
import Link from "next/link"
import { Brain } from "lucide-react"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { useWorkspaceResource } from "@/components/features/crews/memory-workspace"
import { memoryNoteLabel, type MemoryDocument } from "@/components/features/crews/memory-notes-browser"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { entityHref } from "@/lib/entity-links"
import { useChatAgent } from "../chat-agent-context"
import { PreferencesTab } from "./preferences-tab"

interface Inventory { documents: MemoryDocument[] }

// Knowledge first, then the newest daily notes. Chat shows a short read-only
// view; settings, history, export and Forget stay on the agent's Memory page.
const DAILY_NOTES = 3

function ordered(docs: MemoryDocument[]) {
  const knowledge = docs.filter((doc) => !doc.name.startsWith("daily/")).sort((a, b) => (a.name === "AGENT.md" || a.name === "CREW.md" ? -1 : b.name === "AGENT.md" || b.name === "CREW.md" ? 1 : a.name.localeCompare(b.name)))
  const daily = docs.filter((doc) => doc.name.startsWith("daily/")).sort((a, b) => b.name.localeCompare(a.name)).slice(0, DAILY_NOTES)
  return [...knowledge, ...daily]
}

function Note({ doc }: { doc: MemoryDocument }) {
  return <li className="rounded-lg border border-border/60 bg-card p-2.5">
    <p className="mb-1 text-micro font-medium text-foreground">{memoryNoteLabel(doc)}</p>
    {doc.state === "available"
      // Focusable so a long note scrolls with the keyboard, not only the mouse.
      ? <div tabIndex={0} role="region" aria-label={`${memoryNoteLabel(doc)} note`} className="max-h-48 overflow-y-auto rounded focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <MarkdownContent className="break-words text-xs text-muted-foreground [overflow-wrap:anywhere] [&_ul]:list-disc [&_ol]:list-decimal">{doc.content || "This note is empty."}</MarkdownContent>
      </div>
      : <p className="text-xs text-muted-foreground">This note is unavailable right now.</p>}
  </li>
}

/** Read-only projection of what the agent and its crew remember, plus the viewer's own preferences. */
export function AgentMemoryTab({ agentId, workspaceId }: { agentId: string; workspaceId: string | null }) {
  const agent = useChatAgent()
  const name = agent?.name || "This agent"
  const [revision, setRevision] = useState(0)
  // Memory is written during and at the end of a run; there is no memory event.
  const reload = useCallback(() => setRevision((n) => n + 1), [])
  // Only this agent's finished run can have written its memory. A failed,
  // cancelled or timed-out run (run.failed) may have written it too.
  const reloadForAgent = useCallback((event: { payload: Record<string, unknown> }) => { if (event.payload?.agent_id === agentId) reload() }, [agentId, reload])
  useRealtimeEventSafe("run.completed", reloadForAgent)
  useRealtimeEventSafe("run.failed", reloadForAgent)
  useRealtimeEventSafe("realtime.reconnected", reload)
  const ws = workspaceId ? encodeURIComponent(workspaceId) : ""
  const inventory = useWorkspaceResource<Inventory>(ws ? `/api/v1/agents/${encodeURIComponent(agentId)}/memory?workspace_id=${ws}` : "", revision)
  // "Remember between runs" is a per-agent setting; when it is off the agent
  // saves nothing, so "has not saved anything yet" would mislead.
  const settings = useWorkspaceResource<{ memory_enabled?: boolean }>(ws ? `/api/v1/agents/${encodeURIComponent(agentId)}?workspace_id=${ws}` : "")
  const memoryOff = settings.data?.memory_enabled === false
  // Keep the last good read on screen while a refresh is in flight.
  // Compared by the response object's identity, never by a derived value: a
  // reply without a documents array must not make every render set state.
  const [shown, setShown] = useState<{ url: string; data: Inventory } | null>(null)
  const url = workspaceId ? `${agentId}:${workspaceId}` : ""
  if (inventory.data && (shown?.url !== url || shown.data !== inventory.data)) setShown({ url, data: inventory.data })
  const shownDocs = shown?.url === url ? shown.data?.documents : undefined
  const docs = shown?.url !== url ? null : Array.isArray(shownDocs) ? shownDocs : []
  const agentDocs = docs ? ordered(docs.filter((doc) => doc.scope === "agent")) : []
  const crewDocs = docs ? ordered(docs.filter((doc) => doc.scope === "crew" && !doc.name.startsWith("daily/"))) : []

  if (!workspaceId) return <p className="p-3 text-sm text-muted-foreground">Select a workspace to see this agent’s memory.</p>
  return <div className="space-y-5 p-3 text-xs">
    <section aria-label={`What ${name} remembers`} className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <h3 className="eyebrow">What {name} remembers</h3>
        {agent?.slug && <Link href={entityHref({ kind: "agent", slug: agent.slug, tab: "memory" })} className="text-micro text-primary hover:underline">Manage memory</Link>}
      </div>
      {inventory.error && !docs ? <p role="alert" className="text-muted-foreground">Memory could not be loaded.</p>
        : !docs ? <p role="status" className="text-muted-foreground">Loading memory…</p>
          : memoryOff && !agentDocs.length ? <p className="flex items-center gap-2 text-muted-foreground"><Brain className="size-4" aria-hidden />Memory is off for {name}. Turn on Remember between runs in Manage memory.</p>
            : !agentDocs.length && !crewDocs.length ? <p className="flex items-center gap-2 text-muted-foreground"><Brain className="size-4" aria-hidden />{name} has not saved anything yet.</p>
              : agentDocs.length > 0 && <ul className="space-y-1.5">{agentDocs.map((doc) => <Note key={doc.id} doc={doc} />)}</ul>}
      {crewDocs.length > 0 && <>
        <h4 className="pt-2 text-micro font-medium uppercase tracking-wider text-muted-foreground">Shared with the crew</h4>
        <ul className="space-y-1.5">{crewDocs.map((doc) => <Note key={doc.id} doc={doc} />)}</ul>
      </>}
    </section>
    <div className="border-t pt-3"><PreferencesTab agentId={agentId} workspaceId={workspaceId} /></div>
  </div>
}
