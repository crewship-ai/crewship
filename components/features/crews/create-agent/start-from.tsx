"use client"

import { useEffect, useState } from "react"
import { Copy, Layers, Plus } from "lucide-react"

import { CreateSurfaceField } from "@/components/layout/create-surface"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import type { AgentPersona } from "@/lib/entities"

import { TemplateBrowser } from "./template-browser"
import type { AgentRecord } from "../agent-canvas-tabs/types"

export type AgentStart = "blank" | "template" | "copy"

const OPTIONS: Array<{ id: AgentStart; label: string; hint: string; icon: typeof Plus }> = [
  { id: "blank", label: "Blank agent", hint: "Fill in the fields", icon: Plus },
  { id: "template", label: "Agent template", hint: "Researcher, reviewer, writer…", icon: Layers },
  { id: "copy", label: "Copy an agent", hint: "Same model and access as another", icon: Copy },
]

const FIELD = "w-full rounded-md border border-hairline bg-background px-2.5 h-8 text-xs outline-none focus:border-primary coarse:h-[44px] coarse:text-base"

/**
 * New agent › Who › "Start from": a blank agent, a template (the catalogue on
 * the step, not in a popover), or a copy of an agent that exists.
 */
export function StartFrom({ workspaceId, start, onStart, selectedPersona, onPickPersona, onCopy }: {
  workspaceId: string
  start: AgentStart
  onStart: (start: AgentStart) => void
  selectedPersona: AgentPersona | null
  onPickPersona: (persona: AgentPersona) => void
  /** Called with the full source agent (GET /agents/{id}). */
  onCopy: (agent: AgentRecord) => void
}) {
  const [agents, setAgents] = useState<Array<{ id: string; name: string; slug: string }> | null>(null)
  const [copyId, setCopyId] = useState("")
  const [failed, setFailed] = useState(false)
  const ws = encodeURIComponent(workspaceId)

  useEffect(() => {
    if (start !== "copy" || agents !== null) return
    let cancelled = false
    apiFetch(`/api/v1/agents?workspace_id=${ws}&limit=200`)
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((body: unknown) => {
        if (cancelled) return
        const rows = Array.isArray(body) ? body : Array.isArray((body as { data?: unknown })?.data) ? (body as { data: unknown[] }).data : []
        setAgents((rows as Array<{ id: string; name: string; slug: string; expired_at?: string | null }>).filter((a) => !a.expired_at))
      })
      .catch(() => { if (!cancelled) { setAgents([]); setFailed(true) } })
    return () => { cancelled = true }
  }, [start, agents, ws])

  const copy = async (id: string) => {
    setCopyId(id)
    if (!id) return
    setFailed(false)
    try {
      const r = await apiFetch(`/api/v1/agents/${encodeURIComponent(id)}?workspace_id=${ws}`)
      if (!r.ok) throw new Error(String(r.status))
      onCopy(await r.json())
    } catch {
      setFailed(true)
    }
  }

  return (
    <div className="space-y-3" data-testid="agent-start-from">
      <div role="radiogroup" aria-label="Start from" className="grid gap-2 sm:grid-cols-3">
        {OPTIONS.map(({ id, label, hint, icon: Icon }) => (
          <button
            key={id}
            type="button"
            role="radio"
            aria-checked={start === id}
            onClick={() => onStart(id)}
            className={cn(
              "flex flex-col items-start gap-1 rounded-md border p-3 text-left transition-colors coarse:min-h-[44px]",
              start === id ? "border-primary bg-primary/10" : "border-border hover:bg-muted",
            )}
          >
            <Icon className="size-4 text-muted-foreground" aria-hidden />
            <b className="text-body font-medium text-foreground">{label}</b>
            <span className="text-label text-muted-foreground">{hint}</span>
          </button>
        ))}
      </div>

      {start === "template" && (
        <div className="overflow-hidden rounded-md border border-border">
          <TemplateBrowser selected={selectedPersona} onSelect={onPickPersona} />
        </div>
      )}

      {start === "copy" && (
        <CreateSurfaceField label="Agent to copy" htmlFor="agent-copy-from" hint="Copies its model, runner, limits, instructions and skills. Name and slug stay yours.">
          <select id="agent-copy-from" className={FIELD} value={copyId} onChange={(e) => void copy(e.target.value)} disabled={agents === null}>
            <option value="">{agents === null ? "Loading agents…" : agents.length ? "Pick an agent…" : "No agents to copy yet"}</option>
            {agents?.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
          </select>
          {failed && <p role="alert" className="mt-1 text-label text-chip-danger-fg">That agent could not be read. Pick it again or start blank.</p>}
        </CreateSurfaceField>
      )}
    </div>
  )
}
