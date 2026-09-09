"use client"

import Link from "next/link"
import { useState } from "react"
import { Activity, CheckCircle2, CircleAlert, MessageSquare, Sparkles, Users, Inbox, Wallet, Play } from "lucide-react"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { StatusDonut } from "@/components/features/dashboard/status-donut"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { WorkspaceEmpty, WorkspaceGlyph } from "./workspace-visuals"
import { AssignedConnected } from "./assigned-connected"
import { Button } from "@/components/ui/button"
import { useWorkspaceResource } from "./memory-workspace"
import type { AgentRecord, ChatRow, InboxSummary, PeerMessageRow, RunRow } from "./agent-canvas-tabs/types"

export function RunMetrics({ workspaceId, agentId, crewId, revision = 0, cost }: { workspaceId: string; agentId?: string; crewId?: string; revision?: number; cost?: number }) {
  const [retry, setRetry] = useState(0)
  const query = new URLSearchParams({ workspace_id: workspaceId, window: "7d" })
  if (agentId) query.set("agent_id", agentId)
  if (crewId) query.set("crew_id", crewId)
  const { data, error } = useWorkspaceResource<{ totals: { succeeded: number; failed: number; running: number }; truncated: boolean; by_trigger?: { key: string; total: number }[] }>(`/api/v1/runs/insights?${query}`, revision + retry, true)
  const totals = data?.totals
  return <section aria-label="Runs in the last 7 days" className="space-y-3">
    <div className="flex flex-wrap items-center justify-between gap-2"><p className="text-xs text-muted-foreground">Agent runs started in the last 7 days{data?.truncated ? " · most recent 20,000 runs only" : ""}</p>{typeof cost === "number" && <span className="inline-flex items-center gap-2 rounded-lg bg-success/5 px-3 py-1.5 text-xs text-muted-foreground" title="Recorded spending in the current calendar month (UTC)"><Wallet aria-hidden="true" className="h-3.5 w-3.5 text-success" /><span className="font-medium tabular-nums text-foreground">${cost.toFixed(2)}</span> this month · UTC</span>}</div>
    {error ? <p role="alert" className="text-sm text-muted-foreground">Run metrics are unavailable. <Button size="sm" variant="ghost" onClick={() => setRetry(n => n + 1)}>Retry metrics</Button></p> : <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <KpiCard label="Completed runs" value={totals?.succeeded ?? "—"} subtitle="Last 7 days" valueColor="text-success" />
      <KpiCard label="Failed or timed out" value={totals?.failed ?? "—"} subtitle="Last 7 days" valueColor={totals?.failed ? "text-destructive" : undefined} />
      <KpiCard label="Still running" value={totals?.running ?? "—"} subtitle="Started in the last 7 days" valueColor={totals?.running ? "text-primary" : undefined} />
    </div>}
    {!error && totals && totals.succeeded + totals.failed + totals.running > 0 && <div className="grid min-w-0 gap-4 xl:grid-cols-2"><DashboardCard title="Run outcomes" icon={Activity} hint="Last 7 days" className="min-w-0"><StatusDonut centerLabel="runs" data={[
      { key: "succeeded", label: "Completed", count: totals.succeeded, color: "var(--success)" },
      { key: "failed", label: "Failed or timed out", count: totals.failed, color: "var(--destructive)" },
      { key: "running", label: "Running", count: totals.running, color: "var(--primary)" },
    ]} /><p className="mt-3 text-xs text-muted-foreground">Excludes cancelled runs</p></DashboardCard><RunStartBreakdown rows={data?.by_trigger} /></div>}
  </section>
}

function RunStartBreakdown({ rows }: { rows?: { key: string; total: number }[] }) {
  const sorted = [...(rows ?? [])].filter(row => row.total > 0).sort((a, b) => b.total - a.total)
  const total = sorted.reduce((sum, row) => sum + row.total, 0)
  const visible = sorted.length > 5 ? [...sorted.slice(0, 4), { key: "__other", total: sorted.slice(4).reduce((sum, row) => sum + row.total, 0) }] : sorted
  const labels: Record<string, string> = { CHAT: "Chat", MANUAL: "Manual", SCHEDULE: "Schedule", SCHEDULED: "Schedule", CRON: "Schedule", WEBHOOK: "Webhook", API: "API", ASSIGNMENT: "Assignment", UNKNOWN: "Not recorded", __OTHER: "Other" }
  return <DashboardCard title="How runs start" icon={Play} hint="Last 7 days" className="min-w-0 flex flex-col">
    {total ? <><ul className="flex-1 space-y-3 py-2">{visible.map(row => {
      const label = labels[row.key.toUpperCase()] ?? row.key.replaceAll("_", " ").toLowerCase()
      return <li key={row.key}><div className="mb-1.5 flex items-center justify-between gap-3 text-xs"><span className="truncate capitalize">{label}</span><span className="font-mono tabular-nums text-muted-foreground">{row.total}</span></div><div role="meter" aria-label={label} aria-valuenow={row.total} aria-valuemin={0} aria-valuemax={total} className="h-2 overflow-hidden rounded-full bg-primary/10"><div className="h-full rounded-full bg-primary/70" style={{ width: `${row.total / total * 100}%` }} /></div></li>
    })}</ul><p className="mt-3 text-xs text-muted-foreground">Includes cancelled runs</p></> : <WorkspaceEmpty icon={Play} title="Run sources are not available yet." />}
  </DashboardCard>
}

