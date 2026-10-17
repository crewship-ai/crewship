"use client"

import { useCallback, useState } from "react"
import { Clock } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Switch } from "@/components/ui/switch"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { AgentLearningToggle } from "@/components/features/agents/agent-learning-toggle"
import { apiFetch } from "@/lib/api-fetch"
import { toApiError } from "@/lib/api-error"
import { AGENT_SELF_LEARNING } from "@/lib/feature-gates"
import { toastSwitchError } from "@/components/ui/page-save-bar"

import { CrCard, CrLine, CrNote } from "./cr-card"
import { HistoryEntries, MemoryWorkspace, useWorkspaceResource } from "./memory-workspace"
import { MemoryExportButton } from "./agent-canvas-tabs/memory-export-button"
import type { MemoryDocument } from "./memory-notes-browser"

interface Inventory { documents: MemoryDocument[]; scopes: Record<string, string> }
interface MyCards { peers: Array<{ id: string; agent_id?: string; agent_slug: string; content?: string }> }

/**
 * A-MEM: Remember between runs (saves on the spot), what the agent knows
 * about the person looking (Forget asks first), and its AGENT.md with
 * History, Export and every note. "Learn on its own" follows the same
 * feature gate as everywhere else (AGENT_SELF_LEARNING).
 */
export function AgentMemoryCards({
  workspaceId, agentId, agentSlug, agentName, crewId, userId, userName,
  memoryEnabled, canEdit, canExport, onMemoryEnabled,
}: {
  workspaceId: string
  agentId: string
  agentSlug: string
  agentName: string
  crewId?: string
  /** The signed-in person; Forget is theirs only. */
  userId?: string
  userName?: string
  memoryEnabled: boolean
  canEdit: boolean
  canExport: boolean
  onMemoryEnabled: (next: boolean) => Promise<void>
}) {
  const ws = encodeURIComponent(workspaceId)
  const [rev, setRev] = useState(0)
  const [remember, setRemember] = useState(memoryEnabled)
  const [savingRemember, setSavingRemember] = useState(false)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [allNotes, setAllNotes] = useState(false)
  const [confirmForget, setConfirmForget] = useState(false)

  const inventory = useWorkspaceResource<Inventory>(`/api/v1/agents/${agentId}/memory?workspace_id=${ws}`)
  const cards = useWorkspaceResource<MyCards>(`/api/v1/users/me/peer-cards?workspace_id=${ws}`, rev)
  const agentDocs = inventory.data?.documents.filter((d) => d.scope === "agent") ?? []
  const agentMd = agentDocs.find((d) => d.name === "AGENT.md")
  const mine = cards.data?.peers.find((p) => p.agent_id === agentId || p.agent_slug === agentSlug)

  const flipRemember = async (next: boolean) => {
    setRemember(next)
    setSavingRemember(true)
    try {
      await onMemoryEnabled(next)
    } catch (err) {
      setRemember(!next)
      toastSwitchError("Remember between runs", err instanceof Error ? err.message : String(err), () => void flipRemember(next))
    } finally {
      setSavingRemember(false)
    }
  }

  const forget = useCallback(async () => {
    if (!userId) return
    const r = await apiFetch(`/api/v1/agents/${agentId}/peers/${encodeURIComponent(userId)}?workspace_id=${ws}`, { method: "DELETE" })
    if (!r.ok && r.status !== 404) throw await toApiError(r, `HTTP ${r.status}`)
    setRev((n) => n + 1)
  }, [agentId, userId, ws])

  return (
    <div className="space-y-4" data-testid="agent-memory">
      <CrCard title="Memory" concept="memory">
        <CrLine
          title="Remember between runs"
          sub="Daily notes, merged into AGENT.md every night"
          right={<Switch aria-label="Remember between runs" checked={remember} disabled={!canEdit || savingRemember} onCheckedChange={(v) => void flipRemember(v)} />}
        />
        {AGENT_SELF_LEARNING && (
          <div className="border-t border-border">
            <AgentLearningToggle bare agentId={agentId} workspaceId={workspaceId} />
          </div>
        )}
      </CrCard>

      <div className="grid gap-4 xl:grid-cols-2">
        <CrCard title="What it knows about you" concept="memory">
          {cards.error ? <CrNote tone="error">What it knows about you could not be loaded.</CrNote>
            : !cards.data ? <CrNote>Loading…</CrNote>
              : !mine ? <CrNote>Nothing saved about you yet.</CrNote>
                : (
                  <CrLine
                    title={userName ?? "You"}
                    sub={mine.content ? mine.content.replace(/\s+/g, " ").trim() : "A note about working with you"}
                    right={userId ? <Button variant="ghost" size="sm" onClick={() => setConfirmForget(true)}>Forget</Button> : undefined}
                  />
                )}
        </CrCard>

        <CrCard
          title="AGENT.md"
          concept="memory"
          action={
            <span className="flex flex-wrap items-center gap-1">
              {agentMd?.history_path && (
                <Button variant="ghost" size="sm" aria-expanded={historyOpen} onClick={() => setHistoryOpen((o) => !o)}>
                  <Clock className="size-3.5" aria-hidden /> History
                </Button>
              )}
              {canExport && crewId && <MemoryExportButton crewId={crewId} agentSlug={agentSlug} workspaceId={workspaceId} />}
              {agentDocs.length > 0 && (
                <Button variant="ghost" size="sm" aria-expanded={allNotes} onClick={() => setAllNotes((o) => !o)}>All notes · {agentDocs.length}</Button>
              )}
            </span>
          }
        >
          {inventory.error ? <CrNote tone="error">Its memory could not be loaded.</CrNote>
            : !inventory.data ? <CrNote>Loading…</CrNote>
              : !agentMd ? <CrNote>No AGENT.md yet. {agentName} writes it as it learns.</CrNote>
                : (
                  <div className="border-t border-border px-4 py-3">
                    {agentMd.state === "available"
                      ? <MarkdownContent className="max-h-64 overflow-y-auto break-words text-body [overflow-wrap:anywhere] [&_ul]:list-disc [&_ol]:list-decimal">{agentMd.content || "This note is empty."}</MarkdownContent>
                      : <p role="alert" className="text-body text-chip-danger-fg">The current content is unavailable.</p>}
                    {historyOpen && agentMd.history_path && <HistoryEntries path={agentMd.history_path} workspaceId={workspaceId} />}
                  </div>
                )}
        </CrCard>
      </div>

      {allNotes && <MemoryWorkspace key={agentId} workspaceId={workspaceId} agentId={agentId} agentSlug={agentSlug} crewId={crewId} memoryEnabled={remember} />}

      <ConfirmDialog
        open={confirmForget}
        onOpenChange={setConfirmForget}
        title={`Forget what ${agentName} knows about you?`}
        description={`${agentName}’s note about working with you is deleted. It may learn it again while personalization is on.`}
        confirmLabel="Forget"
        destructive
        onConfirm={forget}
      />
    </div>
  )
}
