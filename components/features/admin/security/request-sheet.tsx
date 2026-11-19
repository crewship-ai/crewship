"use client"

import * as React from "react"
import { Check, FileText, History, Shield, X } from "lucide-react"
import { toast } from "@/lib/toast"

import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { redactSecrets } from "@/app/(dashboard)/admin/utils"
import type { KeeperLogEntry } from "@/app/(dashboard)/admin/types"
import { DecisionChip, DECISION_WORD } from "./decision-chip"
import { STREAMS, streamOf } from "./security-model"

export const when = (iso: string) => new Date(iso.includes("T") ? iso : iso.replace(" ", "T") + "Z").toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })

/** What a row is about: the credential for a request, the kind for a review. */
export function subject(e: KeeperLogEntry): string {
  if (streamOf(e) === "requests") return e.credential_name || "—"
  return STREAMS.find((s) => s.key === streamOf(e))?.label ?? e.request_type
}

interface RequestEvent { seq: number; state: string; actor_type: string; recorded_at: string; reason?: string | null }

async function messageOf(r: Response, fallback: string): Promise<string> {
  try {
    const b = (await r.json()) as { detail?: string; error?: string; title?: string }
    return b.detail ?? b.error ?? b.title ?? fallback
  } catch {
    return fallback
  }
}

/**
 * The record of one decision, in a sheet on the right: what was decided and
 * why, what the judge saw and said (secrets redacted), the history of the
 * request, and, while it waits for a person, Allow and Deny.
 *
 * History reads GET /admin/keeper/requests/{id}/events with the request's own
 * workspace in `workspace_id`: that route resolves the workspace from the
 * query and lets an instance admin address one they are not a member of
 * (RequireWorkspaceOrInstanceAdmin), so it answers from this page.
 * Allow and Deny use POST …/resolve, which is workspace-member only on the
 * server; for a workspace the admin is not in, the server's own refusal is
 * what they read.
 */
