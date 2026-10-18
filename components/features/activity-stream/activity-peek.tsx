"use client"

import * as React from "react"
import Link from "next/link"
import type { LucideIcon } from "lucide-react"
import { BarChart3, Bot, ChevronRight, CircleDot, ExternalLink, Hand, Info, Layers, Link2, List, RefreshCw, RotateCcw, Square, Workflow } from "lucide-react"

import { PropertyRow } from "@/components/layout/property-row"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Peek } from "@/components/ui/peek"
import { StatusPill } from "@/components/ui/status-pill"
import { RoutineRunPeek } from "@/components/features/routines/routine-run-peek"
import { routineViewHref } from "@/components/features/routines/routine-navigation"
import { usePipelineRunRecords } from "@/hooks/use-pipeline-run-records"
import {
  CHANGE_KIND_LABEL,
  WORK_SOURCE_LABEL,
  WORK_STATUS_LABEL,
  WORK_STATUS_TONE,
  changeSentence,
  dayLabel,
  runHistory,
  whenLabel,
  type ChangeItem,
  type FeedItem,
  type StoryItem,
  type WorkItem,
} from "@/lib/activity-feed"
import { workflowHandle, workflowSentence } from "@/lib/activity-lenses"
import { runActions, runTone } from "@/lib/activity-run"
import { formatDurationMs } from "@/lib/activity-stream"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "@/lib/toast"
import { cn } from "@/lib/utils"

import { CHANGE_ICON, KindGlyph, RunHistoryStrip, SOURCE_ICON, StatusGlyph } from "./activity-feed-table"

export interface ActivityPeekProps {
  workspaceId: string
  item: FeedItem | null
  onClose: () => void
  onPrev?: () => void
  onNext?: () => void
  now: number
  role: string | null
  routineName: (slug: string) => string | undefined
  /** The work's own page: the routine run, or the agent work. */
  onOpenFull: (item: WorkItem) => void
  /** Another run in this Peek: a run of a bundle, a bar of the history. */
  onPickRun: (id: string) => void
  /** Activity narrowed to one routine or one issue. */
  onShowRoutine: (slug: string, oneByOne?: boolean) => void
  onShowIssue: (id: string) => void
  /** The change's full journal record. */
  onOpenRecord: (item: ChangeItem) => void
  /** Something changed on the server (stopped, started again). */
  onChanged: () => void
}

/** One Peek for both kinds of row (Harbor 2 POP-04). */
export function ActivityPeek(props: ActivityPeekProps) {
  const { item, onClose, onPrev, onNext } = props
  const open = item != null
  // Keep the last item on screen while the sheet animates out.
  const [kept, setKept] = React.useState<FeedItem | null>(item)
  if (item && item !== kept) setKept(item)
  const shown = item ?? kept

  if (!shown) return <Peek open={false} onOpenChange={() => onClose()} title="" />

  if (shown.type === "work") {
    return <WorkPeek {...props} item={shown} open={open} onPrev={onPrev} onNext={onNext} />
  }
  if (shown.type === "story") {
    return <StoryPeek {...props} item={shown} open={open} onPrev={onPrev} onNext={onNext} />
  }
  return <ChangePeek {...props} item={shown} open={open} onPrev={onPrev} onNext={onNext} />
}

/* ------------------------------------------------------------------ *
 *  Pieces
 * ------------------------------------------------------------------ */

function Section({ icon: Icon, title, children }: { icon: LucideIcon; title: React.ReactNode; children: React.ReactNode }) {
  return (
    <section className="grid gap-2">
      <h3 className="flex items-center gap-1.5 text-label font-semibold text-muted-foreground">
        <Icon className="size-3.5" aria-hidden />
        {title}
      </h3>
      {children}
    </section>
  )
}

function LinkButton({ onClick, children }: { onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" onClick={onClick} className="kit-tap inline-flex items-center gap-1 justify-self-start text-label font-medium text-primary hover:underline">
      {children}
      <ChevronRight className="size-3" aria-hidden />
    </button>
  )
}

