"use client"

// The three streams behind an issue's timeline (#2983), each on its own
// cursor: the event log by seq (before_seq), comments by id (before_id), runs
// by offset. Read through the same issue endpoints /issues uses, so the access
// they enforce is the access this has — nothing here widens what a reader sees.

import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"
import { mergeTimeline, type TimelineItem, type TimelineStream } from "@/lib/issue-timeline"

const PAGE = 30

export interface IssueMeta {
  id: string
  identifier?: string | null
  title?: string
  status?: string
  crew_id: string
}

interface EventRow {
  id: string
  seq: number
  actor_type: string
  actor_name?: string | null
  action: string
  details?: string | null
  created_at: string
}
interface CommentRow {
  id: string
  author_type: string
  author_name?: string
  body: string
  created_at: string
}
interface RunRow {
  id: string
  run_id?: string
  trace_id?: string
  status: string
  agent_name?: string
  task?: string
  started_at?: string
  ended_at?: string
  result_summary?: string
  error_message?: string
}

type Cursor = { events: number | null; comments: string | null; runs: number }

const EMPTY: TimelineStream = { items: [], exhausted: false, loaded: false }

export function humanAction(action: string): string {
  const s = action.replace(/[_.]/g, " ").trim()
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : "Changed"
}

function fromEvent(e: EventRow): TimelineItem {
  return {
    kind: "event",
    key: `ev:${e.id}`,
    at: e.created_at,
    title: humanAction(e.action),
    detail: e.details ?? undefined,
    actor: e.actor_name || e.actor_type,
    action: e.action,
  }
}
function fromComment(c: CommentRow): TimelineItem {
  return {
    kind: "comment",
    key: `cm:${c.id}`,
    at: c.created_at,
    title: "Comment",
    detail: c.body,
    actor: c.author_name || c.author_type,
  }
}
function fromRun(r: RunRow): TimelineItem {
  return {
    kind: "run",
    key: `run:${r.id}`,
    at: r.started_at || r.ended_at || "",
    title: r.task || "Agent run",
    detail: r.error_message || r.result_summary || undefined,
    actor: r.agent_name,
    action: r.status,
    runId: r.run_id || r.trace_id || undefined,
  }
}

export function useIssueTimeline(workspaceId: string, issueId: string) {
  const [issue, setIssue] = useState<IssueMeta | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [streams, setStreams] = useState<{ events: TimelineStream; comments: TimelineStream; runs: TimelineStream }>({
    events: EMPTY,
    comments: EMPTY,
    runs: EMPTY,
  })
  const cursor = useRef<Cursor>({ events: 0, comments: null, runs: 0 })
  const [busy, setBusy] = useState(false)

  const ws = `workspace_id=${encodeURIComponent(workspaceId)}`

  useEffect(() => {
    const ctrl = new AbortController()
    setIssue(null)
    setError(null)
    setStreams({ events: EMPTY, comments: EMPTY, runs: EMPTY })
    cursor.current = { events: 0, comments: null, runs: 0 }
    apiFetch(`/api/v1/issues/${encodeURIComponent(issueId)}?${ws}`, { signal: ctrl.signal })
      .then(async (r) => {
        if (!r.ok) throw new Error(r.status === 404 ? "This issue is not available." : `HTTP ${r.status}`)
        return (await r.json()) as IssueMeta
      })
      .then((m) => {
        if (!ctrl.signal.aborted) setIssue(m)
      })
      .catch((e: unknown) => {
        if (!ctrl.signal.aborted) setError(e instanceof Error ? e.message : "Could not load the issue.")
      })
    return () => ctrl.abort()
  }, [issueId, ws])

  const base = issue ? `/api/v1/crews/${encodeURIComponent(issue.crew_id)}/issues/${encodeURIComponent(issue.id)}` : null

  const loadPage = useCallback(
    async (which: "events" | "comments" | "runs") => {
      if (!base) return
      const c = cursor.current
      if (which === "events") {
        if (c.events == null) return
        const r = await apiFetch(`${base}/events?${ws}&before_seq=${c.events}&limit=${PAGE}`)
        if (!r.ok) throw new Error(`events: HTTP ${r.status}`)
        const body = (await r.json()) as { events: EventRow[]; has_older: boolean }
        const rows = body.events ?? []
        const oldest = rows.reduce((m, e) => Math.min(m, e.seq), Number.POSITIVE_INFINITY)
        cursor.current.events = body.has_older && Number.isFinite(oldest) ? oldest : null
        setStreams((s) => ({
          ...s,
          events: { items: [...s.events.items, ...rows.map(fromEvent)], exhausted: !body.has_older, loaded: true },
        }))
      } else if (which === "comments") {
        const before = c.comments ? `&before_id=${encodeURIComponent(c.comments)}` : ""
        const r = await apiFetch(`${base}/comments?${ws}&page_size=${PAGE}${before}`)
        if (!r.ok) throw new Error(`comments: HTTP ${r.status}`)
        const rows = ((await r.json()) as CommentRow[]) ?? []
        const more = r.headers.get("X-Has-More") === "true"
        // Rows come back oldest first within the page.
        cursor.current.comments = more && rows.length > 0 ? rows[0].id : null
        setStreams((s) => ({
          ...s,
          comments: { items: [...s.comments.items, ...rows.map(fromComment)], exhausted: !more, loaded: true },
        }))
      } else {
        const r = await apiFetch(`${base}/runs?${ws}&limit=${PAGE}&offset=${c.runs}`)
        if (!r.ok) throw new Error(`runs: HTTP ${r.status}`)
        const rows = ((await r.json()) as RunRow[]) ?? []
        const total = Number(r.headers.get("X-Total-Count") ?? NaN)
        const next = c.runs + rows.length
        cursor.current.runs = next
        const exhausted = rows.length < PAGE || (Number.isFinite(total) && next >= total)
        setStreams((s) => ({
          ...s,
          runs: { items: [...s.runs.items, ...rows.map(fromRun)], exhausted, loaded: true },
        }))
      }
    },
    [base, ws],
  )

  useEffect(() => {
    if (!base) return
    setBusy(true)
    Promise.all([loadPage("events"), loadPage("comments"), loadPage("runs")])
      .catch((e: unknown) => setError(e instanceof Error ? e.message : "Could not load the history."))
      .finally(() => setBusy(false))
  }, [base, loadPage])

  const merged = useMemo(() => mergeTimeline([streams.events, streams.comments, streams.runs]), [streams])

  const loadOlder = useCallback(async () => {
    setBusy(true)
    try {
      const pending = (["events", "comments", "runs"] as const).filter((k) => !streams[k].exhausted)
      await Promise.all(pending.map((k) => loadPage(k)))
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not load older history.")
    } finally {
      setBusy(false)
    }
  }, [loadPage, streams])

  return { issue, error, items: merged.items, loading: merged.loading || (!issue && !error), canLoadOlder: merged.canLoadOlder, busy, loadOlder }
}
