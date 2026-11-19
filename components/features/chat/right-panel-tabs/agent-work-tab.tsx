"use client"

import { useCallback, useEffect, useId, useRef, useState } from "react"
import Link from "next/link"
import { ChevronRight, CircleDot, Clock, Hand, Loader2, Workflow } from "lucide-react"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { CrewIcon } from "@/components/ui/crew-icon"
import { apiFetch } from "@/lib/api-fetch"
import { resolveRoutineColor, resolveRoutineIcon } from "@/lib/routine-identity"
import { formatShortDate } from "@/lib/time"
import { useDrawerStore } from "@/stores/drawer-store"
import { usePagedList } from "@/hooks/use-paged-list"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
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

interface RunRow { id: string; status?: string; trigger_type?: string | null; mission_identifier?: string | null; started_at?: string | null }
interface AssignmentRow { id: string; task: string; status: string; queued_reason?: string | null; assigned_to_slug?: string | null; assigned_by_name?: string | null }
interface EscalationRow { id: string; reason: string; from_slug?: string | null; status?: string }

/** One fetch generation. `null` marks a source that failed on its own. */
interface WorkData {
  key: string
  issues: IssueRow[] | null
  issueTotal: number
  routines: RoutineRow[] | null
  runs: RunRow[] | null
  queue: AssignmentRow[] | null
  decisions: EscalationRow[] | null
}

type WorkView = "issues" | "routines"

/** Open work only: closed issues are history, not what the agent is doing. */
const OPEN_ISSUE_STATUSES = ["PLANNING", "BACKLOG", "TODO", "IN_PROGRESS", "REVIEW"]
const QUEUED_ASSIGNMENT = new Set(["PENDING", "QUEUED"])
const ISSUE_PAGE = 50
// Safety net for dropped frames; realtime events make updates near-instant.
const WORK_POLL_MS = 20_000
const WORK_DEBOUNCE_MS = 300
const WORK_MAX_WAIT_MS = 2_000

/**
 * Whether a workspace event can change this agent's Work. Events name their
 * agent in different keys (runs: agent_id; assignments: target slug;
 * escalations: from_slug). An event about an issue already listed always
 * counts, so an issue reassigned away still leaves the list. Unknown shapes
 * count, so a missing field never hides a change.
 */
export function workEventConcernsAgent(payload: Record<string, unknown> | undefined, scope: { agentId: string; agentSlug: string | null; issueKeys: Set<string> }): boolean {
  if (!payload) return true
  const field = (name: string) => typeof payload[name] === "string" && payload[name] ? payload[name] as string : null
  const id = field("id"), identifier = field("identifier")
  if ((id && scope.issueKeys.has(id)) || (identifier && scope.issueKeys.has(identifier))) return true
  const owner = field("agent_id") ?? field("assignee_id")
  if (owner) return owner === scope.agentId
  const slug = field("target") ?? field("from_slug") ?? field("agent_slug")
  if (slug) return slug === scope.agentSlug
  return true
}

async function readJSON(response: Response | undefined): Promise<unknown> {
  if (!response?.ok) throw new Error("unavailable")
  return response.json()
}

function settled<T>(result: PromiseSettledResult<unknown>, pick: (value: unknown) => T): T | null {
  if (result.status !== "fulfilled") return null
  try { return pick(result.value) } catch { return null }
}

const queuedReason: Record<string, string> = { agent_busy: "Waits until the agent is free", crew_budget: "Waits for crew budget" }

function NowRow({ icon: Icon, tone, label, title, detail, href }: { icon: typeof Clock; tone: string; label: string; title: string; detail?: string | null; href?: string }) {
  const body = <><Icon className={cn("mt-0.5 size-3.5 shrink-0", tone)} aria-hidden /><span className="min-w-0 flex-1"><span className="block text-micro text-muted-foreground">{label}</span><span className="line-clamp-2 text-label font-medium leading-[1.35] text-foreground">{title}</span>{detail && <span className="block text-micro text-muted-foreground">{detail}</span>}</span></>
  const frame = "flex items-start gap-2 rounded-lg border border-border/60 bg-muted/20 px-2.5 py-2"
  return <li>{href ? <Link href={href} className={cn(frame, "transition-colors hover:border-primary/40 hover:bg-accent/50")}>{body}</Link> : <div className={frame}>{body}</div>}</li>
}

