"use client"

import { useState } from "react"
import { Clock } from "lucide-react"

import { Button } from "@/components/ui/button"

import { CrCard, CrLine, CrNote } from "./cr-card"
import { HistoryEntries, useWorkspaceResource } from "./memory-workspace"
import type { MemoryDocument } from "./memory-notes-browser"
import type { AgentSummary } from "./crew-canvas-tabs/types"

interface Inventory { documents: MemoryDocument[]; scopes: Record<string, string> }
interface Versions { entries?: Array<{ id: string; written_at: string; written_by: string }>; projection?: { state: string } }

/**
 * C-MEM: CREW.md (who wrote it last, version history), and every agent's
 * AGENT.md with a way into that agent's Memory tab. The design's "Waiting for
 * review" queue is not here: the server can approve or reject a memory
 * proposal by id but has no list of them (the Inbox shows them instead).
 */
export function CrewMemoryCards({ workspaceId, crewId, agents, onOpenAgent }: {
  workspaceId: string
  crewId: string
  agents: Array<Pick<AgentSummary, "id" | "name" | "slug"> & { memory_enabled?: boolean }>
  onOpenAgent: (slug: string) => void
}) {
  const ws = encodeURIComponent(workspaceId)
  const inventory = useWorkspaceResource<Inventory>(`/api/v1/crews/${crewId}/memory?workspace_id=${ws}`)
  const crewMd = inventory.data?.documents.find((d) => d.name === "CREW.md")
  const sharedCount = inventory.data?.documents.filter((d) => d.scope === "crew" && d.name !== "CREW.md").length ?? 0

  return (
    <div className="grid gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
      <CrewMdCard workspaceId={workspaceId} doc={crewMd} loading={!inventory.data && !inventory.error} failed={!!inventory.error} />
      <CrCard title="Shared notes and agent knowledge" concept="memory" count={agents.length || undefined}>
        {sharedCount > 0 && <CrLine title="shared/" sub={`${sharedCount} crew note${sharedCount === 1 ? "" : "s"} every agent can read`} />}
        {agents.length === 0 ? (
          <CrNote>No agents in this crew yet.</CrNote>
        ) : agents.map((a) => (
          <CrLine
            key={a.id}
            title={`${a.name} · AGENT.md`}
            sub={a.memory_enabled === false ? "Memory is off for this agent" : "Topic notes, with daily notes merged every night"}
            right={<Button variant="ghost" size="sm" aria-label={`Open ${a.name}’s memory`} onClick={() => onOpenAgent(a.slug)}>Open</Button>}
          />
        ))}
      </CrCard>
    </div>
  )
}

function CrewMdCard({ workspaceId, doc, loading, failed }: { workspaceId: string; doc?: MemoryDocument; loading: boolean; failed: boolean }) {
  const [open, setOpen] = useState(false)
  const path = doc?.history_path
  const versions = useWorkspaceResource<Versions>(path ? `/api/v1/memory/versions?workspace_id=${encodeURIComponent(workspaceId)}&path=${encodeURIComponent(path)}` : "")
  const entries = path ? versions.data?.entries ?? null : null
  const last = entries?.[0]

  return (
    <CrCard
      title="CREW.md · how this crew works"
      concept="memory"
      link={{ label: "Proposals in Inbox", href: "/inbox?kind=memory_consolidation" }}
      action={path && entries ? (
        <Button variant="ghost" size="sm" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
          <Clock className="size-3.5" aria-hidden /> Version history · {entries.length}
        </Button>
      ) : undefined}
    >
      {failed ? <CrNote tone="error">Crew memory could not be loaded.</CrNote>
        : loading ? <CrNote>Loading…</CrNote>
          : !doc ? <CrNote>No CREW.md yet. The lead writes it as the crew settles how it works; every agent reads it before each run.</CrNote>
            : (
              <div className="border-t border-border px-4 py-3">
                <p className="text-label text-muted-foreground">
                  {[last ? `Last written by ${last.written_by}` : null, doc.updated_at ? `updated ${new Date(doc.updated_at).toLocaleString()}` : null, "read by every agent before each run"].filter(Boolean).join(" · ")}
                </p>
                {doc.state === "available"
                  ? <pre className="mt-2 max-h-64 overflow-y-auto whitespace-pre-wrap break-words rounded-md bg-muted px-3 py-2 font-mono text-label text-foreground">{doc.content || "This note is empty."}</pre>
                  : <p role="alert" className="mt-2 text-body text-chip-danger-fg">The current content is unavailable.</p>}
                {open && path && <HistoryEntries path={path} workspaceId={workspaceId} />}
              </div>
            )}
    </CrCard>
  )
}
