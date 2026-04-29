"use client"

import { useEffect, useState } from "react"
import { cn } from "@/lib/utils"

import type { BottomPanelContext, PeerMessage } from "./types"
import { EmptyState, formatTime } from "./shared"

/**
 * Messages — pulls peer messages from the agent inbox. The inbox response
 * also includes escalation/assignment/approval COUNTS (not arrays); those
 * surface in the canvas Inbox banner instead of here.
 */
export function MessagesTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelContext }) {
  const [messages, setMessages] = useState<PeerMessage[] | null>(null)
  const [counters, setCounters] = useState<{ escalations: number; assignments: number; approvals: number } | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!context || context.kind !== "agent") return
    let cancelled = false
    setMessages(null)
    setCounters(null)
    setError(null)
    fetch(`/api/v1/agents/${context.agentId}/inbox?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => {
        if (cancelled) return
        setMessages(Array.isArray(data?.peer_messages) ? data.peer_messages : [])
        setCounters({
          escalations: Number(data?.escalations_open ?? 0),
          assignments: Number(data?.assignments_open ?? 0),
          approvals: Number(data?.approvals_pending ?? 0),
        })
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [context, workspaceId])

  if (!context) return <EmptyState>Select an agent to see its inbox messages.</EmptyState>
  if (context.kind !== "agent") return <EmptyState>Messages are per-agent — select one in the explorer.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (messages === null || counters === null) return <EmptyState>Loading…</EmptyState>

  const totalCounters = counters.escalations + counters.assignments + counters.approvals
  if (messages.length === 0 && totalCounters === 0) {
    return <EmptyState>No messages or escalations for {context.agentName}.</EmptyState>
  }

  return (
    <div className="h-full overflow-y-auto p-3 space-y-1.5 text-xs">
      {totalCounters > 0 && (
        <div className="rounded border border-amber-500/30 bg-amber-500/5 px-3 py-2 flex items-center gap-2">
          <span className="text-amber-300 font-medium">Pending:</span>
          {counters.escalations > 0 && <span className="text-amber-200">{counters.escalations} escalation{counters.escalations === 1 ? "" : "s"}</span>}
          {counters.assignments > 0 && <span className="text-amber-200">{counters.assignments} assignment{counters.assignments === 1 ? "" : "s"}</span>}
          {counters.approvals > 0 && <span className="text-amber-200">{counters.approvals} approval{counters.approvals === 1 ? "" : "s"}</span>}
        </div>
      )}
      {messages.map((m) => <PeerMessageCard key={m.id} m={m} />)}
    </div>
  )
}

/** Single peer-conversation card. Tags: Direction (in/out), Status,
 *  Escalation, Type. Renders both the question and the response (when
 *  COMPLETED) so the user sees the full peer exchange in one place. */
function PeerMessageCard({ m }: { m: PeerMessage }) {
  const status = (m.status ?? "").toUpperCase()
  const statusChip =
    status === "COMPLETED"
      ? { label: "Completed", cls: "bg-emerald-500/15 text-emerald-300" }
      : status === "RUNNING"
        ? { label: "Running", cls: "bg-blue-500/15 text-blue-300" }
        : status === "FAILED"
          ? { label: "Failed", cls: "bg-red-500/15 text-red-300" }
          : { label: "Pending", cls: "bg-amber-500/15 text-amber-300" }
  const directionChip =
    m.direction === "outgoing"
      ? { label: "Sent", cls: "bg-violet-500/15 text-violet-300", icon: "→" }
      : { label: "Received", cls: "bg-blue-500/15 text-blue-300", icon: "←" }
  const peer = m.direction === "outgoing"
    ? (m.to_agent_name ?? "unknown")
    : m.from_agent_name
  return (
    <div className="rounded border border-white/10 bg-zinc-900/40 px-3 py-2 space-y-1.5">
      <div className="flex items-center gap-1.5 flex-wrap">
        <span className={cn("text-[10px] px-1.5 py-px rounded inline-flex items-center gap-0.5", directionChip.cls)}>
          <span className="font-mono">{directionChip.icon}</span>
          {directionChip.label}
        </span>
        <span className={cn("text-[10px] px-1.5 py-px rounded", statusChip.cls)}>
          {statusChip.label}
        </span>
        {m.escalated && (
          <span className="text-[10px] px-1.5 py-px rounded bg-amber-500/15 text-amber-300 inline-flex items-center gap-0.5">
            ⚠ Escalation
          </span>
        )}
        <span className="text-[10px] px-1.5 py-px rounded bg-zinc-800 text-muted-foreground">
          Peer query
        </span>
        <span className="ml-auto text-[10px] text-muted-foreground tabular-nums">
          {formatTime(m.created_at)}
          {m.duration_ms != null && ` · ${(m.duration_ms / 1000).toFixed(1)}s`}
        </span>
      </div>
      <div className="text-blue-300 font-medium text-[11px]">
        {m.direction === "outgoing"
          ? <>→ <span className="text-foreground/85">{peer}</span></>
          : <><span className="text-foreground/85">{peer}</span> →</>}
      </div>
      <div className="text-foreground/85 whitespace-pre-wrap text-xs">{m.question}</div>
      {m.response && (
        <div className="mt-1 pt-1.5 border-t border-white/5">
          <div className="text-[10px] text-muted-foreground mb-0.5">Reply</div>
          <div className="text-foreground/70 whitespace-pre-wrap text-xs italic">{m.response}</div>
        </div>
      )}
    </div>
  )
}
