"use client"

import * as React from "react"
import { BookOpen, Brain, ChevronRight, Eye, KeyRound, Play, Sparkles, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Segmented } from "@/components/ui/segmented"
import { ShowMore } from "@/components/ui/show-more"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { settingsTable, settingsTh, settingsTd, settingsTableRowLink } from "@/components/features/settings/shared"
import type { KeeperLogEntry } from "@/app/(dashboard)/admin/types"
import type { KeeperWsStatus } from "@/app/(dashboard)/admin/hooks/use-admin-websocket"
import { STREAMS, filterActivity, type DecisionFilter, type Stream } from "./security-model"
import { DecisionChip } from "./decision-chip"
import { RequestSheet, subject, when } from "./request-sheet"
import { AskDialog } from "./ask-dialog"
import { WorkspacePicker } from "./workspace-picker"
import type { InstanceGovRow } from "./use-instance-keeper"

export { DecisionChip }

export const STREAM_ICON: Record<Stream, LucideIcon> = {
  requests: KeyRound, skill_review: Sparkles, behavior: Eye, memory_health: Brain, negative_learning: BookOpen,
}

const DECISIONS: { value: DecisionFilter; label: string }[] = [
  { value: "all", label: "All" }, { value: "ALLOW", label: "Allowed" }, { value: "DENY", label: "Denied" },
  { value: "ESCALATE", label: "Escalated" }, { value: "PENDING", label: "Waiting" },
]

/**
 * Admin › Security › Activity: credential decisions and the four reviews as
 * one list, narrowed by kind (the chips), decision and workspace, all on the
 * server (it pages through the whole history, 25 a step). A row opens its
 * record in a sheet; "Try a request" asks the real judge in the admin's
 * current workspace, when there is one.
 */
