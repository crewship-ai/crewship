"use client"

import Link from "next/link"
import { ArrowUpRight, CheckCheck, CircleDot, ScrollText, UserRound } from "lucide-react"
import type { AgentSummary, CrewSummary } from "@/app/(dashboard)/dashboard-types"
import type { Mission } from "@/lib/types/mission"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { Skeleton } from "@/components/ui/skeleton"
import { DashboardCard } from "./dashboard-card"
import { entityHref } from "@/lib/entity-links"
import { formatRelativeTime } from "@/lib/time"

/** A run is a result only after successful completion; failed runs stay in Activity/Inbox. */
export function completedRoutineResults(runs: PipelineRun[]) {
  return runs.filter((run) => run.status === "completed").slice(0, 2)
}

export function DashboardResults({ review, completed, runs, agents, crews, workspaceId, loading, error, routineError, routineLoading, onRetry }: {
  review: Mission[]
  completed: Mission[]
  runs: PipelineRun[]
  agents: AgentSummary[]
  crews: CrewSummary[]
  workspaceId: string | null
  loading: boolean
  error: boolean
  routineError: string | null
  routineLoading: boolean
  onRetry: () => void
}) {
  const routineResults = completedRoutineResults(runs)
  const rows = [...review.slice(0, 4), ...completed.slice(0, 2)]
  const hasResults = rows.length > 0 || routineResults.length > 0
  return (
    <DashboardCard title="Results & review" icon={CheckCheck} hint="Latest work" action={<Link href={entityHref({ kind: "issues" })} className="text-primary-hover hover:underline">All issues →</Link>} className="h-full border-primary/20">
      <p className="mb-4 text-body text-muted-foreground">What your agents have prepared for you.</p>
      {(error || routineError) && <p role="status" className="mb-3 rounded-lg border border-warn/25 bg-warn/10 p-3 text-label text-warn">{error ? "Issue results could not refresh." : "Routine results could not refresh."} Showing available results. <button type="button" onClick={onRetry} className="underline">Retry</button></p>}
      {loading && !hasResults ? <div className="space-y-3" aria-label="Loading results"><Skeleton className="h-20 rounded-lg" /><Skeleton className="h-20 rounded-lg" /></div> : <>
        {!hasResults && !error && !routineError && <p className="py-3 text-body text-muted-foreground">{routineLoading ? "Checking routine results…" : "Finished work will appear here. Start by assigning an issue to an agent."}</p>}
        <div className="flex flex-col gap-2">
          {rows.map((issue) => {
            const humanOwned = Boolean(issue.owner) || issue.assignee_type === "user"
            const agent = humanOwned ? undefined : agents.find((a) => a.id === (issue.assignee_type === "agent" ? issue.assignee_id : issue.lead_agent_id))
            const crew = crews.find((c) => c.id === issue.crew_id)
            const owner = issue.owner ? issue.owner.name || "Issue owner" : humanOwned ? issue.assignee_name || "Issue owner" : agent?.name || issue.assignee_name || issue.lead_agent_name || crew?.name || "Workspace"
            return (
              <Link key={issue.id} href={issue.identifier ? entityHref({ kind: "issue", identifier: issue.identifier }) : entityHref({ kind: "issues" })} className="group flex min-w-0 items-center gap-3 rounded-xl border border-border/60 bg-background/30 p-3 transition-colors duration-150 hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-2 focus-visible:outline-primary">
                {humanOwned ? <UserRound className="h-8 w-8 shrink-0 text-muted-foreground" aria-hidden /> : agent ? <AgentAvatar seed={agent.slug} agentId={agent.id} workspaceId={workspaceId} alt={agent.name} className="h-10 w-10 shrink-0 rounded-xl bg-muted" /> : crew ? <CrewIcon icon={crew.icon || "users"} color={crew.color} size="md" className="shrink-0" /> : <CircleDot className="h-8 w-8 shrink-0 text-primary-hover" aria-hidden />}
                <span className="min-w-0 flex-1">
                  <span className="mb-1 flex flex-wrap items-center gap-2 text-micro text-muted-foreground"><span className="font-mono">{issue.identifier || "Issue"}</span><StatusPill status={issue.status} /></span>
                  <span className="line-clamp-2 text-body font-medium sm:block sm:truncate">{issue.title}</span>
                  <span className="mt-1 block truncate text-label text-muted-foreground">{owner}{crew && owner !== crew.name ? ` · ${crew.name}` : ""} · {formatRelativeTime(issue.updated_at)}</span>
                </span>
                <span className="hidden shrink-0 text-label font-medium text-primary-hover sm:block">{issue.status === "REVIEW" ? "Review work" : "Open issue"}</span><ArrowUpRight className="h-4 w-4 shrink-0 text-primary-hover" aria-hidden />
              </Link>
            )
          })}
          {routineResults.map((run) => {
            const agent = agents.find((a) => a.id === run.invoking_agent_id)
            const crew = crews.find((c) => c.id === run.invoking_crew_id)
            return <Link key={run.id} href={entityHref({ kind: "run", runId: run.id, pipelineSlug: run.pipeline_slug })} className="group flex min-w-0 items-center gap-3 rounded-xl border border-success/20 bg-success/5 p-3 transition-colors duration-150 hover:border-success/40 hover:bg-success/10 focus-visible:outline-2 focus-visible:outline-primary">
              {agent ? <AgentAvatar seed={agent.slug} agentId={agent.id} workspaceId={workspaceId} alt={agent.name} className="h-10 w-10 shrink-0 rounded-xl bg-muted" /> : <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-success/10 text-success"><ScrollText className="h-5 w-5" aria-hidden /></span>}
              <span className="min-w-0 flex-1"><span className="mb-1 flex items-center gap-2 text-micro text-muted-foreground">Routine <StatusPill status="COMPLETED" /></span><span className="line-clamp-2 text-body font-medium sm:block sm:truncate">{run.pipeline_name || run.pipeline_slug}</span><span className="mt-1 block truncate text-label text-muted-foreground">{agent?.name || crew?.name || "Routine run"} · {formatRelativeTime(run.ended_at || run.started_at)}</span></span>
              <span className="hidden shrink-0 text-label font-medium text-primary-hover sm:block">Open result</span><ArrowUpRight className="h-4 w-4 shrink-0 text-primary-hover" aria-hidden />
            </Link>
          })}
        </div>
      </>}
    </DashboardCard>
  )
}
