"use client"

import { useEffect, useId, useState } from "react"
import Link from "next/link"
import { ChevronRight, CircleDot, Workflow } from "lucide-react"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { CrewIcon } from "@/components/ui/crew-icon"
import { apiFetch } from "@/lib/api-fetch"
import { resolveRoutineColor, resolveRoutineIcon } from "@/lib/routine-identity"
import { formatShortDate } from "@/lib/time"
import { useDrawerStore } from "@/stores/drawer-store"
import { usePagedList } from "@/hooks/use-paged-list"
import { useChatAgent } from "../chat-agent-context"
import type { ChatTreeThread } from "../chat-tree-data"
import { cn } from "@/lib/utils"

interface IssueRow {
  id: string
  identifier?: string | null
  title: string
  status?: string | null
  created_at?: string | null
  updated_at?: string | null
}

interface RoutineRow {
  id: string
  slug: string
  name?: string | null
  author_agent_id?: string | null
  icon?: string | null
  color?: string | null
  status?: "active" | "proposed" | "disabled" | null
  invocation_count?: number
  step_count?: number
  last_invocation_status?: string | null
  last_run_outcome?: string | null
  last_invoked_at?: string | null
  updated_at?: string | null
}

type WorkView = "issues" | "routines"

const ISSUE_STATUS_ORDER = ["BACKLOG", "TODO", "IN_PROGRESS", "REVIEW", "DONE", "COMPLETED", "PLANNING", "FAILED", "CANCELLED", "DUPLICATE"]

function issueGroups(issues: IssueRow[]): { status: string; issues: IssueRow[] }[] {
  const groups = new Map<string, IssueRow[]>()
  for (const issue of issues) {
    const status = issue.status?.toUpperCase() || "BACKLOG"
    const group = groups.get(status)
    if (group) group.push(issue)
    else groups.set(status, [issue])
  }
  return [...groups.entries()]
    .sort(([a], [b]) => {
      const ai = ISSUE_STATUS_ORDER.indexOf(a)
      const bi = ISSUE_STATUS_ORDER.indexOf(b)
      return (ai < 0 ? ISSUE_STATUS_ORDER.length : ai) - (bi < 0 ? ISSUE_STATUS_ORDER.length : bi) || a.localeCompare(b)
    })
    .map(([status, rows]) => ({ status, issues: rows }))
}

function routineState(routine: RoutineRow): { label: string; dot: string } {
  if (routine.status === "disabled") return { label: "Disabled", dot: "bg-muted-foreground/50" }
  if (routine.status === "proposed") return { label: "Proposed", dot: "bg-warn" }
  const status = (routine.last_invocation_status || routine.last_run_outcome || "").toLowerCase()
  if (status === "running" || status === "in_progress") return { label: "Running", dot: "bg-primary animate-pulse" }
  if (status === "completed" || status === "succeeded" || status === "success") return { label: "Completed", dot: "bg-success" }
  if (status === "failed" || status === "error") return { label: "Failed", dot: "bg-destructive" }
  if (status === "waiting" || status === "awaiting_approval") return { label: "Waiting", dot: "bg-warn" }
  return { label: (routine.invocation_count ?? 0) > 0 ? "Last run unknown" : "Never run", dot: "bg-muted-foreground/40" }
}

function IssueCard({ issue, agentId, workspaceId }: { issue: IssueRow; agentId: string; workspaceId: string | null }) {
  const status = issue.status?.toUpperCase() || "BACKLOG"
  const updated = !!issue.updated_at && issue.updated_at !== issue.created_at
  const date = updated ? issue.updated_at : issue.created_at
  return <li>
    <Link href={`/issues/${encodeURIComponent(issue.identifier || issue.id)}`} aria-label={`Issue ${issue.identifier || issue.id}: ${issue.title}`} className={cn("group block rounded-lg border border-border/60 bg-muted/20 px-2.5 py-2 transition-colors hover:border-primary/40 hover:bg-accent/50", status === "IN_PROGRESS" && "agent-active-card")}>
      <div className="mb-1 truncate font-mono text-[10px] text-foreground/55">{issue.identifier || issue.id}</div>
      <div className="flex items-start gap-1.5">
        <StatusIcon status={status} className="mt-px size-3.5" />
        <span className="line-clamp-2 text-[12.5px] font-medium leading-[1.35] text-foreground">{issue.title}</span>
      </div>
      {date && <div className="mt-1.5 text-[10px] text-muted-foreground">{updated ? "Updated" : "Created"} {formatShortDate(date)}</div>}
    </Link>
    <WorkConversations agentId={agentId} workspaceId={workspaceId} issueId={issue.id} />
  </li>
}

