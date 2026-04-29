"use client"

import { useMemo, useState } from "react"
import { Search, Terminal, AlertTriangle, MonitorSmartphone } from "lucide-react"
import { cn } from "@/lib/utils"

export interface SessionRow {
  id: string
  title: string | null
  status: string
  message_count: number
  started_at: string
  ended_at: string | null
  /** Origin of the session — backend may emit "CLI", "UI", "WEBHOOK", "CRON",
   *  "AGENT". Older rows without the column return undefined; we fall back
   *  to a heuristic so the sidebar never crashes on missing data. */
  origin?: string | null
  /** Server signal that the last message in the session was an error.
   *  Optional — undefined means we don't know. */
  last_message_error?: boolean | null
}

export interface SessionsSidebarProps {
  sessions: SessionRow[]
  activeSessionId: string | null
  agentSlug: string
  /** Called when the user clicks a session row. Owner is responsible
   *  for updating both local state AND the URL (via history.replaceState
   *  or similar) — this component does not touch the router so layout-
   *  level subtrees (topbar, sidebar) can never be remounted by a swap. */
  onSelect: (sessionId: string) => void
  /** When true the sidebar shows even 0-message sessions. Default false:
   *  empty session rows are noise. Toggleable via the "Show all" link in
   *  the empty-state — useful when the user genuinely wants to clean up. */
  showEmpty?: boolean
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

interface OriginTag {
  label: string
  icon: typeof Terminal
  className: string
}

function originTag(s: SessionRow): OriginTag | null {
  // Honour the explicit field first if backend supplied it. Otherwise
  // we don't guess — we just don't show a tag.
  switch (s.origin) {
    case "CLI":
      return { label: "CLI", icon: Terminal, className: "bg-violet-500/15 text-violet-300" }
    case "UI":
      return { label: "UI", icon: MonitorSmartphone, className: "bg-blue-500/15 text-blue-300" }
    case "WEBHOOK":
      return { label: "Hook", icon: Terminal, className: "bg-amber-500/15 text-amber-300" }
    case "CRON":
      return { label: "Cron", icon: Terminal, className: "bg-amber-500/15 text-amber-300" }
    case "AGENT":
      return { label: "Agent", icon: Terminal, className: "bg-fuchsia-500/15 text-fuchsia-300" }
    default:
      return null
  }
}

/**
 * Left rail of the chat full-page route. Lists recent sessions for the
 * agent with a search box. Empty (0-message) sessions are hidden by
 * default — they're typically auto-created on page load and add noise.
 * Toggle to show them via the empty-state CTA.
 *
 * Click a session → swaps the chat panel via URL ?session=. New session
 * button lives in the header strip above.
 */
export function SessionsSidebar({
  sessions,
  activeSessionId,
  onSelect,
  showEmpty: showEmptyProp,
}: SessionsSidebarProps) {
  const [search, setSearch] = useState("")
  const [showEmptyOverride, setShowEmptyOverride] = useState(showEmptyProp ?? false)
  const showEmpty = showEmptyOverride

  const visible = useMemo(() => {
    let out = sessions
    if (!showEmpty) {
      // Always keep the active session visible even if 0-msg, so the
      // user can see + return to a freshly-created chat they're about
      // to type into.
      out = out.filter((s) => s.message_count > 0 || s.id === activeSessionId)
    }
    if (search.trim()) {
      const q = search.toLowerCase()
      out = out.filter((s) => (s.title ?? "Untitled").toLowerCase().includes(q))
    }
    return out
  }, [sessions, showEmpty, activeSessionId, search])

  const hiddenCount = sessions.length - visible.length

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
        {visible.length === 0 ? (
          <div className="px-3 py-6 text-center text-xs text-muted-foreground space-y-2">
            <p>
              {sessions.length === 0
                ? "No sessions yet — start one above."
                : search.trim()
                  ? "No matches."
                  : "No active sessions."}
            </p>
            {hiddenCount > 0 && !showEmpty && (
              <button
                type="button"
                onClick={() => setShowEmptyOverride(true)}
                className="text-blue-300 hover:underline"
              >
                Show {hiddenCount} empty session{hiddenCount === 1 ? "" : "s"}
              </button>
            )}
          </div>
        ) : (
          <>
            {visible.map((s) => {
              const active = s.id === activeSessionId
              const tag = originTag(s)
              const TagIcon = tag?.icon
              return (
                <button
                  key={s.id}
                  type="button"
                  onClick={() => onSelect(s.id)}
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
                  <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
                    {/* Status pill */}
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
                    {/* Origin tag (CLI / UI / Hook / Cron / Agent) */}
                    {tag && TagIcon && (
                      <span className={cn("px-1 py-0.5 rounded inline-flex items-center gap-0.5", tag.className)}>
                        <TagIcon className="h-2.5 w-2.5" />
                        {tag.label}
                      </span>
                    )}
                    {/* Error marker */}
                    {s.last_message_error && (
                      <span className="px-1 py-0.5 rounded bg-red-500/15 text-red-300 inline-flex items-center gap-0.5">
                        <AlertTriangle className="h-2.5 w-2.5" />
                        Error
                      </span>
                    )}
                    {/* Message count — colored amber on 0 (visible only when override active) */}
                    <span className={cn(s.message_count === 0 && "text-amber-400/70 italic")}>
                      {s.message_count} msg{s.message_count === 1 ? "" : "s"}
                    </span>
                  </div>
                </button>
              )
            })}
            {hiddenCount > 0 && !showEmpty && (
              <button
                type="button"
                onClick={() => setShowEmptyOverride(true)}
                className="w-full text-center px-3 py-2 text-[10px] text-muted-foreground hover:text-foreground hover:bg-white/[0.03] transition-colors"
              >
                + {hiddenCount} empty session{hiddenCount === 1 ? "" : "s"} hidden — show
              </button>
            )}
          </>
        )}
      </div>
    </aside>
  )
}
