"use client"

// The Work queue, as Activity's home tells runs (#3012).
//
//   header        one sentence: pieces of work · needs you · in line · done · failed
//   flow          Arrived → In line → Running → Done, with the three ways out
//   needs you     work nobody can call done or failed, settled here
//   why failed    failures grouped by cause
//   what came in  one lane per agent; every piece of work a dot to open
//   latest work   one line each — what came in, who took it, how it ended
//
// The table it replaces listed ids and ten state chips, two of which nothing
// ever fills.

import * as React from "react"
import { AlertTriangle, ArrowRight, GanttChartSquare, Inbox, ListChecks } from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { Button } from "@/components/ui/button"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Appear } from "@/components/ui/detail"
import { CrewIcon } from "@/components/ui/crew-icon"
import { iconColorProps } from "@/lib/crew-icons"
import type { LedgerAgent, LedgerCrew, WorkItem } from "@/hooks/use-work-items"
import { formatDurationMs } from "@/lib/activity-stream"
import { relTime } from "@/lib/time"
import { cn } from "@/lib/utils"
import {
  LEDGER_TONE_DOT,
  LEDGER_TONE_LABEL,
  LEDGER_TONE_TEXT,
  agentName,
  failureCauses,
  ledgerCounts,
  ledgerFlow,
  ledgerHeadline,
  ledgerLanes,
  ledgerTone,
  needsYou,
  workLine,
  workSubject,
  type LedgerTone,
} from "@/lib/work-ledger"
import { ResolveWorkDialog, type ResolveMode } from "./resolve-work-dialog"

export type LedgerWindow = "24h" | "7d"
export const LEDGER_WINDOW_MS: Record<LedgerWindow, number> = { "24h": 24 * 3_600_000, "7d": 7 * 24 * 3_600_000 }

const HEAD_TONE = {
  default: "text-muted-foreground",
  primary: "text-primary",
  warn: "text-warn",
  destructive: "text-destructive",
} as const

export function LedgerAvatar({ agent, className }: { agent: LedgerAgent | null | undefined; className?: string }) {
  if (!agent) {
    return <span aria-hidden className={cn("inline-block shrink-0 rounded-full border border-dashed border-muted-foreground/50", className)} />
  }
  return (
    <AgentAvatar
      seed={agent.avatar_seed || agent.id}
      style={agent.avatar_style || undefined}
      agentId={agent.id}
      alt=""
      className={cn("shrink-0 rounded-full", className)}
    />
  )
}

/** The crew in its own colour and icon, as the rest of the product draws it. */
export function CrewChip({ crew, className }: { crew: LedgerCrew; className?: string }) {
  const glyph = iconColorProps(crew.color)
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1", className)}>
      <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" className="h-4 w-4 rounded [&_svg]:h-2.5 [&_svg]:w-2.5" />
      <span className={cn("truncate text-[10.5px]", glyph.className)} style={glyph.style}>
        {crew.name}
      </span>
    </span>
  )
}

export function WindowToggle({ value, onChange }: { value: LedgerWindow; onChange: (w: LedgerWindow) => void }) {
  return (
    <div role="group" aria-label="Window" className="flex overflow-hidden rounded-md border border-border font-mono text-[11px]">
      {(["24h", "7d"] as const).map((w) => (
        <button
          key={w}
          type="button"
          aria-pressed={value === w}
          onClick={() => onChange(w)}
          className={cn(
            "px-2.5 py-1 transition-colors",
            value === w ? "bg-foreground/[0.08] text-foreground" : "text-muted-foreground hover:text-foreground",
          )}
        >
          {w === "24h" ? "24 h" : "7 d"}
        </button>
      ))}
    </div>
  )
}

export function LaneAxis({ from, to, win }: { from: number; to: number; win: LedgerWindow }) {
  const ticks = 6
  const labels = Array.from({ length: ticks + 1 }, (_, i) => {
    const t = new Date(from + ((to - from) * i) / ticks)
    if (i === ticks) return "now"
    return win === "24h"
      ? t.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false })
      : t.toLocaleDateString(undefined, { weekday: "short" })
  })
  return (
    <div className="grid grid-cols-[minmax(0,200px)_1fr_96px] gap-3 font-mono text-[10px] text-muted-foreground-soft">
      <span />
      <span className="flex justify-between">
        {labels.map((l, i) => (
          <span key={i}>{l}</span>
        ))}
      </span>
      <span />
    </div>
  )
}

export interface WorkQueuePageProps {
  workspaceId: string
  /** The window's work, already narrowed by the rail. */
  items: WorkItem[]
  /** The window's work before narrowing — what "behind it" counts against. */
  allItems: WorkItem[]
  win: LedgerWindow
  onWin: (w: LedgerWindow) => void
  from: number
  now: number
  narrowedTo: string | null
  capped: boolean
  loading: boolean
  canResolve: boolean
  onTone: (t: LedgerTone) => void
  onOpen: (id: string) => void
}

