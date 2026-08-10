"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

/**
 * One row of GET /api/v1/chains — a single run of a workflow.
 *
 * Mirrors internal/api.ChainSummary. A chain is every piece of work that shares
 * one `chain_origin`: the rule or person that started it, the routine runs it
 * caused, and the agent work those dispatched. Grouping by cause rather than by
 * type is the whole point — "what did the agents do today" is not answerable
 * from a list of issues beside a list of routines.
 */
export interface ChainSummary {
  origin: string
  /** "automation" | "issue" | "run" | "user" | "schedule" | "webhook" | "unknown" */
  started_by_kind: string
  started_by_id?: string
  started_by_key?: string
  /** Human label for whoever started it. */
  started_by: string
  triggered_via?: string
  routine_id?: string
  routine_slug?: string
  runs: number
  max_chain_depth: number
  failed_runs: number
  failed: boolean
  first_activity: string
  last_activity: string
}

interface UseChainsResult {
  chains: ChainSummary[]
  loading: boolean
  error: string | null
  /**
   * True when the workspace holds runs from before chain recording existed.
   *
   * Surfaced rather than swallowed: those runs cannot be grouped — the link was
   * never written and cannot be derived — so an index that quietly omitted them
   * would read as "nothing ever ran here" on a workspace with months of
   * history. The rail says so instead.
   */
  hasUnrecordedRuns: boolean
  refresh: () => Promise<void>
}

/**
 * Lists the workflow runs in a workspace, newest first.
 *
 * Deliberately NOT streamed. The rail is a place you return to between actions,
 * not a tape you watch: re-fetching on demand keeps one query per look instead
 * of one per event, and a chain that finishes a second after you glanced is a
 * cheaper miss than a rail that re-sorts under the cursor.
 */
export function useChains(workspaceId: string | null, limit = 25): UseChainsResult {
  const [chains, setChains] = useState<ChainSummary[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [hasUnrecordedRuns, setHasUnrecordedRuns] = useState(false)
  // Guards against an older response landing after a newer one and overwriting
  // it — the same reason use-journal-list keeps a request id.
  const reqIdRef = useRef(0)

  const refresh = useCallback(async () => {
    if (!workspaceId) return
    const reqId = ++reqIdRef.current
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(
        `/api/v1/chains?workspace_id=${encodeURIComponent(workspaceId)}&limit=${limit}`,
      )
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const body = (await res.json()) as {
        chains?: ChainSummary[]
        has_unrecorded_runs?: boolean
      }
      if (reqId !== reqIdRef.current) return
      setChains(body.chains ?? [])
      setHasUnrecordedRuns(body.has_unrecorded_runs === true)
    } catch (e: unknown) {
      if (reqId !== reqIdRef.current) return
      setError(e instanceof Error ? e.message : "could not load workflows")
    } finally {
      if (reqId === reqIdRef.current) setLoading(false)
    }
  }, [workspaceId, limit])

  useEffect(() => {
    void refresh()
  }, [refresh])

  return { chains, loading, error, hasUnrecordedRuns, refresh }
}
