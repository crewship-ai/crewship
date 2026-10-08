"use client"

import * as React from "react"
import { Shield } from "lucide-react"

import { cn } from "@/lib/utils"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { SettingsSegmented, SettingsSummary, SummaryItem, settingsTable, settingsTh, settingsTd, settingsTableRowLink } from "@/components/features/settings/shared"
import { redactSecrets } from "@/app/(dashboard)/admin/utils"
import type { KeeperLogEntry } from "@/app/(dashboard)/admin/types"
import type { KeeperWsStatus } from "@/app/(dashboard)/admin/hooks/use-admin-websocket"
import { STREAMS, filterActivity, streamOf, type DecisionFilter, type Stream } from "./security-model"
import { StatusPill } from "@/components/ui/status-pill"
import type { StatusTone } from "@/lib/format-status"

const DECISION_TONE: Record<string, StatusTone> = {
  ALLOW: "success",
  DENY: "danger",
  ESCALATE: "warn",
  PENDING: "muted",
}

export function DecisionChip({ decision }: { decision: string | null | undefined }) {
  const d = decision ?? "PENDING"
  return <StatusPill tone={DECISION_TONE[d] ?? DECISION_TONE.PENDING} label={d} />
}

const when = (iso: string) => new Date(iso.includes("T") ? iso : iso.replace(" ", "T") + "Z").toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })

/** What a row is about: the credential for a request, the kind for a review. */
function subject(e: KeeperLogEntry): string {
  if (streamOf(e) === "requests") return e.credential_name || "—"
  return STREAMS.find((s) => s.key === streamOf(e))?.label ?? e.request_type
}

/**
 * Admin › Security › Activity: credential decisions and the four Phase-2
 * reviews as one list, narrowed by stream (from the side panel) and decision.
 * A row opens its full record: intent, reason, command, the judge's prompt and
 * its raw answer, secrets redacted.
 */
