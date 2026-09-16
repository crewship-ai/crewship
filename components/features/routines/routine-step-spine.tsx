"use client"

import * as React from "react"
import {
  ArrowLeftRight,
  Bell,
  Bot,
  Braces,
  Clock,
  Cog,
  Database,
  FileCode2,
  Globe,
  List,
  Network,
  PenSquare,
  Repeat2,
  ScrollText,
  type LucideIcon,
} from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { DetailCard, Pill } from "@/components/ui/detail"
import { cn } from "@/lib/utils"
import { describeStep, isRecord } from "@/lib/routine-step-describe"
import {
  layoutRoutineSteps,
  stepChips,
  stepRoleLabel,
  type LayoutRow,
  type RoutineStepsLayout,
  type Step,
} from "@/lib/routine-steps-layout"
import { formatDurationMs } from "@/lib/activity-stream"
import { mapSubSpans } from "@/lib/trace/sub-spans"
import { useWorkspaceAgentDirectory } from "@/hooks/use-workspace-agent-directory"
import { getChatFileIcon } from "@/components/features/chat/chat-tree-row"
import { RoutineAgentLink } from "./routine-agent-link"
import type { RoutineBehavior, RoutineStepBehavior } from "@/lib/routine-behavior"
import { RoutineStepChecks } from "./routine-behavior"
import { RoutineStepDefinition } from "./routine-step-definition"
import { RoutineRecordedValue, readableFieldName } from "./routine-saved-inputs"
import { RoutineGroupedMap } from "./routine-grouped-map"
import type { StepExecutionSummary } from "@/hooks/use-run-executions"

// routine-step-spine — the one list of steps, used by the saved recipe and by
// a recorded run.
//
// Why one component: a routine's recipe and one of its runs were two screens
// with two step lists. Here the run is the recipe with what happened written
// onto it: the same rows, in the same order, gaining a state, a duration and
// the recorded response.
//
// The rows come from lib/routine-steps-layout: phases from `needs`, helper
// transforms folded, a foreach nested, hooks around the run, and — for a
// recipe over 12 steps — collapsed phases that open into groups. Nothing here
// invents execution order or success. Rows are listed in the order the recipe
// declares them; dependencies and conditions decide what actually runs, and
// that stays visible on the row as a chip. A step with no recorded execution
// says so rather than reading as skipped.

const STEP_VISUALS: Record<string, { icon: LucideIcon; label: string; tone: string }> = {
  agent_run: { icon: Bot, label: "Agent", tone: "bg-purple/10 text-purple" },
  script: { icon: FileCode2, label: "Script", tone: "bg-info/10 text-info" },
  transform: { icon: ArrowLeftRight, label: "Prepare data", tone: "bg-success/10 text-success" },
  http: { icon: Globe, label: "Call a service", tone: "bg-info/10 text-info" },
  wait: { icon: Clock, label: "A person", tone: "bg-warn/10 text-warn" },
  code: { icon: Braces, label: "Run code", tone: "bg-warn/10 text-warn" },
  notify: { icon: Bell, label: "Notification", tone: "bg-purple/10 text-purple" },
  query: { icon: Database, label: "Read stored data", tone: "bg-info/10 text-info" },
  foreach: { icon: Repeat2, label: "For each item", tone: "bg-purple/10 text-purple" },
  call_pipeline: { icon: ScrollText, label: "Run another routine", tone: "bg-purple/10 text-purple" },
  crewship: { icon: PenSquare, label: "Crewship action", tone: "bg-warn/10 text-warn" },
}

/** How a recorded status reads on the row. Unknown values keep their own word. */
const STATUS_PRESENTATION: Record<string, { label: string; className: string; dot: string }> = {
  completed: { label: "Done", className: "text-success", dot: "bg-success" },
  succeeded: { label: "Done", className: "text-success", dot: "bg-success" },
  failed: { label: "Failed", className: "text-destructive", dot: "bg-destructive" },
  error: { label: "Failed", className: "text-destructive", dot: "bg-destructive" },
  running: { label: "Running", className: "text-primary", dot: "bg-primary animate-pulse" },
  queued: { label: "Queued", className: "text-muted-foreground", dot: "bg-muted-foreground" },
  waiting: { label: "Waiting", className: "text-warn", dot: "bg-warn" },
  paused: { label: "Waiting", className: "text-warn", dot: "bg-warn" },
  cancelled: { label: "Stopped", className: "text-muted-foreground", dot: "bg-muted-foreground" },
  interrupted: { label: "Interrupted", className: "text-warn", dot: "bg-warn" },
  skipped: { label: "Skipped", className: "text-muted-foreground", dot: "bg-muted-foreground" },
}

