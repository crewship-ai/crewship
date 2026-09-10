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
  GitBranch,
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
import { formatDurationMs } from "@/lib/activity-stream"
import { mapSubSpans } from "@/lib/trace/sub-spans"
import { useWorkspaceAgentDirectory } from "@/hooks/use-workspace-agent-directory"
import { RoutineAgentLink } from "./routine-agent-link"
import { RoutineStepDefinition } from "./routine-step-definition"
import { RoutineRecordedValue, readableFieldName } from "./routine-saved-inputs"
import type { StepExecutionSummary } from "@/hooks/use-run-executions"

// routine-step-spine — the one list of steps, used by the saved recipe and by
// a recorded run.
//
// Why one component: a routine's recipe and one of its runs were two screens
// with two step lists, two step-detail panels and two ways to reach a step's
// output — a map with a dropdown beside it in the run, a disclosure list in
// the recipe. Reading a run therefore meant learning a second layout for the
// same object. Here the run is the recipe with what happened written onto it:
// the same rows, in the same order, gaining a state, a duration and the
// recorded response.
//
// It never invents execution order or success. Rows are listed in the order
// the recipe declares them; dependencies and conditions decide what actually
// runs, and that stays visible on the row. A step with no recorded execution
// says so rather than reading as skipped.

const STEP_VISUALS: Record<string, { icon: LucideIcon; label: string; tone: string }> = {
  agent_run: { icon: Bot, label: "Agent task", tone: "bg-purple/10 text-purple" },
  script: { icon: FileCode2, label: "Run a script", tone: "bg-info/10 text-info" },
  transform: { icon: ArrowLeftRight, label: "Prepare data", tone: "bg-success/10 text-success" },
  http: { icon: Globe, label: "Call a service", tone: "bg-info/10 text-info" },
  wait: { icon: Clock, label: "Wait", tone: "bg-warn/10 text-warn" },
  code: { icon: Braces, label: "Run code", tone: "bg-warn/10 text-warn" },
  notify: { icon: Bell, label: "Send a notification", tone: "bg-purple/10 text-purple" },
  query: { icon: Database, label: "Read stored data", tone: "bg-info/10 text-info" },
  foreach: { icon: Repeat2, label: "For each item", tone: "bg-purple/10 text-purple" },
  call_pipeline: {
    icon: ScrollText,
    label: "Run another routine",
    tone: "bg-purple/10 text-purple",
  },
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
   * detail panel to keep in step with them.
   */
  map?: React.ReactNode | ((ctx: { openStep: (stepId: string) => void }) => React.ReactNode)
  /** Steps shown before "Show all"; the rest stay one click away. */
  initialLimit?: number
  onEdit?: () => void
  /** Overrides the card title. Defaults to the mode's wording. */
  title?: string
}

