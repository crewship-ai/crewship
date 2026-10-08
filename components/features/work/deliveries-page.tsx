"use client"

// Webhook deliveries, as Activity's home tells runs (#3012).
//
//   header      received · accepted · ignored · last arrival
//   endpoints   one card per receiving agent: is it arriving, is it signed,
//               does its work move — "is this webhook working at all?"
//   arrivals    one lane per event family; every delivery a dot
//   latest      what arrived and what it became
//   detail      a delivery opened beside the list; its work one click away
//
// The raw payload is never shown: the API does not return it (a signed
// third-party body can carry anything). Its size, hash and retention are.

import * as React from "react"
import { CheckCircle2, GanttChartSquare, ListChecks, Radio, X } from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { Appear } from "@/components/ui/detail"
import type { WebhookDelivery } from "@/hooks/use-webhook-deliveries"
import { relTime } from "@/lib/time"
import { cn } from "@/lib/utils"
import {
  LEDGER_TONE_DOT,
  LEDGER_TONE_LABEL,
  LEDGER_TONE_TEXT,
  agentName,
  deliveryLanes,
  deliveryLine,
  deliveryTone,
  endpointHealth,
  isDeletedAgent,
  ledgerTone,
} from "@/lib/work-ledger"
import { LaneAxis, LedgerAvatar, WindowToggle, type LedgerWindow } from "./work-queue-page"

function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

function dotClass(d: { tone: "accepted" | "ignored"; workTone: keyof typeof LEDGER_TONE_DOT | null }): string {
  if (d.tone === "ignored") return "bg-muted-foreground/50"
  return d.workTone ? LEDGER_TONE_DOT[d.workTone] : "bg-success"
}

export interface DeliveriesPageProps {
  deliveries: WebhookDelivery[]
  win: LedgerWindow
  onWin: (w: LedgerWindow) => void
  from: number
  now: number
  narrowedTo: string | null
  capped: boolean
  loading: boolean
  onOpenWork: (workId: string) => void
  onOpenAgentWork: (agentId: string) => void
}