export function SecurityActivity({ entries, stream, onStream, live, error, server }: {
  entries: KeeperLogEntry[]
  stream: Stream | "all"
  onStream: (s: Stream | "all") => void
  live: KeeperWsStatus
  error: string | null
  /**
   * The instance log, filtered and paged on the server (review R6): the
   * entries already match stream and decision, the counts and total come
   * from the server over the whole history, and more pages load on demand.
   * Without it, the list is filtered here, in what was read.
   */
  server?: {
    decision: DecisionFilter
    onDecision: (d: DecisionFilter) => void
    counts: { allow: number; deny: number; escalate: number; pending: number }
    total: number
    loading: boolean
    onLoadMore: () => void
  }
}) {
  const [localDecision, setLocalDecision] = React.useState<DecisionFilter>("all")
  const decision = server ? server.decision : localDecision
  const setDecision = server ? server.onDecision : setLocalDecision
  const [open, setOpen] = React.useState<KeeperLogEntry | null>(null)
  const inStream = filterActivity(entries, stream, "all")
  const rows = filterActivity(entries, stream, decision)
  const serverCount: Record<string, number> | null = server
    ? { ALLOW: server.counts.allow, DENY: server.counts.deny, ESCALATE: server.counts.escalate, PENDING: server.counts.pending }
    : null
  const count = (d: string) => serverCount ? serverCount[d] ?? 0 : inStream.filter((e) => (e.decision ?? "PENDING") === d).length
  const meta = STREAMS.find((s) => s.key === stream)
  // Rows from the instance log carry their workspace; show it.
  const byWorkspace = entries.some((e) => !!e.workspace_name)

  return (
    <>
      <SettingsSummary>
        <SummaryItem tone={live === "connected" ? "success" : undefined}>{live === "connected" ? "Live" : live === "connecting" ? "Connecting…" : "Not live"}</SummaryItem>
        <SummaryItem n={count("ALLOW")}>allowed</SummaryItem>
        <SummaryItem n={count("DENY")} tone={count("DENY") ? "danger" : undefined}>denied</SummaryItem>
        <SummaryItem n={count("ESCALATE")} tone={count("ESCALATE") ? "warn" : undefined}>escalated</SummaryItem>
        <span className="ml-auto">
          <SettingsSegmented label="Decision" value={decision} onChange={setDecision}
            options={[{ value: "all", label: "All" }, { value: "ALLOW", label: "Allow" }, { value: "DENY", label: "Deny" }, { value: "ESCALATE", label: "Escalate" }]} />
        </span>
      </SettingsSummary>

      {error && <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">{error}</p>}

      <section aria-label="Activity" className="overflow-hidden rounded-card border border-border bg-card">
        <div className="flex items-center gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0 flex-1">
            <h3 className="text-sm font-semibold">{meta?.label ?? "All activity"}</h3>
            <p className="mt-0.5 text-label text-muted-foreground">{meta?.about ?? "Credential decisions and background reviews, newest first"}</p>
          </div>
          {stream !== "all" && (
            <button type="button" onClick={() => onStream("all")} className="text-label text-primary-hover hover:underline">Show all</button>
          )}
        </div>
        <div className="overflow-x-auto">
          <table className={settingsTable}>
            <thead>
              <tr>
                <th className={settingsTh}>When</th>
                {byWorkspace && <th className={settingsTh}>Workspace</th>}
                <th className={settingsTh}>Agent</th>
                <th className={settingsTh}>About</th>
                <th className={settingsTh}>Decision</th>
                <th className={cn(settingsTh, "text-right")}>Risk</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((e) => (
                <tr key={e.id} tabIndex={0} onClick={() => setOpen(e)} onKeyDown={(k) => { if (k.key === "Enter") setOpen(e) }}
                  className={settingsTableRowLink} data-entry={e.id}>
                  <td className={cn(settingsTd, "whitespace-nowrap font-mono text-micro text-muted-foreground")}>{when(e.created_at)}</td>
                  {byWorkspace && <td className={cn(settingsTd, "whitespace-nowrap text-muted-foreground")}>{e.workspace_name}</td>}
                  <td className={settingsTd}>{e.agent_name}</td>
                  <td className={settingsTd}>
                    <span className={cn(streamOf(e) === "requests" && "font-mono text-label")}>{subject(e)}</span>
                    {e.request_type === "execute" && <span className="ml-1.5 text-label text-muted-foreground">exec</span>}
                  </td>
                  <td className={settingsTd}><DecisionChip decision={e.decision} /></td>
                  <td className={cn(settingsTd, "text-right font-mono")}>{e.risk_score != null ? `${e.risk_score}/10` : "—"}</td>
                </tr>
              ))}
              {rows.length === 0 && !server?.loading && (
                <tr><td colSpan={byWorkspace ? 6 : 5} className="px-3 py-8 text-center text-label text-muted-foreground">
                  {inStream.length === 0 && stream !== "requests" && stream !== "all"
                    ? "Nothing recorded yet. These reviews run on a schedule, and behaviour only while the watchdog is on."
                    : "Nothing matches."}
                </td></tr>
              )}
            </tbody>
          </table>
        </div>
        {server && (
          <div className="flex items-center gap-3 border-t border-border px-4 py-2 text-label text-muted-foreground" data-slot="activity-paging">
            <span>{server.loading ? "Loading…" : `Showing ${rows.length} of ${server.total}`}</span>
            {rows.length < server.total && !server.loading && (
              <button type="button" onClick={server.onLoadMore} className="ml-auto rounded-md border border-control-border px-2.5 py-1 text-label hover:bg-accent coarse:min-h-[2.75rem]">
                Load more
              </button>
            )}
          </div>
        )}
      </section>

      <Sheet open={!!open} onOpenChange={(o) => { if (!o) setOpen(null) }}>
        <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-2xl">
          <SheetHeader>
            <SheetTitle className="flex items-center gap-2 text-sm"><Shield className="h-3.5 w-3.5" />{open ? subject(open) : ""}</SheetTitle>
            <SheetDescription className="sr-only">The full record of this decision</SheetDescription>
          </SheetHeader>
          {open && (
            <div className="mt-2 grid gap-4 px-4 pb-6">
              <dl className="grid grid-cols-2 gap-3 text-label">
                <Fact label="Agent" value={open.agent_name} />
                <Fact label="Decision" value={<DecisionChip decision={open.decision} />} />
                <Fact label="Kind" value={STREAMS.find((s) => s.key === streamOf(open))?.label ?? open.request_type} />
                <Fact label="Risk" value={open.risk_score != null ? `${open.risk_score}/10` : "—"} />
                <Fact label="Time" value={when(open.created_at)} />
                {open.credential_name && <Fact label="Credential" value={<span className="font-mono">{open.credential_name}</span>} />}
              </dl>
              <Block label="Intent" text={open.intent} />
              {open.reason && <Block label="Reason" text={open.reason} />}
              {open.command && <Block label="Command" text={open.command} mono />}
              <Block label="Judge prompt" text={open.ollama_prompt} mono empty="Not recorded (L1 auto-allow or an older request)" />
              <Block label="Judge answer" text={open.ollama_raw_response} mono empty="Not recorded (L1 auto-allow or an older request)" />
              <p className="text-label text-muted-foreground">Request <span className="font-mono">{open.id}</span></p>
            </div>
          )}
        </SheetContent>
      </Sheet>
    </>
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
      <h4 className="mb-1 font-mono text-micro uppercase tracking-[0.08em] text-muted-foreground-soft">{label}</h4>
      {text ? (
        <pre className={cn("max-h-64 overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted/40 p-2.5 text-label", mono && "font-mono text-micro")}>{redactSecrets(text)}</pre>
      ) : (
        <p className="rounded-md border border-border bg-muted/40 p-2.5 text-label italic text-muted-foreground">{empty ?? "—"}</p>
      )}
    </section>
  )
}
