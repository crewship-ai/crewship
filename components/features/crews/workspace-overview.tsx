"use client"

import Link from "next/link"
import { Button } from "@/components/ui/button"
import { useWorkspaceResource } from "./memory-workspace"
import type { AgentRecord, ChatRow, InboxSummary, PeerMessageRow, RunRow } from "./agent-canvas-tabs/types"

export function RunMetrics({ workspaceId, agentId, crewId, revision = 0 }: { workspaceId: string; agentId?: string; crewId?: string; revision?: number }) {
  const query = new URLSearchParams({ workspace_id: workspaceId, window: "7d" })
  if (agentId) query.set("agent_id", agentId)
  if (crewId) query.set("crew_id", crewId)
  const { data, error } = useWorkspaceResource<{ totals: { succeeded: number; failed: number; running: number }; truncated: boolean }>(`/api/v1/runs/insights?${query}`, revision)
  return <section aria-label="Runs in the last 7 days" className="space-y-2"><p className="text-xs text-muted-foreground">Agent runs started in the last 7 days{data?.truncated ? " · most recent 20,000 runs only" : ""}</p>{error ? <p role="alert" className="text-sm text-muted-foreground">Run metrics are unavailable.</p> : <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">{[['Completed runs', data?.totals.succeeded], ['Failed or timed out', data?.totals.failed], ['Still running', data?.totals.running]].map(([label, value]) => <div key={label} className="rounded-2xl border border-border bg-card p-4"><p className="text-xs text-muted-foreground">{label}</p><p className="mt-2 text-2xl font-semibold tabular-nums">{value ?? '—'}</p></div>)}</div>}</section>
}

export function AgentOverview({ workspaceId, agent, inbox, runs, chats, peerMessages, error, onRetry, onWork, onEdit, revision }: {
  workspaceId: string; agent: AgentRecord; inbox: InboxSummary; runs: RunRow[] | null; chats: ChatRow[] | null;
  peerMessages: PeerMessageRow[]; error?: string | null; onRetry: () => void; onWork: () => void; onEdit: () => void; revision: number
}) {
  const active = runs?.filter((run) => run.status === "RUNNING") ?? []
  const outcomes = runs?.filter((run) => run.status !== "RUNNING").slice(0, 5)
  return <div className="space-y-6 max-w-7xl">
    {error && <div role="alert" className="rounded-xl border border-border p-4 text-sm">{error} <Button variant="ghost" size="sm" onClick={onRetry}>Retry</Button></div>}
    {inbox.count > 0 && <div className="rounded-2xl border border-warn/30 bg-warn/5 p-4 flex flex-wrap items-center justify-between gap-3"><div><h2 className="font-medium">Needs a decision</h2><p className="text-sm text-muted-foreground">{inbox.summary}</p></div><Button asChild size="sm"><Link href={`/inbox?agent=${encodeURIComponent(agent.slug)}`}>Review in Inbox</Link></Button></div>}
    <section className="rounded-2xl border border-border bg-card p-5"><div className="flex justify-between gap-3"><h2 className="font-medium">Current work</h2><button className="text-sm text-primary" onClick={onWork}>All work →</button></div>{runs === null ? <p className="mt-3 text-sm text-muted-foreground">{error ? "Current work unavailable." : "Loading current work…"}</p> : active.length ? active.slice(0, 3).map((run) => <Link key={run.id} href={`/journal?agent=${encodeURIComponent(agent.slug)}&trace_id=${encodeURIComponent(run.id)}`} className="block mt-3 text-sm">{run.trigger_type || "Agent"} run · Running</Link>) : <p className="mt-3 text-sm text-muted-foreground">{agent.status === "RUNNING" ? "This agent is running. Open Journal for its current execution." : "No run in progress. Start a conversation or assign an issue."}</p>}</section>
    <RunMetrics workspaceId={workspaceId} agentId={agent.id} revision={revision} />
    <div className="grid gap-4 xl:grid-cols-2">
      <section className="rounded-2xl border border-border bg-card p-5"><div className="flex justify-between gap-3"><h2 className="font-medium">Recent outcomes</h2><Link className="text-sm text-primary" href={`/journal?agent=${encodeURIComponent(agent.slug)}`}>Journal ↗</Link></div>{outcomes === undefined ? <p className="mt-4 text-sm text-muted-foreground">{error ? "Outcomes unavailable." : "Loading outcomes…"}</p> : !outcomes.length ? <p className="mt-4 text-sm text-muted-foreground">Completed work will appear here.</p> : <ul className="mt-3 divide-y divide-border">{outcomes.map((run) => <li key={run.id} className="py-3"><Link className="text-sm" href={`/journal?agent=${encodeURIComponent(agent.slug)}&trace_id=${encodeURIComponent(run.id)}`}>{run.trigger_type || "Agent"} run · {run.status.toLowerCase()}</Link>{run.error_message && <p className="mt-1 text-xs text-muted-foreground line-clamp-2">{run.error_message}</p>}<p className="mt-1 text-xs text-muted-foreground">{new Date(run.finished_at ?? run.created_at).toLocaleString()}</p></li>)}</ul>}</section>
      <section className="rounded-2xl border border-border bg-card p-5"><h2 className="font-medium">Recent conversations</h2>{chats === null ? <p className="mt-4 text-sm text-muted-foreground">{error ? "Conversations unavailable." : "Loading conversations…"}</p> : !chats.length ? <p className="mt-4 text-sm text-muted-foreground">Conversations with this agent will appear here.</p> : <ul className="mt-3 divide-y divide-border">{chats.slice(0, 5).map((chat) => <li key={chat.id} className="py-3"><Link href={`/chat/${encodeURIComponent(agent.slug)}?session=${encodeURIComponent(chat.id)}`} className="text-sm">{chat.title || "Conversation"}</Link><p className="mt-1 text-xs text-muted-foreground">{chat.message_count} messages · {new Date(chat.last_activity_at ?? chat.created_at).toLocaleString()}</p></li>)}</ul>}
        {!!peerMessages.length && <details className="mt-4 border-t border-border pt-3"><summary className="text-sm cursor-pointer">Recent agent collaboration</summary>{peerMessages.slice(0, 3).map((message, index) => <div key={message.id ?? index} className="mt-3"><p className="text-xs text-muted-foreground">{message.direction === "outgoing" ? `To ${message.to_agent_name ?? message.to_agent_slug ?? "agent"}` : `From ${message.from_agent_name ?? message.from_agent_slug ?? "agent"}`}</p><p className="text-sm line-clamp-2">{message.response || message.question}</p></div>)}</details>}
      </section>
    </div>
    <div className="rounded-xl border border-border p-4 flex flex-wrap items-center justify-between gap-3"><p className="text-sm text-muted-foreground">{agent.role_title || (agent.agent_role === "LEAD" ? "Team lead" : "Agent")} · {agent.llm_model || "Model not selected"}{typeof inbox.cost === 'number' && ` · $${inbox.cost.toFixed(2)} recorded this month (UTC)`}</p><Button size="sm" variant="ghost" onClick={onEdit}>Edit agent</Button><Button size="sm" variant="ghost" onClick={onWork}>Skills and access</Button></div>
  </div>
}