const presentStatus = (status: string) =>
  STATUS_PRESENTATION[status.toLowerCase()] ?? {
    label: readableFieldName(status),
    className: "text-muted-foreground",
    dot: "bg-muted-foreground",
  }

export interface StepSpineRecord {
  /** Recorded executions for this step, when the run stored any. */
  execution?: StepExecutionSummary
  /** Whether a final response was stored under this step id. */
  hasOutput: boolean
  output?: unknown
}

export interface RoutineStepSpineProps {
  behavior?: RoutineBehavior
  workspaceId?: string
  /** The recipe to list — the saved one, or the version a run executed. */
  definition: unknown
  /** Absent in recipe mode. Present — even when empty — means "this is a run". */
  record?: {
    lookup: (stepId: string) => StepSpineRecord
    /** false when the step-output blob failed to load, which is not "no output". */
    outputsAvailable: boolean
    /** Raw sub_spans map from the run row. */
    subSpans?: Record<string, unknown>
    failedStepId?: string | null
    currentStepId?: string | null
    active: boolean
    /** Recorded executions could not be read at all. */
    executionsError?: boolean
    /** More executions exist than were fetched. */
    executionsTruncated?: boolean
    onRetryExecutions?: () => void
    onLoadMoreExecutions?: () => void
  }
  /**
   * The graph for this same recipe, rendered when the reader switches to Map.
   *
   * Given a callback so selecting a node returns to the list with that step
   * open: the map is a second view of these rows, not a place with its own
   * detail panel to keep in step with them. A recipe over 12 steps draws its
   * own grouped map instead — one node per group with ×N.
   */
  map?: React.ReactNode | ((ctx: { openStep: (stepId: string) => void }) => React.ReactNode)
  /** Rows shown before "Show all"; the rest stay one click away. */
  initialLimit?: number
  /** Slug of the routine, for the "Change it" hint. */
  slug?: string
  /** Overrides the card title. Defaults to the mode's wording. */
  title?: string
}