/** What the agent is doing right now: running work, decisions it waits for, its queue. */
function WorkNow({ runs, queue, decisions }: Pick<WorkData, "runs" | "queue" | "decisions">) {
  if (!runs?.length && !queue?.length && !decisions?.length) return null
  return <section aria-label="Working now" className="mb-3 space-y-1.5">
    <p className="px-1 text-micro font-medium uppercase tracking-wider text-muted-foreground">Now</p>
    <ul className="space-y-1.5">
      {runs?.map((run) => <NowRow key={run.id} icon={Loader2} tone="animate-spin text-primary motion-reduce:animate-none" label="Running"
        title={run.mission_identifier ? `Working on ${run.mission_identifier}` : run.trigger_type === "chat" ? "Replying in a chat" : "Working"}
        detail={run.started_at ? `Started ${formatShortDate(run.started_at)}` : null}
        href={run.mission_identifier ? `/issues/${encodeURIComponent(run.mission_identifier)}` : undefined} />)}
      {decisions?.map((item) => <NowRow key={item.id} icon={Hand} tone="text-warn" label="Waiting for a decision" title={item.reason} href="/inbox" />)}
      {queue?.map((item) => <NowRow key={item.id} icon={Clock} tone="text-muted-foreground" label="Queued" title={item.task}
        detail={(item.queued_reason && queuedReason[item.queued_reason]) || (item.assigned_by_name ? `From ${item.assigned_by_name}` : null)} />)}
    </ul>
  </section>
}

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
  const agent = useChatAgent()
  const crewId = agent?.crewId ?? null
  const agentSlug = agent?.slug ?? null
  const key = JSON.stringify([workspaceId, agentId, crewId, agentSlug])
  const [data, setData] = useState<WorkData | null>(null)
  const [revision, setRevision] = useState(0)
  const debounce = useRef<ReturnType<typeof setTimeout> | null>(null)
  const lastFetch = useRef(0)
  const tabId = useId()
  useEffect(() => { if (source) setView(source.kind === "routine" ? "routines" : "issues") }, [source])

  useEffect(() => {
    if (!workspaceId) return
    lastFetch.current = Date.now()
    const controller = new AbortController()
    const { signal } = controller
    const ws = encodeURIComponent(workspaceId)
    const who = encodeURIComponent(agentId)
    const crew = crewId ? encodeURIComponent(crewId) : null
    const noCrew = Promise.reject(new Error("no crew"))
    noCrew.catch(() => undefined)
    void Promise.allSettled([
      apiFetch(`/api/v1/issues?workspace_id=${ws}&assignee_id=${who}&status=${OPEN_ISSUE_STATUSES.join(",")}&limit=${ISSUE_PAGE}`, { signal })
        .then(async (res) => ({ rows: await readJSON(res), total: Number(res.headers?.get?.("X-Total-Count") ?? NaN) })),
      apiFetch(`/api/v1/workspaces/${ws}/pipelines?order=recent&limit=100`, { signal }).then(readJSON),
      apiFetch(`/api/v1/runs?workspace_id=${ws}&agent_id=${who}&status=RUNNING&limit=20`, { signal }).then(readJSON),
      // Known limit: the crew assignments API has no agent or status filter
      // (assignments.go List), so this reads the crew's 100 newest rows and
      // filters here. In a very busy crew an older queued row can be missed.
      crew ? apiFetch(`/api/v1/crews/${crew}/assignments?workspace_id=${ws}&limit=100`, { signal }).then(readJSON) : noCrew,
      crew ? apiFetch(`/api/v1/crews/${crew}/escalations?workspace_id=${ws}&status=PENDING&limit=100`, { signal }).then(readJSON) : noCrew,
    ]).then(([issueRes, routineRes, runRes, queueRes, decisionRes]) => {
      if (signal.aborted) return
      const issuePage = settled(issueRes, (value) => {
        const page = value as { rows: unknown; total: number }
        if (!Array.isArray(page.rows)) throw new Error("shape")
        return { rows: (page.rows as IssueRow[]).filter((row) => OPEN_ISSUE_STATUSES.includes((row.status || "BACKLOG").toUpperCase())), total: page.total }
      })
      setData({
        key,
        issues: issuePage?.rows ?? null,
        // The count comes from the same filtered query (X-Total-Count).
        issueTotal: issuePage ? (Number.isFinite(issuePage.total) && issuePage.total >= issuePage.rows.length ? issuePage.total : issuePage.rows.length) : 0,
        routines: settled(routineRes, (value) => Array.isArray(value) ? (value as RoutineRow[]).filter((row) => row.author_agent_id === agentId) : []),
        runs: settled(runRes, (value) => {
          const rows = (value as { data?: RunRow[] } | null)?.data
          return Array.isArray(rows) ? rows.filter((row) => !row.status || row.status.toUpperCase() === "RUNNING") : []
        }),
        queue: settled(queueRes, (value) => Array.isArray(value)
          ? (value as AssignmentRow[]).filter((row) => !!agentSlug && row.assigned_to_slug === agentSlug && QUEUED_ASSIGNMENT.has((row.status || "").toUpperCase()))
          : []),
        decisions: settled(decisionRes, (value) => Array.isArray(value)
          ? (value as EscalationRow[]).filter((row) => !!agentSlug && row.from_slug === agentSlug && (row.status || "PENDING").toUpperCase() === "PENDING")
          : []),
      })
    })
    return () => controller.abort()
  }, [agentId, workspaceId, crewId, agentSlug, key, revision])

  // Realtime events only invalidate. Events for other agents are skipped;
  // bursts coalesce (trailing debounce, capped so a steady stream still
  // refreshes), and the safety poll stays quiet while events keep it fresh.
  const scope = useRef({ agentId, agentSlug, issueKeys: new Set<string>() })
  useEffect(() => {
    scope.current = { agentId, agentSlug, issueKeys: new Set((data?.key === key ? data.issues ?? [] : []).flatMap((row) => [row.id, row.identifier ?? ""]).filter(Boolean)) }
  }, [agentId, agentSlug, data, key])
  const firstPending = useRef<number | null>(null)
  const refresh = useCallback((event?: { payload?: Record<string, unknown> }) => {
    if (event && !workEventConcernsAgent(event.payload, scope.current)) return
    const now = Date.now()
    firstPending.current ??= now
    if (debounce.current) clearTimeout(debounce.current)
    const wait = Math.max(0, Math.min(WORK_DEBOUNCE_MS, firstPending.current + WORK_MAX_WAIT_MS - now))
    debounce.current = setTimeout(() => { debounce.current = null; firstPending.current = null; setRevision((n) => n + 1) }, wait)
  }, [])
  const reload = useCallback(() => setRevision((n) => n + 1), [])
  useEffect(() => () => { if (debounce.current) clearTimeout(debounce.current) }, [])
  useEffect(() => {
    if (!workspaceId) return
    const timer = setInterval(() => {
      if (document.visibilityState === "hidden" || Date.now() - lastFetch.current < WORK_POLL_MS) return
      setRevision((n) => n + 1)
    }, WORK_POLL_MS / 4)
    return () => clearInterval(timer)
  }, [workspaceId, key])
  useRealtimeEventSafe("issue.created", refresh)
  useRealtimeEventSafe("issue.updated", refresh)
  useRealtimeEventSafe("issue.status_changed", refresh)
  useRealtimeEventSafe("issue.started", refresh)
  useRealtimeEventSafe("issue.deleted", refresh)
  useRealtimeEventSafe("issues.bulk_updated", refresh)
  useRealtimeEventSafe("issue.session.state", refresh)
  useRealtimeEventSafe("run.outcome", refresh)
  useRealtimeEventSafe("run.started", refresh)
  useRealtimeEventSafe("run.completed", refresh)
  useRealtimeEventSafe("run.failed", refresh)
  useRealtimeEventSafe("assignment.updated", refresh)
  useRealtimeEventSafe("assignment_queued", refresh)
  useRealtimeEventSafe("assignment_unqueued", refresh)
  useRealtimeEventSafe("task.updated", refresh)
  useRealtimeEventSafe("escalation.created", refresh)
  useRealtimeEventSafe("escalation.resolved", refresh)
  useRealtimeEventSafe("escalation.cancelled", refresh)
  useRealtimeEventSafe("escalation.expired", refresh)
  useRealtimeEventSafe("realtime.reconnected", reload)

  // A result for another agent or workspace is never painted (switch mid-request).
  const current = data?.key === key ? data : null
  const loading = !!workspaceId && !current
  const error = !!current && current.issues === null
  const issues = current?.issues ?? []
  const routines = current?.routines ?? []
  const groups = issueGroups(issues)
  return <div className="@container p-3 text-xs">
    {source && <section aria-label="Source of this conversation" className="mb-4 rounded-lg border border-primary/25 bg-primary/5 p-3"><p className="mb-1 text-[10px] text-muted-foreground">Source of this conversation</p><Link className="text-primary hover:underline" href={source.kind === "routine" ? `/routines?slug=${encodeURIComponent(source.slug || source.id)}${source.run_id ? `&run=${encodeURIComponent(source.run_id)}` : ""}` : `/issues/${encodeURIComponent(source.slug || source.id)}`}>{source.name} ↗</Link>{source.step_id && <p className="mt-1 text-[10px] text-muted-foreground">Step: {source.step_id}</p>}<WorkConversations agentId={agentId} workspaceId={workspaceId} routineId={source.kind === "routine" ? source.id : undefined} issueId={source.kind === "issue" ? source.id : undefined} /></section>}
    {current && <WorkNow runs={current.runs} queue={current.queue} decisions={current.decisions} />}
    <div role="tablist" aria-label="Agent work type" className="mb-3 grid grid-cols-2 gap-1 rounded-lg bg-muted/40 p-1">
      {(["issues", "routines"] as const).map((kind) => {
        const Icon = kind === "issues" ? CircleDot : Workflow
        return <button key={kind} id={`${tabId}-${kind}`} type="button" role="tab" aria-selected={view === kind} aria-controls={`${tabId}-panel`} onClick={() => setView(kind)} className={cn("kit-tap flex min-h-8 items-center justify-center gap-1.5 rounded-md px-2 text-[11px] transition-colors", view === kind ? "bg-card font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground hover:bg-card/50")}>
          <Icon className="size-3.5" />{kind === "issues" ? "Issues" : "Routines"}<span className="rounded-full bg-foreground/[0.06] px-1.5 text-[10px] tabular-nums text-muted-foreground">{kind === "issues" ? (current?.issueTotal ?? 0) : routines.length}</span>
        </button>
      })}
    </div>
    <div id={`${tabId}-panel`} role="tabpanel" aria-labelledby={`${tabId}-${view}`}>
      {loading && <p role="status" className="py-2 text-muted-foreground">Loading agent work…</p>}
      {error && <div role="alert" className="space-y-2 rounded-lg border border-destructive/25 p-2.5 text-muted-foreground"><p>Work could not be loaded.</p><button type="button" onClick={() => setRevision((n) => n + 1)} className="text-primary hover:underline">Try again</button></div>}
      {!workspaceId && !loading && !error && <p className="rounded-lg border border-dashed p-2.5 text-muted-foreground">Select a workspace to see agent work.</p>}
      {workspaceId && !loading && !error && view === "issues" && (groups.length ? <div className="grid grid-cols-1 items-start gap-3">{groups.map(({ status, issues: groupIssues }) => <IssueGroup key={`${agentId}:${status}`} status={status} issues={groupIssues} agentId={agentId} workspaceId={workspaceId} />)}{(current?.issueTotal ?? 0) > issues.length && <Link href={`/issues?assignee_id=${encodeURIComponent(agentId)}&status=${OPEN_ISSUE_STATUSES.join(",")}`} className="px-1 text-micro text-primary hover:underline">Show all {current?.issueTotal} open issues</Link>}</div> : <p className="rounded-lg border border-dashed p-2.5 text-muted-foreground">No open issues assigned to this agent.</p>)}
      {workspaceId && !loading && !error && view === "routines" && (current?.routines === null ? <p role="alert" className="rounded-lg border border-dashed p-2.5 text-muted-foreground">Routines could not be loaded.</p> : routines.length ? <ul className="space-y-1.5">{routines.map((routine) => <RoutineCard key={routine.id} routine={routine} agentId={agentId} workspaceId={workspaceId} />)}</ul> : <p className="rounded-lg border border-dashed p-2.5 text-muted-foreground">No routines created by this agent.</p>)}
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