function ObjectChip({ icon: Icon, label, href, onClick }: { icon: LucideIcon; label: string; href?: string; onClick?: () => void }) {
  const cls = "kit-tap inline-flex max-w-full items-center gap-1.5 rounded-md border border-border bg-card px-2 py-1 text-label text-foreground hover:border-line-strong"
  const body = (
    <>
      <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      <span className="truncate">{label}</span>
    </>
  )
  if (href) {
    return (
      <Link href={href} className={cls}>
        {body}
      </Link>
    )
  }
  // Nothing to open (a page: Activity has no page filter yet): a label, not a dead button.
  if (!onClick) return <span className={cls}>{body}</span>
  return (
    <button type="button" onClick={onClick} className={cls}>
      {body}
    </button>
  )
}

interface ChainStep {
  icon: LucideIcon
  title: string
  sub?: string
  status?: WorkItem["status"]
  planned?: boolean
}

/** "How it connects": cause → the work → what it caused, in order. */
function HowItConnects({ steps }: { steps: ChainStep[] }) {
  return (
    <ol className="grid">
      {steps.map((s, i) => {
        const Icon = s.icon
        return (
          <li key={i} className="relative flex items-start gap-2.5 py-1.5 pl-0.5">
            {i < steps.length - 1 && <span aria-hidden className="absolute bottom-[-6px] left-[9px] top-6 w-px bg-line-strong" />}
            <Icon className="relative z-10 mt-0.5 size-3.5 shrink-0 bg-card text-muted-foreground" aria-hidden />
            <span className="min-w-0 flex-1">
              <b className={cn("block truncate text-body font-medium", s.planned && "text-muted-foreground")}>{s.title}</b>
              {s.sub && <small className="block truncate text-label text-muted-foreground">{s.sub}</small>}
            </span>
            {s.status && <StatusGlyph status={s.status} size="sm" />}
          </li>
        )
      })}
    </ol>
  )
}

/* ------------------------------------------------------------------ *
 *  Work
 * ------------------------------------------------------------------ */

function fmtFull(ms: number) {
  return new Date(ms).toLocaleString("en-GB", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hour12: false })
}