export function RoutineStepSpine({
  behavior,
  workspaceId,
  definition,
  record,
  map,
  initialLimit = 9,
  slug,
  title,
}: RoutineStepSpineProps) {
  const { agents } = useWorkspaceAgentDirectory(workspaceId)
  const [expanded, setExpanded] = React.useState(false)
  const [openIds, setOpenIds] = React.useState<ReadonlySet<string>>(() => new Set())
  const [openPhases, setOpenPhases] = React.useState<ReadonlySet<number>>(() => new Set())
  const [openGroups, setOpenGroups] = React.useState<ReadonlySet<string>>(() => new Set())
  const dsl = React.useMemo(() => (isRecord(definition) ? definition : {}), [definition])
  const running = record?.currentStepId ?? null
  const steps = React.useMemo(
    () => (Array.isArray(dsl.steps) ? dsl.steps.filter(isRecord) : []),
    [dsl.steps],
  )
  const importantIds = steps
    .filter((step) => {
      const id = String(step.id)
      return (
        id === record?.failedStepId ||
        (record?.active && id === running) ||
        ["failed", "waiting"].includes(record?.lookup(id).execution?.latest.status ?? "")
      )
    })
    .map((step) => String(step.id))
  const importantKey = JSON.stringify(importantIds)
  // In a run the failed, waiting and current steps are always visible, so
  // the phase and group holding them open on their own.
  const layout = React.useMemo(() => {
    const base = layoutRoutineSteps(dsl)
    const phases = new Set(openPhases)
    const groups = new Set(openGroups)
    const ids: string[] = JSON.parse(importantKey)
    for (const phase of base.phases) {
      if (!phase.collapsed) continue
      for (const group of phase.groups) {
        if (group.steps.some((s) => ids.includes(String(s.id)))) {
          phases.add(phase.level)
          groups.add(group.key)
        }
      }
    }
    return layoutRoutineSteps(dsl, { openPhases: phases, openGroups: groups })
  }, [dsl, openPhases, openGroups, importantKey])
  // A big recipe opens on its map; a run opens on the list, where what
  // happened is written.
  const [view, setView] = React.useState<"list" | "map">(() => (layout.big && !record ? "map" : "list"))
  const toggleOpen = React.useCallback((stepId: string, open: boolean) => {
    setOpenIds((previous) => {
      if (previous.has(stepId) === open) return previous
      const next = new Set(previous)
      if (open) next.add(stepId)
      else next.delete(stepId)
      return next
    })
  }, [])
  const openPhase = React.useCallback((level: number, groupKey?: string) => {
    setOpenPhases((previous) => new Set([...previous, level]))
    if (groupKey) setOpenGroups((previous) => new Set([...previous, groupKey]))
    setExpanded(true)
    setView("list")
  }, [])
  const openFromMap = React.useCallback((stepId: string) => {
    setOpenIds(new Set([stepId]))
    setExpanded(true)
    setView("list")
  }, [])

  const autoOpened = React.useRef(new Set<string>())
  React.useEffect(() => {
    const ids: string[] = JSON.parse(importantKey)
    const newlyImportant = ids.filter((id) => !autoOpened.current.has(id))
    if (!newlyImportant.length) return
    newlyImportant.forEach((id) => autoOpened.current.add(id))
    setOpenIds((previous) => new Set([...previous, ...newlyImportant]))
  }, [importantKey])

  const heading = title ?? (record ? "What happened, step by step" : "How it works")
  // Rows that count against the cap: everything but phase headers. A row that
  // matters in a run — failed, waiting, current — is always shown.
  const rows = layout.rows
  let shownRows = rows
  if (!expanded && !layout.big) {
    let budget = initialLimit
    const kept: LayoutRow[] = []
    for (const row of rows) {
      if (row.kind === "phase") {
        kept.push(row)
        continue
      }
      const important = row.kind === "step" && importantIds.includes(row.id)
      if (budget > 0 || important) {
        kept.push(row)
        if (!important) budget -= 1
      }
    }
    // Drop phase headers left with nothing under them.
    shownRows = kept.filter((row, i) => row.kind !== "phase" || (kept[i + 1] && kept[i + 1].kind !== "phase"))
  }
  const countable = rows.filter((r) => r.kind !== "phase").length
  const shownCountable = shownRows.filter((r) => r.kind !== "phase").length
  const subtitle = steps.length
    ? `${steps.length} ${steps.length === 1 ? "step" : "steps"}${layout.nested ? ` (${steps.length + layout.nested} with nested)` : ""}${layout.hookCount ? ` · ${layout.hookCount} ${layout.hookCount === 1 ? "hook" : "hooks"}` : ""}`
    : undefined
  const mapNode = layout.big ? (
    <RoutineGroupedMap layout={layout} agents={agents} workspaceId={workspaceId} onOpenPhase={openPhase} />
  ) : typeof map === "function" ? (
    map({ openStep: openFromMap })
  ) : (
    map
  )

  return (
    <DetailCard
      title={heading}
      icon={List}
      subtitle={subtitle}
      action={
        (map || layout.big) && (
          <div className="flex items-center gap-2">
            {layout.hasNeeds && (
              <span className="hidden text-[11px] text-muted-foreground lg:inline">
                Phases come from declared dependencies; steps in one phase may run in parallel
              </span>
            )}
            <div className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5">
              {(["list", "map"] as const).map((option) => (
                <button
                  key={option}
                  type="button"
                  onClick={() => setView(option)}
                  aria-pressed={view === option}
                  className={cn(
                    "inline-flex items-center gap-1.5 rounded px-2 py-1 text-[11px] font-medium capitalize transition-colors",
                    view === option ? "bg-primary/15 text-primary" : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {option === "list" ? <List className="h-3 w-3" aria-hidden="true" /> : <Network className="h-3 w-3" aria-hidden="true" />}
                  {option}
                </button>
              ))}
            </div>
          </div>
        )
      }
      bare={view === "map"}
    >
      {view === "map" ? (
        mapNode
      ) : !steps.length ? (
        <p className="text-sm text-muted-foreground">This routine has no steps yet.</p>
      ) : (
        <>
          {layout.hasNeeds && (
            <p className="mb-3 text-xs text-muted-foreground lg:hidden">
              Phases come from declared dependencies; steps in one phase may run in parallel.
            </p>
          )}
          {record?.executionsError && (
            <p role="alert" className="mb-3 text-xs text-destructive">
              Recorded step executions could not be loaded, so per-step state is missing below.{" "}
              {record.onRetryExecutions && (
                <button type="button" className="underline" onClick={record.onRetryExecutions}>
                  Try again
                </button>
              )}
            </p>
          )}
          {shownRows.map((row) => {
            if (row.kind === "phase") {
              if (row.collapsed)
                return (
                  <button
                    key={row.key}
                    type="button"
                    data-testid={`routine-${row.key.replace(":", "-")}`}
                    aria-expanded={row.open}
                    onClick={() =>
                      setOpenPhases((previous) => {
                        const next = new Set(previous)
                        if (next.has(row.level)) next.delete(row.level)
                        else next.add(row.level)
                        return next
                      })
                    }
                    className="flex w-full items-center gap-2 border-t border-border/60 py-2.5 text-left first:border-t-0"
                  >
                    <span className="text-muted-foreground">{row.open ? "▾" : "▸"}</span>
                    <span className="text-[13px] font-medium">{row.label}</span>
                    <span className="text-xs text-muted-foreground">
                      {row.kinds.map(([k, n]) => `${k} ×${n}`).join(" · ")}
                      {row.groups > 1 ? ` · ${row.groups} groups` : ""}
                    </span>
                  </button>
                )
              return (
                <div
                  key={row.key}
                  data-testid={`routine-${row.key.replace(":", "-")}`}
                  className={cn(
                    "flex items-center gap-2 pb-1 pt-2.5 text-[10.5px] font-semibold uppercase tracking-wider text-muted-foreground-soft",
                    row.level !== 0 || row.label !== "Before the run" ? "border-t border-border/60 first:border-t-0" : "",
                  )}
                >
                  {row.label}
                  <span className="h-px flex-1 bg-border/60" aria-hidden="true" />
                </div>
              )
            }
            if (row.kind === "fold")
              return (
                <details key={row.key} data-testid={`routine-fold-${row.level}`} className="border-t border-border/60 py-2.5 first:border-t-0">
                  <summary className="flex cursor-pointer list-none items-start gap-3">
                    <span className={cn("flex h-9 w-9 shrink-0 items-center justify-center rounded-xl", STEP_VISUALS.transform.tone)}>
                      <ArrowLeftRight className="h-4 w-4" aria-hidden="true" />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block text-[13px] font-medium text-muted-foreground">
                        {row.steps.length} data preparations from {row.from}
                      </span>
                      <span className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                        <RoleChip label="Prepare data" tone={STEP_VISUALS.transform.tone} />
                        <span>no effects</span>
                        <span className="text-primary">Show the {row.steps.length}</span>
                      </span>
                    </span>
                  </summary>
                  <ul className="ml-[48px] mt-2 space-y-1 text-xs">
                    {row.steps.map((s, i) => (
                      <li key={String(s.id)} className="flex flex-wrap items-baseline gap-2">
                        <span>{describeStep(s, i + 1).title}</span>
                        <span className="font-mono text-[10px] text-muted-foreground">{String(s.id)}</span>
                      </li>
                    ))}
                  </ul>
                </details>
              )
            if (row.kind === "group")
              return (
                <button
                  key={row.key}
                  type="button"
                  data-testid={`routine-group-${row.key}`}
                  aria-expanded={row.open}
                  onClick={() =>
                    setOpenGroups((previous) => {
                      const key = row.key.replace(/^group:\d+:/, "")
                      const next = new Set(previous)
                      if (next.has(key)) next.delete(key)
                      else next.add(key)
                      return next
                    })
                  }
                  className="flex w-full items-start gap-3 border-t border-border/60 py-2.5 pl-6 text-left"
                >
                  <StepGlyph step={row.steps[0]} agents={agents} workspaceId={workspaceId} />
                  <span className="min-w-0 flex-1">
                    <span className="block text-[13px] font-medium">
                      {row.name} · {row.steps.length} steps
                    </span>
                    <span className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                      <RoleChip label={stepRoleLabel(row.steps[0])} tone={(STEP_VISUALS[String(row.steps[0].type)] ?? STEP_VISUALS.transform).tone} />
                      {row.after.length > 0 && <span>after {row.after.join(", ")}</span>}
                      <span className="text-primary">{row.open ? "Hide" : `Show the ${row.steps.length}`}</span>
                    </span>
                  </span>
                </button>
              )
            return (
              <SpineRow
                key={row.key}
                row={row}
                layout={layout}
                behavior={behavior?.steps.find((s) => s.id === row.step.id)}
                workspaceId={workspaceId}
                agents={agents}
                record={record}
                running={running}
                slug={slug}
                open={openIds.has(row.id)}
                onOpenChange={toggleOpen}
              />
            )
          })}
          {!layout.big && countable > shownCountable && !expanded && (
            <button type="button" onClick={() => setExpanded(true)} className="mt-3 text-xs text-primary hover:underline">
              Show all {countable} rows
            </button>
          )}
          {!layout.big && expanded && countable > initialLimit && (
            <button type="button" onClick={() => setExpanded(false)} className="mt-3 text-xs text-primary hover:underline">
              Show fewer rows
            </button>
          )}
          {record?.executionsTruncated && (
            <p className="mt-3 text-[11px] text-muted-foreground">
              Only the first recorded executions were loaded for this run. Later attempts are not shown here.
              {record.onLoadMoreExecutions && (
                <button type="button" className="ml-2 underline" onClick={record.onLoadMoreExecutions}>
                  Load more executions
                </button>
              )}
            </p>
          )}
        </>
      )}
    </DetailCard>
  )
}

type AgentDirectory = ReturnType<typeof useWorkspaceAgentDirectory>["agents"]

function RoleChip({ label, tone }: { label: string; tone: string }) {
  return <span className={cn("rounded px-1.5 py-px text-[11px] font-medium", tone)}>{label}</span>
}

/** The file chip on a step row: language icon and file name, full path on hover. */
export function StepFileChip({ path }: { path: string }) {
  const name = path.split("/").pop() ?? path
  return (
    <span
      data-testid="routine-step-file"
      title={`${path} on /crew/shared`}
      className="inline-flex items-center gap-1 rounded border border-border/60 px-1.5 py-px font-mono text-[11px]"
    >
      {getChatFileIcon(name, false)}
      {name}
    </span>
  )
}

function StepGlyph({ step, agents, workspaceId, badge }: { step: Step; agents: AgentDirectory; workspaceId?: string; badge?: number }) {
  const visual = STEP_VISUALS[String(step.type)] ?? { icon: Cog, label: "Step", tone: "bg-muted text-muted-foreground" }
  const Icon = visual.icon
  const agent = agents?.find((a) => a.slug === step.agent_slug)
  return (
    <span className={cn("relative flex h-9 w-9 shrink-0 items-center justify-center rounded-xl", visual.tone)} title={visual.label} data-step-kind={String(step.type)}>
      {agent ? (
        <AgentAvatar
          seed={agent.avatar_seed || agent.name}
          style={agent.avatar_style || agent.crew?.avatar_style || undefined}
          agentId={agent.id}
          avatarUrl={agent.avatar_url}
          workspaceId={workspaceId}
          className="h-7 w-7"
          alt=""
        />
      ) : (
        <Icon className="h-4 w-4" aria-hidden="true" />
      )}
      {badge != null && (
        <span className="absolute -bottom-1 -right-1 flex h-4 min-w-4 items-center justify-center rounded-full border border-border bg-card px-0.5 text-[9px] tabular-nums text-muted-foreground">
          {badge}
        </span>
      )}
    </span>
  )
}

function SpineRow({
  row,
  layout,
  behavior,
  workspaceId,
  agents,
  record,
  running,
  slug,
  open,
  onOpenChange,
}: {
  row: Extract<LayoutRow, { kind: "step" }>
  layout: RoutineStepsLayout
  behavior?: RoutineStepBehavior
  workspaceId?: string
  agents: AgentDirectory
  record: RoutineStepSpineProps["record"]
  running: string | null
  slug?: string
  open: boolean
  onOpenChange: (stepId: string, open: boolean) => void
}) {
  const step = row.step
  const stepId = row.id
  const description = describeStep(step, row.position || 1)
  const visual = STEP_VISUALS[String(step.type)] ?? { icon: Cog, label: "Step", tone: "bg-muted text-muted-foreground" }
  const agent = agents?.find((a) => a.slug === step.agent_slug)
  const name = description.title
  const action = description.kind === "unknown" ? visual.label : description.title
  const chips = stepChips(step, layout.nameOf)
  const stored = record?.lookup(stepId)
  const execution = stored?.execution
  const spans = record?.subSpans ? mapSubSpans(record.subSpans[stepId]) : []
  const performer =
    step.type === "agent_run"
      ? agent?.name || (typeof step.agent_slug === "string" ? step.agent_slug : "an agent")
      : step.type === "http"
        ? description.technical?.replace(/^http /, "")
        : step.type === "notify"
          ? description.technical
          : step.type === "crewship"
            ? description.technical
            : step.type === "call_pipeline"
              ? description.technical
              : undefined

  // The right-hand state carries the exception, never the rule. On a run whose
  // every step behaved, a column of ten identical labels is a column of
  // nothing; the reader wants the row that did not.
  let state: React.ReactNode = null
  if (record) {
    if (record.active && running === stepId) {
      state = (
        <span className="flex shrink-0 items-center gap-1.5 text-[11px] text-primary">
          <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-primary" aria-hidden="true" />
          Current step
        </span>
      )
    } else if (execution) {
      const presentation = presentStatus(execution.latest.status)
      state = (
        <span className={cn("flex shrink-0 items-center gap-1.5 text-[11px]", presentation.className)}>
          <span className={cn("h-1.5 w-1.5 rounded-full", presentation.dot)} aria-hidden="true" />
          {presentation.label}
          {execution.durationMs != null && (
            <span className="tabular-nums text-muted-foreground">{formatDurationMs(execution.durationMs)}</span>
          )}
          {execution.attempts > 1 && <span className="text-muted-foreground">· {execution.attempts} attempts</span>}
        </span>
      )
    } else if (!record.active && record.outputsAvailable && !stored?.hasOutput) {
      state = <span className="shrink-0 text-[11px] text-muted-foreground">No response recorded</span>
    }
  }

  return (
    <details
      data-step-id={stepId}
      data-nested={row.nested || undefined}
      data-hook={row.hook}
      open={open}
      onToggle={(event) => onOpenChange(stepId, event.currentTarget.open)}
      className={cn(
        "group border-t border-border/60 py-2.5 first:border-t-0",
        row.nested && "ml-11 border-l-2 border-l-purple/20 border-t-0 pl-3",
        row.hook && "opacity-75",
      )}
    >
      <summary className="flex cursor-pointer list-none items-start gap-3 rounded-lg focus-visible:outline focus-visible:outline-primary">
        <StepGlyph step={step} agents={agents} workspaceId={workspaceId} badge={!layout.hasNeeds && !row.nested && !row.hook ? row.position : undefined} />
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-2">
            <span className="text-[13px] font-medium break-all text-foreground">{name}</span>
            <span className="font-mono text-[10px] text-muted-foreground">{stepId}</span>
          </span>
          <span className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
            <RoleChip label={stepRoleLabel(step)} tone={visual.tone} />
            {performer && <span className="truncate">{performer}</span>}
            {chips.file && <StepFileChip path={chips.file} />}
            {chips.checks && (
              <span className="rounded bg-success/10 px-1.5 py-px text-[11px] text-success" data-testid="routine-step-checks-chip">
                ✓ {chips.checks.count} {chips.checks.grader ? `rules · ${chips.checks.grader}` : "checks"}
              </span>
            )}
            {chips.attempts && <span className="rounded bg-muted px-1.5 py-px font-mono text-[11px]">{chips.attempts} attempts</span>}
            {chips.timeout && <span className="rounded bg-muted px-1.5 py-px font-mono text-[11px]">⏱ {chips.timeout}</span>}
          </span>
          {!chips.file && !performer && description.detail && action !== name && (
            <span className="mt-0.5 block truncate text-xs text-muted-foreground">{description.detail}</span>
          )}
          {row.loop && (
            <span className="mt-1 block text-xs text-muted-foreground" data-testid="routine-step-loop">
              For each of <b className="font-medium text-foreground/85">{row.loop.items || "the items"}</b>
              {row.loop.parallelism > 0 ? ` · ${row.loop.parallelism} at a time` : ""} · {row.loop.count} {row.loop.count === 1 ? "step" : "steps"} per item
            </span>
          )}
          {chips.only && (
            <span className="mt-1 inline-flex items-center gap-1 text-[11px] text-warn" data-testid="routine-step-only">
              <Pill tone="warn">◐ Only when {chips.only}</Pill>
            </span>
          )}
          {chips.after.length > 0 && (
            <span className="mt-1 block text-[11px] text-muted-foreground" data-testid="routine-step-after">
              after {chips.after.map((n, i) => (
                <React.Fragment key={`${n}-${i}`}>
                  {i > 0 && ", "}
                  <b className="font-medium text-foreground/85">{n}</b>
                </React.Fragment>
              ))}
            </span>
          )}
        </span>
        {state && <span className="hidden shrink-0 sm:inline-flex">{state}</span>}
        <span className="shrink-0 text-xs text-primary group-open:hidden">Details</span>
      </summary>
      <div className="ml-[48px] mt-3 space-y-3">
        {execution?.error && (
          <p role="alert" className="whitespace-pre-wrap break-words rounded-lg border border-destructive/20 bg-destructive/5 px-3 py-2 text-xs text-destructive">
            {execution.error}
          </p>
        )}
        {record && (
          <section>
            <h4 className="mb-1.5 text-[11px] font-medium text-muted-foreground">Recorded response</h4>
            {record.outputsAvailable === false ? (
              <p className="text-xs text-destructive">Step responses could not be loaded.</p>
            ) : stored?.hasOutput ? (
              <RoutineRecordedValue value={stored.output} />
            ) : (
              <p className="text-xs text-muted-foreground">
                {record.active ? "No response recorded yet." : "No response was recorded for this step."}
              </p>
            )}
          </section>
        )}
        {spans.length > 0 && (
          <details className="text-xs">
            <summary className="cursor-pointer text-primary">Agent and tool activity · {spans.length}</summary>
            <div className="mt-2 space-y-2">
              {spans.map((span, position) => (
                <details key={`${span.startedAt}-${span.name}-${position}`} className="rounded-lg border border-border/60 p-2">
                  <summary className="cursor-pointer">
                    {span.name} · {span.status}
                  </summary>
                  <p className="mt-2 whitespace-pre-wrap break-words">{span.detail}</p>
                </details>
              ))}
            </div>
          </details>
        )}
        {execution && execution.rows.length > 1 && (
          <details className="text-xs">
            <summary className="cursor-pointer text-primary">Attempts and invocations · {execution.rows.length}</summary>
            <ul className="mt-2 space-y-1.5">
              {execution.rows.map((r) => (
                <li key={r.id} className="flex flex-wrap items-center gap-x-2 text-muted-foreground">
                  <span className="text-foreground">
                    {r.kind === "agent_attempt" ? "Agent invocation" : readableFieldName(r.kind || "Attempt")} · attempt {r.attempt}
                  </span>
                  <span>{presentStatus(r.status).label}</span>
                  {r.agent_slug && <span>{r.agent_slug}</span>}
                  {r.model && <span>requested {r.model}</span>}
                </li>
              ))}
            </ul>
          </details>
        )}
        <RoutineStepChecks behavior={behavior} />
        {typeof step.agent_slug === "string" && <RoutineAgentLink slug={step.agent_slug} agent={agent} workspaceId={workspaceId} />}
        {chips.file && (
          <p className="text-xs text-muted-foreground">
            <span className="font-medium text-foreground">File:</span> <StepFileChip path={chips.file} /> · on the crew share
          </p>
        )}
        <details className="text-xs">
          <summary className="cursor-pointer text-primary">{record ? "What this step was asked to do" : "Step configuration"}</summary>
          <div className="mt-3">
            <RoutineStepDefinition step={step} />
          </div>
        </details>
        {Boolean(step.if) && <p className="break-words text-xs text-muted-foreground">Runs only if: {String(step.if)}</p>}
        {Array.isArray(step.needs) && step.needs.length > 0 && (
          <p className="text-xs text-muted-foreground">Runs after: {step.needs.map(String).join(", ")}</p>
        )}
        {!record && (
          <p className="text-xs text-muted-foreground">
            <span className="font-medium text-foreground">Change it:</span> with the CLI (
            <code className="font-mono">crewship routine draft get {slug ?? "<slug>"}</code>) or by asking the lead agent.
          </p>
        )}
      </div>
    </details>
  )
}
