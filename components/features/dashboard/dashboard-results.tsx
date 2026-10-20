"use client"

import { useState } from "react"
import Link from "next/link"
import { Bot, CircleDot, UserRound } from "lucide-react"
import type { AgentSummary, CrewSummary } from "@/app/(dashboard)/dashboard-types"
import type { Mission } from "@/lib/types/mission"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { DashboardActiveRun } from "@/hooks/use-dashboard-data"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { ConceptIcon } from "@/components/ui/concept-icon"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { Skeleton } from "@/components/ui/skeleton"
import { DashCard, DashLine, DashNote, DashSubHead } from "./dash-card"
import { Segmented } from "@/components/ui/segmented"
import { entityHref } from "@/lib/entity-links"
import { formatRelativeTime } from "@/lib/time"
import { cn } from "@/lib/utils"

/** Four views: running work is a group inside In progress, not a fifth view. */
export type ResultsFilter = "all" | "review" | "progress" | "done"
export const RESULTS_FILTERS: { key: ResultsFilter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "review", label: "For review" },
  { key: "progress", label: "In progress" },
  { key: "done", label: "Finished" },
]

/** Failed routines belong in Activity and Inbox; a result exists after success. */
export interface RoutineResultGroup {
  /** The newest completed run — the row links to its result. */
  latest: PipelineRun
  /** How many completed runs of this routine the row stands for. */
  count: number
}

/**
 * One row per routine. A routine on a five-minute schedule used to fill every
 * row with its own name; the group keeps the newest run as the link and says
 * how many runs it covers. Order is the order of each routine's newest run.
 */
export function groupRoutineResults(runs: PipelineRun[], limit = 8): RoutineResultGroup[] {
  const groups = new Map<string, RoutineResultGroup>()
  for (const run of runs) {
    if (run.status !== "completed") continue
    const key = run.pipeline_slug || run.pipeline_name || run.id
    const group = groups.get(key)
    if (group) group.count += 1
    else groups.set(key, { latest: run, count: 1 })
  }
  return [...groups.values()].slice(0, limit)
}

/** An agent's face with the live dot when it is running right now. */
function Lead({ children, live }: { children: React.ReactNode; live?: boolean }) {
  return (
    <span className="relative flex h-7 w-7 shrink-0 items-center justify-center">
      {children}
      {live && (
        <span className="absolute -bottom-0.5 -right-0.5 h-2.5 w-2.5 rounded-full border-2 border-card bg-primary motion-safe:animate-pulse" aria-hidden />
      )}
    </span>
  )
}

/** The row's action: blue only for the first review, quiet everywhere else.
 *  On a phone only review actions stay — the rest are the row's own link. */
function RowAction({ href, label, variant }: { href: string; label: string; variant: "default" | "outline" | "ghost" }) {
  return (
    <Button asChild size="sm" variant={variant} className={cn("coarse:min-h-[44px]", variant !== "default" && label !== "Review work" && "max-md:hidden")}>
      <Link href={href} tabIndex={-1} aria-hidden>{label}</Link>
    </Button>
  )
}

