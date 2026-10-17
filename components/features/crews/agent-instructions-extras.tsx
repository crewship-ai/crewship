"use client"

import { useCallback, useEffect, useState } from "react"
import { Clock, RotateCcw } from "lucide-react"
import { toast } from "@/lib/toast"

import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { apiFetch } from "@/lib/api-fetch"
import { toApiError } from "@/lib/api-error"
import { cn } from "@/lib/utils"

import { AgentReadOrder } from "./agent-read-order"
import { CrCard, CrLine, CrNote } from "./cr-card"

interface PersonaState { layer: string; from_default?: boolean }
interface HistoryEntry { id: string; sha256: string; bytes: number; written_at: string; written_by?: string }

const FIELD = cn("w-full rounded-md border border-hairline bg-background px-2.5 py-2 font-mono outline-none focus:border-primary", CREATE_SURFACE_INPUT)

/**
 * A-PERS around the inline instructions form: where the agent's PERSONA.md
 * comes from, its version history, Reset to crew default (asks first), and
 * the crew default it falls back to. "Suggest improvements" is not here: the
 * server's persona/suggest is the agent's own proposal flow, not a generator.
 */
export function AgentInstructionsExtras({ workspaceId, agentId, agentName, crew, canEdit, memoryEnabled = true, onReset }: {
  workspaceId: string
  agentId: string
  agentName: string
  crew: { id: string; name: string } | null
  /** MANAGER+ (persona PUT/DELETE are roleCreate). */
  canEdit: boolean
  /** Remember between runs: without it a run reads no memory and no PERSONA.md. */
  memoryEnabled?: boolean
  /** After a reset: the page reloads the inline form. */
  onReset: () => void
}) {
  const ws = encodeURIComponent(workspaceId)
  const [rev, setRev] = useState(0)
  const [persona, setPersona] = useState<PersonaState | null>(null)
  const [personaFailed, setPersonaFailed] = useState(false)
  const [history, setHistory] = useState<HistoryEntry[] | null>(null)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [historyFailed, setHistoryFailed] = useState(false)
  const [confirmReset, setConfirmReset] = useState(false)

  useEffect(() => {
    let cancelled = false
    setPersonaFailed(false)
    apiFetch(`/api/v1/agents/${agentId}/persona?workspace_id=${ws}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((v: PersonaState) => { if (!cancelled) setPersona(v) })
      .catch(() => { if (!cancelled) setPersonaFailed(true) })
    return () => { cancelled = true }
  }, [agentId, ws, rev])

  useEffect(() => {
    if (!historyOpen) return
    let cancelled = false
    setHistory(null)
    setHistoryFailed(false)
    apiFetch(`/api/v1/agents/${agentId}/persona/history?workspace_id=${ws}&limit=20`)
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((v: { entries?: HistoryEntry[] }) => { if (!cancelled) setHistory(Array.isArray(v?.entries) ? v.entries : []) })
      .catch(() => { if (!cancelled) setHistoryFailed(true) })
    return () => { cancelled = true }
  }, [agentId, ws, historyOpen, rev])

  const reset = useCallback(async () => {
    const r = await apiFetch(`/api/v1/agents/${agentId}/persona?workspace_id=${ws}`, { method: "DELETE" })
    if (!r.ok) {
      const err = await toApiError(r, `HTTP ${r.status}`)
      toast.error("Instructions were not reset", { description: err.message })
      throw err
    }
    toast.success(crew ? `${agentName} uses the ${crew.name} default again` : `${agentName} uses the default instructions again`)
    setRev((n) => n + 1)
    onReset()
  }, [agentId, ws, agentName, crew, onReset])

  const own = persona?.layer === "agent" && !persona.from_default
  const source = !persona
    ? personaFailed ? "Where its instructions come from could not be read." : "Loading…"
    : own ? "Its own instructions" : persona.layer === "crew" && crew ? `Uses the ${crew.name} default` : "Uses the built-in default"

  return (
    <>
      <CrCard
        title={`${agentName}’s instructions`}
        concept="crews"
        data-testid="agent-instructions-source"
        action={
          <span className="flex items-center gap-1">
            <Button variant="ghost" size="sm" aria-expanded={historyOpen} onClick={() => setHistoryOpen((o) => !o)}>
              <Clock className="size-3.5" aria-hidden /> History
            </Button>
            {canEdit && own && (
              <Button variant="ghost" size="sm" onClick={() => setConfirmReset(true)}>
                <RotateCcw className="size-3.5" aria-hidden /> Reset to crew default…
              </Button>
            )}
          </span>
        }
      >
        <CrLine title={source} sub="Saved as PERSONA.md with version history. The save bar saves edits above." />
        {historyOpen && (
          historyFailed ? <CrNote tone="error">The history could not be loaded.</CrNote>
            : history === null ? <CrNote>Loading history…</CrNote>
              : history.length === 0 ? <CrNote>No recorded versions yet.</CrNote>
                : history.map((h) => (
                  <CrLine
                    key={h.id}
                    title={new Date(h.written_at).toLocaleString()}
                    sub={[h.written_by || null, `${h.bytes} bytes`, h.sha256.slice(0, 8)].filter(Boolean).join(" · ")}
                  />
                ))
        )}
      </CrCard>

      {crew && <CrewDefaultCard workspaceId={workspaceId} crew={crew} canEdit={canEdit} onSaved={() => setRev((n) => n + 1)} />}

      <AgentReadOrder crewName={crew?.name ?? null} memoryEnabled={memoryEnabled} />

      <ConfirmDialog
        open={confirmReset}
        onOpenChange={setConfirmReset}
        title={crew ? `Reset ${agentName}’s instructions to the ${crew.name} default?` : `Reset ${agentName}’s instructions to the default?`}
        description="Its own PERSONA.md is removed; the next run reads the crew default instead."
        consequences={[
          { tone: "lost", text: "Its own instructions stop applying" },
          { tone: "kept", text: "Earlier versions stay in History" },
        ]}
        confirmLabel="Reset instructions"
        onConfirm={reset}
      />
    </>
  )
}

/** "Crew default · {crew}": what every agent of the crew reads without its own instructions. */
function CrewDefaultCard({ workspaceId, crew, canEdit, onSaved }: {
  workspaceId: string
  crew: { id: string; name: string }
  canEdit: boolean
  onSaved: () => void
}) {
  const ws = encodeURIComponent(workspaceId)
  const [content, setContent] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)
  const [rev, setRev] = useState(0)
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState("")
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    setFailed(false)
    apiFetch(`/api/v1/crews/${crew.id}/persona?workspace_id=${ws}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((v: { content?: string }) => { if (!cancelled) setContent(v?.content ?? "") })
      .catch(() => { if (!cancelled) setFailed(true) })
    return () => { cancelled = true }
  }, [crew.id, ws, rev])

  const save = async () => {
    setBusy(true)
    try {
      const r = await apiFetch(`/api/v1/crews/${crew.id}/persona?workspace_id=${ws}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ content: draft }),
      })
      if (!r.ok) throw await toApiError(r, `HTTP ${r.status}`)
      toast.success(`${crew.name} default saved`)
      setOpen(false)
      setRev((n) => n + 1)
      onSaved()
    } catch (err) {
      toast.error("The crew default was not saved", { description: err instanceof Error ? err.message : String(err) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <CrCard
      title={`Crew default · ${crew.name}`}
      concept="crews"
      action={canEdit && content !== null ? (
        <Button variant="ghost" size="sm" onClick={() => { setDraft(content); setOpen(true) }}>Edit crew default</Button>
      ) : undefined}
    >
      {failed ? <CrNote tone="error">The crew default could not be loaded.</CrNote>
        : content === null ? <CrNote>Loading…</CrNote>
          : content.trim() === "" ? <CrNote>No crew default yet. Agents without their own instructions use the built-in default.</CrNote>
            : <pre className="max-h-40 overflow-y-auto whitespace-pre-wrap break-words border-t border-border px-4 py-3 font-mono text-label text-muted-foreground">{content}</pre>}

      <CreateSurface open={open} onOpenChange={setOpen} size="md" dirty={draft !== content} discardLabel="this crew default" onSubmit={() => void save()}>
        <CreateSurfaceHeader concept="crews" context={crew.name} title="Crew default instructions" description="Every agent of this crew reads these unless it has its own." onClose={() => setOpen(false)} />
        <CreateSurfaceBody>
          <CreateSurfaceField label="Instructions" htmlFor="crew-default-persona">
            <textarea id="crew-default-persona" aria-label="Crew default instructions" rows={10} spellCheck={false} className={cn(FIELD, "h-auto")} value={draft} onChange={(e) => setDraft(e.target.value)} />
          </CreateSurfaceField>
        </CreateSurfaceBody>
        <CreateSurfaceFooter onCancel={() => setOpen(false)} primaryLabel="Save crew default" busy={busy} onPrimary={() => void save()} />
      </CreateSurface>
    </CrCard>
  )
}