export function DeliveriesPage({
  deliveries,
  win,
  onWin,
  from,
  now,
  narrowedTo,
  capped,
  loading,
  onOpenWork,
  onOpenAgentWork,
}: DeliveriesPageProps) {
  const [openId, setOpenId] = React.useState<string | null>(null)
  const accepted = deliveries.filter((d) => deliveryTone(d) === "accepted").length
  const ignored = deliveries.length - accepted
  const latest = [...deliveries].sort((a, b) => b.received_at.localeCompare(a.received_at))
  const health = endpointHealth(deliveries, now)
  const lanes = deliveryLanes(deliveries, { from, to: now })
  const opened = deliveries.find((d) => d.id === openId) ?? null

  return (
    <div className="flex min-h-full">
      <div className="mx-auto flex min-w-0 max-w-[1800px] flex-1 flex-col gap-4 p-4 md:p-6">
        <Appear order={0}>
          <div className="flex flex-wrap items-end gap-3">
            <div className="flex min-w-0 flex-col gap-1">
              <h1 className="text-lg font-semibold tracking-tight">
                Webhook deliveries
                {narrowedTo && <span className="text-muted-foreground"> · {narrowedTo}</span>}
              </h1>
              <p className="flex flex-wrap items-center gap-x-1.5 text-xs text-muted-foreground" aria-label="Summary">
                <span>{deliveries.length} received</span>
                <span className="text-muted-foreground-soft">·</span>
                <span className="text-success">{accepted} accepted</span>
                <span className="text-muted-foreground-soft">·</span>
                <span>{ignored} ignored</span>
                {latest[0] && (
                  <>
                    <span className="text-muted-foreground-soft">·</span>
                    <span>last {relTime(latest[0].received_at)}</span>
                  </>
                )}
              </p>
            </div>
            <div className="flex-1" />
            <WindowToggle value={win} onChange={onWin} />
          </div>
        </Appear>

        {/* ── Endpoints ── */}
        <Appear order={1}>
          <div role="region" aria-label="Endpoints" className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
            {health.length === 0 && (
              <DashboardCard title="Endpoints" icon={Radio}>
                <p className="text-xs text-muted-foreground">{loading ? "Loading deliveries…" : "Nothing arrived in this window."}</p>
              </DashboardCard>
            )}
            {health.map((h) => (
              <div
                key={h.endpointId}
                className={cn(
                  "flex flex-col gap-2 rounded-card border bg-card p-4",
                  h.verdict === "blocked" ? "border-warn/40 bg-warn/[0.04]" : h.verdict === "gone" ? "border-dashed border-border" : "border-border",
                )}
              >
                <div className="flex items-center gap-2.5">
                  <LedgerAvatar agent={h.agent} className="h-7 w-7" />
                  <div className="min-w-0 flex-1">
                    <p className={cn("truncate text-sm font-medium", h.verdict === "gone" && "text-muted-foreground line-through")}>{h.name}</p>
                    <p className="truncate text-[11px] text-muted-foreground">
                      {h.count} {h.count === 1 ? "delivery" : "deliveries"} · {h.families.join(", ") || "—"} · last {relTime(h.last)}
                    </p>
                  </div>
                  {h.verdict !== "gone" && (
                    <span aria-label="Receiving" className="relative inline-flex h-2 w-2 shrink-0">
                      <span className="absolute inset-0 animate-ping rounded-full bg-success opacity-50" />
                      <span className="relative h-2 w-2 rounded-full bg-success" />
                    </span>
                  )}
                </div>
                {h.verdict === "ok" && (
                  <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <CheckCircle2 className="h-3.5 w-3.5 text-success" />
                    Arriving · verified with <span className="font-mono">{h.profile}</span>
                  </p>
                )}
                {h.verdict === "blocked" && (
                  <p className="flex flex-wrap items-center gap-x-2 text-xs text-warn">
                    Arriving fine — but the agent&apos;s queue is blocked
                    {h.agent && (
                      <button type="button" onClick={() => onOpenAgentWork(h.agent!.id)} className="underline-offset-2 hover:underline">
                        Open in Work queue →
                      </button>
                    )}
                  </p>
                )}
                {h.verdict === "gone" && (
                  <p className="text-xs text-muted-foreground">Endpoint gone — new deliveries get 404.</p>
                )}
              </div>
            ))}
          </div>
        </Appear>

        {/* ── Arrivals ── */}
        <Appear order={2}>
          <DashboardCard role="region" aria-label="Arrivals" title="Arrivals" icon={GanttChartSquare} hint="one lane per event family · every delivery a dot">
            {lanes.length === 0 ? (
              <p className="py-6 text-center text-xs text-muted-foreground">{loading ? "Loading deliveries…" : "Nothing arrived in this window."}</p>
            ) : (
              <div className="flex flex-col gap-1.5">
                <LaneAxis from={from} to={now} win={win} />
                {lanes.map((lane) => (
                  <div key={lane.family} className="grid grid-cols-[minmax(0,200px)_1fr_96px] items-center gap-3">
                    <span className="truncate font-mono text-xs">{lane.family}</span>
                    <span className="relative h-3.5 rounded bg-foreground/[0.04]">
                      {lane.dots.map((d) => (
                        <button
                          key={d.id}
                          type="button"
                          onClick={() => setOpenId(d.id)}
                          aria-label={`${lane.family}: ${d.tone}, ${relTime(d.at)}`}
                          title={`${d.tone}${d.workTone ? ` → ${LEDGER_TONE_LABEL[d.workTone].toLowerCase()}` : ""} · ${new Date(d.at).toLocaleString(undefined, { hour12: false })}`}
                          className={cn(
                            "absolute top-1/2 h-2.5 w-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2 ring-card transition-transform hover:scale-150",
                            dotClass(d),
                          )}
                          style={{ left: `${d.left}%` }}
                        />
                      ))}
                    </span>
                    <span className="text-right font-mono text-[11px] text-muted-foreground">
                      {lane.accepted}
                      {lane.ignored > 0 ? ` · ${lane.ignored} ignored` : ""}
                    </span>
                  </div>
                ))}
                <div className="mt-1 flex flex-wrap gap-3 pl-[212px] text-[10.5px] text-muted-foreground">
                  {(
                    [
                      ["bg-success", "accepted → done"],
                      ["bg-destructive", "→ failed"],
                      ["bg-warn", "→ needs you"],
                      ["bg-muted-foreground/50", "ignored"],
                    ] as const
                  ).map(([c, l]) => (
                    <span key={l} className="inline-flex items-center gap-1.5">
                      <span className={cn("h-2 w-2 rounded-full", c)} />
                      {l}
                    </span>
                  ))}
                </div>
              </div>
            )}
          </DashboardCard>
        </Appear>

        {/* ── Latest deliveries ── */}
        <Appear order={3}>
          <DashboardCard role="region" aria-label="Latest deliveries" title="Latest deliveries" icon={ListChecks} hint="what arrived, what it became">
            {latest.length === 0 ? (
              <p className="text-xs text-muted-foreground">{loading ? "Loading deliveries…" : "No delivery matches."}</p>
            ) : (
              <ul className="flex flex-col">
                {latest.slice(0, 15).map((d) => {
                  const work = d.work_state ? ledgerTone(d.work_state) : null
                  const ignoredRow = deliveryTone(d) === "ignored"
                  return (
                    <li key={d.id}>
                      <button
                        type="button"
                        onClick={() => setOpenId(d.id)}
                        aria-current={openId === d.id ? "true" : undefined}
                        className={cn(
                          "flex w-full items-center gap-2.5 rounded-md px-1.5 py-1.5 text-left transition-colors hover:bg-foreground/[0.04]",
                          openId === d.id && "bg-foreground/[0.05]",
                        )}
                      >
                        <span className={cn("h-2 w-2 shrink-0 rounded-full", dotClass({ tone: deliveryTone(d), workTone: work }))} />
                        <span className={cn("w-[170px] shrink-0 truncate font-mono text-xs", ignoredRow && "text-muted-foreground")}>
                          {d.event_type || "—"}
                        </span>
                        <span className="flex w-[150px] shrink-0 items-center gap-1.5 truncate text-xs text-muted-foreground">
                          <LedgerAvatar agent={d.agent} className="h-4 w-4" />
                          <span className={cn("truncate", isDeletedAgent(d.agent) && "line-through")}>{agentName(d.agent)}</span>
                        </span>
                        <span
                          className={cn(
                            "min-w-0 flex-1 truncate text-xs",
                            ignoredRow ? "text-muted-foreground-soft" : work ? LEDGER_TONE_TEXT[work] : "text-muted-foreground",
                          )}
                        >
                          {deliveryLine(d)}
                        </span>
                        <span className="w-32 shrink-0 whitespace-nowrap text-right font-mono text-[10.5px] text-muted-foreground">
                          {bytes(d.body_bytes)} · {relTime(d.received_at)}
                        </span>
                      </button>
                    </li>
                  )
                })}
              </ul>
            )}
            {capped && (
              <p className="mt-2 text-[10.5px] text-muted-foreground-soft">Showing the newest 100 deliveries; older ones are not counted.</p>
            )}
          </DashboardCard>
        </Appear>
      </div>

      {opened && <DeliveryDetail delivery={opened} onClose={() => setOpenId(null)} onOpenWork={onOpenWork} />}
    </div>
  )
}

