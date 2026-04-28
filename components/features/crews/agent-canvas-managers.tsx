"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { Plus, Trash2 } from "lucide-react"
import type { AgentCredRow, AgentSkillRow } from "./agent-canvas"

interface WorkspaceSkill {
  id: string
  name: string
  slug?: string | null
  display_name?: string | null
  category?: string | null
  description?: string | null
  icon?: string | null
}

function SkillsManager({ agentId, agentSlug, workspaceId, onChange }: { agentId: string; agentSlug: string; workspaceId: string; onChange: () => void }) {
  const [assigned, setAssigned] = useState<AgentSkillRow[] | null>(null)
  const [available, setAvailable] = useState<WorkspaceSkill[] | null>(null)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [busy, setBusy] = useState(false)

  const refresh = useCallback(async () => {
    try {
      const r = await fetch(`/api/v1/agents/${agentId}/skills?workspace_id=${workspaceId}`)
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const data: AgentSkillRow[] = await r.json()
      setAssigned(Array.isArray(data) ? data : [])
    } catch (err) {
      toast.error(`Could not load skills: ${err instanceof Error ? err.message : err}`)
      setAssigned([])
    }
  }, [agentId, workspaceId])

  useEffect(() => { void refresh() }, [refresh])

  const openPicker = useCallback(async () => {
    setPickerOpen(true)
    if (available !== null) return
    try {
      const r = await fetch(`/api/v1/skills?workspace_id=${workspaceId}`)
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const data: WorkspaceSkill[] = await r.json()
      setAvailable(Array.isArray(data) ? data : [])
    } catch (err) {
      toast.error(`Could not load workspace skills: ${err instanceof Error ? err.message : err}`)
      setAvailable([])
    }
  }, [available, workspaceId])

  const assign = useCallback(async (skillId: string) => {
    setBusy(true)
    try {
      const r = await fetch(`/api/v1/agents/${agentId}/skills`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ skill_id: skillId }),
      })
      if (!r.ok) throw new Error(await r.text())
      toast.success("Skill assigned")
      setPickerOpen(false)
      await refresh()
      onChange()
    } catch (err) {
      toast.error(`Assign failed: ${err instanceof Error ? err.message : err}`)
    } finally {
      setBusy(false)
    }
  }, [agentId, refresh, onChange])

  const remove = useCallback(async (assignmentId: string, name: string) => {
    if (!confirm(`Remove skill "${name}" from ${agentSlug}?`)) return
    setBusy(true)
    try {
      const r = await fetch(`/api/v1/agents/${agentId}/skills/${assignmentId}`, { method: "DELETE" })
      if (!r.ok) throw new Error(await r.text())
      toast.success(`Skill "${name}" removed`)
      await refresh()
      onChange()
    } catch (err) {
      toast.error(`Remove failed: ${err instanceof Error ? err.message : err}`)
    } finally {
      setBusy(false)
    }
  }, [agentId, agentSlug, refresh, onChange])

  const assignedIds = useMemo(() => new Set((assigned ?? []).map((a) => a.skill_id)), [assigned])
  const pickable = useMemo(() => (available ?? []).filter((s) => !assignedIds.has(s.id)), [available, assignedIds])

  return (
    <section className="space-y-3">
      <div className="flex items-baseline justify-between">
        <h2 className="text-lg font-semibold">Skills</h2>
        <button
          type="button"
          onClick={openPicker}
          className="text-xs px-2.5 py-1 rounded bg-blue-500/15 hover:bg-blue-500/25 text-blue-300 border border-blue-500/30 flex items-center gap-1.5"
        >
          <Plus className="h-3 w-3" />
          Assign skill
        </button>
      </div>
      <div className="rounded-xl border border-white/8 bg-card overflow-hidden divide-y divide-white/5">
        {assigned === null ? (
          <div className="px-4 py-6 text-xs text-muted-foreground">Loading…</div>
        ) : assigned.length === 0 ? (
          <div className="px-4 py-6 text-xs text-muted-foreground italic">No skills assigned. Click <em>Assign skill</em> to attach one from the workspace library.</div>
        ) : (
          assigned.map((row) => (
            <div key={row.id} className="px-4 py-3 flex items-center gap-3 hover:bg-white/[0.025]">
              <div className="w-8 h-8 rounded-lg bg-zinc-800 grid place-items-center text-foreground/60 shrink-0">
                <span className="text-xs">{(row.skill.display_name ?? row.skill.name).slice(0, 2).toUpperCase()}</span>
              </div>
              <div className="flex-1 min-w-0">
                <div className="text-sm text-foreground truncate">{row.skill.display_name ?? row.skill.name}</div>
                <div className="text-[10px] text-muted-foreground truncate">
                  {row.skill.category ?? "—"}{row.skill.version ? ` · v${row.skill.version}` : ""}
                  {row.skill.description ? ` · ${row.skill.description}` : ""}
                </div>
              </div>
              <button
                type="button"
                disabled={busy}
                onClick={() => remove(row.id, row.skill.display_name ?? row.skill.name)}
                className="text-[11px] px-2 py-1 rounded text-muted-foreground hover:bg-red-500/10 hover:text-red-300 flex items-center gap-1"
                title="Remove skill"
              >
                <Trash2 className="h-3 w-3" />
                Remove
              </button>
            </div>
          ))
        )}
      </div>

      {pickerOpen && (
        <PickerSheet
          title="Assign skill"
          subtitle="Pick a workspace skill to attach to this agent."
          items={pickable}
          renderItem={(s) => ({
            primary: s.display_name ?? s.name,
            secondary: [s.category, s.description].filter(Boolean).join(" · "),
          })}
          onPick={(s) => assign(s.id)}
          onClose={() => setPickerOpen(false)}
          loading={available === null}
          busy={busy}
        />
      )}
    </section>
  )
}


