"use client"

import { useEffect, useRef, useState } from "react"
import { Send } from "lucide-react"

import { seedColor } from "@/lib/agent-avatar"
import { apiFetch } from "@/lib/api-fetch"

import type { BottomPanelContext } from "./types"
import { EmptyState, formatRelative } from "./shared"

// Mirror of internal/api/issue_handler.go commentResponse.
interface Comment {
  id: string
  mission_id: string
  author_type: string
  author_id: string
  author_name?: string
  body: string
  created_at: string
  updated_at: string
}

/**
 * Comments — discussion thread on the selected issue/mission (humans +
 * agents). Distinct from the agent peer-inbox "Messages" tab. Reads/writes
 * the existing GET/POST /api/v1/crews/{crewId}/issues/{identifier}/comments.
 */
export function CommentsTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelContext }) {
  const [comments, setComments] = useState<Comment[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [draft, setDraft] = useState("")
  const [sending, setSending] = useState(false)
  const endRef = useRef<HTMLDivElement | null>(null)

  const isMission = context?.kind === "mission"
  const crewId = isMission ? context.crewId : null
  const identifier = isMission ? context.identifier : null
  const base = crewId && identifier
    ? `/api/v1/crews/${crewId}/issues/${encodeURIComponent(identifier)}/comments?workspace_id=${workspaceId}`
    : null

  useEffect(() => {
    if (!isMission || !base) return
    let cancelled = false
    setComments(null)
    setError(null)
    apiFetch(base)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => { if (!cancelled) setComments(Array.isArray(data) ? data : []) })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    // Ignore a slow response after the user switches issues — otherwise the
    // previous thread's comments can overwrite the current one.
    return () => { cancelled = true }
  }, [isMission, base])

  useEffect(() => { endRef.current?.scrollIntoView({ behavior: "smooth" }) }, [comments?.length])

  const send = async () => {
    if (!base || !draft.trim() || sending) return
    setSending(true)
    setError(null)
    try {
      const postUrl = base.split("?")[0] + `?workspace_id=${workspaceId}`
      const r = await apiFetch(postUrl, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ body: draft.trim() }),
      })
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const created: Comment = await r.json()
      setComments((prev) => [...(prev ?? []), created])
      setDraft("")
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSending(false)
    }
  }

  if (!context) return <EmptyState>Select an issue to see its comments.</EmptyState>
  if (context.kind !== "mission") return <EmptyState>Comments are per-issue — select one.</EmptyState>
  if (error && comments === null) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (comments === null) return <EmptyState>Loading…</EmptyState>

  return (
    <div className="h-full flex flex-col">
      <div className="flex-1 min-h-0 overflow-y-auto p-3 text-xs">
        {comments.length === 0 && (
          <div className="text-muted-foreground text-center py-4">No comments yet. Start the discussion.</div>
        )}
        {comments.map((c) => {
          const name = c.author_name || c.author_type || "?"
          return (
            <div key={c.id} className="flex gap-2.5 py-2 border-b border-white/5 last:border-0">
              <span
                className="h-6 w-6 rounded-md shrink-0 grid place-items-center text-[10px] font-semibold text-white"
                style={{ background: seedColor(name) }}
              >
                {name.slice(0, 1).toUpperCase()}
              </span>
              <div className="min-w-0">
                <div className="text-muted-foreground text-[11px] mb-0.5">
                  <span className="text-foreground font-medium mr-1.5">{name}</span>
                  {c.author_type === "agent" && <span className="mr-1.5 text-emerald-300/70">agent</span>}
                  {formatRelative(c.created_at)}
                </div>
                <div className="text-foreground/85 whitespace-pre-wrap break-words">{c.body}</div>
              </div>
            </div>
          )
        })}
        <div ref={endRef} />
      </div>
      {/* Send failures surface here — the load-error path returns early
          above, so once the thread is shown this is the only place a
          failed POST can report itself. */}
      {error && comments !== null && (
        <div className="shrink-0 px-3 py-1.5 text-[11px] text-red-300 border-t border-red-500/20 bg-red-500/5">
          Failed to send: {error}
        </div>
      )}
      <div className="shrink-0 flex gap-2 p-2 border-t border-white/8">
        <input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send() } }}
          placeholder="Write a comment…"
          aria-label="Write a comment"
          className="flex-1 bg-background border border-white/10 rounded-md px-3 py-1.5 text-xs text-foreground placeholder:text-muted-foreground focus:outline-none focus:border-blue-500/50"
        />
        <button
          type="button"
          onClick={send}
          disabled={sending || !draft.trim()}
          className="px-3 rounded-md bg-blue-600 text-white text-xs flex items-center gap-1.5 disabled:opacity-40"
        >
          <Send className="h-3 w-3" /> Send
        </button>
      </div>
    </div>
  )
}
