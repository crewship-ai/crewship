"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import {
  AlertTriangle,
  CalendarClock,
  CircleCheck,
  CircleDot,
  FileText,
  Hand,
  KeyRound,
  MessageSquare,
  MousePointerClick,
  Play,
  Square,
  Users,
  Webhook,
  Workflow,
  Zap,
} from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import {
  WORK_STATUS_LABEL,
  type ChangeItem,
  type ChangeKind,
  type FeedGroup,
  type FeedItem,
  type StoryItem,
  type WorkItem,
  type WorkSource,
  type WorkStatus,
  whenLabel,
} from "@/lib/activity-feed"
import { formatDurationMs } from "@/lib/activity-stream"
import type { RunTone } from "@/lib/activity-run"
import { cn } from "@/lib/utils"

/* ------------------------------------------------------------------ *
 *  Glyphs
 * ------------------------------------------------------------------ */

const STATUS_GLYPH: Record<WorkStatus, { icon: LucideIcon; className: string }> = {
  running: { icon: Play, className: "bg-chip-info-bg text-chip-info-fg" },
  waiting: { icon: Hand, className: "bg-chip-warn-bg text-chip-warn-fg" },
  failed: { icon: AlertTriangle, className: "bg-chip-danger-bg text-chip-danger-fg" },
  completed: { icon: CircleCheck, className: "bg-chip-ok-bg text-chip-ok-fg" },
  stopped: { icon: Square, className: "bg-chip-neutral-bg text-chip-neutral-fg" },
}

/** The 24 px status tile at the start of a work row. */
export function StatusGlyph({ status, size = "md" }: { status: WorkStatus; size?: "sm" | "md" }) {
  const g = STATUS_GLYPH[status]
  const Icon = g.icon
  return (
    <span
      className={cn("inline-grid shrink-0 place-items-center rounded-lg", size === "sm" ? "size-5 rounded-md" : "size-6", g.className)}
      title={WORK_STATUS_LABEL[status]}
      role="img"
      aria-label={WORK_STATUS_LABEL[status]}
    >
      <Icon className={size === "sm" ? "size-3" : "size-3.5"} aria-hidden />
    </span>
  )
}

export const SOURCE_ICON: Record<WorkSource, LucideIcon> = {
  schedule: CalendarClock,
  webhook: Webhook,
  issue: CircleDot,
  chat: MessageSquare,
  person: MousePointerClick,
  routine: Workflow,
  rule: Zap,
  other: Workflow,
}

export const CHANGE_ICON: Record<ChangeKind, LucideIcon> = {
  issue: CircleDot,
  routine: Workflow,
  artifact: FileText,
  crew: Users,
  access: KeyRound,
}

export function KindGlyph({ kind, size = "md" }: { kind: ChangeKind; size?: "sm" | "md" }) {
  const Icon = CHANGE_ICON[kind]
  return (
    <span
      className={cn(
        "inline-grid shrink-0 place-items-center border border-border bg-muted text-muted-foreground",
        size === "sm" ? "size-5 rounded-md" : "size-6 rounded-lg",
      )}
      aria-hidden
    >
      <Icon className={size === "sm" ? "size-3" : "size-3.5"} />
    </span>
  )
}

export type AgentResolver = (id: string) => { seed: string; style?: string | null; name: string } | undefined

function AgentStack({ agents, resolve }: { agents: { id: string; name: string }[]; resolve?: AgentResolver }) {
  return (
    <span className="hidden min-w-0 items-center gap-0.5 md:inline-flex" aria-label={agents.map((a) => a.name).join(", ") || undefined}>
      {agents.slice(0, 3).map((a) => {
        const r = resolve?.(a.id)
        return (
          <AgentAvatar
            key={a.id}
            seed={r?.seed ?? a.id}
            style={r?.style}
            alt=""
            title={r?.name ?? a.name}
            className="size-5 shrink-0 rounded-full"
          />
        )
      })}
      {agents.length > 3 && <span className="font-mono text-micro text-muted-foreground">+{agents.length - 3}</span>}
    </span>
  )
}

/* ------------------------------------------------------------------ *
 *  Columns
 * ------------------------------------------------------------------ */

// Work · Agent · Took · Started. Progress (steps) and Cost need fields the
// chain index does not send yet, so they are not drawn (ODCH-15).
// Phone: status, work, time. Tablet: + agent, took. Desktop: + started.
export const FEED_GRID =
  "grid grid-cols-[26px_minmax(0,1fr)_auto] items-center gap-3 px-3 md:grid-cols-[26px_minmax(0,1fr)_54px_80px] md:px-4 xl:grid-cols-[26px_minmax(0,1fr)_64px_92px_74px]"