interface WorkspaceCredential {
  id: string
  name: string
  type: string
  provider: string
  status?: string | null
  default_env_var?: string | null
}

function CredentialsManager({ agentId, agentSlug, workspaceId, onChange }: { agentId: string; agentSlug: string; workspaceId: string; onChange: () => void }) {
  const [assigned, setAssigned] = useState<AgentCredRow[] | null>(null)
  const [available, setAvailable] = useState<WorkspaceCredential[] | null>(null)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [busy, setBusy] = useState(false)

  const refresh = useCallback(async () => {
    try {
      const r = await fetch(`/api/v1/agents/${agentId}/credentials?workspace_id=${workspaceId}`)
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const data: AgentCredRow[] = await r.json()
      setAssigned(Array.isArray(data) ? data : [])
    } catch (err) {
      toast.error(`Could not load credentials: ${err instanceof Error ? err.message : err}`)
      setAssigned([])
    }
  }, [agentId, workspaceId])

  useEffect(() => { void refresh() }, [refresh])

  const openPicker = useCallback(async () => {
    setPickerOpen(true)
    if (available !== null) return
    try {
      const r = await fetch(`/api/v1/credentials?workspace_id=${workspaceId}`)
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const data: WorkspaceCredential[] = await r.json()
      setAvailable(Array.isArray(data) ? data : [])
    } catch (err) {
      toast.error(`Could not load workspace credentials: ${err instanceof Error ? err.message : err}`)
      setAvailable([])
    }
  }, [available, workspaceId])

  const assign = useCallback(async (cred: WorkspaceCredential) => {
    const envVar = cred.default_env_var || cred.name.toUpperCase().replace(/[^A-Z0-9_]/g, "_")
    setBusy(true)
    try {
      const r = await fetch(`/api/v1/agents/${agentId}/credentials`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ credential_id: cred.id, env_var_name: envVar, priority: 0 }),
      })
      if (!r.ok) throw new Error(await r.text())
      toast.success("Credential assigned")
      setPickerOpen(false)
      await refresh()
      onChange()
    } catch (err) {
      toast.error(`Assign failed: ${err instanceof Error ? err.message : err}`)
    } finally {
      setBusy(false)
    }
  }, [agentId, refresh, onChange])

  const remove = useCallback(async (assignmentId: string, name: string) => {
    if (!confirm(`Unassign credential "${name}" from ${agentSlug}?`)) return
    setBusy(true)
    try {
      const r = await fetch(`/api/v1/agents/${agentId}/credentials/${assignmentId}`, { method: "DELETE" })
      if (!r.ok) throw new Error(await r.text())
      toast.success(`Credential "${name}" unassigned`)
      await refresh()
      onChange()
    } catch (err) {
      toast.error(`Unassign failed: ${err instanceof Error ? err.message : err}`)
    } finally {
      setBusy(false)
    }
  }, [agentId, agentSlug, refresh, onChange])

  const assignedIds = useMemo(() => new Set((assigned ?? []).map((a) => a.credential_id)), [assigned])
  const pickable = useMemo(() => (available ?? []).filter((c) => !assignedIds.has(c.id)), [available, assignedIds])

  return (
    <section className="space-y-3">
      <div className="flex items-baseline justify-between">
        <h2 className="text-lg font-semibold">Credentials</h2>
        <button
          type="button"
          onClick={openPicker}
          className="text-xs px-2.5 py-1 rounded bg-blue-500/15 hover:bg-blue-500/25 text-blue-300 border border-blue-500/30 flex items-center gap-1.5"
        >
          <Plus className="h-3 w-3" />
          Assign credential
        </button>
      </div>
      <div className="rounded-xl border border-white/8 bg-card overflow-hidden divide-y divide-white/5">
        {assigned === null ? (
          <div className="px-4 py-6 text-xs text-muted-foreground">Loading…</div>
        ) : assigned.length === 0 ? (
          <div className="px-4 py-6 text-xs text-muted-foreground italic">No credentials assigned. SECRETs are surfaced through Keeper at runtime — assign them here once and the agent fetches them on demand.</div>
        ) : (
          assigned.map((row) => (
            <div key={row.id} className="px-4 py-3 flex items-center gap-3 hover:bg-white/[0.025]">
              <div className="w-8 h-8 rounded-lg bg-amber-500/15 text-amber-300 grid place-items-center shrink-0">
                <span className="text-xs">{row.credential_provider.slice(0, 2).toUpperCase()}</span>
              </div>
              <div className="flex-1 min-w-0">
                <div className="text-sm text-foreground truncate flex items-center gap-2">
                  {row.credential_name}
                  {row.credential_type === "SECRET" && (
                    <span className="text-[9px] px-1.5 rounded bg-red-500/20 text-red-300 border border-red-500/30">SECRET</span>
                  )}
                </div>
                <div className="text-[10px] text-muted-foreground truncate">
                  {row.credential_provider} · env: <code className="text-foreground/70">{row.env_var_name}</code> · {row.credential_status?.toLowerCase()}
                </div>
              </div>
              <button
                type="button"
                disabled={busy}
                onClick={() => remove(row.id, row.credential_name)}
                className="text-[11px] px-2 py-1 rounded text-muted-foreground hover:bg-red-500/10 hover:text-red-300 flex items-center gap-1"
                title="Unassign credential"
              >
                <Trash2 className="h-3 w-3" />
                Unassign
              </button>
            </div>
          ))
        )}
      </div>

      {pickerOpen && (
        <PickerSheet
          title="Assign credential"
          subtitle="Pick a workspace credential to attach. The agent reads it at runtime via Keeper."
          items={pickable}
          renderItem={(c) => ({
            primary: c.name,
            secondary: `${c.provider} · ${c.type}${c.default_env_var ? ` · env: ${c.default_env_var}` : ""}`,
          })}
          onPick={(c) => assign(c)}
          onClose={() => setPickerOpen(false)}
          loading={available === null}
          busy={busy}
        />
      )}
    </section>
  )
}