export function RoutineStepSpine({
  workspaceId,
  definition,
  record,
  map,
  initialLimit = 6,
  onEdit,
  title,
}: RoutineStepSpineProps) {
  const { agents } = useWorkspaceAgentDirectory(workspaceId)
  const [expanded, setExpanded] = React.useState(false)
  const [view, setView] = React.useState<"list" | "map">("list")
  const [openIds, setOpenIds] = React.useState<ReadonlySet<string>>(() => new Set())
  const toggleOpen = React.useCallback((stepId: string, open: boolean) => {
    setOpenIds((previous) => {
      if (previous.has(stepId) === open) return previous
      const next = new Set(previous)
      if (open) next.add(stepId)
      else next.delete(stepId)
      return next
    })
  }, [])
  const openFromMap = React.useCallback((stepId: string) => {
    setOpenIds(new Set([stepId]))
    setExpanded(true)
    setView("list")
  }, [])

  const dsl = isRecord(definition) ? definition : {}
  const steps = React.useMemo(
    () => (Array.isArray(dsl.steps) ? dsl.steps.filter(isRecord) : []),
    [dsl.steps],
  )
  const running = record?.currentStepId ?? null

  const heading = title ?? (record ? "What happened, step by step" : "What this routine does")
  const shown = expanded ? steps : steps.slice(0, initialLimit)

  return (
    <DetailCard
      title={heading}
      icon={List}
      subtitle={
        steps.length ? `${steps.length} ${steps.length === 1 ? "step" : "steps"}` : undefined
      }
      action={
        map && (
          <div className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5">
            {(["list", "map"] as const).map((option) => (
              <button
                key={option}
                type="button"
                onClick={() => setView(option)}
                aria-pressed={view === option}
                className={cn(
                  "inline-flex items-center gap-1.5 rounded px-2 py-1 text-[11px] font-medium capitalize transition-colors",
                  view === option
                    ? "bg-primary/15 text-primary"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {option === "list" ? (
                  <List className="h-3 w-3" aria-hidden="true" />
                ) : (
                  <Network className="h-3 w-3" aria-hidden="true" />
                )}
                {option}
              </button>
            ))}
          </div>
        )
      }
      bare={view === "map"}
    >
      {view === "map" ? (
        typeof map === "function" ? (
          map({ openStep: openFromMap })
        ) : (
          map
        )
      ) : !steps.length ? (
        <p className="text-sm text-muted-foreground">This recipe has no steps yet.</p>
      ) : (
        <>
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
          {shown.map((step, index) => (
            <SpineRow
              key={String(step.id || index)}
              step={step}
              index={index}
              workspaceId={workspaceId}
              agents={agents}
              record={record}
              running={running}
              onEdit={onEdit}
              open={openIds.has(String(step.id ?? index))}
              onOpenChange={toggleOpen}
            />
          ))}
          {steps.length > initialLimit && (
            <button
              type="button"
              onClick={() => setExpanded((value) => !value)}
              className="mt-3 text-xs text-primary hover:underline"
            >
              {expanded ? "Show fewer steps" : `Show all ${steps.length} steps`}
            </button>
          )}
          {record?.executionsTruncated && (
            <p className="mt-3 text-[11px] text-muted-foreground">
              Only the first recorded executions were loaded for this run. Later attempts are not
              shown here.
              {record.onLoadMoreExecutions && (
                <button
                  type="button"
                  className="ml-2 underline"
                  onClick={record.onLoadMoreExecutions}
                >
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

function SpineRow({
  step,
  index,
  workspaceId,
  agents,
  record,
  running,
  onEdit,
  open,
  onOpenChange,
}: {
  step: Record<string, unknown>
  index: number
  workspaceId?: string
  agents: AgentDirectory
  record: RoutineStepSpineProps["record"]
  running: string | null
  onEdit?: () => void
  open: boolean
  onOpenChange: (stepId: string, open: boolean) => void
}) {
  const stepId = String(step.id ?? index)
  const description = describeStep(step, index + 1)
  const visual = STEP_VISUALS[String(step.type)] ?? {
    icon: Cog,
    label: "Step",
    tone: "bg-muted text-muted-foreground",
  }
  const Icon = visual.icon
  const agent = agents?.find((a) => a.slug === step.agent_slug)
  const name =
    typeof step.name === "string" && step.name
      ? step.name
      : readableFieldName(String(step.id ?? description.title))
  const action = description.kind === "unknown" ? visual.label : description.title
  const stored = record?.lookup(stepId)
  const execution = stored?.execution
  const spans = record?.subSpans ? mapSubSpans(record.subSpans[stepId]) : []

  // The right-hand state carries the exception, never the rule. On a run whose
  // every step behaved, a column of ten identical labels is a column of
  // nothing; the reader wants the row that did not. So: the live current step,
  // a real recorded execution, or the fact that a finished step stored no
  // response. Silence means ordinary. Borrowing the run's own status, or
  // calling an unrecorded step skipped, would both be claims the record does
  // not make.
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
        <span
          className={cn("flex shrink-0 items-center gap-1.5 text-[11px]", presentation.className)}
        >
          <span className={cn("h-1.5 w-1.5 rounded-full", presentation.dot)} aria-hidden="true" />
          {presentation.label}
          {execution.durationMs != null && (
            <span className="tabular-nums text-muted-foreground">
              {formatDurationMs(execution.durationMs)}
            </span>
          )}
          {execution.attempts > 1 && (
            <span className="text-muted-foreground">· {execution.attempts} attempts</span>
          )}
        </span>
      )
    } else if (!record.active && record.outputsAvailable && !stored?.hasOutput) {
      state = (
        <span className="shrink-0 text-[11px] text-muted-foreground">No response recorded</span>
      )
    }
  }

  return (
    <details
      open={open}
      onToggle={(event) => onOpenChange(stepId, event.currentTarget.open)}
      className="group border-t border-border/60 py-2.5 first:border-t-0"
    >
      <summary className="flex cursor-pointer list-none items-start gap-3 rounded-lg focus-visible:outline focus-visible:outline-primary">
        <span
          className={cn(
            "relative flex h-9 w-9 shrink-0 items-center justify-center rounded-xl",
            visual.tone,
          )}
          title={visual.label}
          data-step-kind={String(step.type)}
        >
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
          <span className="absolute -bottom-1 -right-1 flex h-4 min-w-4 items-center justify-center rounded-full border border-border bg-card px-0.5 text-[9px] tabular-nums text-muted-foreground">
            {index + 1}
          </span>
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-2">
            <span className="text-[13px] font-medium capitalize text-foreground">{name}</span>
            {Boolean(step.if) && (
              <Pill tone="warn">
                <GitBranch className="h-3 w-3" />
                Only if
              </Pill>
            )}
          </span>
          <span className="mt-0.5 block truncate text-xs text-muted-foreground">
            {action}
            {description.detail ? ` · ${description.detail}` : ""}
          </span>
        </span>
        {/* Hidden on a phone: the row's name and action are what a narrow
            column has room for, and the disclosure carries the same fact. */}
        {state && <span className="hidden shrink-0 sm:inline-flex">{state}</span>}
        <span className="shrink-0 text-xs text-primary group-open:hidden">Details</span>
      </summary>
      <div className="ml-[48px] mt-3 space-y-3">
        {execution?.error && (
          <p
            role="alert"
            className="whitespace-pre-wrap break-words rounded-lg border border-destructive/20 bg-destructive/5 px-3 py-2 text-xs text-destructive"
          >
            {execution.error}
          </p>
        )}
        {record && (
          <section>
            <h4 className="mb-1.5 text-[11px] font-medium text-muted-foreground">
              Recorded response
            </h4>
            {record.outputsAvailable === false ? (
              <p className="text-xs text-destructive">Step responses could not be loaded.</p>
            ) : stored?.hasOutput ? (
              <RoutineRecordedValue value={stored.output} />
            ) : (
              <p className="text-xs text-muted-foreground">
                {record.active
                  ? "No response recorded yet."
                  : "No response was recorded for this step."}
              </p>
            )}
          </section>
        )}
        {spans.length > 0 && (
          <details className="text-xs">
            <summary className="cursor-pointer text-primary">
              Agent and tool activity · {spans.length}
            </summary>
            <div className="mt-2 space-y-2">
              {spans.map((span, position) => (
                <details
                  key={`${span.startedAt}-${span.name}-${position}`}
                  className="rounded-lg border border-border/60 p-2"
                >
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
            <summary className="cursor-pointer text-primary">
              Attempts and invocations · {execution.rows.length}
            </summary>
            <ul className="mt-2 space-y-1.5">
              {execution.rows.map((row) => (
                <li
                  key={row.id}
                  className="flex flex-wrap items-center gap-x-2 text-muted-foreground"
                >
                  <span className="text-foreground">
                    {row.kind === "agent_attempt"
                      ? "Agent invocation"
                      : readableFieldName(row.kind || "Attempt")}{" "}
                    · attempt {row.attempt}
                  </span>
                  <span>{presentStatus(row.status).label}</span>
                  {row.agent_slug && <span>{row.agent_slug}</span>}
                  {row.model && <span>requested {row.model}</span>}
                </li>
              ))}
            </ul>
          </details>
        )}
        {typeof step.agent_slug === "string" && (
          <RoutineAgentLink slug={step.agent_slug} agent={agent} workspaceId={workspaceId} />
        )}
        <details className="text-xs">
          <summary className="cursor-pointer text-primary">
            {record ? "What this step was asked to do" : "Step configuration"}
          </summary>
          <div className="mt-3">
            <RoutineStepDefinition step={step} />
          </div>
        </details>
        {Boolean(step.if) && (
          <p className="break-words text-xs text-muted-foreground">
            Runs only if: {String(step.if)}
          </p>
        )}
        {Array.isArray(step.needs) && step.needs.length > 0 && (
          <p className="text-xs text-muted-foreground">
            Runs after: {step.needs.map(String).join(", ")}
          </p>
        )}
        {onEdit && (
          <button type="button" onClick={onEdit} className="text-xs text-primary hover:underline">
            Edit recipe →
          </button>
        )}
      </div>
    </details>
  )
}
