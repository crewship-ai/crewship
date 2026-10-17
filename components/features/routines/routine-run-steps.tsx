"use client"

// The run page's steps (Harbor 2 RT-RUN, routines.md R13): the steps in order
// with their state, and an inspector for the one selected — Output, Input,
// Log, Details. Desktop: side by side; tablet: the inspector under the list;
// phone: the inspector is its own screen with Back to the steps.
//
// Every fact comes from what the run records: the step outputs (GetRun), the
// execution rows (status, agent, model, attempts, times), the run's inputs and
// the step as the executed recipe defines it. The run log has no step id, so
// the Log tab shows the lines recorded while the step ran (its execution
// window) and says so.

import * as React from "react"
import { ChevronLeft } from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { Segmented } from "@/components/ui/segmented"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { formatDurationMs } from "@/lib/activity-stream"
import { formatRoutineTime } from "@/lib/routine-time"
import { describeStep } from "@/lib/routine-step-describe"
import type { TraceStep } from "@/lib/trace/types"
import type { StepExecutionSummary } from "@/hooks/use-run-executions"
import { RoutineResultContent } from "./routine-result-content"

type Tab = "out" | "in" | "log" | "det"
const TABS: { value: Tab; label: string }[] = [
  { value: "out", label: "Output" },
  { value: "in", label: "Input" },
  { value: "log", label: "Log" },
  { value: "det", label: "Details" },
]

const STATE: Record<string, { tone: "success" | "danger" | "warn" | "blue" | "muted"; label: string }> = {
  completed: { tone: "success", label: "Completed" },
  succeeded: { tone: "success", label: "Completed" },
  failed: { tone: "danger", label: "Could not finish" },
  running: { tone: "blue", label: "Running" },
  waiting: { tone: "warn", label: "Waiting for a person" },
  paused: { tone: "warn", label: "Waiting for a person" },
  cancelled: { tone: "muted", label: "Stopped" },
  skipped: { tone: "muted", label: "Skipped" },
}

interface LogLine {
  ts: string
  level: string
  message: string
}

export interface RoutineRunStepsProps {
  workspaceId: string
  runId: string
  steps: TraceStep[]
  executions: ReadonlyMap<string, StepExecutionSummary>
  outputs?: Record<string, unknown> | null
  runInputs?: Record<string, unknown> | null
  currentStepId?: string | null
  failedStepId?: string | null
  /** The run's limits for Details: the cost cap and how many run at once. */
  limits?: { costCap?: number | null; concurrency?: string | null }
  /** A phone: the inspector replaces the list until Back. */
  phone?: boolean
}

function stepState(s: StepExecutionSummary | undefined, id: string, failedStepId?: string | null) {
  if (!s) return id === failedStepId ? STATE.failed : { tone: "muted" as const, label: "Not run" }
  return STATE[s.latest.status] ?? { tone: "muted" as const, label: s.latest.status }
}

const durationOf = (s?: StepExecutionSummary) => {
  if (!s) return null
  if (s.durationMs != null) return s.durationMs
  const a = Date.parse(s.latest.started_at)
  const b = Date.parse(s.latest.ended_at)
  return Number.isFinite(a) && Number.isFinite(b) && b >= a ? b - a : null
}

const pretty = (v: unknown) => {
  if (typeof v === "string") {
    try {
      return JSON.stringify(JSON.parse(v), null, 2)
    } catch {
      return v
    }
  }
  return JSON.stringify(v, null, 2)
}

/** The step's own settings, without its id and type, as the recipe defines it. */
function stepConfig(step: TraceStep): Record<string, unknown> {
  const { id: _id, type: _type, ...rest } = step as TraceStep & Record<string, unknown>
  return rest
}