function IssueGroup({ status, issues, agentId, workspaceId }: { status: string; issues: IssueRow[]; agentId: string; workspaceId: string | null }) {
  const [expanded, setExpanded] = useState(true)
  const label = statusLabel[status] || status.toLowerCase().replaceAll("_", " ")
  return <section aria-label={`${label} issues`} className="min-w-0">
    <button type="button" aria-expanded={expanded} onClick={() => setExpanded((value) => !value)} className="kit-tap mb-1.5 flex w-full items-center gap-1.5 rounded-md px-1 py-1 text-left text-[10px] font-medium uppercase tracking-wider text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground">
      <ChevronRight className={cn("size-3 shrink-0 transition-transform duration-200 motion-reduce:transition-none", expanded && "rotate-90")} aria-hidden />
      <StatusIcon status={status} className="size-3" />
      <span>{label}</span>
      <span className="ml-auto tabular-nums">{issues.length}</span>
    </button>
    <div className={cn("grid transition-[grid-template-rows,opacity] duration-200 ease-out motion-reduce:transition-none", expanded ? "grid-rows-[1fr] opacity-100" : "grid-rows-[0fr] opacity-0")} aria-hidden={!expanded} inert={!expanded}>
      <div className="min-h-0 overflow-hidden">
        <ul className="space-y-1.5">{issues.map((issue) => <IssueCard key={issue.id} issue={issue} agentId={agentId} workspaceId={workspaceId} />)}</ul>
      </div>
    </div>
  </section>
}

function RoutineCard({ routine, agentId, workspaceId }: { routine: RoutineRow; agentId: string; workspaceId: string | null }) {
  const state = routineState(routine)
  const date = routine.last_invoked_at || routine.updated_at
  const count = routine.invocation_count ?? 0
  return <li>
    <Link href={`/routines?slug=${encodeURIComponent(routine.slug)}`} aria-label={`Routine ${routine.name || routine.slug}`} className="group block rounded-lg border border-border/60 bg-muted/20 px-2.5 py-2 transition-colors hover:border-primary/40 hover:bg-accent/50">
      <div className="mb-1 flex items-center justify-between gap-2 text-[10px] text-muted-foreground">
        <span className="min-w-0 truncate font-mono text-foreground/55">{routine.slug}</span>
        {date && <span className="shrink-0">{formatShortDate(date)}</span>}
      </div>
      <div className="flex items-center gap-2">
        <CrewIcon icon={resolveRoutineIcon(routine)} color={resolveRoutineColor(routine)} size="sm" className="!size-6 !rounded-md" />
        <span className="min-w-0 flex-1 line-clamp-2 text-[12.5px] font-medium leading-[1.35] text-foreground">{routine.name || routine.slug}</span>
      </div>
      <div className="mt-1.5 flex items-center gap-1.5 pl-8 text-[10px] text-muted-foreground">
        <span aria-hidden="true" className={cn("size-1.5 shrink-0 rounded-full", state.dot)} />
        <span>{state.label}</span>
        <span aria-hidden="true">·</span>
        <span>{count} {count === 1 ? "run" : "runs"}</span>
        {typeof routine.step_count === "number" && <><span aria-hidden="true">·</span><span>{routine.step_count} {routine.step_count === 1 ? "step" : "steps"}</span></>}
      </div>
    </Link>
    <WorkConversations agentId={agentId} workspaceId={workspaceId} routineId={routine.id} />
  </li>
}

