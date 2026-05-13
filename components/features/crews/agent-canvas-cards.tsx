"use client"

import Link from "next/link"
import { ChevronRight } from "lucide-react"
import { cn } from "@/lib/utils"
import type { ChatRow, PeerMessageRow, RunRow } from "./agent-canvas"
import { formatDuration, formatRelative } from "./agent-canvas"

function RecentSessionsCard({ agentSlug, chats }: { agentSlug: string; chats: ChatRow[] | null }) {
  const recent = chats === null ? null : chats.slice(0, 5)
  return (
    <div className="rounded-xl border border-white/8 bg-card overflow-hidden">
      <div className="px-4 py-2.5 border-b border-white/5 flex items-center justify-between">
        <h3 className="text-sm font-semibold">Recent sessions</h3>
        <Link href={`/chat/${encodeURIComponent(agentSlug)}`} className="text-[11px] text-blue-300 hover:underline">
          Open chat →
        </Link>
      </div>
      <div className="divide-y divide-white/5">
        {recent === null ? (
          <div className="px-4 py-6 text-xs text-muted-foreground">Loading…</div>
        ) : recent.length === 0 ? (
          <div className="px-4 py-6 text-xs text-muted-foreground italic">No sessions yet.</div>
        ) : (
          recent.map((c) => (
            <Link
              key={c.id}
              href={`/chat/${encodeURIComponent(agentSlug)}?session=${encodeURIComponent(c.id)}`}
              className="px-4 py-2.5 flex items-center gap-3 hover:bg-white/[0.025]"
            >
              <span className={cn(
                "w-1.5 h-1.5 rounded-full shrink-0",
                c.status === "ACTIVE" ? "bg-emerald-400" : "bg-muted-foreground/50",
              )} />
              <div className="flex-1 min-w-0">
                <div className="text-xs text-foreground truncate">{c.title || "Untitled session"}</div>
                <div className="text-[10px] text-muted-foreground">
                  {formatRelative(c.created_at)} · {c.message_count} message{c.message_count === 1 ? "" : "s"}
                </div>
              </div>
              <ChevronRight className="h-3 w-3 text-muted-foreground/50" />
            </Link>
          ))
        )}
      </div>
    </div>
  )
}


function RecentRunsCard({ agentId, runs }: { agentId: string; runs: RunRow[] | null }) {
  const recent = runs === null ? null : runs.slice(0, 5)
  return (
    <div className="rounded-xl border border-white/8 bg-card overflow-hidden">
      <div className="px-4 py-2.5 border-b border-white/5 flex items-center justify-between">
        <h3 className="text-sm font-semibold">Recent runs</h3>
        <Link href={`/runs?agent_id=${encodeURIComponent(agentId)}`} className="text-[11px] text-blue-300 hover:underline">
          View all →
        </Link>
      </div>
      <div className="divide-y divide-white/5">
        {recent === null ? (
          <div className="px-4 py-6 text-xs text-muted-foreground">Loading…</div>
        ) : recent.length === 0 ? (
          <div className="px-4 py-6 text-xs text-muted-foreground italic">No runs yet.</div>
        ) : (
          recent.map((r) => (
            <div key={r.id} className="px-4 py-2.5 flex items-center gap-3">
              <span className={cn(
                "w-1.5 h-1.5 rounded-full shrink-0",
                r.status === "SUCCESS" ? "bg-emerald-400" :
                r.status === "FAILED" ? "bg-red-400" :
                r.status === "RUNNING" ? "bg-blue-400 animate-pulse" :
                "bg-muted-foreground/50",
              )} />
              <div className="flex-1 min-w-0">
                <div className="text-xs text-foreground truncate">
                  {r.trigger_type.toLowerCase()}{r.error_message ? ` — ${r.error_message}` : ""}
                </div>
                <div className="text-[10px] text-muted-foreground">
                  {formatRelative(r.created_at)}{r.finished_at && r.started_at ? ` · ${formatDuration(r.started_at, r.finished_at)}` : ""} · {r.status.toLowerCase()}
                </div>
              </div>
            </div>
          ))
        )}
      </div>
    </div>
  )
}


function PeersCard({ messages }: { messages: PeerMessageRow[] }) {
  return (
    <section className="rounded-xl border border-white/8 bg-card overflow-hidden">
      <div className="px-4 py-2.5 border-b border-white/5">
        <h3 className="text-sm font-semibold">Crew peers</h3>
      </div>
      <div className="divide-y divide-white/5">
        {messages.slice(0, 4).map((m, i) => (
          <div key={m.id ?? i} className="px-4 py-2.5 flex items-center gap-3">
            <div className="w-7 h-7 rounded-full bg-zinc-700 grid place-items-center text-[10px] shrink-0">
              {m.from_agent_name?.[0] ?? "?"}
            </div>
            <div className="flex-1 min-w-0">
              <div className="text-xs text-foreground truncate">
                <span className="font-medium">{m.from_agent_name ?? "Unknown"}</span>
                {m.preview && <span className="text-muted-foreground"> · {m.preview}</span>}
              </div>
              <div className="text-[10px] text-muted-foreground">
                {m.created_at ? formatRelative(m.created_at) : ""}
              </div>
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}

// =============================================================================
// Skills + Credentials managers (skills tab)
// =============================================================================


export { RecentSessionsCard, RecentRunsCard, PeersCard }