export function RoutineRunSteps({ workspaceId, runId, steps, executions, outputs, runInputs, currentStepId, failedStepId, limits, phone = false }: RoutineRunStepsProps) {
  const initial = phone ? null : (steps.find((s) => s.id === (failedStepId || currentStepId))?.id ?? steps[0]?.id ?? null)
  const [selected, setSelected] = React.useState<string | null>(initial)
  const [tab, setTab] = React.useState<Tab>("out")
  const [log, setLog] = React.useState<{ lines: LogLine[] | null; error: boolean }>({ lines: null, error: false })
  const logRequested = React.useRef(false)

  React.useEffect(() => {
    if (tab !== "log" || logRequested.current) return
    logRequested.current = true
    const controller = new AbortController()
    apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs/${encodeURIComponent(runId)}/logs?limit=1000`, { signal: controller.signal })
      .then(async (res) => {
        if (!res.ok) throw new Error("logs")
        const body = (await res.json()) as unknown
        setLog({ lines: Array.isArray(body) ? (body as LogLine[]) : [], error: false })
      })
      .catch(() => {
        if (!controller.signal.aborted) setLog({ lines: null, error: true })
      })
  }, [tab, workspaceId, runId])

  const step = steps.find((s) => s.id === selected) ?? null
  const exec = step ? executions.get(step.id) : undefined
  const titleOf = (s: TraceStep, i: number) => describeStep(s, i + 1).title

  const list = (
    <ol aria-label="Steps of this run" className="flex flex-col gap-1">
      {steps.map((s, i) => {
        const e = executions.get(s.id)
        const st = stepState(e, s.id, failedStepId)
        const ms = durationOf(e)
        const out = outputs && Object.hasOwn(outputs, s.id) ? String(outputs[s.id] ?? "").split(/\r?\n/)[0].slice(0, 80) : ""
        return (
          <li key={s.id}>
            <button
              type="button"
              aria-current={selected === s.id ? "true" : undefined}
              onClick={() => setSelected(s.id)}
              className={cn(
                "flex w-full items-center gap-3 rounded-md border border-transparent px-3 py-2 text-left transition-colors hover:bg-foreground/[0.03] coarse:min-h-12",
                selected === s.id && "border-hairline bg-foreground/[0.04]",
              )}
            >
              <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{i + 1}</span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-body font-medium text-foreground">{titleOf(s, i)}</span>
                <span className="block truncate text-label text-muted-foreground">
                  <span className="font-mono">{s.id}</span>
                  {out ? ` · ${out}` : e?.error ? ` · ${e.error.split(/\r?\n/)[0]}` : ""}
                </span>
              </span>
              <StatusPill tone={st.tone} label={st.label} />
              <span className="w-14 shrink-0 text-right font-mono text-label tabular-nums text-muted-foreground">{ms != null ? formatDurationMs(ms) : ""}</span>
            </button>
          </li>
        )
      })}
    </ol>
  )

  const lines =
    step && log.lines
      ? log.lines.filter((l) => {
          const t = Date.parse(l.ts)
          const a = exec ? Date.parse(exec.rows[0]?.started_at ?? exec.latest.started_at) : NaN
          const b = exec?.latest.ended_at ? Date.parse(exec.latest.ended_at) : Date.now()
          return Number.isFinite(t) && Number.isFinite(a) && t >= a && t <= b
        })
      : null

  const inspector = step && (
    <section aria-label="Step inspector" className="flex min-w-0 flex-col gap-3 rounded-card border border-hairline bg-card p-3">
      {phone && (
        <Button type="button" variant="ghost" size="sm" className="-ml-1 w-fit gap-1 coarse:h-11" onClick={() => setSelected(null)}>
          <ChevronLeft className="h-3.5 w-3.5" aria-hidden />
          Back to steps
        </Button>
      )}
      <header className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <h3 className="truncate text-body font-semibold text-foreground">{titleOf(step, steps.indexOf(step))}</h3>
          <p className="text-label text-muted-foreground">
            {stepState(exec, step.id, failedStepId).label}
            {durationOf(exec) != null ? ` · ${formatDurationMs(durationOf(exec)!)}` : ""}
          </p>
        </div>
      </header>
      <Segmented<Tab> ariaLabel="Inspect" fill size="xs" value={tab} onChange={setTab} options={TABS} />
      <div className="min-w-0 text-label">
        {tab === "out" &&
          (outputs && Object.hasOwn(outputs, step.id) ? (
            <div className="max-h-[50dvh] overflow-auto break-words">
              <RoutineResultContent output={String(outputs[step.id] ?? "")} />
            </div>
          ) : outputs == null ? (
            <p className="text-muted-foreground">The outputs of this run could not be loaded.</p>
          ) : (
            <div className="flex flex-col gap-2">
              <p className="text-muted-foreground">No response was recorded for this step{exec ? "" : "; it did not run"}.</p>
              {exec?.error && <p className="whitespace-pre-wrap break-words text-destructive">{exec.error}</p>}
            </div>
          ))}
        {tab === "in" && (
          <div className="flex flex-col gap-3">
            <div>
              <p className="mb-1 font-medium text-muted-foreground">Inputs of this run</p>
              <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-md bg-foreground/[0.04] p-2 font-mono">{pretty(runInputs ?? {})}</pre>
            </div>
            <div>
              <p className="mb-1 font-medium text-muted-foreground">What this step was asked to do</p>
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-foreground/[0.04] p-2 font-mono">{pretty(stepConfig(step))}</pre>
            </div>
          </div>
        )}
        {tab === "log" &&
          (log.error ? (
            <p className="text-destructive" role="alert">The run log could not be loaded.</p>
          ) : !lines ? (
            <p className="text-muted-foreground">Loading the log…</p>
          ) : lines.length === 0 ? (
            <p className="text-muted-foreground">No log lines were recorded while this step ran.</p>
          ) : (
            <>
              <p className="mb-1 text-muted-foreground-soft">Lines recorded while this step ran.</p>
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-foreground/[0.04] p-2 font-mono">
                {lines.map((l) => `${new Date(l.ts).toLocaleTimeString(undefined, { hour12: false })} ${l.message}`).join("\n")}
              </pre>
            </>
          ))}
        {tab === "det" && (
          <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-1">
            <dt className="text-muted-foreground">Step</dt>
            <dd className="font-mono">{step.id}</dd>
            <dt className="text-muted-foreground">Kind</dt>
            <dd>{step.type}</dd>
            {(exec?.latest.agent_slug || step.agent_slug) && (
              <>
                <dt className="text-muted-foreground">Agent</dt>
                <dd>{exec?.latest.agent_slug || step.agent_slug}</dd>
              </>
            )}
            {exec?.latest.model && (
              <>
                <dt className="text-muted-foreground">Model</dt>
                <dd className="font-mono">{exec.latest.model}</dd>
              </>
            )}
            <dt className="text-muted-foreground">Attempts</dt>
            <dd className="font-mono">{exec ? exec.attempts : 0}</dd>
            {exec?.latest.started_at && (
              <>
                <dt className="text-muted-foreground">Started</dt>
                <dd className="font-mono">{formatRoutineTime(exec.latest.started_at)}</dd>
              </>
            )}
            {exec?.latest.ended_at && (
              <>
                <dt className="text-muted-foreground">Ended</dt>
                <dd className="font-mono">{formatRoutineTime(exec.latest.ended_at)}</dd>
              </>
            )}
            {exec?.error && (
              <>
                <dt className="text-muted-foreground">Error</dt>
                <dd className="whitespace-pre-wrap break-words">{exec.error}</dd>
              </>
            )}
            {limits?.costCap != null && (
              <>
                <dt className="text-muted-foreground">Cost cap</dt>
                <dd className="font-mono">${limits.costCap.toFixed(2)} per run</dd>
              </>
            )}
            {limits?.concurrency && (
              <>
                <dt className="text-muted-foreground">Concurrency</dt>
                <dd>{limits.concurrency}</dd>
              </>
            )}
          </dl>
        )}
      </div>
    </section>
  )

  if (phone) return <div data-testid="run-steps">{inspector || list}</div>
  return (
    <div data-testid="run-steps" className="grid grid-cols-1 gap-3 xl:grid-cols-[minmax(0,1fr)_minmax(0,26rem)]">
      {list}
      {inspector}
    </div>
  )
}