/**
 * Generic picker sheet for assign-skill / assign-credential dialogs.
 * Centered modal — a real Sheet/Dialog primitive could replace this later.
 */

function PickerSheet<T>({
  title, subtitle, items, renderItem, onPick, onClose, loading, busy,
}: {
  title: string
  subtitle: string
  items: T[]
  renderItem: (item: T) => { primary: string; secondary?: string }
  onPick: (item: T) => void
  onClose: () => void
  loading: boolean
  busy: boolean
}) {
  return (
    <div
      className="fixed inset-0 z-50 bg-black/60 grid place-items-center"
      onClick={onClose}
      onKeyDown={(e) => { if (e.key === "Escape") onClose() }}
      role="presentation"
    >
      <div
        className="w-[460px] max-w-[90vw] max-h-[70vh] rounded-xl border border-white/10 bg-card shadow-2xl overflow-hidden flex flex-col"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-label={title}
      >
        <div className="px-4 py-3 border-b border-white/8">
          <h3 className="text-sm font-semibold">{title}</h3>
          <p className="text-[11px] text-muted-foreground mt-0.5">{subtitle}</p>
        </div>
        <div className="flex-1 overflow-y-auto divide-y divide-white/5">
          {loading ? (
            <div className="px-4 py-6 text-xs text-muted-foreground">Loading…</div>
          ) : items.length === 0 ? (
            <div className="px-4 py-6 text-xs text-muted-foreground italic">Nothing else to assign — all available items are already attached.</div>
          ) : (
            items.map((item, i) => {
              const { primary, secondary } = renderItem(item)
              return (
                <button
                  key={i}
                  type="button"
                  disabled={busy}
                  onClick={() => onPick(item)}
                  className="w-full text-left px-4 py-2.5 hover:bg-white/[0.04] disabled:opacity-50"
                >
                  <div className="text-sm text-foreground">{primary}</div>
                  {secondary && <div className="text-[10px] text-muted-foreground">{secondary}</div>}
                </button>
              )
            })
          )}
        </div>
        <div className="px-4 py-2 border-t border-white/8 flex items-center justify-end">
          <button
            type="button"
            onClick={onClose}
            className="text-xs px-3 py-1.5 rounded border border-white/10 hover:bg-white/5 text-foreground"
          >
            Cancel
          </button>
        </div>
      </div>
    </div>
  )
}

// =============================================================================
// Date / cost helpers
// =============================================================================


export { SkillsManager, CredentialsManager, PickerSheet }
export type { WorkspaceSkill, WorkspaceCredential }
