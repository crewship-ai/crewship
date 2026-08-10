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
  /**
   * Runs of this chain still going, and runs still asking a person.
   *
   * These are what make "Active now" a state the rail can render rather than a
   * guess from a timestamp. A chain whose last activity was three days ago but
   * which still holds an approval is the most urgent row on the page; without
   * these two counts it is indistinguishable from one that finished then.
   *
   * Optional on the type — not on the wire — so a client compiled against a
   * newer server than it talks to degrades to the `failed` flag rather than
   * reading `undefined` as 0. See chainStatus in lib/activity-lenses.
   */
  running_runs?: number
  waiting_runs?: number
  first_activity: string
  last_activity: string
  /**
   * Wall clock from `first_activity` to `last_activity`, in milliseconds.
   *
   * `null` when there is nothing to measure between — one datable moment, which
   * on this endpoint means a single run that has not ended yet. Render that as
   * "running", never as 0: 0 asserts the work was instant.
   *
   * The server computes it exactly the way `chainElapsedMs` does in
   * lib/activity-stream — wall clock, NOT the sum of the runs' own durations,
   * which reads 0 for agentless work and double-counts nested spans.
   */
  duration_ms: number | null
  /**
   * The issues this chain created or changed, and how many there are in total.
   *
   * `issues` is capped server-side (5 per row); `issue_count` is the full
   * number, so `issue_count > issues.length` is the "+N more" case. Both absent
   * arrays and a zero count mean the chain touched no issue.
   */
  issues?: ChainIssueRef[]
  issue_count: number
  /** The agents this chain put to work. Same cap and same total rule as `issues`. */
  agents?: ChainAgentRef[]
  agent_count: number
}

/** One issue a chain touched. Mirrors internal/api.ChainIssueRef. */
export interface ChainIssueRef {
  id: string
  /** "ENG-7" — what a human recognises, and a valid anchor for the walk. */
  identifier?: string
  /** User- and agent-written. Escape before rendering. */
  title?: string
  /**
   * The chain AUTHORED this issue, rather than merely moving one that already
   * existed. Read from `missions.author_run_id`; absent means "changed", which
   * is a weaker and different claim.
   */
  created?: boolean
}

/** One agent a chain dispatched. Mirrors internal/api.ChainAgentRef. */
export interface ChainAgentRef {
  id: string
  slug?: string
  /** User-written. Escape before rendering. */
  name?: string
  /** Pieces of work this agent took in THIS chain — "ada ×3" on the row. */
  assignments: number
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
