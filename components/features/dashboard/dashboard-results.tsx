"use client"

import { useState } from "react"
import Link from "next/link"
import { ArrowUpRight, Bot, CheckCheck, CircleDot, ScrollText, UserRound } from "lucide-react"
import type { AgentSummary, CrewSummary } from "@/app/(dashboard)/dashboard-types"
import type { Mission } from "@/lib/types/mission"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { DashboardActiveRun } from "@/hooks/use-dashboard-data"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { Skeleton } from "@/components/ui/skeleton"
import { DashboardCard } from "./dashboard-card"
import { entityHref } from "@/lib/entity-links"
import { formatRelativeTime } from "@/lib/time"
import { cn } from "@/lib/utils"

type Filter = "all" | "running" | "progress" | "review" | "finished"
const FILTERS: { key: Filter; label: string }[] = [
  { key: "all", label: "All" }, { key: "running", label: "Running" },
  { key: "progress", label: "In progress" }, { key: "review", label: "For review" },
  { key: "finished", label: "Finished" },
]

/** Failed routines belong in Activity and Inbox; a result exists after success. */
export function completedRoutineResults(runs: PipelineRun[]) {
  return runs.filter((run) => run.status === "completed").slice(0, 8)
}

function WorkRow({ href, icon, kind, status, title, meta, action }: {
  href: string; icon: React.ReactNode; kind: string; status: React.ReactNode
  title: string; meta: string; action: string
}) {
  return <Link href={href} className="group flex min-w-0 items-center gap-2.5 rounded-md px-2 py-2 transition-colors hover:bg-primary/5 focus-visible:outline-2 focus-visible:outline-primary coarse:min-h-12">
    {icon}
    <span className="hidden w-16 shrink-0 font-mono text-micro text-muted-foreground sm:block">{kind}</span>
    <span className="hidden w-24 shrink-0 sm:block">{status}</span>
    <span className="min-w-0 flex-1 truncate text-body font-medium">{title}</span>
    <span className="hidden max-w-[180px] shrink-0 truncate text-label text-muted-foreground md:block">{meta}</span>
    <span className="hidden shrink-0 text-label font-medium text-primary-hover lg:block">{action}</span>
    <ArrowUpRight className="h-3.5 w-3.5 shrink-0 text-primary-hover" aria-hidden />
  </Link>
}

function WorkSection({ title, count, children }: { title: string; count: number; children: React.ReactNode }) {
  if (!count) return null
  return <section aria-label={title}>
    <div className="sticky top-0 z-10 flex items-center gap-2 border-y border-border/60 bg-card px-2 py-1.5 text-micro font-semibold uppercase tracking-wider text-muted-foreground first:border-t-0">
      {title}<span className="rounded-full bg-muted px-1.5 py-0.5 font-mono text-[10px] tabular-nums">{count}</span>
    </div>
    <div className="divide-y divide-border/40">{children}</div>
  </section>
}

