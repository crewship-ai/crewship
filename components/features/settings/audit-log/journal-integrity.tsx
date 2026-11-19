"use client"

import { useState } from "react"
import { Loader2, ShieldCheck } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader, CreateSurfaceNotice } from "@/components/layout/create-surface"
import { SettingsCard, SettingsRow } from "../shared"

/**
 * Settings › Audit log: the integrity line in the page head ("Record intact ·
 * checked 02:00" and "Check now") and the dialog that shows what the check
 * found.
 *
 * The journal is a hash chain; GET /api/v1/admin/journal/verify (ADMIN+, the
 * same check as `crewship journal verify`) walks every entry, so it runs when
 * asked, not on every visit. The dialog shows the whole answer the server
 * gives: the result, the entries and checkpoints walked, the first break and
 * every break after it, whether the continuation verified and the historical
 * status. A break also raises an incident on the server; the dialog says so.
 */

export interface ChainBreak {
  seq?: number
  id?: string
  kind?: string
  reason?: string
}

export interface VerifyResult {
  ok: boolean
  count?: number
  checkpoints?: number
  broken_seq?: number
  broken_id?: string
  reason?: string
  breaks?: ChainBreak[]
  break_count?: number
  breaks_truncated?: boolean
  continuation_verified?: boolean
  historical_status?: string
}

export type IntegrityState =
  | { kind: "idle" }
  | { kind: "running" }
  | { kind: "ok"; result: VerifyResult; at: Date }
  | { kind: "broken"; result: VerifyResult; at: Date }
  | { kind: "error"; message: string; at: Date }

// The wireframe's 24-hour clock.
const clock = (d: Date) => d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", hour12: false })
const n = (v: number | undefined) => (v ?? 0).toLocaleString("en-US")

/** Runs the check and keeps the last answer for the line and the dialog. */
export function useJournalIntegrity(workspaceId: string) {
  const [state, setState] = useState<IntegrityState>({ kind: "idle" })
  const [open, setOpen] = useState(false)

  async function check() {
    setOpen(true)
    setState({ kind: "running" })
    try {
      const res = await apiFetch(`/api/v1/admin/journal/verify?workspace_id=${encodeURIComponent(workspaceId)}`)
      const body = (await res.json().catch(() => null)) as (VerifyResult & { error?: string }) | null
      if (!res.ok || !body) {
        setState({ kind: "error", message: typeof body?.error === "string" ? body.error : `Request failed (${res.status})`, at: new Date() })
        return
      }
      setState(body.ok ? { kind: "ok", result: body, at: new Date() } : { kind: "broken", result: body, at: new Date() })
    } catch {
      setState({ kind: "error", message: "The server could not be reached.", at: new Date() })
    }
  }

  return { state, open, setOpen, check }
}

/** The quiet line in the page head, and Check now beside it. */
export function JournalIntegrityLine({ integrity }: { integrity: ReturnType<typeof useJournalIntegrity> }) {
  const { state, setOpen, check } = integrity
  const running = state.kind === "running"
  const pill =
    state.kind === "ok" ? <StatusPill tone="success" label={`Record intact · checked ${clock(state.at)}`} />
    : state.kind === "broken" ? <StatusPill tone="danger" label={`Record broken${state.result.broken_seq ? ` at entry ${state.result.broken_seq}` : ""} · checked ${clock(state.at)}`} />
    : state.kind === "error" ? <StatusPill tone="warn" label="Couldn’t check the record" />
    : running ? <StatusPill tone="muted" label="Checking the record…" />
    : <StatusPill tone="muted" label="Record not checked yet" />
  return (
    <span data-slot="journal-integrity" data-state={state.kind} className="flex flex-wrap items-center justify-end gap-2">
      {state.kind === "ok" || state.kind === "broken" || state.kind === "error" ? (
        <button type="button" onClick={() => setOpen(true)} title="Each entry is chained to the one before it. Show what the check found." className="rounded-full outline-none focus-visible:ring-2 focus-visible:ring-ring/50 coarse:min-h-[44px]">
          {pill}
        </button>
      ) : (
        <span title="Each entry is chained to the one before it; crewship journal verify" role="status">{pill}</span>
      )}
      <Button size="sm" variant="outline" onClick={() => void check()} disabled={running} className="coarse:min-h-[44px]">
        {running && <Loader2 className="h-3 w-3 animate-spin" aria-hidden />}
        Check now
      </Button>
    </span>
  )
}

