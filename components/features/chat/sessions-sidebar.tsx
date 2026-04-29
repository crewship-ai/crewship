"use client"

import { useState } from "react"
import { useRouter } from "next/navigation"
import { Search } from "lucide-react"
import { cn } from "@/lib/utils"

export interface SessionsSidebarProps {
  sessions: Array<{
    id: string
    title: string | null
    status: string
    message_count: number
    started_at: string
    ended_at: string | null
  }>
  activeSessionId: string | null
  agentSlug: string
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ""
  const diffMs = Date.now() - d.getTime()
  const m = Math.floor(diffMs / 60000)
  if (m < 1) return "just now"
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  const days = Math.floor(h / 24)
  return `${days}d`
}

/**
 * Left rail of the chat full-page route. Lists recent sessions for the
 * agent with a search box. Click a session → swaps the chat panel via
 * URL ?session=. New session button lives in the header strip above.
 */
export function SessionsSidebar({ sessions, activeSessionId, agentSlug }: SessionsSidebarProps) {
  const router = useRouter()
  const [search, setSearch] = useState("")

  const filtered = search.trim()
    ? sessions.filter((s) => (s.title ?? "Untitled").toLowerCase().includes(search.toLowerCase()))
    : sessions

  return (
    <aside className="border-r border-white/8 bg-card flex flex-col min-h-0">
      <div className="px-3 py-2 border-b border-white/8 flex items-center gap-2">
        <div className="flex-1 flex items-center gap-2 px-2 py-1.5 rounded bg-zinc-900 border border-white/10">
          <Search className="h-3 w-3 text-muted-foreground" />
          <input
            type="search"
            aria-label="Search chat sessions"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search sessions…"
            className="flex-1 bg-transparent text-xs outline-none placeholder:text-muted-foreground/60"
          />
        </div>
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto py-1">
        {filtered.length === 0 ? (
          <div className="px-3 py-6 text-center text-xs text-muted-foreground">
            {sessions.length === 0 ? "No sessions yet — start one above." : "No matches."}
          </div>
        ) : (
          filtered.map((s) => {
            const active = s.id === activeSessionId
            return (
              <button
                key={s.id}
                type="button"
                onClick={() =>
                  router.replace(`/chat/${encodeURIComponent(agentSlug)}?session=${encodeURIComponent(s.id)}`)
                }
                className={cn(
                  "w-full text-left px-3 py-2 hover:bg-white/[0.04] border-l-2 transition-colors",
                  active ? "bg-blue-500/10 border-blue-400" : "border-transparent",
                )}
              >
                <div className="flex items-center justify-between gap-2 mb-0.5">
                  <span className={cn("text-xs truncate", s.title ? "text-foreground" : "text-muted-foreground italic")}>
                    {s.title || "Untitled session"}
                  </span>
                  <span className="text-[10px] text-muted-foreground shrink-0">{formatTime(s.started_at)}</span>
                </div>
                <div className="flex items-center gap-2 text-[10px] text-muted-foreground">
                  <span
                    className={cn(
                      "px-1 py-0.5 rounded",
                      s.status === "ACTIVE"
                        ? "bg-emerald-500/15 text-emerald-300"
                        : "bg-zinc-800 text-muted-foreground",
                    )}
                  >
                    {s.status?.toLowerCase() || "active"}
                  </span>
                  <span>{s.message_count} msg{s.message_count === 1 ? "" : "s"}</span>
                </div>
              </button>
            )
          })
        )}
      </div>
    </aside>
  )
}