export function RequestSheet({ entry, onClose, onResolved }: { entry: KeeperLogEntry | null; onClose: () => void; onResolved: () => void }) {
  const canAct = useIsInstanceAdmin() === true
  const [events, setEvents] = React.useState<RequestEvent[] | null>(null)
  const [eventsError, setEventsError] = React.useState<string | null>(null)
  const [reason, setReason] = React.useState("")
  const [busy, setBusy] = React.useState<"ALLOW" | "DENY" | null>(null)
  const [refused, setRefused] = React.useState<string | null>(null)
  const id = entry?.id
  const ws = entry?.workspace_id

  React.useEffect(() => {
    setEvents(null); setEventsError(null); setReason(""); setRefused(null)
    if (!id || !ws) return
    const controller = new AbortController()
    void (async () => {
      try {
        const r = await apiFetch(`/api/v1/admin/keeper/requests/${encodeURIComponent(id)}/events?workspace_id=${encodeURIComponent(ws)}`, { signal: controller.signal })
        if (controller.signal.aborted) return
        if (!r.ok) { setEventsError(await messageOf(r, `The history could not be read (HTTP ${r.status}).`)); return }
        const body = (await r.json()) as RequestEvent[]
        if (!controller.signal.aborted) setEvents(Array.isArray(body) ? body : [])
      } catch (e) {
        if (!(e instanceof DOMException && e.name === "AbortError")) setEventsError("The history could not be read.")
      }
    })()
    return () => controller.abort()
  }, [id, ws])

  const waiting = entry != null && (entry.decision === "ESCALATE" || entry.decision == null || entry.decision === "PENDING")

  async function resolve(decision: "ALLOW" | "DENY") {
    if (!entry || busy) return
    setBusy(decision); setRefused(null)
    try {
      const r = await apiFetch(`/api/v1/admin/keeper/requests/${encodeURIComponent(entry.id)}/resolve?workspace_id=${encodeURIComponent(entry.workspace_id ?? "")}`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ decision, reason: reason.trim() }),
      })
      if (!r.ok) {
        // 403 is usually the second approver, 409 a request already settled: the
        // server's sentence says which, so it is shown, not replaced.
        const m = await messageOf(r, `Could not resolve (HTTP ${r.status})`)
        setRefused(m); toast.error(m)
        return
      }
      toast.success(decision === "ALLOW" ? "Allowed" : "Denied")
      onResolved(); onClose()
    } catch (e) {
      const m = e instanceof Error ? `Could not reach the server: ${e.message}` : "Could not reach the server"
      setRefused(m); toast.error(m)
    } finally {
      setBusy(null)
    }
  }

  return (
    <Sheet open={!!entry} onOpenChange={(o) => { if (!o) onClose() }}>
      <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2 text-sm"><Shield className="h-3.5 w-3.5" />{entry ? subject(entry) : ""}</SheetTitle>
          <SheetDescription className="text-label">{entry ? `${entry.agent_name}${entry.workspace_name ? ` · ${entry.workspace_name}` : ""} · ${when(entry.created_at)}` : ""}</SheetDescription>
        </SheetHeader>
        {entry && (
          <div className="mt-2 grid gap-4 px-4 pb-6">
            <dl className="grid grid-cols-2 gap-3 text-label">
              <Fact label="Decision" value={<DecisionChip decision={entry.decision} />} />
              <Fact label="Risk" value={entry.risk_score != null ? `${entry.risk_score}/10` : "—"} />
              <Fact label="Agent" value={entry.agent_name} />
              <Fact label="Workspace" value={entry.workspace_name || "—"} />
              <Fact label="Kind" value={STREAMS.find((s) => s.key === streamOf(entry))?.label ?? entry.request_type} />
              {entry.credential_name && <Fact label="Credential" value={<span className="font-mono">{entry.credential_name}</span>} />}
            </dl>
            <Block label="Intent" text={entry.intent} />
            {entry.reason && <Block label="Reason" text={entry.reason} />}
            {entry.command && <Block label="Command" text={entry.command} mono />}
            <Block label="Judge prompt" text={entry.ollama_prompt} mono empty="Not recorded (a level 1 allowed on sight, or an older request)" />
            <Block label="Judge answer" text={entry.ollama_raw_response} mono empty="Not recorded (a level 1 allowed on sight, or an older request)" />
            <p className="text-label text-muted-foreground">Request <span className="font-mono">{entry.id}</span></p>

            <SettingsCard icon={History} title="History of this request" description="Every state it went through; nothing here can be edited">
              {eventsError ? (
                <p className="px-4 py-3 text-label text-destructive" role="alert">{eventsError}</p>
              ) : events === null ? (
                <p className="px-4 py-3 text-label text-muted-foreground">Loading…</p>
              ) : events.length === 0 ? (
                <p className="px-4 py-3 text-label text-muted-foreground">No history recorded for this request.</p>
              ) : events.map((ev, i) => (
                <SettingsRow key={ev.seq} border={i < events.length - 1}
                  label={<span className="flex items-center gap-2"><span aria-hidden className="grid size-5 place-items-center rounded-full bg-muted text-micro tabular-nums">{i + 1}</span>{DECISION_WORD[ev.state] ?? ev.state.charAt(0) + ev.state.slice(1).toLowerCase().replace(/_/g, " ")}</span>}
                  description={ev.reason ? redactSecrets(ev.reason) : undefined}>
                  <span className="font-mono text-micro text-muted-foreground">{when(ev.recorded_at)}</span>
                </SettingsRow>
              ))}
            </SettingsCard>

            {waiting && (
              <section aria-label="Decide" data-slot="resolve" className="grid gap-2 rounded-card border border-border bg-card p-3">
                <div className="flex items-center gap-2 text-control font-medium"><FileText className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />Waiting for a person</div>
                {canAct ? (
                  <>
                    <Input value={reason} onChange={(e) => setReason(e.target.value)} aria-label="Reason" placeholder="Reason (optional)" disabled={busy !== null} />
                    {refused && <p role="alert" className="text-label text-destructive">{refused}</p>}
                    <div className="flex justify-end gap-2">
                      <Button type="button" variant="outline" size="sm" disabled={busy !== null} onClick={() => void resolve("DENY")}><X className="mr-1 h-3.5 w-3.5" />{busy === "DENY" ? "Denying…" : "Deny"}</Button>
                      <Button type="button" size="sm" disabled={busy !== null} onClick={() => void resolve("ALLOW")}><Check className="mr-1 h-3.5 w-3.5" />{busy === "ALLOW" ? "Allowing…" : "Allow"}</Button>
                    </div>
                  </>
                ) : (
                  <p className="text-label text-muted-foreground">Only an instance administrator can decide here.</p>
                )}
              </section>
            )}
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}

function Fact({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div>
      <dt className="text-label text-muted-foreground">{label}</dt>
      <dd className="mt-0.5">{value}</dd>
    </div>
  )
}

function Block({ label, text, mono, empty }: { label: string; text: string | null | undefined; mono?: boolean; empty?: string }) {
  return (
    <section aria-label={label}>
      <h4 className="mb-1 text-micro text-muted-foreground-soft">{label}</h4>
      {text ? (
        <pre className={cn("max-h-64 overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted/40 p-2.5 text-label", mono && "font-mono text-micro")}>{redactSecrets(text)}</pre>
      ) : (
        <p className="rounded-md border border-border bg-muted/40 p-2.5 text-label italic text-muted-foreground">{empty ?? "—"}</p>
      )}
    </section>
  )
}