export function AgentWorkTab({ agentId, workspaceId }: { agentId: string; workspaceId: string | null }) {
  const selection = useDrawerStore((state) => state.workSource)
  const source = selection?.workspaceId === workspaceId && selection.agentId === agentId ? selection.source : null
  const [view, setView] = useState<WorkView>("issues")
  const [issues, setIssues] = useState<IssueRow[]>([])
  const [routines, setRoutines] = useState<RoutineRow[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const [revision, setRevision] = useState(0)
  const tabId = useId()
  useEffect(() => { if (source) setView(source.kind === "routine" ? "routines" : "issues") }, [source])

  useEffect(() => {
    if (!workspaceId) { setIssues([]); setRoutines([]); return }
    const controller = new AbortController()
    const ws = encodeURIComponent(workspaceId)
    setIssues([])
    setRoutines([])
    setLoading(true)
    setError(false)
    void Promise.all([
      apiFetch(`/api/v1/issues?workspace_id=${ws}&assignee_id=${encodeURIComponent(agentId)}&limit=20&sort=updated_at`, { signal: controller.signal }),
      apiFetch(`/api/v1/workspaces/${ws}/pipelines?order=recent&limit=100`, { signal: controller.signal }),
    ]).then(async ([issueRes, routineRes]) => {
      if (!issueRes.ok || !routineRes.ok) throw new Error("Work unavailable")
      const [issueRows, routineRows] = await Promise.all([issueRes.json(), routineRes.json()])
      if (controller.signal.aborted) return
      setIssues(Array.isArray(issueRows) ? issueRows : [])
      setRoutines(Array.isArray(routineRows) ? routineRows.filter((row: RoutineRow) => row.author_agent_id === agentId) : [])
    }).catch(() => { if (!controller.signal.aborted) setError(true) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [agentId, workspaceId, revision])

  const groups = issueGroups(issues)
  return <div className="@container p-3 text-xs">
    {source && <section aria-label="Source of this conversation" className="mb-4 rounded-lg border border-primary/25 bg-primary/5 p-3"><p className="mb-1 text-[10px] text-muted-foreground">Source of this conversation</p><Link className="text-primary hover:underline" href={source.kind === "routine" ? `/routines?slug=${encodeURIComponent(source.slug || source.id)}${source.run_id ? `&run=${encodeURIComponent(source.run_id)}` : ""}` : `/issues/${encodeURIComponent(source.slug || source.id)}`}>{source.name} ↗</Link>{source.step_id && <p className="mt-1 text-[10px] text-muted-foreground">Step: {source.step_id}</p>}<WorkConversations agentId={agentId} workspaceId={workspaceId} routineId={source.kind === "routine" ? source.id : undefined} issueId={source.kind === "issue" ? source.id : undefined} /></section>}
    <div role="tablist" aria-label="Agent work type" className="mb-3 grid grid-cols-2 gap-1 rounded-lg bg-muted/40 p-1">
      {(["issues", "routines"] as const).map((kind) => {
        const Icon = kind === "issues" ? CircleDot : Workflow
        return <button key={kind} id={`${tabId}-${kind}`} type="button" role="tab" aria-selected={view === kind} aria-controls={`${tabId}-panel`} onClick={() => setView(kind)} className={cn("kit-tap flex min-h-8 items-center justify-center gap-1.5 rounded-md px-2 text-[11px] transition-colors", view === kind ? "bg-card font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground hover:bg-card/50")}>
          <Icon className="size-3.5" />{kind === "issues" ? "Issues" : "Routines"}<span className="rounded-full bg-white/[0.06] px-1.5 text-[10px] tabular-nums text-muted-foreground">{kind === "issues" ? issues.length : routines.length}</span>
        </button>
      })}
    </div>
    <div id={`${tabId}-panel`} role="tabpanel" aria-labelledby={`${tabId}-${view}`}>
      {loading && <p role="status" className="py-2 text-muted-foreground">Loading agent work…</p>}
      {error && <div role="alert" className="space-y-2 rounded-lg border border-destructive/25 p-2.5 text-muted-foreground"><p>Work could not be loaded.</p><button type="button" onClick={() => setRevision((n) => n + 1)} className="text-primary hover:underline">Try again</button></div>}
      {!workspaceId && !loading && !error && <p className="rounded-lg border border-dashed p-2.5 text-muted-foreground">Select a workspace to see agent work.</p>}
      {workspaceId && !loading && !error && view === "issues" && (groups.length ? <div className="grid grid-cols-1 items-start gap-3">{groups.map(({ status, issues: groupIssues }) => <IssueGroup key={`${agentId}:${status}`} status={status} issues={groupIssues} agentId={agentId} workspaceId={workspaceId} />)}</div> : <p className="rounded-lg border border-dashed p-2.5 text-muted-foreground">No issues assigned to this agent.</p>)}
      {workspaceId && !loading && !error && view === "routines" && (routines.length ? <ul className="space-y-1.5">{routines.map((routine) => <RoutineCard key={routine.id} routine={routine} agentId={agentId} workspaceId={workspaceId} />)}</ul> : <p className="rounded-lg border border-dashed p-2.5 text-muted-foreground">No routines authored by this agent.</p>)}
    </div>
  </div>
}

function WorkConversations({ agentId, workspaceId, routineId, issueId }: { agentId: string; workspaceId: string | null; routineId?: string; issueId?: string }) {
  const [open, setOpen] = useState(false)
  const agent = useChatAgent()
  const list = usePagedList<ChatTreeThread>({ url: open && workspaceId ? `/api/v1/agents/${encodeURIComponent(agentId)}/chats?workspace_id=${encodeURIComponent(workspaceId)}&${routineId ? `routine_id=${encodeURIComponent(routineId)}` : `chat_id=${encodeURIComponent(issueId!)}`}` : null })
  return <div className="mt-1"><button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="kit-tap flex items-center gap-1 px-1 py-1 text-[10px] text-muted-foreground hover:text-primary"><ChevronRight className={cn("size-3", open && "rotate-90")} />Conversations</button>{open && <div className="pl-4">
    {list.loading && <p role="status">Loading conversations…</p>}
    {list.error && <button type="button" onClick={() => void list.refresh()}>Could not load conversations. Retry</button>}
    {!list.loading && !list.error && !list.items.length && <p className="py-1 text-[10px] text-muted-foreground">No linked conversations. Older runs may not have a recorded link.</p>}
    {list.items.map((chat) => <Link key={chat.id} className="block truncate py-1 text-[11px] text-primary hover:underline" href={`/chat/${encodeURIComponent(agent?.slug || agentId)}?session=${encodeURIComponent(chat.id)}&workspace_id=${encodeURIComponent(workspaceId!)}`}>{chat.title || "Untitled session"}</Link>)}
    {list.hasMore && <button type="button" disabled={list.loadingMore} onClick={() => void list.loadMore()} className="text-primary">Load more conversations</button>}
  </div>}</div>
}
