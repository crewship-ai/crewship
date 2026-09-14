"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

// use-run-executions — the recorded execution rows of one run, grouped by the
// step they belong to.
//
// The rows already existed; only the standalone "Recorded executions and
// attempts" list read them, at the bottom of a tab the reader had to know to
// open. A step's attempts belong to that step, so this hook exists to hand the
// same rows to the step list and let one row carry both "what this step was
// asked to do" and "what actually happened".
//
// Paging: the endpoint returns 100 rows per page. A foreach over a large list
// can exceed that. Load one page for the initial detail; additional pages are
// an explicit reader action. Always disclose a partial execution history.

const MAX_PAGES = 5

export interface RunExecution {
  id: string
  parent_execution_id: string
  step_id: string
  execution_path: string
  attempt: number
  kind: string
  status: string
  agent_slug: string
  model: string
  started_at: string
  ended_at: string
  error: string
  output_bytes: number
}

export interface StepExecutionSummary {
  /** Every recorded row for this step, in the order the server returned them. */
  rows: RunExecution[]
  /** The latest row — the one whose status the step row reports. */
  latest: RunExecution
  /** Highest recorded attempt number. 1 means "no retry happened". */
  attempts: number
  /** Wall time of the latest row, when it has finished. */
  durationMs?: number
  /** Recorded failure text of the latest row, if any. */
  error?: string
}

const endedMs = (row: RunExecution) => {
  if (!row.ended_at) return undefined
  const start = Date.parse(row.started_at)
  const end = Date.parse(row.ended_at)
  return Number.isFinite(start) && Number.isFinite(end) && end >= start ? end - start : undefined
}

/**
 * Ranks two rows for "which one describes this step now". Later attempt wins;
 * within an attempt, the later start. Ties keep server order, which is rowid
 * order — i.e. insertion order.
 */
const laterThan = (a: RunExecution, b: RunExecution) => {
  if (a.attempt !== b.attempt) return a.attempt > b.attempt
  const at = Date.parse(a.started_at)
  const bt = Date.parse(b.started_at)
  if (Number.isFinite(at) && Number.isFinite(bt) && at !== bt) return at > bt
  return true
}

export function useRunExecutions(workspaceId: string, runId: string | null, active: boolean) {
  const [rows, setRows] = useState<RunExecution[] | null>(null)
  const [truncated, setTruncated] = useState(false)
  const [error, setError] = useState(false)
  const [loading, setLoading] = useState(true)
  const pageLimit = useRef(1)
  const requestSequence = useRef(0)
  const inFlight = useRef(false)
  const scopeController = useRef<AbortController | null>(null)
  const invalidatePendingRequests = useCallback(() => {
    requestSequence.current++
  }, [])
  useEffect(() => {
    pageLimit.current = 1
  }, [workspaceId, runId])

  const load = useCallback(
    async (signal: AbortSignal) => {
      if (!runId) return
      const sequence = ++requestSequence.current
      inFlight.current = true
      setLoading(true)
      const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs/${encodeURIComponent(runId)}/executions`
      try {
        const collected: RunExecution[] = []
        let cursor: string | null = null
        let more = false
        const pages = pageLimit.current
        for (let page = 0; page < pages; page++) {
          const res: Response = await apiFetch(
            base + (cursor ? `?after=${encodeURIComponent(cursor)}` : ""),
            { signal },
          )
          if (!res.ok) throw new Error("executions")
          const data: { rows?: RunExecution[]; next_cursor?: string | null } = await res.json()
          collected.push(...(data.rows ?? []))
          cursor = data.next_cursor ?? null
          if (!cursor) break
          more = page === pages - 1
        }
        if (signal.aborted || sequence !== requestSequence.current) return
        setRows(collected)
        setTruncated(more)
        setError(false)
      } catch {
        if (!signal.aborted && sequence === requestSequence.current) setError(true)
      } finally {
        if (sequence === requestSequence.current) inFlight.current = false
        if (!signal.aborted && sequence === requestSequence.current) setLoading(false)
      }
    },
    [workspaceId, runId],
  )

  useEffect(() => {
    setRows(null)
    setLoading(true)
    setError(false)
    setTruncated(false)
    const controller = new AbortController()
    scopeController.current = controller
    void load(controller.signal)
    const timer = active ? setInterval(() => void load(controller.signal), 3000) : null
    return () => {
      invalidatePendingRequests()
      controller.abort()
      if (scopeController.current === controller) scopeController.current = null
      if (timer) clearInterval(timer)
    }
  }, [load, active, invalidatePendingRequests])

  /** step_id → what was recorded for it. Absent step_id means "no row yet". */
  const byStep = useMemo(() => {
    const map = new Map<string, StepExecutionSummary>()
    for (const row of rows ?? []) {
      if (!row.step_id) continue
      const existing = map.get(row.step_id)
      if (!existing) {
        map.set(row.step_id, {
          rows: [row],
          latest: row,
          attempts: row.attempt,
          durationMs: endedMs(row),
          error: row.error || undefined,
        })
        continue
      }
      existing.rows.push(row)
      existing.attempts = Math.max(existing.attempts, row.attempt)
      if (laterThan(row, existing.latest)) {
        existing.latest = row
        existing.durationMs = endedMs(row)
        existing.error = row.error || undefined
      }
    }
    return map
  }, [rows])

  const loadMore =
    truncated && pageLimit.current < MAX_PAGES
      ? () => {
          if (inFlight.current || pageLimit.current >= MAX_PAGES || !scopeController.current) return
          pageLimit.current++
          void load(scopeController.current.signal)
        }
      : undefined
  const refresh = () => {
    if (!inFlight.current && scopeController.current) void load(scopeController.current.signal)
  }
  return { rows, byStep, truncated, error, loading, loadMore, refresh }
}
