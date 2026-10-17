"use client"

import { useEffect, useState } from "react"
import { toast } from "@/lib/toast"

import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CREATE_SURFACE_INPUT,
} from "@/components/layout/create-surface"
import { apiFetch } from "@/lib/api-fetch"
import { toApiError } from "@/lib/api-error"
import { cn } from "@/lib/utils"

import { AUTONOMY_LABEL } from "./crew-header"

/** 30 minutes to 24 hours, the window POST /agents/hire clamps to. */
export const HIRE_DURATIONS: Array<{ minutes: number; label: string }> = [
  { minutes: 30, label: "30 minutes" },
  { minutes: 60, label: "1 hour" },
  { minutes: 120, label: "2 hours" },
  { minutes: 240, label: "4 hours" },
  { minutes: 480, label: "8 hours" },
  { minutes: 1440, label: "24 hours" },
]

const FIELD = cn("w-full rounded-md border border-hairline bg-background px-2.5 outline-none focus:border-primary", CREATE_SURFACE_INPUT)

/** The hire request, as `crewship hire` sends it. */
export function hireBody(input: { crewId: string; templateSlug: string; minutes: number; reason: string; model?: string }) {
  const body: Record<string, unknown> = {
    crew_id: input.crewId,
    template_slug: input.templateSlug,
    ttl_minutes: input.minutes,
    reason: input.reason.trim(),
  }
  if (input.model?.trim()) body.model = input.model.trim()
  return body
}

/**
 * D-CRHIRE: hire a temporary agent for a set time. In a Guided crew the hire
 * waits for approval in the Inbox; in Strict it is refused by the server.
 */
export function HireDialog({ open, onOpenChange, workspaceId, crew, autonomy, inUse, limit, onHired }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  crew: { id: string; name: string }
  autonomy: string | null
  inUse: number
  limit: number | null
  onHired: () => void
}) {
  const [templates, setTemplates] = useState<Array<{ slug: string; name: string }> | null>(null)
  const [template, setTemplate] = useState("")
  const [minutes, setMinutes] = useState(30)
  const [reason, setReason] = useState("")
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) return
    setReason("")
    setMinutes(30)
    let cancelled = false
    apiFetch(`/api/v1/crew-templates?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: Array<{ slug: string; name: string }>) => {
        if (cancelled) return
        const list = Array.isArray(data) ? data : []
        setTemplates(list)
        setTemplate((t) => t || list[0]?.slug || "")
      })
      .catch(() => { if (!cancelled) setTemplates([]) })
    return () => { cancelled = true }
  }, [open, workspaceId])

  const guided = autonomy === "guided" || autonomy === "strict"
  const submit = async () => {
    if (busy || !template || !reason.trim()) return
    setBusy(true)
    try {
      const res = await apiFetch(`/api/v1/agents/hire?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(hireBody({ crewId: crew.id, templateSlug: template, minutes, reason })),
      })
      if (!res.ok) throw await toApiError(res, `The hire was refused (${res.status}).`)
      toast.success(res.status === 202 ? "Sent for approval · it is in the Inbox" : `Hired for ${HIRE_DURATIONS.find((d) => d.minutes === minutes)?.label ?? `${minutes} minutes`}`)
      onOpenChange(false)
      onHired()
    } catch (e) {
      toast.error("Could not hire a temporary agent", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm" dirty={reason.trim() !== ""} discardLabel="this hire" onSubmit={() => void submit()}>
      <CreateSurfaceHeader
        concept="crews"
        context={crew.name}
        title={`Hire a temporary agent for ${crew.name}`}
        description="It joins for a set time and then goes away; its runs and notes stay."
        onClose={() => onOpenChange(false)}
      />
      <CreateSurfaceBody className="space-y-4">
        <CreateSurfaceField label="Template" htmlFor="hire-template">
          <select id="hire-template" className={FIELD} value={template} onChange={(e) => setTemplate(e.target.value)} disabled={!templates?.length}>
            {templates === null && <option>Loading…</option>}
            {templates?.length === 0 && <option value="">No templates in this workspace</option>}
            {templates?.map((t) => <option key={t.slug} value={t.slug}>{t.name || t.slug}</option>)}
          </select>
        </CreateSurfaceField>
        <CreateSurfaceField label="For how long" htmlFor="hire-ttl" hint="30 minutes to 24 hours. When it ends, rehire it if you still need it.">
          <select id="hire-ttl" className={FIELD} value={minutes} onChange={(e) => setMinutes(Number(e.target.value))}>
            {HIRE_DURATIONS.map((d) => <option key={d.minutes} value={d.minutes}>{d.label}</option>)}
          </select>
        </CreateSurfaceField>
        <CreateSurfaceField label="Why" htmlFor="hire-reason" required>
          <textarea id="hire-reason" rows={3} className={cn(FIELD, "h-auto py-2")} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Check the numbers in this week’s report" />
        </CreateSurfaceField>
        <p className="text-label text-muted-foreground">
          {crew.name} is {autonomy ? AUTONOMY_LABEL[autonomy]?.label ?? autonomy : "…"}:{" "}
          {autonomy === "strict" ? "hires are not allowed" : guided ? "the hire waits for approval in the Inbox" : "it starts now and you are told in the Inbox"}.
          {limit != null && ` ${inUse} of ${limit} temporary agents in use.`}
        </p>
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel={guided ? "Ask for approval" : "Hire"}
        primaryDisabled={!template || !reason.trim() || autonomy === "strict"}
        busy={busy}
        onPrimary={() => void submit()}
      />
    </CreateSurface>
  )
}