export function FeedHead() {
  return (
    <div
      role="row"
      className={cn(FEED_GRID, "hidden h-[34px] border-b border-border bg-muted/50 text-label text-muted-foreground md:grid")}
    >
      <span role="columnheader" aria-label="Status" />
      <span role="columnheader">Work</span>
      <span role="columnheader">Agent</span>
      <span role="columnheader">Took</span>
      <span role="columnheader" className="hidden text-right xl:block">
        Started
      </span>
    </div>
  )
}

function took(w: WorkItem): string {
  if (w.durationMs == null) return w.active ? "running" : "—"
  return formatDurationMs(w.durationMs)
}

/** The result in the row's second line, in the state's tone. */
function outcome(w: WorkItem): { text: string; className?: string } | null {
  if (w.runs) return { text: `${w.runs.length} runs · all completed` }
  if (w.status === "waiting") return { text: "waits for a person", className: "text-warn" }
  if (w.status === "failed") {
    const runs = w.chain.runs
    const failed = w.chain.failed_runs
    return { text: runs > 1 && failed > 0 ? `${failed} of ${runs} runs could not finish` : "could not finish", className: "text-destructive" }
  }
  if (w.status === "stopped") return { text: "stopped" }
  if (w.issues.length > 0) {
    const more = Math.max(w.chain.issue_count, w.issues.length) - 2
    return { text: w.issues.slice(0, 2).map((i) => i.label).join(", ") + (more > 0 ? ` +${more}` : "") }
  }
  return null
}

const ROW =
  "w-full min-h-[58px] border-b border-border text-left transition-colors last:border-b-0 hover:bg-muted/60 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring data-[selected=true]:bg-primary/10"

export function WorkRow({
  item,
  now,
  selected,
  onSelect,
  resolveAgent,
}: {
  item: WorkItem
  now: number
  selected: boolean
  onSelect: () => void
  resolveAgent?: AgentResolver
}) {
  const SourceIcon = SOURCE_ICON[item.source]
  const out = outcome(item)
  return (
    <button
      type="button"
      data-row-id={item.id}
      data-selected={selected}
      aria-current={selected || undefined}
      onClick={onSelect}
      className={cn(FEED_GRID, ROW, "py-2")}
    >
      <StatusGlyph status={item.status} />
      <span className="min-w-0">
        <b className="block truncate font-medium text-foreground">
          {item.title}
          {item.runs && (
            <span className="ml-1.5 inline-block rounded-sm bg-muted px-1.5 align-[1px] text-micro font-semibold text-muted-foreground ring-1 ring-border">
              ×{item.runs.length} runs
            </span>
          )}
        </b>
        <small className="block truncate text-label text-muted-foreground">
          <span className="inline-flex items-center gap-1 align-[-2px]">
            <SourceIcon className="size-3" aria-hidden />
            {item.origin}
          </span>
          {out && <span className={out.className}> · {out.text}</span>}
        </small>
      </span>
      <AgentStack agents={item.agents} resolve={resolveAgent} />
      <span className="hidden font-mono text-label text-muted-foreground md:block">
        {item.runs ? <span title="Average of the runs">{took(item)}</span> : took(item)}
      </span>
      <span className="text-right font-mono text-label text-muted-foreground md:hidden xl:block">{whenLabel(item.ts, now)}</span>
    </button>
  )
}

export function ChangeRow({
  item,
  now,
  selected,
  onSelect,
  resolveAgent,
}: {
  item: ChangeItem
  now: number
  selected: boolean
  onSelect: () => void
  resolveAgent?: AgentResolver
}) {
  return (
    <button
      type="button"
      data-row-id={item.id}
      data-selected={selected}
      aria-current={selected || undefined}
      onClick={onSelect}
      className={cn(FEED_GRID, ROW, "py-2")}
    >
      <KindGlyph kind={item.kind} />
      <span className="min-w-0">
        <b className="block truncate font-medium text-foreground">{item.object}</b>
        <small className="block truncate text-label text-muted-foreground">
          {item.action} · {item.who}
        </small>
      </span>
      <AgentStack agents={item.agentId ? [{ id: item.agentId, name: item.who }] : []} resolve={resolveAgent} />
      <span className="hidden truncate font-mono text-label text-muted-foreground md:block" title={item.diff}>
        {item.diff ?? ""}
      </span>
      <span className="text-right font-mono text-label text-muted-foreground md:hidden xl:block">{whenLabel(item.ts, now)}</span>
    </button>
  )
}

