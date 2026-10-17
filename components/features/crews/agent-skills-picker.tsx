"use client"

import { useEffect, useMemo, useState } from "react"

import { toast } from "@/lib/toast"
import { apiFetch } from "@/lib/api-fetch"
import { toApiError } from "@/lib/api-error"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"

import type { AgentSkillRow } from "./agent-canvas-tabs/types"

// "Give to" seen from one agent (A-TOOLS Add skill, K09). The Skills area's
// GiveToPicker (components/features/skills/give-to-picker.tsx, #3399) gives
// skills to agents; this is the same contract turned around — one agent, the
// workspace's skills — until the kit carries one shared picker (kit request in
// #3490): a draft checklist, "N added · M removed", Apply sends only the diff,
// and a partial failure keeps the dialog open with what failed and a Retry.

interface LibrarySkill {
  id: string
  name: string
  display_name?: string | null
  category?: string | null
  description?: string | null
}

type Change = { kind: "add"; skillId: string } | { kind: "remove"; skillId: string; assignmentId: string }

/** skill id → assignment id for what the agent holds; `draft` is what should be held. */
export function agentSkillDiff(held: Map<string, string>, draft: Set<string>) {
  return {
    add: [...draft].filter((id) => !held.has(id)),
    remove: [...held].filter(([id]) => !draft.has(id)).map(([skillId, assignmentId]) => ({ skillId, assignmentId })),
  }
}

const label = (s: { name: string; display_name?: string | null }) => s.display_name || s.name