export function AgentOverview({ workspaceId, agent, inbox, runs, chats, peerMessages, error, onRetry, onWork, revision }: {
  workspaceId: string; agent: AgentRecord; inbox: InboxSummary; runs: RunRow[] | null; chats: ChatRow[] | null;
  peerMessages: PeerMessageRow[]; error?: string | null; onRetry: () => void; onWork: () => void; revision: number
}) {
  const active = runs?.filter((run) => run.status === "RUNNING") ?? []
  const outcomes = runs?.filter((run) => run.status !== "RUNNING").slice(0, 5)
  return <div className="space-y-6 max-w-7xl">
    {error && <div role="alert" className="rounded-xl border border-border p-4 text-sm">{error} <Button variant="ghost" size="sm" onClick={onRetry}>Retry</Button></div>}
    {inbox.count > 0 && <div className="rounded-2xl border border-warn/30 bg-warn/5 p-4 flex flex-wrap items-center justify-between gap-3"><div><h2 className="font-medium flex items-center gap-2"><Inbox className="h-4 w-4" />Needs a decision</h2><p className="text-sm text-muted-foreground">{inbox.summary}</p></div><Button asChild size="sm"><Link href={`/inbox?agent=${encodeURIComponent(agent.slug)}`}>Review in Inbox</Link></Button></div>}
    <RunMetrics workspaceId={workspaceId} agentId={agent.id} revision={revision} cost={inbox.cost} />
    <DashboardCard title="Current work" icon={Activity} className="bg-gradient-to-br from-primary/5 via-card to-card" action={<button className="text-primary" onClick={onWork}>All work →</button>}>{runs === null ? <p className="mt-3 text-sm text-muted-foreground">{error ? "Current work unavailable." : "Loading current work…"}</p> : active.length ? active.slice(0, 3).map((run) => <Link key={run.id} href={`/journal?agent=${encodeURIComponent(agent.slug)}&trace_id=${encodeURIComponent(run.id)}`} className="flex items-center gap-3 mt-3 rounded-xl p-2 hover:bg-muted/50 text-sm"><AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style || agent.crew?.avatar_style} avatarUrl={agent.avatar_url} className="h-10 w-10" /><span>{run.trigger_type || "Agent"} run<span className="block text-xs text-primary mt-1">● Running · Open execution</span></span></Link>) : <WorkspaceEmpty icon={Sparkles} title={agent.status === "RUNNING" ? "This agent is running. Open Journal for its current execution." : agent.llm_model ? "Ready for the next idea" : "Choose a model to get started"} description={agent.llm_model ? "Start a conversation or assign an issue." : "Use Edit to choose a provider and model."} />}</DashboardCard>
    <div className="grid gap-4 xl:grid-cols-2">
      <DashboardCard title="Recent outcomes" icon={CheckCircle2} action={<Link className="text-primary" href={`/journal?agent=${encodeURIComponent(agent.slug)}`}>Journal ↗</Link>}>{outcomes === undefined ? <p className="mt-4 text-sm text-muted-foreground">{error ? "Outcomes unavailable." : "Loading outcomes…"}</p> : !outcomes.length ? <WorkspaceEmpty icon={CheckCircle2} title="Completed work will appear here." /> : <ul className="mt-3 divide-y divide-border">{outcomes.map((run) => <li key={run.id} className="py-3 flex items-start gap-3"><WorkspaceGlyph icon={run.status === "COMPLETED" ? CheckCircle2 : CircleAlert} tone={run.status === "COMPLETED" ? "green" : "amber"} /><div className="min-w-0"><Link className="text-sm" href={`/journal?agent=${encodeURIComponent(agent.slug)}&trace_id=${encodeURIComponent(run.id)}`}>{run.trigger_type || "Agent"} run · {run.status.toLowerCase()}</Link>{run.error_message && <p className="mt-1 text-xs text-muted-foreground line-clamp-2">{run.error_message}</p>}<p className="mt-1 text-xs text-muted-foreground">{new Date(run.finished_at ?? run.created_at).toLocaleString()}</p></div></li>)}</ul>}</DashboardCard>
      <DashboardCard title="Recent conversations" icon={MessageSquare}>{chats === null ? <p className="mt-4 text-sm text-muted-foreground">{error ? "Conversations unavailable." : "Loading conversations…"}</p> : !chats.length ? <WorkspaceEmpty icon={MessageSquare} title="Conversations with this agent will appear here." /> : <ul className="mt-3 divide-y divide-border">{chats.slice(0, 5).map((chat) => <li key={chat.id} className="py-3 flex items-start gap-3"><WorkspaceGlyph icon={MessageSquare} /><div className="min-w-0"><Link href={`/chat/${encodeURIComponent(agent.slug)}?session=${encodeURIComponent(chat.id)}`} className="text-sm">{chat.title || "Conversation"}</Link><p className="mt-1 text-xs text-muted-foreground">{chat.message_count} messages · {new Date(chat.last_activity_at ?? chat.created_at).toLocaleString()}</p></div></li>)}</ul>}
        {!!peerMessages.length && <details className="mt-4 border-t border-border pt-3"><summary className="text-sm cursor-pointer"><Users className="inline h-4 w-4 mr-2" />Recent agent collaboration</summary>{peerMessages.slice(0, 3).map((message, index) => <div key={message.id ?? index} className="mt-3"><p className="text-xs text-muted-foreground">{message.direction === "outgoing" ? `To ${message.to_agent_name ?? message.to_agent_slug ?? "agent"}` : `From ${message.from_agent_name ?? message.from_agent_slug ?? "agent"}`}</p><p className="text-sm line-clamp-2">{message.response || message.question}</p></div>)}</details>}
      </DashboardCard>
    </div>
    <AssignedConnected workspaceId={workspaceId} agentId={agent.id} crewId={agent.crew_id ?? undefined} slug={agent.slug} name={agent.name} revision={revision} />
  </div>
}