/** Several updates to one issue or page on one day (v67 `srow`). */
export function StoryRow({
  item,
  now,
  selected,
  onSelect,
  resolveAgent,
}: {
  item: StoryItem
  now: number
  selected: boolean
  onSelect: () => void
  resolveAgent?: AgentResolver
}) {
  return (
    <button
      type="button"
      data-row-id={item.id}
      data-selected={selected}
      aria-current={selected || undefined}
      onClick={onSelect}
      className={cn(FEED_GRID, ROW, "py-2")}
    >
      <KindGlyph kind={item.objectKind} />
      <span className="min-w-0">
        <b className="block truncate font-medium text-foreground">
          {item.title}
          <span className="ml-1.5 inline-block rounded-sm bg-muted px-1.5 align-[1px] text-micro font-semibold text-muted-foreground ring-1 ring-border">
            {item.items.length} updates
          </span>
        </b>
        <small className="block truncate text-label text-muted-foreground">{item.flow}</small>
      </span>
      <AgentStack agents={item.agents} resolve={resolveAgent} />
      <span className="hidden truncate font-mono text-label text-muted-foreground md:block" title={item.diff}>
        {item.diff ?? ""}
      </span>
      <span className="text-right font-mono text-label text-muted-foreground md:hidden xl:block">{whenLabel(item.ts, now)}</span>
    </button>
  )
}

export function GroupHeader({ group }: { group: FeedGroup }) {
  return (
    <div
      role="rowgroup"
      className="flex items-center gap-2 border-b border-border bg-muted/40 px-3 pb-1.5 pt-2.5 text-label font-semibold text-foreground/80 md:px-4"
    >
      {group.label}
      <span className="font-mono text-micro font-medium text-muted-foreground">{group.items.length}</span>
    </div>
  )
}

/** The list itself: a header, then each group's rows. */
export function FeedTable({
  groups,
  now,
  selectedId,
  onSelect,
  resolveAgent,
}: {
  groups: FeedGroup[]
  now: number
  selectedId: string | null
  onSelect: (item: FeedItem) => void
  resolveAgent?: AgentResolver
}) {
  return (
    <div role="table" aria-label="Activity" className="overflow-hidden rounded-xl border border-border bg-card">
      <FeedHead />
      {groups.map((g) => (
        <div key={g.key} role="presentation">
          <GroupHeader group={g} />
          {g.items.map((x) =>
            x.type === "story" ? (
              <StoryRow key={x.id} item={x} now={now} selected={selectedId === x.id} onSelect={() => onSelect(x)} resolveAgent={resolveAgent} />
            ) : x.type === "work" ? (
              <WorkRow key={x.id} item={x} now={now} selected={selectedId === x.id} onSelect={() => onSelect(x)} resolveAgent={resolveAgent} />
            ) : (
              <ChangeRow key={x.id} item={x} now={now} selected={selectedId === x.id} onSelect={() => onSelect(x)} resolveAgent={resolveAgent} />
            ),
          )}
        </div>
      ))}
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Run history — the last runs of one routine as bars
 * ------------------------------------------------------------------ */

const HISTORY_TONE: Record<RunTone, string> = {
  done: "bg-success",
  failed: "bg-destructive",
  waiting: "bg-warn",
  running: "bg-primary",
  stopped: "bg-line-strong",
}

export interface HistoryBar {
  id: string
  tone: RunTone
  durationMs?: number
  current?: boolean
  label: string
}

/**
 * Up to 20 columns, oldest left. Height follows the run's duration against
 * the longest one shown, so a run that took twice as long stands out; a run
 * that ended without doing anything (stopped) stays short.
 */
export function RunHistoryStrip({ bars, onPick }: { bars: HistoryBar[]; onPick?: (id: string) => void }) {
  const max = Math.max(1, ...bars.map((b) => b.durationMs ?? 0))
  return (
    <div className="flex h-[38px] items-end gap-[3px] py-1" role="list" aria-label="Earlier runs">
      {bars.map((b) => {
        const h = b.tone === "stopped" ? 30 : 40 + Math.round(((b.durationMs ?? 0) / max) * 60)
        const cls = cn("min-w-[3px] flex-1 rounded-t-xs", HISTORY_TONE[b.tone], b.current && "ring-2 ring-ring ring-offset-1 ring-offset-card")
        return onPick ? (
          <button
            key={b.id}
            type="button"
            role="listitem"
            title={b.label}
            aria-label={b.label}
            onClick={() => onPick(b.id)}
            className={cls}
            style={{ height: `${h}%` }}
          />
        ) : (
          <span key={b.id} role="listitem" title={b.label} aria-label={b.label} className={cls} style={{ height: `${h}%` }} />
        )
      })}
    </div>
  )
}