/** D-CRREHIRE: bring an expired hire back with its notes. */
export function RehireDialog({ open, onOpenChange, workspaceId, agent, onRehired }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  agent: { id: string; name: string } | null
  onRehired: () => void
}) {
  const [minutes, setMinutes] = useState(30)
  const [reason, setReason] = useState("")
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (open) { setMinutes(30); setReason("") } }, [open])

  const submit = async () => {
    if (!agent || busy || !reason.trim()) return
    setBusy(true)
    try {
      const res = await apiFetch(`/api/v1/agents/${agent.id}/rehire?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ttl_minutes: minutes, reason: reason.trim() }),
      })
      if (!res.ok) throw await toApiError(res, `Rehire was refused (${res.status}).`)
      toast.success(`${agent.name} is back for ${HIRE_DURATIONS.find((d) => d.minutes === minutes)?.label ?? `${minutes} minutes`}`)
      onOpenChange(false)
      onRehired()
    } catch (e) {
      toast.error(`Could not rehire ${agent.name}`, { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm" onSubmit={() => void submit()}>
      <CreateSurfaceHeader concept="crews" title={`Rehire ${agent?.name ?? "agent"}`} description="Brings the same agent back with its notes." onClose={() => onOpenChange(false)} />
      <CreateSurfaceBody className="space-y-4">
        <CreateSurfaceField label="For how long" htmlFor="rehire-ttl" hint="30 minutes to 24 hours">
          <select id="rehire-ttl" className={FIELD} value={minutes} onChange={(e) => setMinutes(Number(e.target.value))}>
            {HIRE_DURATIONS.map((d) => <option key={d.minutes} value={d.minutes}>{d.label}</option>)}
          </select>
        </CreateSurfaceField>
        <CreateSurfaceField label="Why" htmlFor="rehire-reason" required>
          <textarea id="rehire-reason" rows={3} className={cn(FIELD, "h-auto py-2")} value={reason} onChange={(e) => setReason(e.target.value)} />
        </CreateSurfaceField>
      </CreateSurfaceBody>
      <CreateSurfaceFooter onCancel={() => onOpenChange(false)} primaryLabel="Rehire" primaryDisabled={!reason.trim()} busy={busy} onPrimary={() => void submit()} />
    </CreateSurface>
  )
}