/** What the check found, all of it. */
export function JournalIntegrityDialog({ integrity }: { integrity: ReturnType<typeof useJournalIntegrity> }) {
  const { state, open, setOpen, check } = integrity
  const result = state.kind === "ok" || state.kind === "broken" ? state.result : null
  const title = state.kind === "ok" ? "Record intact" : state.kind === "broken" ? "Record broken" : state.kind === "error" ? "Couldn’t check the record" : "Checking the record…"
  const sub =
    state.kind === "ok" || state.kind === "broken"
      ? `Checked ${clock(state.at)} · ${n(state.result.count)} ${state.result.count === 1 ? "entry" : "entries"}${state.kind === "ok" ? ", none changed" : ""}`
      : state.kind === "error" ? `Checked ${clock(state.at)}`
      : "Every entry is checked, so this can take a moment."
  const breaks = result?.breaks ?? []
  const breakCount = result?.break_count ?? breaks.length
  return (
    <CreateSurface open={open} onOpenChange={setOpen} size="md" ariaLabel="Record integrity">
      <CreateSurfaceHeader icon={ShieldCheck} title={title} description={sub} onClose={() => setOpen(false)} />
      <CreateSurfaceBody className="flex flex-col gap-3" data-slot="journal-integrity-dialog" data-state={state.kind}>
        {state.kind === "running" && (
          <p role="status" className="flex items-center gap-2 text-control text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden /> Checking the record…
          </p>
        )}
        {state.kind === "error" && (
          <CreateSurfaceNotice tone="error">{state.message}</CreateSurfaceNotice>
        )}
        {result && (
          <>
            <SettingsCard title="Result" hideHeader>
              <SettingsRow label="Result" description="A break names the entry and the reason.">
                <StatusPill tone={result.ok ? "success" : "danger"} label={result.ok ? "Intact" : "Broken"} />
              </SettingsRow>
              <SettingsRow label="Entries checked"><span className="font-mono text-control">{n(result.count)}</span></SettingsRow>
              <SettingsRow label="Earlier checkpoints" description="Compaction checkpoints that were applied and verified.">
                <span className="font-mono text-control">{n(result.checkpoints)}</span>
              </SettingsRow>
              <SettingsRow label="Continuation verified" description="Whether the chain continues unbroken from the last checkpoint.">
                {result.continuation_verified === undefined ? (
                  <span className="text-control text-muted-foreground">Not reported</span>
                ) : (
                  <StatusPill tone={result.continuation_verified ? "success" : "danger"} label={result.continuation_verified ? "Yes" : "No"} />
                )}
              </SettingsRow>
              {result.historical_status && (
                <SettingsRow label="Historical status"><span className="font-mono text-control">{result.historical_status}</span></SettingsRow>
              )}
              {!result.ok && (
                <SettingsRow label="First break" description={result.reason}>
                  <span className="font-mono text-control">
                    {result.broken_seq ? `#${result.broken_seq}` : "—"}
                    {result.broken_id ? ` · ${result.broken_id}` : ""}
                  </span>
                </SettingsRow>
              )}
            </SettingsCard>
            {breaks.length > 0 && (
              <SettingsCard title="Breaks" description={`${n(breakCount)} ${breakCount === 1 ? "break" : "breaks"} found`}>
                <ul data-testid="journal-breaks" className="max-h-56 overflow-y-auto">
                  {breaks.map((b, i) => (
                    <li key={`${b.id ?? ""}-${b.seq ?? i}`} className="border-b border-border px-4 py-2 last:border-b-0">
                      <div className="flex flex-wrap items-center gap-2 text-control">
                        <span className="font-mono">#{b.seq ?? "?"}</span>
                        {b.id && <span className="font-mono text-muted-foreground">{b.id}</span>}
                        {b.kind && <StatusPill tone="muted" label={b.kind} />}
                      </div>
                      {b.reason && <p className="mt-0.5 text-label text-muted-foreground">{b.reason}</p>}
                    </li>
                  ))}
                </ul>
                {(result.breaks_truncated || breakCount > breaks.length) && (
                  <p className="px-4 pb-3 text-label text-muted-foreground">
                    Showing the first {n(breaks.length)} of {n(breakCount)}. <span className="font-mono">crewship journal verify</span> lists the rest.
                  </p>
                )}
              </SettingsCard>
            )}
            {!result.ok && (
              <CreateSurfaceNotice tone="error">The server raised an incident for this break. Entries after it were still checked.</CreateSurfaceNotice>
            )}
          </>
        )}
        <p className="text-label text-muted-foreground">
          {result?.ok === false ? "" : "A break raises an incident on the server. "}Re-sealing and rotating the signing key stay on the command line, for the instance owner.
        </p>
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        hint={<span className="font-mono">crewship journal verify</span>}
        secondary={state.kind === "error" || state.kind === "broken" || state.kind === "ok" ? <Button variant="outline" size="sm" onClick={() => void check()}>Check again</Button> : undefined}
        onCancel={() => setOpen(false)}
        cancelLabel="Close"
        primaryLabel="Done"
        onPrimary={() => setOpen(false)}
        primaryDisabled={state.kind === "running"}
      />
    </CreateSurface>
  )
}