function WorkPeek({
  workspaceId,
  item,
  open,
  onClose,
  onPrev,
  onNext,
  now,
  role,
  routineName,
  onOpenFull,
  onPickRun,
  onShowRoutine,
  onShowIssue,
  onChanged,
}: ActivityPeekProps & { item: WorkItem; open: boolean }) {
  const c = item.chain
  const rname = item.routineSlug ? routineName(item.routineSlug) ?? item.routineSlug : undefined
  const isRoutineRun = c.kind !== "assignment" && !!item.routineSlug
  const { records } = usePipelineRunRecords(workspaceId, open && item.routineSlug ? item.routineSlug : null)
  const history = React.useMemo(() => runHistory(records, item.runs ? null : item.id), [records, item.id, item.runs])
  const [confirm, setConfirm] = React.useState<"stop" | "again" | null>(null)
  const SourceIcon = SOURCE_ICON[item.source]

  // The server's gates: cancel is OWNER/ADMIN, replay MANAGER+.
  const can = runActions(item.active ? "running" : "completed", role)
  const canStop = isRoutineRun && !item.runs && item.active && can.stop
  const canAgain = isRoutineRun && !item.runs && !item.active && can.retry

  const steps: ChainStep[] = [
    { icon: SourceIcon, title: c.started_by?.trim() || WORK_SOURCE_LABEL[item.source], sub: item.origin },
    {
      icon: isRoutineRun ? Workflow : Bot,
      title: item.runs ? `${rname} · ${item.runs.length} runs` : isRoutineRun ? `${item.title} · ${workflowHandle(item.id)}` : item.title,
      sub: c.runs > 1 ? `${c.runs} runs in this chain` : WORK_STATUS_LABEL[item.status],
      status: item.status,
    },
    ...item.agents.map((a) => ({ icon: Bot, title: `${a.name} worked on it` })),
    ...(c.issues ?? []).map((i) => ({
      icon: CircleDot,
      title: [i.identifier, i.title].filter(Boolean).join(" ") || i.id,
      sub: i.created ? "created by this work" : "changed by this work",
    })),
  ]

  async function stop() {
    const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/runs/${encodeURIComponent(item.id)}/cancel`, { method: "POST" })
    if (!res.ok) {
      toast.error(`Could not stop it: HTTP ${res.status}`)
      return
    }
    toast.success("Stopping — the run halts before its next step")
    onChanged()
  }

  async function again() {
    const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/runs/${encodeURIComponent(item.id)}/replay`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    })
    const body = (await res.json().catch(() => ({}))) as { run_id?: string; error?: string }
    if (!res.ok) {
      toast.error(`Could not start it again: ${body.error || `HTTP ${res.status}`}`)
      return
    }
    toast.success("Started again with the same input")
    onChanged()
    if (body.run_id) onPickRun(body.run_id)
  }

  const firstIssue = (c.issues ?? []).find((i) => i.identifier)

  const actionButtons = (
    <>
            {firstIssue && (
              <Button size="sm" variant="outline" asChild>
                <Link href={`/issues/${encodeURIComponent(firstIssue.identifier!)}`}>Open issue</Link>
              </Button>
            )}
            <span className="flex-1" />
            {canStop && (
              <Button size="sm" variant="ghost" onClick={() => setConfirm("stop")}>
                <Square className="size-3.5" aria-hidden />
                Stop
              </Button>
            )}
            {canAgain && (
              <Button size="sm" variant="ghost" onClick={() => setConfirm("again")}>
                <RotateCcw className="size-3.5" aria-hidden />
                Run again
              </Button>
            )}
    </>
  )
  const confirms = (
    <>
      <ConfirmDialog
        open={confirm === "stop"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={`Stop ${item.title}?`}
        description="The run halts before its next step."
        consequences={[
          { tone: "kept", text: "Steps that already finished keep their results." },
          { tone: "lost", text: "Steps that have not started will not run." },
        ]}
        confirmLabel="Stop run"
        destructive
        onConfirm={stop}
      />
      <ConfirmDialog
        open={confirm === "again"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={`Run ${item.title} again?`}
        description="A new run starts with the same input as this one."
        consequences={[{ tone: "kept", text: "This run and its results stay as they are." }]}
        confirmLabel="Run again"
        onConfirm={again}
      />
    </>
  )
  const sections = (
    <>
          {item.status === "waiting" && (
            <div className="flex items-start gap-2 rounded-lg bg-chip-warn-bg px-3 py-2.5 text-body text-chip-warn-fg">
              <Hand className="mt-0.5 size-3.5 shrink-0" aria-hidden />
              <span>
                <b className="block font-semibold">Waiting for a person</b>
                Open the run to answer or decide; the work continues after that.
              </span>
            </div>
          )}

          <Section icon={Layers} title="How it connects">
            <HowItConnects steps={steps} />
          </Section>

          <Section icon={Info} title="Details">
            <div>
              {item.agents.length > 0 && <PropertyRow label="Done by">{item.agents.map((a) => a.name).join(" · ")}</PropertyRow>}
              <PropertyRow label="Started">
                {fmtFull(item.ts)} · {WORK_SOURCE_LABEL[item.source]}
              </PropertyRow>
              <PropertyRow label="Took">
                {item.durationMs == null ? (item.active ? "still running" : "—") : formatDurationMs(item.durationMs)}
                {item.runs && <span className="text-muted-foreground"> on average</span>}
              </PropertyRow>
              {c.runs > 1 && !item.runs && (
                <PropertyRow label="Runs">
                  {c.runs}
                  {c.failed_runs > 0 && <span className="text-destructive"> · {c.failed_runs} could not finish</span>}
                </PropertyRow>
              )}
            </div>
          </Section>

          {item.runs && (
            <Section icon={List} title={`Runs in this group · ${item.runs.length}`}>
              <div className="grid gap-0.5">
                {item.runs.slice(0, 8).map((r) => (
                  <button
                    key={r.id}
                    type="button"
                    onClick={() => onPickRun(r.id)}
                    className="kit-tap flex items-center gap-2 rounded-md px-1.5 py-1 text-left hover:bg-muted"
                  >
                    <StatusGlyph status={r.status} size="sm" />
                    <span className="min-w-0 flex-1">
                      <b className="block truncate text-body font-medium">{workflowHandle(r.id)}</b>
                      <small className="block truncate text-label text-muted-foreground">
                        {whenLabel(r.ts, now)} · {r.durationMs == null ? "—" : formatDurationMs(r.durationMs)}
                      </small>
                    </span>
                  </button>
                ))}
              </div>
              {item.runs.length > 8 && <LinkButton onClick={() => onShowRoutine(item.routineSlug!, true)}>All runs of {rname}, one by one</LinkButton>}
            </Section>
          )}

          {item.routineSlug && history.bars.length > 0 && (
            <Section icon={BarChart3} title={`Earlier runs of ${rname}`}>
              <RunHistoryStrip
                bars={history.bars.map((b) => ({
                  id: b.id,
                  tone: runTone(b.status),
                  durationMs: b.durationMs,
                  current: b.current,
                  label: `${fmtFull(Date.parse(b.startedAt))} · ${b.status}`,
                }))}
                onPick={onPickRun}
              />
              <div className="flex items-center justify-between text-label text-muted-foreground">
                <span>
                  {history.successPct != null && `${history.successPct} % completed`}
                  {history.medianMs != null && ` · median ${formatDurationMs(history.medianMs)}`}
                </span>
                <LinkButton onClick={() => onShowRoutine(item.routineSlug!)}>All runs</LinkButton>
              </div>
            </Section>
          )}

          {(item.routineSlug || (c.issues ?? []).length > 0) && (
            <Section icon={Link2} title="Related">
              <div className="flex flex-wrap gap-1.5">
                {item.routineSlug && <ObjectChip icon={Workflow} label={rname!} href={routineViewHref(item.routineSlug, "definition")} />}
                {(c.issues ?? []).map((i) =>
                  i.identifier ? (
                    <ObjectChip key={i.id} icon={CircleDot} label={i.identifier} href={`/issues/${encodeURIComponent(i.identifier)}`} />
                  ) : (
                    <ObjectChip key={i.id} icon={CircleDot} label={i.title || i.id} onClick={() => onShowIssue(i.id)} />
                  ),
                )}
              </div>
              {item.routineSlug ? (
                <LinkButton onClick={() => onShowRoutine(item.routineSlug!)}>Everything about {rname}</LinkButton>
              ) : (
                c.issues?.[0] && <LinkButton onClick={() => onShowIssue(c.issues![0].id)}>Everything about {c.issues[0].identifier || c.issues[0].title || "this issue"}</LinkButton>
              )}
            </Section>
          )}
    </>
  )

  // RT-07: one routine run is the routine run peek (version, cost, J / K over
  // the routine's runs); Activity's connections, history and actions ride in it.
  const record = isRoutineRun && !item.runs ? records.find((r) => r.id === item.id) : undefined
  if (record) {
    return (
      <>
        <RoutineRunPeek
          workspaceId={workspaceId}
          open={open}
          runs={records}
          runId={item.id}
          subtitle="Routine run"
          routineName={() => rname}
          onChange={onPickRun}
          onClose={onClose}
          onOpen={() => onOpenFull(item)}
          onPrev={onPrev}
          onNext={onNext}
          footerExtra={
            <>
              {firstIssue && (
                <Button size="sm" variant="outline" asChild>
                  <Link href={`/issues/${encodeURIComponent(firstIssue.identifier!)}`}>Open issue</Link>
                </Button>
              )}
              <span className="flex-1" />
              {canStop && (
                <Button size="sm" variant="ghost" onClick={() => setConfirm("stop")}>
                  <Square className="size-3.5" aria-hidden />
                  Stop
                </Button>
              )}
              {canAgain && (
                <Button size="sm" variant="ghost" onClick={() => setConfirm("again")}>
                  <RotateCcw className="size-3.5" aria-hidden />
                  Run again
                </Button>
              )}
            </>
          }
          extra={
            <div className="grid gap-4" data-testid="activity-peek-work">
              {sections}
            </div>
          }
        />
        {confirms}
      </>
    )
  }

  return (
    <>
      <Peek
        open={open}
        onOpenChange={(o) => !o && onClose()}
        title={item.title}
        subtitle={item.runs ? `${item.runs.length} runs, grouped` : isRoutineRun ? "Routine run" : c.kind === "assignment" ? "Agent work" : "Work"}
        onPrev={onPrev}
        onNext={onNext}
        onOpenFull={item.runs ? undefined : () => onOpenFull(item)}
        footer={
          <div className="flex flex-wrap items-center gap-2">
            {item.runs ? (
              <Button size="sm" onClick={() => onShowRoutine(item.routineSlug!, true)}>
                All runs, one by one
              </Button>
            ) : (
              <Button size="sm" onClick={() => onOpenFull(item)}>
                <ExternalLink className="size-3.5" aria-hidden />
                {isRoutineRun ? "Open run" : "Open work"}
              </Button>
            )}
            {actionButtons}
          </div>
        }
      >
        <div className="grid gap-4" data-testid="activity-peek-work">
          <div className="flex flex-wrap items-center gap-2">
            <StatusPill tone={WORK_STATUS_TONE[item.status]} label={WORK_STATUS_LABEL[item.status]} live={item.status === "running"} />
            <span className="text-label text-muted-foreground">
              {whenLabel(item.ts, now)} · {item.origin}
            </span>
          </div>
          <p className="text-body text-foreground/80">{workflowSentence(c, rname)}</p>

          {sections}
        </div>
      </Peek>
      {confirms}
    </>
  )
}

/* ------------------------------------------------------------------ *
 *  Change
 * ------------------------------------------------------------------ */

function ChangePeek({
  item,
  open,
  onClose,
  onPrev,
  onNext,
  now,
  routineName,
  onShowRoutine,
  onShowIssue,
  onOpenRecord,
}: ActivityPeekProps & { item: ChangeItem; open: boolean }) {
  const KindIcon = CHANGE_ICON[item.kind]
  const [from, to] = item.diff ? item.diff.split(" → ") : []
  const rname = item.routineSlug ? routineName(item.routineSlug) ?? item.routineSlug : undefined
  return (
    <Peek
      open={open}
      onOpenChange={(o) => !o && onClose()}
      title={item.object}
      subtitle={CHANGE_KIND_LABEL[item.kind]}
      onPrev={onPrev}
      onNext={onNext}
      onOpenFull={() => onOpenRecord(item)}
      footer={
        <div className="flex flex-wrap items-center gap-2">
          {item.routineSlug ? (
            <Button size="sm" asChild>
              <Link href={routineViewHref(item.routineSlug, "definition")}>Open routine</Link>
            </Button>
          ) : item.missionId ? (
            <Button size="sm" onClick={() => onShowIssue(item.missionId!)}>
              Everything about this issue
            </Button>
          ) : null}
          <Button size="sm" variant="outline" onClick={() => onOpenRecord(item)}>
            Open full record
          </Button>
        </div>
      }
    >
      <div className="grid gap-4" data-testid="activity-peek-change">
        <div className="flex flex-wrap items-center gap-2">
          <StatusPill tone="blue" label={item.action} />
          <span className="text-label text-muted-foreground">
            {whenLabel(item.ts, now)} · {item.who}
          </span>
        </div>
        <p className="text-body text-foreground/80">{changeSentence(item)}</p>

        {from && to && (
          <Section icon={RefreshCw} title="What changed">
            <div className="flex flex-wrap items-center gap-2 font-mono text-label">
              <span className="rounded-md bg-chip-danger-bg px-2 py-1 text-foreground/80 line-through decoration-destructive/60">{from}</span>
              <ChevronRight className="size-3.5 text-muted-foreground" aria-hidden />
              <span className="rounded-md bg-chip-ok-bg px-2 py-1 text-foreground">{to}</span>
            </div>
          </Section>
        )}

        <Section icon={Info} title="Details">
          <div>
            <PropertyRow label="By">{item.who}</PropertyRow>
            <PropertyRow label="When">{fmtFull(item.ts)}</PropertyRow>
            <PropertyRow label="Object">
              <span className="inline-flex items-center gap-1.5">
                <KindIcon className="size-3.5 text-muted-foreground" aria-hidden />
                {item.object}
              </span>
            </PropertyRow>
          </div>
        </Section>

        {(item.missionId || item.routineSlug) && (
          <Section icon={Link2} title="Related">
            <div className="flex flex-wrap gap-1.5">
              {item.routineSlug && <ObjectChip icon={Workflow} label={rname!} href={routineViewHref(item.routineSlug, "definition")} />}
              {item.missionId && <ObjectChip icon={CircleDot} label={item.object} onClick={() => onShowIssue(item.missionId!)} />}
            </div>
            {item.routineSlug ? (
              <LinkButton onClick={() => onShowRoutine(item.routineSlug!)}>Everything about {rname}</LinkButton>
            ) : (
              <LinkButton onClick={() => onShowIssue(item.missionId!)}>Everything about {item.object}</LinkButton>
            )}
          </Section>
        )}
      </div>
    </Peek>
  )
}

/* ------------------------------------------------------------------ *
 *  Story — several updates to one object on one day (v67 `acstory`)
 * ------------------------------------------------------------------ */

const STORY_OBJECT_WORD: Record<StoryItem["objectKind"], string> = { issue: "issue", artifact: "page" }

function StoryStep({ item, now, onPick }: { item: WorkItem | ChangeItem; now: number; onPick: () => void }) {
  return (
    <button
      type="button"
      onClick={onPick}
      className="kit-tap flex w-full min-w-0 items-center gap-2.5 rounded-md px-1.5 py-1.5 text-left hover:bg-muted"
    >
      {item.type === "work" ? <StatusGlyph status={item.status} size="sm" /> : <KindGlyph kind={item.kind} size="sm" />}
      <span className="min-w-0 flex-1">
        <b className="block truncate text-body font-medium">{item.type === "work" ? item.title : item.action}</b>
        <small className="block truncate text-label text-muted-foreground">
          {item.type === "work"
            ? `${WORK_STATUS_LABEL[item.status]} · ${item.agents[0]?.name ?? "An agent"}`
            : [item.diff, item.who].filter(Boolean).join(" · ")}
          {` · ${whenLabel(item.ts, now)}`}
        </small>
      </span>
    </button>
  )
}

function StoryPeek({
  item,
  open,
  onClose,
  onPrev,
  onNext,
  now,
  onPickRun,
  onShowIssue,
}: ActivityPeekProps & { item: StoryItem; open: boolean }) {
  const word = STORY_OBJECT_WORD[item.objectKind]
  const Icon = CHANGE_ICON[item.objectKind]
  return (
    <Peek
      open={open}
      onOpenChange={(o) => !o && onClose()}
      title={item.title}
      subtitle={`${item.items.length} updates`}
      onPrev={onPrev}
      onNext={onNext}
      footer={
        item.missionId ? (
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" onClick={() => onShowIssue(item.missionId!)}>
              Everything about this issue
            </Button>
          </div>
        ) : undefined
      }
    >
      <div className="grid gap-4" data-testid="activity-peek-story">
        <div className="flex flex-wrap items-center gap-2">
          <StatusPill tone="muted" label={dayLabel(item.ts, now)} />
          <span className="text-label text-muted-foreground">
            {whenLabel(item.first, now)} – {whenLabel(item.ts, now)} · {item.who.join(", ")}
          </span>
        </div>
        <p className="text-body text-foreground/80">
          {item.items.length} updates about one {word} on one day, shown as one row so the list stays readable. Turn off Group repeated runs in
          Display to list them one by one.
        </p>

        <Section icon={List} title="What happened, in order">
          <div className="grid">
            {item.items
              .slice()
              .reverse()
              .map((x) => (
                <StoryStep key={x.id} item={x} now={now} onPick={() => onPickRun(x.id)} />
              ))}
          </div>
        </Section>

        <Section icon={Link2} title="Related">
          <div className="flex flex-wrap gap-1.5">
            <ObjectChip icon={Icon} label={item.title} onClick={item.missionId ? () => onShowIssue(item.missionId!) : undefined} />
          </div>
          {item.missionId && <LinkButton onClick={() => onShowIssue(item.missionId!)}>Everything about {item.title}</LinkButton>}
        </Section>
      </div>
    </Peek>
  )
}
