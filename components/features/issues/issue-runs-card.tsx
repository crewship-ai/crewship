"use client"

import Link from "next/link"
import { ArrowUpRight, BookOpen, Play } from "lucide-react"

import { DetailCard } from "@/components/ui/detail"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { StatusPill } from "@/components/ui/status-pill"
import { entityHref } from "@/lib/entity-links"
import { formatDurationDecimal, relTime } from "@/lib/time"
import { formatStatus, type StatusMeta } from "@/lib/format-status"
import { hasIssueAgentDelegate } from "@/lib/issue-execution"
import type { Mission } from "@/lib/types/mission"

/** `GET /api/v1/crews/{crewId}/issues/{identifier}/runs` (issue_handler_runs.go). */
export interface IssueRun {
  id: string
  /** The journal run id — what the run page and `?trace_id=` take. Absent when the assignment never ran. */
  run_id?: string
  trace_id?: string
  status: string
  agent_id?: string
  agent_slug?: string
  agent_name?: string
  task?: string
  started_at?: string
  ended_at?: string
  duration_ms: number
  result_summary?: string
  error_message?: string
  outcome?: string
  source?: "task" | "mention" | "delegation"
}

const OUTCOMES: Record<string, StatusMeta> = {
  SUCCEEDED: { label: "Reported success", tone: "success" },
  NO_CHANGE: { label: "No changes", tone: "muted" },
  WORK_CREATED: { label: "Work created", tone: "blue" },
  PARTIAL: { label: "Partial result", tone: "warn" },
  NEEDS_HUMAN: { label: "Needs human input", tone: "warn" },
  FAILED: { label: "Failed", tone: "danger" },
  CANCELLED: { label: "Cancelled", tone: "muted" },
}

/** Process completion alone says nothing about the result or human acceptance. */
export function issueRunStatus(run: Pick<IssueRun, "status" | "outcome" | "error_message">): StatusMeta {
  const status = run.status.trim().toUpperCase()
  // Live and stopped processes take precedence over stale reported results.
  if (status !== "COMPLETED") return formatStatus(status)
  const outcome = run.outcome?.trim().toUpperCase() ?? ""
  if (Object.hasOwn(OUTCOMES, outcome)) return OUTCOMES[outcome]
  if (run.error_message?.trim()) return { label: "Run needs attention", tone: "warn" }
  return { label: "Outcome not reported", tone: "muted" }
}

function runTitle(issue: Mission, run: IssueRun): string {
  const task = run.task?.trim()
  // The runs API also returns complete model prompts in `task`. Keep them
  // on the run's technical surface instead of rendering prompt scaffolding.
  if (!task || task.includes("\n") || task.startsWith("[")) return issue.title || "Agent run"
  return task
}

/**
 * Where the runs of an issue point. Pure so the links are testable without
 * a page: the run page by run id, the agent by slug, the issue's journal by
 * identifier, and every run of the issue in Activity.
 */
export function issueRunLinks(issue: Pick<Mission, "id" | "identifier">, run?: Pick<IssueRun, "run_id" | "agent_slug">) {
  return {
    run: run?.run_id ? entityHref({ kind: "run", runId: run.run_id }) : null,
    agent: run?.agent_slug ? entityHref({ kind: "agent", slug: run.agent_slug }) : null,
    journal: entityHref({ kind: "journal", missionId: issue.identifier ?? issue.id }),
    activity: `/activity?mission=${encodeURIComponent(issue.id)}`,
  }
}

/** What the empty card says, by where the issue is in its life. */
export function issueRunsEmptyCopy(issue: Mission): string {
  if (issue.status !== "BACKLOG" && issue.status !== "TODO") return "No agent run recorded against this issue."
  if (hasIssueAgentDelegate(issue)) return "No agent runs yet. Start work to hand this issue to its agent."
  if (issue.owner || issue.assignee_type === "user") return "No agent runs recorded. This issue has a human owner; an agent delegate is optional."
  return "No agent runs recorded. Assign an agent if you want it to run this issue."
}

/**
 * Every run on the issue, newest first — not the latest one alone.
 *
 * The rail used to show `runs[0]` as a tinted card and discard the rest,
 * with one link to a trace page filtered by mission. An issue that was
 * planned, delegated twice and merged has four runs, and the person reading
 * it wants each: who ran, what they were asked, how it went, and a way into
 * the run itself and into its journal. That is the first leg of the one
 * timeline (issue → run → journal) and it was the one that could not be
 * followed.
 */