export function SecurityActivity({ entries, stream, onStream, streamCount, streamTotal, live, error, decision, onDecision, total, loading, onMore, workspaces, picked, onPick, session, onChanged }: {
  entries: KeeperLogEntry[]
  stream: Stream | "all"
  onStream: (s: Stream | "all") => void
  /** Per kind, from the server, over the workspaces asked for. */
  streamCount: Record<Stream, number>
  streamTotal: number
  live: KeeperWsStatus
  error: string | null
  decision: DecisionFilter
  onDecision: (d: DecisionFilter) => void
  /** Rows the whole filter matches, for paging. */
  total: number
  loading: boolean
  onMore: () => void
  workspaces: InstanceGovRow[]
  picked: string[]
  onPick: (slugs: string[]) => void
  /** The admin's current workspace, for "Try a request". Null: not known. */
  session: { id: string; name: string } | null
  onChanged: () => void
}) {
  const canAct = useIsInstanceAdmin() === true
  const [open, setOpen] = React.useState<KeeperLogEntry | null>(null)
  const [ask, setAsk] = React.useState(false)
  // Rows already match stream and decision (the server filtered); this keeps a
  // live row that arrived in another kind out of the list until the reload.
  const rows = filterActivity(entries, stream, decision)
  const meta = STREAMS.find((s) => s.key === stream)
  const byWorkspace = entries.some((e) => !!e.workspace_name)

  return (
    <>
      <div role="group" aria-label="Kind" className="flex flex-wrap gap-1.5">
        <button type="button" aria-pressed={stream === "all"} onClick={() => onStream("all")} className={chip(stream === "all")}>
          All <span className="tabular-nums text-muted-foreground">{streamTotal}</span>
        </button>
        {STREAMS.map((s) => {
          const Icon = STREAM_ICON[s.key]
          return (
            <button key={s.key} type="button" aria-pressed={stream === s.key} onClick={() => onStream(s.key)} className={chip(stream === s.key)}>
              <Icon className="size-3.5" aria-hidden />{s.label} <span className="tabular-nums text-muted-foreground">{streamCount[s.key]}</span>
            </button>
          )
        })}
      </div>

      {error && <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">{error}</p>}

      <section aria-label="Activity" className="overflow-hidden rounded-card border border-border bg-card">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border px-4 py-3">
          <div className="min-w-0 flex-[1_1_14rem]">
            <h3 className="text-sm font-semibold">{meta?.label ?? "All activity"}</h3>
            <p className="mt-0.5 text-label text-muted-foreground">{meta?.about ?? "Credential decisions and background reviews, newest first"}</p>
          </div>
          <span data-slot="live" className={cn("inline-flex items-center gap-1.5 text-label", live === "connected" ? "text-success" : "text-muted-foreground")}>
            <span aria-hidden className={cn("size-1.5 rounded-full", live === "connected" ? "bg-success" : "bg-muted-foreground/50")} />
            {live === "connected" ? "Live" : live === "connecting" ? "Connecting…" : "Not live"}
          </span>
          <WorkspacePicker rows={workspaces} value={picked} onChange={onPick} />
          <Segmented ariaLabel="Decision" value={decision} onChange={onDecision} options={DECISIONS} />
          {session ? (
            <Button type="button" variant="outline" size="sm" disabled={!canAct} onClick={() => setAsk(true)}><Play className="mr-1 size-3.5" />Try a request</Button>
          ) : (
            <span data-testid="ask-unavailable" className="text-label text-muted-foreground" title="A request is asked in your current workspace, and you have none">
              Try a request needs a workspace
            </span>
          )}
        </div>

        <ul className="md:hidden" aria-label="Decisions" data-slot="activity-list">
          {rows.map((e) => (
            <li key={e.id} className="border-b border-border last:border-b-0">
              <button type="button" onClick={() => setOpen(e)} className="flex coarse:min-h-[44px] w-full items-center gap-3 px-4 py-3 text-left">
                <span className="min-w-0 flex-1">
                  <span className={cn("block truncate text-control font-medium", e.credential_name && "font-mono")}>{subject(e)}</span>
                  <span className="block truncate text-label text-muted-foreground">{e.agent_name}{e.workspace_name ? ` · ${e.workspace_name}` : ""} · {when(e.created_at)}</span>
                </span>
                <DecisionChip decision={e.decision} />
              </button>
            </li>
          ))}
        </ul>
        <div className="overflow-x-auto max-md:hidden">
          <table className={settingsTable}>
            <thead>
              <tr>
                <th className={settingsTh}>When</th>
                {byWorkspace && <th className={settingsTh}>Workspace</th>}
                <th className={settingsTh}>Agent</th>
                <th className={settingsTh}>About</th>
                <th className={settingsTh}>Decision</th>
                <th className={cn(settingsTh, "text-right")}>Risk</th>
                <th className={cn(settingsTh, "w-6")} aria-hidden />
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
                    <span className={cn(e.credential_name && "font-mono text-label")}>{subject(e)}</span>
                    {e.request_type === "execute" && <span className="ml-1.5 text-label text-muted-foreground">exec</span>}
                  </td>
                  <td className={settingsTd}><DecisionChip decision={e.decision} /></td>
                  <td className={cn(settingsTd, "text-right font-mono")}>{e.risk_score != null ? `${e.risk_score}/10` : "—"}</td>
                  <td className={settingsTd}><ChevronRight className="size-3.5 text-muted-foreground-soft" aria-hidden /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {rows.length === 0 && !loading && (
          <p className="px-4 py-8 text-center text-label text-muted-foreground">
            {decision === "all" && stream !== "requests" && stream !== "all" && total === 0
              ? "Nothing recorded yet. These reviews run on a schedule, and behaviour only while the watchdog is on."
              : "Nothing here yet. Decisions appear as agents ask."}
          </p>
        )}
        <ShowMore shown={entries.length} total={total} nextCount={Math.min(25, Math.max(total - entries.length, 0))} onMore={onMore} noun="decisions" paged={total > 25} loading={loading} />
      </section>

      <RequestSheet entry={open} onClose={() => setOpen(null)} onResolved={onChanged} />
      {session && <AskDialog open={ask} onOpenChange={setAsk} workspaceId={session.id} workspaceName={session.name} onAsked={onChanged} />}
    </>
  )
}

function chip(on: boolean) {
  return cn("inline-flex h-8 items-center gap-1.5 rounded-lg border px-3 text-control transition-colors coarse:min-h-[44px]",
    on ? "border-primary bg-primary/10 font-medium text-foreground" : "border-border text-muted-foreground hover:text-foreground")
}
