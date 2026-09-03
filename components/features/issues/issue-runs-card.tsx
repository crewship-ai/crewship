"use client"

import Link from "next/link"
import { ArrowUpRight, BookOpen, Play } from "lucide-react"

import { DetailCard } from "@/components/ui/detail"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { StatusPill } from "@/components/ui/status-pill"
import { entityHref } from "@/lib/entity-links"
import { formatDurationDecimal, relTime } from "@/lib/time"
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
export function issueRunsEmptyCopy(status: string): string {
  return status === "BACKLOG" || status === "TODO"
    ? "Not started yet — nothing has run. Start the issue to hand it to its agent."
    : "No agent run recorded against this issue."
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
export function IssueRunsCard({ issue, runs }: { issue: Mission; runs: IssueRun[] }) {
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
      {runs.length === 0 ? (
        <InlineEmpty icon={Play} text={issueRunsEmptyCopy(issue.status)} />
      ) : (
        <div className="flex flex-col">
          {runs.map((run) => {
            const l = issueRunLinks(issue, run)
            const summary = run.error_message || run.result_summary
            return (
              <div
                key={run.id}
                data-testid="issue-run-row"
                className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-3 gap-y-1 border-t border-border/50 py-2.5 first:border-t-0 md:grid-cols-[auto_minmax(0,1.6fr)_auto_minmax(0,1fr)_auto] md:items-center"
              >
                <AgentAvatar seed={run.agent_id ?? run.agent_name ?? run.id} alt="" className="h-6 w-6 shrink-0 rounded-full" />
                <div className="min-w-0">
                  <p className="truncate text-[12.5px] font-medium">{run.task || "Agent run"}</p>
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
                  </p>
                </div>
                <StatusPill status={run.status} live={run.status === "RUNNING"} className="col-start-2 md:col-start-auto" />
                <p
                  className={
                    "col-start-2 min-w-0 truncate text-[11px] md:col-start-auto " +
                    (run.error_message ? "text-destructive/90" : "text-muted-foreground")
                  }
                  title={summary}
                >
                  {summary || ""}
                </p>
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
              </div>
            )
          })}
          <div className="mt-2 flex flex-wrap items-center justify-between gap-2 border-t border-border/50 pt-2 text-[11px] text-muted-foreground">
            <span>Every run on this issue, newest first. The journal keeps each run&apos;s steps.</span>
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