function DeliveryDetail({
  delivery: d,
  onClose,
  onOpenWork,
}: {
  delivery: WebhookDelivery
  onClose: () => void
  onOpenWork: (id: string) => void
}) {
  const work = d.work_state ? ledgerTone(d.work_state) : null
  const rows: [string, React.ReactNode][] = [
    ["Received", new Date(d.received_at).toLocaleString(undefined, { hour12: false })],
    ["Endpoint", agentName(d.agent)],
    ["Verified with", <span key="p" className="font-mono">{d.profile || "—"}</span>],
    ["Sender's id", <span key="s" className="font-mono">{d.source_delivery_id || "—"}</span>],
    [
      "Decision",
      deliveryTone(d) === "ignored" ? (
        <span key="d">Ignored · {deliveryLine(d)}</span>
      ) : (
        <span key="d" className="text-success">Accepted</span>
      ),
    ],
    [
      "Became",
      d.work_id ? (
        <button key="w" type="button" onClick={() => onOpenWork(d.work_id!)} className={cn("underline-offset-2 hover:underline", work && LEDGER_TONE_TEXT[work])}>
          work · {work ? LEDGER_TONE_LABEL[work].toLowerCase() : "open"} →
        </button>
      ) : (
        "nothing — no work was created"
      ),
    ],
    ["Payload", `${bytes(d.body_bytes)} · sha256 ${d.body_sha256.slice(0, 12) || "—"}`],
    [
      "Kept",
      d.raw_body_available
        ? d.raw_body_expires_at
          ? `until ${new Date(d.raw_body_expires_at).toLocaleDateString()} · replay available`
          : "replay available"
        : "dropped — replay unavailable",
    ],
  ]
  return (
    <aside
      aria-label="Delivery"
      className="hidden w-[360px] shrink-0 border-l border-hairline bg-card/40 p-4 animate-in fade-in-0 slide-in-from-right-3 duration-200 ease-out md:block"
    >
      <div className="mb-3 flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="eyebrow text-muted-foreground">Delivery</p>
          <p className="truncate font-mono text-sm">{d.event_type || "—"}</p>
        </div>
        <button type="button" aria-label="Close" onClick={onClose} className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground">
          <X className="h-4 w-4" />
        </button>
      </div>
      <dl className="grid grid-cols-[96px_1fr] gap-x-3 gap-y-2 text-xs">
        {rows.map(([k, v]) => (
          <React.Fragment key={k}>
            <dt className="text-muted-foreground">{k}</dt>
            <dd className="min-w-0 break-words">{v}</dd>
          </React.Fragment>
        ))}
      </dl>
      <p className="mt-4 text-[10.5px] leading-snug text-muted-foreground-soft">
        The body itself is never shown: a signed third-party payload can carry anything the sender put in it.
      </p>
    </aside>
  )
}