export function WorkQueuePage({
  workspaceId,
  items,
  allItems,
  win,
  onWin,
  from,
  now,
  narrowedTo,
  capped,
  loading,
  canResolve,
  onTone,
  onOpen,
}: WorkQueuePageProps) {
  const [resolving, setResolving] = React.useState<{ item: WorkItem; mode: ResolveMode } | null>(null)
  const counts = ledgerCounts(items)
  const parts = ledgerHeadline(counts)
  const flow = ledgerFlow(items)
  // "Behind it" is the agent's whole line, not only what the rail shows.
  const needs = needsYou(allItems).filter((n) => items.some((i) => i.id === n.item.id))
  const causes = failureCauses(items)
  const lanes = ledgerLanes(items, { from, to: now })
  const latest = [...items].sort((a, b) => b.created_at.localeCompare(a.created_at)).slice(0, 12)

  return (
    <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
      <Appear order={0}>
        <div className="flex flex-wrap items-end gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <h1 className="text-lg font-semibold tracking-tight">
              Work queue
              {narrowedTo && <span className="text-muted-foreground"> · {narrowedTo}</span>}
            </h1>
            <p className="flex flex-wrap items-center gap-x-1.5 text-xs" aria-label="Summary">
              {parts.map((p, i) => (
                <React.Fragment key={p.text}>
                  {i > 0 && <span className="text-muted-foreground-soft">·</span>}
                  <span className={HEAD_TONE[p.tone]}>{p.text}</span>
                </React.Fragment>
              ))}
              <span className="text-muted-foreground-soft">· {win === "24h" ? "last 24 h" : "last 7 days"}</span>
            </p>
          </div>
          <div className="flex-1" />
          <WindowToggle value={win} onChange={onWin} />
        </div>
      </Appear>

      {/* ── Flow ── */}
      <Appear order={1}>
        <DashboardCard role="region" aria-label="Flow" title="Flow" icon={ArrowRight} hint="click a step to narrow">
          <div className="flex flex-wrap items-stretch gap-2">
            <FlowStep label="Arrived" value={flow.arrived} note={`from ${flow.sources} ${flow.sources === 1 ? "source" : "sources"}`} />
            <FlowArrow />
            <FlowStep
              label="In line"
              value={flow.line.count}
              note={flow.line.blocked ? "behind a blocked item" : "waiting for capacity"}
              tone={flow.line.blocked ? "text-warn" : undefined}
              onClick={() => onTone("line")}
            />
            <FlowArrow />
            <FlowStep label="Running" value={flow.running} note="1 slot per agent" onClick={() => onTone("running")} />
            <FlowArrow />
            <FlowStep
              label="Done"
              value={flow.done}
              note={flow.medianDoneMs != null ? `median ${formatDurationMs(flow.medianDoneMs)}` : "—"}
              tone="text-success"
              onClick={() => onTone("done")}
            />
            <span aria-hidden className="mx-1 hidden w-px self-stretch bg-border md:block" />
            <FlowStep label="Needs you" value={flow.needs} note="outcome unclear" tone="text-warn" onClick={() => onTone("needs")} />
            <FlowStep
              label="Failed"
              value={flow.failed}
              note={`${flow.causes} ${flow.causes === 1 ? "cause" : "causes"}`}
              tone="text-destructive"
              onClick={() => onTone("failed")}
            />
            <FlowStep label="Cancelled" value={flow.cancelled} note="stopped on purpose" onClick={() => onTone("cancelled")} />
          </div>
        </DashboardCard>
      </Appear>

      {/* ── Needs you · Why work failed ── */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)]">
        <Appear order={2}>
          <DashboardCard
            role="region"
            aria-label="Needs you"
            title="Needs you"
            icon={Inbox}
            hint={needs.length ? "the agent takes no new work until this is settled" : undefined}
            className={cn("h-full", needs.length > 0 && "border-warn/40 bg-warn/[0.04]")}
          >
            {needs.length === 0 ? (
              <p className="text-xs text-muted-foreground">No work is waiting for a decision.</p>
            ) : (
              <ul className="flex flex-col gap-4">
                {needs.slice(0, 3).map(({ item, behind }) => (
                  <li key={item.id} className="flex flex-col gap-2">
                    <div className="flex items-start gap-2.5">
                      <LedgerAvatar agent={item.agent} className="mt-0.5 h-6 w-6" />
                      <div className="flex min-w-0 flex-col gap-0.5">
                        <span className="text-xs text-muted-foreground">
                          <span className="font-mono text-foreground/85">{workSubject(item)}</span> → {agentName(item.agent)}
                        </span>
                        <span className="line-clamp-2 text-sm font-medium">
                          {item.state_reason || "The run ended without a recorded outcome."} Crewship cannot tell whether it happened.
                        </span>
                        <span className="text-[11px] text-muted-foreground">
                          {relTime(item.created_at)} · attempt {item.attempt_count}
                          {behind > 0 && (
                            <span className="text-warn">
                              {" "}
                              · {behind} more {behind === 1 ? "item waits" : "items wait"} behind it
                            </span>
                          )}
                        </span>
                      </div>
                    </div>
                    <div className="flex flex-wrap gap-2 pl-[34px]">
                      {canResolve && (
                        <>
                          <Button size="sm" onClick={() => setResolving({ item, mode: "retry" })}>
                            Retry
                          </Button>
                          <Button size="sm" variant="outline" onClick={() => setResolving({ item, mode: "failed" })}>
                            Mark as failed
                          </Button>
                          <Button size="sm" variant="outline" onClick={() => setResolving({ item, mode: "succeeded" })}>
                            Mark as done
                          </Button>
                        </>
                      )}
                      <Button size="sm" variant="ghost" onClick={() => onOpen(item.id)}>
                        Open
                      </Button>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </DashboardCard>
        </Appear>
        <Appear order={3}>
          <DashboardCard role="region" aria-label="Why work failed" title="Why work failed" icon={AlertTriangle} hint="grouped by cause" className="h-full">
            {causes.length === 0 ? (
              <p className="text-xs text-muted-foreground">Nothing failed in this window.</p>
            ) : (
              <ul className="flex flex-col gap-1">
                {causes.slice(0, 5).map((c) => (
                  <li key={c.cause}>
                    <button
                      type="button"
                      onClick={() => onOpen(c.latestId)}
                      className="flex w-full items-start gap-2.5 rounded-md px-1.5 py-1.5 text-left transition-colors hover:bg-foreground/[0.04]"
                    >
                      <span className="w-7 shrink-0 font-mono text-xs text-destructive">{c.count}×</span>
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-xs first-letter:uppercase">{c.cause}</span>
                        <span className="block truncate text-[11px] text-muted-foreground">
                          {c.agents.join(", ")} · {c.events.join(", ")}
                        </span>
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </DashboardCard>
        </Appear>
      </div>

      {/* ── What came in ── */}
      <Appear order={4}>
        <DashboardCard
          role="region"
          aria-label="What came in"
          title="What came in"
          icon={GanttChartSquare}
          hint="one lane per agent · every piece of work a dot · click to open"
        >
          {lanes.length === 0 ? (
            <p className="py-6 text-center text-xs text-muted-foreground">{loading ? "Loading work…" : "No work arrived in this window."}</p>
          ) : (
            <div className="flex flex-col gap-1.5">
              <LaneAxis from={from} to={now} win={win} />
              {lanes.map((lane) => (
                <div key={lane.key} className="grid grid-cols-[minmax(0,200px)_1fr_96px] items-center gap-3">
                  <span className="flex min-w-0 items-center gap-2">
                    <LedgerAvatar agent={lane.agent} className="h-5 w-5" />
                    <span className="min-w-0">
                      <span className={cn("block truncate text-xs", !lane.agent && "text-muted-foreground line-through")}>{lane.name}</span>
                      <span className="flex min-w-0 items-center gap-1.5">
                        {lane.crew && <CrewChip crew={lane.crew} className="shrink-0" />}
                        <span className="truncate font-mono text-[10px] text-muted-foreground-soft">{lane.feeds.join(" · ")}</span>
                      </span>
                    </span>
                  </span>
                  <span className="relative h-3.5 rounded bg-foreground/[0.04]">
                    {lane.dots.map((d) => (
                      <button
                        key={d.id}
                        type="button"
                        onClick={() => onOpen(d.id)}
                        aria-label={`${d.subject}: ${LEDGER_TONE_LABEL[d.tone]}, ${relTime(d.at)}`}
                        title={`${d.subject} · ${LEDGER_TONE_LABEL[d.tone]} · ${new Date(d.at).toLocaleString(undefined, { hour12: false })}`}
                        className={cn(
                          "absolute top-1/2 h-2.5 w-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2 ring-card transition-transform hover:scale-150",
                          LEDGER_TONE_DOT[d.tone],
                        )}
                        style={{ left: `${d.left}%` }}
                      />
                    ))}
                  </span>
                  <span
                    className={cn(
                      "text-right font-mono text-[11px]",
                      lane.summary.tone === "default" ? "text-muted-foreground" : LEDGER_TONE_TEXT[lane.summary.tone],
                    )}
                  >
                    {lane.summary.text}
                  </span>
                </div>
              ))}
              <Legend />
            </div>
          )}
        </DashboardCard>
      </Appear>

      {/* ── Latest work ── */}
      <Appear order={5}>
        <DashboardCard role="region" aria-label="Latest work" title="Latest work" icon={ListChecks} hint="what came in, who took it, how it ended">
          {latest.length === 0 ? (
            <p className="text-xs text-muted-foreground">{loading ? "Loading work…" : "No work matches."}</p>
          ) : (
            <ul className="flex flex-col">
              {latest.map((item) => (
                <li key={item.id}>
                  <WorkRow item={item} onOpen={onOpen} />
                </li>
              ))}
            </ul>
          )}
          {capped && (
            <p className="mt-2 text-[10.5px] text-muted-foreground-soft">Showing the newest 100 pieces of work; older ones are not counted.</p>
          )}
        </DashboardCard>
      </Appear>

      <ResolveWorkDialog
        workspaceId={workspaceId}
        item={resolving?.item ?? null}
        mode={resolving?.mode ?? "failed"}
        onOpenChange={(o) => !o && setResolving(null)}
      />
    </div>
  )
}

function FlowStep({
  label,
  value,
  note,
  tone,
  onClick,
}: {
  label: string
  value: number
  note: string
  tone?: string
  onClick?: () => void
}) {
  const body = (
    <>
      <span className="eyebrow text-muted-foreground">{label}</span>
      <span className={cn("font-mono text-xl tabular-nums", value === 0 ? "text-muted-foreground-soft" : tone ?? "text-foreground")}>{value}</span>
      <span className="truncate text-[10.5px] text-muted-foreground">{note}</span>
    </>
  )
  const cls = "flex min-w-[112px] flex-1 flex-col gap-0.5 rounded-md border border-border px-3 py-2 text-left"
  return onClick ? (
    <button type="button" onClick={onClick} className={cn(cls, "transition-colors hover:bg-foreground/[0.04]")}>
      {body}
    </button>
  ) : (
    <div className={cls}>{body}</div>
  )
}

function FlowArrow() {
  return <ArrowRight aria-hidden className="hidden h-4 w-4 shrink-0 self-center text-muted-foreground-soft md:block" />
}

function Legend() {
  return (
    <div className="mt-1 flex flex-wrap gap-3 pl-[212px] text-[10.5px] text-muted-foreground">
      {(["done", "failed", "needs", "line", "running", "cancelled"] as const).map((t) => (
        <span key={t} className="inline-flex items-center gap-1.5">
          <span className={cn("h-2 w-2 rounded-full", LEDGER_TONE_DOT[t])} />
          {LEDGER_TONE_LABEL[t].toLowerCase()}
        </span>
      ))}
    </div>
  )
}

function WorkRow({ item, onOpen }: { item: WorkItem; onOpen: (id: string) => void }) {
  const tone = ledgerTone(item.state)
  return (
    <button
      type="button"
      onClick={() => onOpen(item.id)}
      className="group flex w-full items-center gap-2.5 rounded-md px-1.5 py-1.5 text-left transition-colors hover:bg-foreground/[0.04]"
    >
      <span className="relative inline-flex h-2 w-2 shrink-0">
        {tone === "running" && <span className={cn("absolute inset-0 animate-ping rounded-full opacity-60", LEDGER_TONE_DOT[tone])} />}
        <span className={cn("relative h-2 w-2 rounded-full", LEDGER_TONE_DOT[tone])} />
      </span>
      <span className="w-[170px] shrink-0 truncate font-mono text-xs">{workSubject(item)}</span>
      <span className="flex w-[230px] shrink-0 items-center gap-1.5 truncate text-xs text-muted-foreground">
        <LedgerAvatar agent={item.agent} className="h-4 w-4" />
        <span className={cn("truncate", !item.agent && "line-through")}>{agentName(item.agent)}</span>
        {item.crew && <CrewChip crew={item.crew} className="hidden lg:inline-flex" />}
      </span>
      <span
        className={cn(
          "min-w-0 flex-1 truncate text-xs",
          tone === "failed" ? "text-destructive/90" : tone === "needs" ? "text-warn" : "text-muted-foreground",
        )}
      >
        {workLine(item)}
      </span>
      <span className="hidden shrink-0 rounded border border-border px-1.5 py-px font-mono text-[10px] text-muted-foreground md:inline">
        {item.attempt_count} {item.attempt_count === 1 ? "attempt" : "attempts"}
      </span>
      <span className="w-14 shrink-0 text-right font-mono text-[10.5px] text-muted-foreground">{relTime(item.created_at)}</span>
    </button>
  )
}