export function DashboardResults({ review, inProgress, completed, activeAgentRuns, activeRoutineRuns, recentRoutineRuns, agents, crews, loading, error, routineError, routineLoading, onRetry }: {
  review: Mission[]; inProgress: Mission[]; completed: Mission[]
  activeAgentRuns: DashboardActiveRun[]; activeRoutineRuns: PipelineRun[]; recentRoutineRuns: PipelineRun[]
  agents: AgentSummary[]; crews: CrewSummary[]
  loading: boolean; error: boolean; routineError: string | null; routineLoading: boolean; onRetry: () => void
}) {
  const [filter, setFilter] = useState<ResultsFilter>("all")
  const routineResults = groupRoutineResults(recentRoutineRuns)
  const liveRoutines = activeRoutineRuns.filter((run) => run.status === "running" || run.status === "queued" || run.status === "waiting")
  const runningCount = activeAgentRuns.length + liveRoutines.length
  const finishedCount = completed.length + routineResults.length
  const counts: Record<ResultsFilter, number> = {
    all: runningCount + inProgress.length + review.length + finishedCount,
    review: review.length,
    progress: runningCount + inProgress.length,
    done: finishedCount,
  }
  const show = (group: "running" | "progress" | "review" | "done") =>
    filter === "all" || filter === group || (filter === "progress" && group === "running")

  function issueLead(issue: Mission) {
    const humanOwned = Boolean(issue.owner) || issue.assignee_type === "user"
    const agent = humanOwned ? undefined : agents.find((a) => a.id === (issue.assignee_type === "agent" ? issue.assignee_id : issue.lead_agent_id))
    const crew = crews.find((c) => c.id === issue.crew_id)
    const owner = issue.owner ? issue.owner.name || "Issue owner" : humanOwned ? issue.assignee_name || "Issue owner" : agent?.name || issue.assignee_name || issue.lead_agent_name || crew?.name || "Workspace"
    const lead = humanOwned
      ? <UserRound className="h-6 w-6 text-muted-foreground" aria-hidden />
      : agent
        ? <AgentAvatar seed={agent.slug} alt={agent.name} className="h-7 w-7 rounded-md bg-muted" />
        : crew
          ? <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" />
          : <CircleDot className="h-6 w-6 text-primary-hover" aria-hidden />
    return { lead, owner, crew }
  }

  function issueRow(issue: Mission, kind: "progress" | "review" | "done", index: number) {
    const { lead, owner, crew } = issueLead(issue)
    const href = issue.identifier ? entityHref({ kind: "issue", identifier: issue.identifier }) : entityHref({ kind: "issues" })
    const sub = [owner !== crew?.name ? crew?.name : null, owner, issue.identifier, formatRelativeTime(issue.updated_at)].filter(Boolean).join(" · ")
    const pill = kind === "review"
      ? <StatusPill tone="purple" label="For review" />
      : kind === "done"
        ? <StatusPill tone="success" label="Done" />
        : <StatusPill status={issue.status} />
    const action = kind === "review"
      ? <RowAction href={href} label="Review work" variant={index === 0 ? "default" : "outline"} />
      : kind === "done"
        ? <RowAction href={href} label="Open result" variant="ghost" />
        : null
    return <DashLine key={`issue-${issue.id}`} lead={<Lead>{lead}</Lead>} title={issue.title} sub={sub} href={href} right={<>{pill}{action}</>} />
  }

  const empty = counts[filter] === 0

  return (
    <DashCard
      title="Results & review"
      concept="issues"
      link={{ label: "All issues", href: entityHref({ kind: "issues" }) }}
      className="xl:col-span-2"
      tools={
        <Segmented
          ariaLabel="Filter dashboard work"
          size="xs"
          value={filter}
          onChange={setFilter}
          options={RESULTS_FILTERS.map(({ key, label }) => ({ value: key, label, count: counts[key] }))}
        />
      }
    >
      {(error || routineError) && (
        <DashNote tone="error" action={<Button size="xs" variant="outline" onClick={onRetry} className="coarse:min-h-[44px]">Retry</Button>}>
          Some work could not refresh. Showing available results.
        </DashNote>
      )}
      {loading && counts.all === 0 ? (
        <div className="space-y-2 border-t border-border px-4 py-3" aria-label="Loading results">
          <Skeleton className="h-9 rounded-md" /><Skeleton className="h-9 rounded-md" /><Skeleton className="h-9 rounded-md" />
        </div>
      ) : empty ? (
        <DashNote>
          {routineLoading && filter === "progress"
            ? "Checking live routines…"
            : filter === "all"
              ? "Nothing running or waiting for review"
              : `No ${RESULTS_FILTERS.find((item) => item.key === filter)?.label.toLowerCase()} work right now`}
        </DashNote>
      ) : (
        <div className="max-h-[560px] overflow-y-auto overscroll-contain" aria-label="Work list">
          {show("running") && runningCount > 0 && (
            <section aria-label="Running now">
              <DashSubHead title="Running now" count={runningCount} />
              {activeAgentRuns.map((run) => {
                const agent = agents.find((item) => item.id === run.agent_id)
                const issue = inProgress.find((item) => item.id === run.mission_id)
                const name = run.agent_name || agent?.name || "Agent"
                const href = run.mission_identifier ? entityHref({ kind: "issue", identifier: run.mission_identifier }) : run.agent_slug ? entityHref({ kind: "chat", agentSlug: run.agent_slug }) : "/activity"
                const sub = [run.crew_name, name, run.mission_identifier, formatRelativeTime(run.started_at || run.created_at)].filter(Boolean).join(" · ")
                return (
                  <DashLine
                    key={`agent-${run.id}`}
                    lead={<Lead live>{agent ? <AgentAvatar seed={agent.slug} alt={agent.name} className="h-7 w-7 rounded-md bg-muted" /> : <Bot className="h-6 w-6 text-primary-hover" aria-hidden />}</Lead>}
                    title={issue?.title || `${name} is working`}
                    sub={sub}
                    href={href}
                    right={<><StatusPill tone="blue" label="Running" live /><RowAction href={href} label="Follow run" variant="outline" /></>}
                  />
                )
              })}
              {liveRoutines.map((run) => {
                const href = entityHref({ kind: "run", runId: run.id, pipelineSlug: run.pipeline_slug })
                return (
                  <DashLine
                    key={`routine-live-${run.id}`}
                    lead={<Lead live><ConceptIcon concept="routines" variant="chip" className="h-7 w-7 rounded-md" /></Lead>}
                    title={run.pipeline_name || run.pipeline_slug}
                    sub={["Routine", formatRelativeTime(run.started_at)].join(" · ")}
                    href={href}
                    right={<><StatusPill status={run.status.toUpperCase()} live /><RowAction href={href} label="Follow run" variant="outline" /></>}
                  />
                )
              })}
            </section>
          )}
          {show("review") && review.length > 0 && (
            <section aria-label="For review">
              <DashSubHead title="For review" count={review.length} />
              {review.map((issue, i) => issueRow(issue, "review", i))}
            </section>
          )}
          {show("progress") && inProgress.length > 0 && (
            <section aria-label="In progress">
              <DashSubHead title="In progress" count={inProgress.length} />
              {inProgress.map((issue, i) => issueRow(issue, "progress", i))}
            </section>
          )}
          {show("done") && finishedCount > 0 && (
            <section aria-label="Finished recently">
              <DashSubHead title="Finished recently" count={finishedCount} />
              {completed.map((issue, i) => issueRow(issue, "done", i))}
              {routineResults.map(({ latest: run, count }) => {
                const href = entityHref({ kind: "run", runId: run.id, pipelineSlug: run.pipeline_slug })
                const when = formatRelativeTime(run.ended_at || run.started_at)
                return (
                  <DashLine
                    key={`routine-done-${run.id}`}
                    lead={<Lead><ConceptIcon concept="routines" variant="chip" className="h-7 w-7 rounded-md" /></Lead>}
                    title={<>{run.pipeline_name || run.pipeline_slug}{count > 1 && <span className="ml-1.5 text-label font-normal tabular-nums text-muted-foreground" title={`${count} completed runs`}>×{count}</span>}</>}
                    sub={["Routine", count > 1 ? `last ${when}` : when].join(" · ")}
                    href={href}
                    right={<><StatusPill tone="success" label="Done" /><RowAction href={href} label="Open result" variant="ghost" /></>}
                  />
                )
              })}
            </section>
          )}
        </div>
      )}
    </DashCard>
  )
}