export function DashboardResults({ review, inProgress, completed, activeAgentRuns, activeRoutineRuns, recentRoutineRuns, agents, crews, workspaceId, loading, error, routineError, routineLoading, onRetry }: {
  review: Mission[]; inProgress: Mission[]; completed: Mission[]
  activeAgentRuns: DashboardActiveRun[]; activeRoutineRuns: PipelineRun[]; recentRoutineRuns: PipelineRun[]
  agents: AgentSummary[]; crews: CrewSummary[]; workspaceId: string | null
  loading: boolean; error: boolean; routineError: string | null; routineLoading: boolean; onRetry: () => void
}) {
  const [filter, setFilter] = useState<Filter>("all")
  const routineResults = completedRoutineResults(recentRoutineRuns)
  const liveRoutines = activeRoutineRuns.filter((run) => run.status === "running" || run.status === "queued" || run.status === "waiting")
  const runningCount = activeAgentRuns.length + liveRoutines.length
  const counts: Record<Filter, number> = {
    all: runningCount + inProgress.length + review.length + completed.length + routineResults.length,
    running: runningCount, progress: inProgress.length, review: review.length,
    finished: completed.length + routineResults.length,
  }
  const show = (section: Filter) => filter === "all" || filter === section

  function issueRow(issue: Mission) {
    const humanOwned = Boolean(issue.owner) || issue.assignee_type === "user"
    const agent = humanOwned ? undefined : agents.find((a) => a.id === (issue.assignee_type === "agent" ? issue.assignee_id : issue.lead_agent_id))
    const crew = crews.find((c) => c.id === issue.crew_id)
    const owner = issue.owner ? issue.owner.name || "Issue owner" : humanOwned ? issue.assignee_name || "Issue owner" : agent?.name || issue.assignee_name || issue.lead_agent_name || crew?.name || "Workspace"
    const icon = humanOwned ? <UserRound className="h-6 w-6 shrink-0 text-muted-foreground" aria-hidden /> : agent ? <AgentAvatar seed={agent.slug} agentId={agent.id} workspaceId={workspaceId} alt={agent.name} className="h-6 w-6 shrink-0 rounded-md bg-muted" /> : crew ? <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" className="shrink-0" /> : <CircleDot className="h-6 w-6 shrink-0 text-primary-hover" aria-hidden />
    return <WorkRow key={`issue-${issue.id}`} href={issue.identifier ? entityHref({ kind: "issue", identifier: issue.identifier }) : entityHref({ kind: "issues" })} icon={icon} kind={issue.identifier || "Issue"} status={<StatusPill status={issue.status} />} title={issue.title} meta={`${owner} · ${formatRelativeTime(issue.updated_at)}`} action={issue.status === "REVIEW" ? "Review work" : "Open issue"} />
  }

  return <DashboardCard title="Results & review" icon={CheckCheck} hint={runningCount > 0 ? `${runningCount} live now` : "Live work and outcomes"} action={<Link href={entityHref({ kind: "issues" })} className="text-primary-hover hover:underline">All issues →</Link>} className="h-full border-primary/20">
    <div className="mb-3 flex flex-wrap gap-1" role="group" aria-label="Filter dashboard work">
      {FILTERS.map(({ key, label }) => <button key={key} type="button" aria-pressed={filter === key} onClick={() => setFilter(key)} className={cn("rounded-md px-2.5 py-1.5 text-label transition-colors coarse:min-h-12", filter === key ? "bg-primary/15 font-medium text-primary-hover" : "text-muted-foreground hover:bg-muted hover:text-foreground")}>{label}<span className="ml-1.5 font-mono text-micro tabular-nums opacity-70">{counts[key]}</span></button>)}
    </div>
    {(error || routineError) && <p role="status" className="mb-3 rounded-lg border border-warn/25 bg-warn/10 p-3 text-label text-warn">Some work could not refresh. Showing available results. <button type="button" onClick={onRetry} className="underline">Retry</button></p>}
    {loading && counts.all === 0 ? <div className="space-y-3" aria-label="Loading results"><Skeleton className="h-9 rounded-md" /><Skeleton className="h-9 rounded-md" /><Skeleton className="h-9 rounded-md" /></div> : <div className="max-h-[440px] min-h-[210px] overflow-y-auto overscroll-contain pr-1 [scrollbar-color:var(--border)_transparent]" tabIndex={0} aria-label="Work list">
      {counts[filter] === 0 && <p className="px-2 py-8 text-center text-body text-muted-foreground">{routineLoading && filter === "running" ? "Checking live routines…" : filter === "all" ? "No work yet. Assign an issue or start a routine to see it here." : `No ${FILTERS.find((item) => item.key === filter)?.label.toLowerCase()} work right now.`}</p>}
      {show("running") && <WorkSection title="Running now" count={runningCount}>
        {activeAgentRuns.map((run) => {
          const agent = agents.find((item) => item.id === run.agent_id)
          const issue = inProgress.find((item) => item.id === run.mission_id)
          const name = run.agent_name || agent?.name || "Agent"
          return <WorkRow key={`agent-${run.id}`} href={run.mission_identifier ? entityHref({ kind: "issue", identifier: run.mission_identifier }) : run.agent_slug ? entityHref({ kind: "chat", agentSlug: run.agent_slug }) : "/activity"} icon={agent ? <AgentAvatar seed={agent.slug} agentId={agent.id} workspaceId={workspaceId} alt={agent.name} className="h-6 w-6 shrink-0 rounded-md bg-muted" /> : <Bot className="h-6 w-6 shrink-0 text-primary-hover" aria-hidden />} kind="Agent" status={<StatusPill status="RUNNING" live />} title={issue?.title || `${name} is working`} meta={`${name} · ${formatRelativeTime(run.started_at || run.created_at)}`} action="Follow run" />
        })}
        {liveRoutines.map((run) => <WorkRow key={`routine-live-${run.id}`} href={entityHref({ kind: "run", runId: run.id, pipelineSlug: run.pipeline_slug })} icon={<ScrollText className="h-6 w-6 shrink-0 text-primary-hover" aria-hidden />} kind="Routine" status={<StatusPill status={run.status.toUpperCase()} live />} title={run.pipeline_name || run.pipeline_slug} meta={formatRelativeTime(run.started_at)} action="Follow run" />)}
      </WorkSection>}
      {show("progress") && <WorkSection title="In progress" count={inProgress.length}>{inProgress.map(issueRow)}</WorkSection>}
      {show("review") && <WorkSection title="For review" count={review.length}>{review.map(issueRow)}</WorkSection>}
      {show("finished") && <WorkSection title="Finished recently" count={completed.length + routineResults.length}>
        {completed.map(issueRow)}
        {routineResults.map((run) => <WorkRow key={`routine-done-${run.id}`} href={entityHref({ kind: "run", runId: run.id, pipelineSlug: run.pipeline_slug })} icon={<ScrollText className="h-6 w-6 shrink-0 text-success" aria-hidden />} kind="Routine" status={<StatusPill status="COMPLETED" />} title={run.pipeline_name || run.pipeline_slug} meta={formatRelativeTime(run.ended_at || run.started_at)} action="Open result" />)}
      </WorkSection>}
    </div>}
  </DashboardCard>
}