export function AgentSkillsPicker({ open, agentId, agentName, workspaceId, assigned, onClose, onApplied }: {
  open: boolean
  agentId: string
  agentName: string
  workspaceId: string
  /** What the agent holds now (GET /agents/{id}/skills). */
  assigned: AgentSkillRow[]
  onClose: () => void
  /** After any write, so the caller reloads the agent's skills. */
  onApplied: () => void
}) {
  const held = useMemo(() => new Map(assigned.map((a) => [a.skill_id, a.id])), [assigned])
  const [library, setLibrary] = useState<LibrarySkill[] | null>(null)
  const [loadFailed, setLoadFailed] = useState(false)
  const [draft, setDraft] = useState<Set<string>>(() => new Set(held.keys()))
  const [query, setQuery] = useState("")
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<Change[]>([])

  useEffect(() => {
    if (!open) return
    setDraft(new Set(held.keys()))
    setQuery("")
    setFailed([])
    // Reset per opening, not on every reload of what the agent holds.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  useEffect(() => {
    if (!open || library !== null) return
    let cancelled = false
    apiFetch(`/api/v1/skills?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((v: LibrarySkill[]) => { if (!cancelled) setLibrary(Array.isArray(v) ? v : []) })
      .catch(() => { if (!cancelled) setLoadFailed(true) })
    return () => { cancelled = true }
  }, [open, library, workspaceId])

  const diff = useMemo(() => agentSkillDiff(held, draft), [held, draft])
  const changes: Change[] = useMemo(
    () => [...diff.add.map((skillId) => ({ kind: "add" as const, skillId })), ...diff.remove.map((r) => ({ kind: "remove" as const, ...r }))],
    [diff],
  )

  const q = query.trim().toLowerCase()
  const shown = (library ?? []).filter((s) => !q || [s.name, s.display_name, s.category].some((f) => f?.toLowerCase().includes(q)))
  const nameOf = (id: string) => {
    const s = library?.find((x) => x.id === id) ?? assigned.find((a) => a.skill_id === id)?.skill
    return s ? label(s) : id
  }

  if (!open) return null

  const toggle = (id: string) => setDraft((d) => {
    const next = new Set(d)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    return next
  })

  const write = async (c: Change) => {
    const r = c.kind === "add"
      ? await apiFetch(`/api/v1/agents/${agentId}/skills`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ skill_id: c.skillId }) })
      : await apiFetch(`/api/v1/agents/${agentId}/skills/${c.assignmentId}`, { method: "DELETE" })
    if (!r.ok) throw await toApiError(r, `HTTP ${r.status}`)
  }

  const run = async (batch: Change[]) => {
    if (batch.length === 0) return
    setBusy(true)
    const results = await Promise.allSettled(batch.map(write))
    setBusy(false)
    const bad = batch.filter((_, i) => results[i].status === "rejected")
    if (bad.length < batch.length) onApplied()
    if (bad.length > 0) {
      setFailed(bad)
      return
    }
    setFailed([])
    const added = batch.filter((c) => c.kind === "add").length
    const removed = batch.length - added
    toast.success(
      added && removed
        ? `${agentName}: ${added} given, ${removed} taken away · from its next run`
        : removed
          ? `${removed} ${removed === 1 ? "skill" : "skills"} taken from ${agentName}`
          : `${added} ${added === 1 ? "skill" : "skills"} given to ${agentName} · from its next run`,
    )
    onClose()
  }

  const failedNames = [...new Set(failed.map((c) => nameOf(c.skillId)))]

  return (
    <CreateSurface
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      size="sm"
      dirty={changes.length > 0 && failed.length === 0}
      discardLabel="these changes"
      onSubmit={() => { if (!busy) void run(failed.length ? failed : changes) }}
    >
      <CreateSurfaceHeader
        concept="skills"
        context={agentName}
        title={`Give skills to ${agentName}`}
        description="Checked skills load on its next run. A skill is instructions; it never runs anything by itself."
        onClose={() => !busy && onClose()}
      />
      <CreateSurfaceBody padded={false} data-slot="give-to-picker">
        <div className="sticky top-0 z-10 border-b border-border bg-card px-4 py-2.5 sm:px-5">
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search skills…"
            aria-label="Search skills"
            className={CREATE_SURFACE_INPUT}
          />
        </div>
        {loadFailed ? (
          <p role="alert" className="px-5 py-6 text-center text-label text-muted-foreground">The workspace’s skills could not be loaded.</p>
        ) : library === null ? (
          <p role="status" className="px-5 py-6 text-center text-label text-muted-foreground">Loading skills…</p>
        ) : shown.length === 0 ? (
          <p className="px-5 py-6 text-center text-label text-muted-foreground">{library.length ? "No skill matches." : "The workspace has no skills yet. Add them on Skills."}</p>
        ) : (
          <div role="group" aria-label="Skills">
            {shown.map((s) => (
              <label key={s.id} className="row-hover flex min-h-11 cursor-pointer items-center gap-2.5 border-b border-border px-4 py-1.5 text-control last:border-0 sm:px-5">
                <Checkbox checked={draft.has(s.id)} aria-label={label(s)} onCheckedChange={() => toggle(s.id)} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate">{label(s)}</span>
                  {(s.category || s.description) && (
                    <span className="block truncate text-micro text-muted-foreground">{[s.category, s.description].filter(Boolean).join(" · ")}</span>
                  )}
                </span>
              </label>
            ))}
          </div>
        )}
      </CreateSurfaceBody>
      <CreateSurfaceRefusal
        message={failed.length ? `${failed.length} ${failed.length === 1 ? "change" : "changes"} failed for ${failedNames.join(", ")}. The others were applied.` : null}
      />
      <CreateSurfaceFooter
        lead={
          <span className="mr-auto text-label tabular-nums text-muted-foreground">
            {diff.add.length} added · {diff.remove.length} removed
          </span>
        }
        hint={null}
        onCancel={onClose}
        guardCancel={failed.length === 0}
        primaryLabel={failed.length ? "Retry" : busy ? "Applying…" : "Apply"}
        onPrimary={() => void run(failed.length ? failed : changes)}
        primaryDisabled={failed.length === 0 && changes.length === 0}
        busy={busy}
      />
    </CreateSurface>
  )
}