export function IssueRunsCard({ issue, runs, unavailable = false }: { issue: Mission; runs: IssueRun[]; unavailable?: boolean }) {
  const running = runs.filter((r) => r.status === "RUNNING").length
  const hint =
    runs.length === 0
      ? undefined
      : `${runs.length} ${runs.length === 1 ? "run" : "runs"}${running > 0 ? ` · ${running} running` : ""}`
  const links = issueRunLinks(issue)

  return (
    <DetailCard
      title="Runs"
      icon={Play}
      subtitle={hint}
      action={
        runs.length > 0 ? (
          <Link href={links.activity} className="inline-flex items-center gap-1 text-[11px] text-primary hover:underline">
            All runs in Activity
            <ArrowUpRight className="h-3 w-3" />
          </Link>
        ) : undefined
      }
    >
      {unavailable && runs.length === 0 ? (
        <InlineEmpty icon={Play} text="Could not load the runs — try again above." className="border-destructive/40 text-destructive" />
      ) : runs.length === 0 ? (
        <InlineEmpty icon={Play} text={issueRunsEmptyCopy(issue)} />
      ) : (
        <div className="flex flex-col">
          {runs.map((run) => {
            const l = issueRunLinks(issue, run)
            const result = issueRunStatus(run)
            const source = run.source === "mention" ? "From a message" : run.source === "delegation" ? "Delegated work" : run.source === "task" ? "Planned task" : null
            return (
              <div
                key={run.id}
                data-testid="issue-run-row"
                className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-3 gap-y-1 border-t border-border/50 py-2.5 first:border-t-0 md:grid-cols-[auto_minmax(0,1fr)_auto_auto] md:items-center"
              >
                <AgentAvatar seed={run.agent_id ?? run.agent_name ?? run.id} alt="" className="h-6 w-6 shrink-0 rounded-full" />
                <div className="min-w-0">
                  <p className="truncate text-[12.5px] font-medium">{runTitle(issue, run)}</p>
                  <p className="truncate text-[11px] text-muted-foreground">
                    {l.agent ? (
                      <Link href={l.agent} className="hover:underline">
                        {run.agent_name || run.agent_slug}
                      </Link>
                    ) : (
                      run.agent_name || "—"
                    )}
                    {run.started_at && <> · started {relTime(run.started_at)}</>}
                    {run.duration_ms > 0 && <> · {formatDurationDecimal(run.duration_ms)}</>}
                    {source && <> · {source}</>}
                  </p>
                </div>
                <StatusPill label={result.label} tone={result.tone} live={run.status === "RUNNING"} className="col-start-2 w-fit md:col-start-auto" />
                {l.run ? (
                  <Link
                    href={l.run}
                    className="col-start-2 inline-flex w-fit items-center gap-1 rounded-md border border-border/60 px-2 py-0.5 text-[11px] font-medium text-foreground hover:border-primary md:col-start-auto"
                  >
                    Open run
                    <ArrowUpRight className="h-3 w-3" />
                  </Link>
                ) : (
                  <span className="col-start-2 text-[11px] text-muted-foreground-soft md:col-start-auto" title="This assignment never reached a run">
                    no run
                  </span>
                )}
                <div className="col-start-2 col-end-[-1] min-w-0 text-[11px]">
                  {run.error_message && <p className="break-words text-destructive/90">{run.error_message}</p>}
                  {run.result_summary?.trim() ? (
                    <details className="text-muted-foreground">
                      <summary className="cursor-pointer rounded focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary">Reported summary</summary>
                      <p className="mt-1 whitespace-pre-wrap break-words [overflow-wrap:anywhere]">{run.result_summary}</p>
                    </details>
                  ) : run.status === "COMPLETED" ? (
                    <p className="text-muted-foreground">No summary recorded.</p>
                  ) : null}
                </div>
              </div>
            )
          })}
          <div className="mt-2 flex flex-wrap items-center justify-between gap-2 border-t border-border/50 pt-2 text-[11px] text-muted-foreground">
            <span>Recent runs, newest first. Reported success does not confirm that the issue was accepted.</span>
            <Link href={links.journal} className="inline-flex items-center gap-1 text-primary hover:underline">
              <BookOpen className="h-3 w-3" />
              Journal for {issue.identifier ?? "this issue"}
              <ArrowUpRight className="h-3 w-3" />
            </Link>
          </div>
        </div>
      )}
    </DetailCard>
  )
}
