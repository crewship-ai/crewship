"use client"

// The routine page's Overview (Harbor 2 RT-OVTAB): what needs a person first —
// a decision the latest run waits for, or why the last run could not finish —
// then what the routine does, its steps in one line, the last 20 runs, and on
// the right what starts it, what it runs as and what it asks for.

import * as React from "react"
import Link from "next/link"
import { AlertTriangle, BarChart3, CheckCircle2, ChevronRight, Circle, Info, ListOrdered, Play, RotateCcw, Type, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { DetailCard } from "@/components/ui/detail"
import { cn } from "@/lib/utils"
import { describeStep } from "@/lib/routine-step-describe"
import { describeRoutine } from "@/lib/routine-readable"
import { routineInputSpecs, formatInputDefault } from "@/lib/routine-inputs"
import { formatRoutineTime } from "@/lib/routine-time"
import { lastRunBars, routineActivityHref, runBarTone, type DetailDepth, type RunBarTone, type TriggerRow } from "@/lib/routine-page"
import { usePendingApproval } from "@/hooks/use-pending-approval"
import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"
import { RoutineApprovalBanner } from "./routine-approval-banner"
import { InOneLookCard, lastRunReason } from "./routine-card-detail"
import { nameLookup } from "@/lib/routine-steps-layout"
import { isRecord } from "@/lib/routine-step-describe"
import type { RoutineDetail } from "./routines-detail-panel"

const BAR: Record<RunBarTone, string> = {
  ok: "bg-success",
  bad: "bg-destructive",
  warn: "bg-warn",
  mut: "bg-muted-foreground/40",
  info: "bg-primary",
}

/** "What it does": the description, else the steps in order. Written from the
 *  definition in the browser — never by a model. */
export function whatItDoes(routine: Pick<RoutineDetail, "description" | "definition">): string {
  if (routine.description?.trim()) return routine.description.trim()
  const steps = describeRoutine(routine.definition).steps.map((s) => s.title)
  if (steps.length === 0) return "It has no steps yet."
  if (steps.length === 1) return `${steps[0]}.`
  return `${steps.slice(0, -1).join(", ")}, then ${steps[steps.length - 1]}.`
}

export function RoutineOverviewTab({
  routine,
  workspaceId,
  depth,
  records,
  triggers,
  files,
  canRun,
  onRun,
  onOpenRun,
  onTab,
  top,
}: {
  routine: RoutineDetail
  workspaceId: string
  depth: DetailDepth
  records: PipelineRunRecord[]
  triggers: TriggerRow[]
  files: { path: string; language: string }[]
  canRun: boolean
  onRun: () => void
  onOpenRun: (runId: string) => void
  onTab: (tab: "steps" | "triggers" | "settings") => void
  /** Banners the panel owns: a proposal to approve, live runs. */
  top?: React.ReactNode
}) {
  const def = React.useMemo(() => (isRecord(routine.definition) ? routine.definition : {}), [routine.definition])
  const steps = Array.isArray(def.steps) ? def.steps.filter(isRecord) : []
  const nameOf = React.useMemo(() => nameLookup(def), [def])
  const last = records[0] ?? null
  const waiting = records.find((r) => r.status === "waiting") ?? null
  const approval = usePendingApproval(workspaceId, waiting?.id ?? null)
  const failed = last && runBarTone(last).tone === "bad" ? last : null
  const published = (routine.head_version ?? 0) > 0 && !routine.draft_only
  const fresh = published && records.length === 0 && (routine.invocation_count ?? 0) === 0
  const bars = lastRunBars(records)
  const completed = records.slice(0, 20).filter((r) => runBarTone(r).tone === "ok").length
  const inputs = routineInputSpecs(def)
  const schedules = triggers.filter((t) => t.kind === "schedule")
  const nextRun = schedules.map((t) => t.detail.match(/next ([^·]+)/)?.[1]?.trim()).find(Boolean)
  const cap = typeof def.max_cost_usd === "number" && def.max_cost_usd > 0 ? `$${def.max_cost_usd.toFixed(2)} per run` : "no cost cap"
  const concurrency = typeof def.max_concurrent === "number" && def.max_concurrent > 1 ? `${def.max_concurrent} at a time` : typeof def.concurrency_key === "string" && def.concurrency_key ? "1 at a time · queue if busy" : "no overlap limit"
  const agent = routine.manifest?.agents?.[0]

  return (
    <div className="flex flex-col gap-4">
      {top}
      {approval.waitpoint && (
        <div className="flex flex-col gap-2" data-testid="routine-decision">
          <RoutineApprovalBanner waitpoint={approval.waitpoint} deciding={approval.deciding} onDecide={approval.decide} />
          {waiting && (
            <Button type="button" variant="outline" size="sm" className="w-fit" onClick={() => onOpenRun(waiting.id)}>
              Open run
            </Button>
          )}
        </div>
      )}
      {failed && !approval.waitpoint && (
        <FailureBlock run={failed} steps={steps.map((s) => ({ id: String(s.id), title: nameOf(String(s.id)) }))} reason={lastRunReason(failed, nameOf)} canRun={canRun} onRun={onRun} onOpen={() => onOpenRun(failed.id)} />
      )}
      {fresh && !routine.draft && (
        <section className="flex flex-col gap-3 rounded-lg border border-success/25 bg-success/[0.06] px-4 py-3 sm:flex-row sm:items-center">
          <CheckCircle2 className="hidden h-4 w-4 shrink-0 text-success sm:block" aria-hidden />
          <div className="min-w-0 flex-1">
            <p className="text-sm font-medium">Live · no runs yet</p>
            <p className="text-xs text-muted-foreground">{nextRun ? `First run ${nextRun}. Run it now to see a real result before then.` : "It runs when someone presses Run."}</p>
          </div>
          {canRun && (
            <Button type="button" variant="outline" size="sm" onClick={onRun} className="gap-1.5 coarse:h-11">
              <Play className="h-3.5 w-3.5" aria-hidden />
              Run it now
            </Button>
          )}
        </section>
      )}

      <section className="flex flex-col gap-1" aria-labelledby="routine-what-it-does">
        <h3 id="routine-what-it-does" className="type-section text-primary-hover">What it does</h3>
        <p className="max-w-prose text-sm leading-relaxed" data-testid="routine-what-it-does">{whatItDoes(routine)}</p>
        <p className="text-xs text-muted-foreground-soft">Written from the definition, not by a model.</p>
      </section>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-4">
          <DetailCard
            title="Steps"
            icon={ListOrdered}
            subtitle={String(steps.length)}
            action={
              <Button type="button" variant="ghost" size="sm" onClick={() => onTab("steps")} className="h-7 text-xs">
                Open steps
              </Button>
            }
          >
            {steps.length === 0 ? (
              <p className="text-xs text-muted-foreground">No steps yet.</p>
            ) : (
              <ol className="flex flex-wrap items-center gap-1.5" data-testid="routine-step-line">
                {steps.map((s, i) => {
                  const d = describeStep(s, i + 1)
                  return (
                    <li key={String(s.id)} className="flex items-center gap-1.5">
                      {i > 0 && <ChevronRight className="h-3 w-3 text-muted-foreground-soft" aria-hidden />}
                      <span className="inline-flex items-center gap-1.5 rounded-md border border-hairline bg-foreground/[0.03] px-2 py-1 text-xs">
                        <span className="font-mono text-muted-foreground">{i + 1}</span>
                        {d.title}
                      </span>
                    </li>
                  )
                })}
              </ol>
            )}
          </DetailCard>

          <DetailCard
            title={records.length ? "Last 20 runs" : "Runs"}
            icon={BarChart3}
            action={
              <Link href={routineActivityHref(routine.slug)} className="inline-flex items-center gap-1 text-xs text-primary hover:underline">
                All runs in Activity
                <ChevronRight className="h-3 w-3" aria-hidden />
              </Link>
            }
          >
            {records.length === 0 ? (
              <p className="text-xs text-muted-foreground">No runs yet.</p>
            ) : (
              <div className="flex flex-col gap-2">
                <div className="flex h-12 items-end gap-1" role="list" aria-label="Last runs, oldest first" data-testid="routine-run-bars">
                  {bars.map((b) => (
                    <button
                      key={b.id}
                      type="button"
                      role="listitem"
                      title={b.label}
                      aria-label={`Run ${b.id.slice(0, 8)} · ${b.label}`}
                      onClick={() => onOpenRun(b.id)}
                      className={cn("min-w-1.5 flex-1 rounded-sm transition-opacity hover:opacity-80", BAR[b.tone], b.tone === "mut" ? "h-1/3" : "h-full")}
                    />
                  ))}
                </div>
                <p className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
                  <span>{bars.length} {bars.length === 1 ? "run" : "runs"}</span>
                  <span>{completed} completed</span>
                  {last?.started_at && <span>last {formatRoutineTime(last.started_at)}</span>}
                </p>
                <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground-soft">
                  {(["ok", "mut", "warn", "bad"] as const).map((t) => (
                    <span key={t} className="inline-flex items-center gap-1">
                      <i className={cn("inline-block h-2 w-2 rounded-sm", BAR[t])} aria-hidden />
                      {{ ok: "Completed", mut: "Nothing changed", warn: "Waiting", bad: "Could not finish" }[t]}
                    </span>
                  ))}
                </p>
              </div>
            )}
          </DetailCard>

          <InOneLookCard routine={routine} files={files} />
        </div>

        <div className="flex min-w-0 flex-col gap-4">
          <DetailCard title="In one look" icon={Info} data-testid="routine-at-a-glance">
            <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-2 text-xs">
              <dt className="text-muted-foreground">Triggers</dt>
              <dd className="flex flex-wrap gap-1">
                {triggers.map((t) => (
                  <button key={t.key} type="button" onClick={() => onTab("triggers")} className="rounded-md border border-hairline px-1.5 py-0.5 hover:border-primary/40">
                    {t.kind === "manual" ? "By hand" : t.kind === "chat" ? t.title : t.kindLabel}
                  </button>
                ))}
              </dd>
              <dt className="text-muted-foreground">Next run</dt>
              <dd className="font-mono">{nextRun ?? "—"}</dd>
              <dt className="text-muted-foreground">Runs as</dt>
              <dd>{agent ? `${agent} · the crew's credentials` : "the crew's agents and credentials"}</dd>
              <dt className="text-muted-foreground">Limits</dt>
              <dd>
                {cap} · {concurrency}
              </dd>
              {depth === "full" && (
                <>
                  <dt className="text-muted-foreground">Version</dt>
                  <dd className="font-mono">{published ? `v${routine.head_version}` : "not published"}{routine.draft ? ` · draft r${routine.draft.revision}` : ""}</dd>
                </>
              )}
            </dl>
          </DetailCard>
          {inputs.length > 0 && (
            <DetailCard title="Inputs" icon={Type} action={
              <Button type="button" variant="ghost" size="sm" onClick={() => onTab("settings")} className="h-7 text-xs">Edit</Button>
            }>
              <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-2 text-xs" data-testid="routine-inputs">
                {inputs.map((i) => (
                  <React.Fragment key={i.name}>
                    <dt className="font-mono">{i.name}</dt>
                    <dd className="text-muted-foreground">
                      {[i.label && i.label !== i.name ? i.label : null, i.type || "text", i.required ? "required" : null, i.default !== undefined ? `default ${formatInputDefault(i.default)}` : null].filter(Boolean).join(" · ")}
                    </dd>
                  </React.Fragment>
                ))}
              </dl>
            </DetailCard>
          )}
        </div>
      </div>
    </div>
  )
}

function FailureBlock({
  run,
  steps,
  reason,
  canRun,
  onRun,
  onOpen,
}: {
  run: PipelineRunRecord
  steps: { id: string; title: string }[]
  reason?: string
  canRun: boolean
  onRun: () => void
  onOpen: () => void
}) {
  const at = steps.findIndex((s) => s.id === run.failed_at_step)
  return (
    <section className="flex flex-col gap-3 rounded-lg border border-destructive/30 bg-destructive/[0.06] px-4 py-3" data-testid="routine-failure">
      <header className="flex items-start gap-2">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" aria-hidden />
        <div className="min-w-0">
          <p className="text-sm font-medium">Could not finish</p>
          <p className="text-xs text-muted-foreground">
            Last run · {run.started_at ? formatRoutineTime(run.started_at) : "time unknown"}
            {at >= 0 ? ` · step “${steps[at].title}”` : ""}
          </p>
        </div>
      </header>
      {reason && <p className="text-sm">{reason}</p>}
      {at >= 0 && (
        <ol className="flex flex-wrap items-center gap-1.5 text-xs" aria-label="What ran">
          {steps.map((s, i) => {
            const state = i < at ? "ok" : i === at ? "bad" : "mut"
            const Icon = state === "ok" ? CheckCircle2 : state === "bad" ? X : Circle
            return (
              <li key={s.id} className={cn("inline-flex items-center gap-1", state === "ok" && "text-success", state === "bad" && "text-destructive", state === "mut" && "text-muted-foreground")}>
                <Icon className="h-3 w-3" aria-hidden />
                {s.title}
              </li>
            )
          })}
        </ol>
      )}
      <footer className="flex flex-wrap items-center gap-2">
        <Button type="button" variant="outline" size="sm" onClick={onOpen} className="coarse:h-11">
          Open run
        </Button>
        {canRun && (
          <Button type="button" size="sm" onClick={onRun} className="gap-1.5 coarse:h-11">
            <RotateCcw className="h-3.5 w-3.5" aria-hidden />
            Run again
          </Button>
        )}
      </footer>
    </section>
  )
}
