"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import {
  approvalDecideResponseSchema,
  approvalListResponseSchema,
  type ApprovalRow,
  type ApprovalStatus,
} from "@/lib/types/approvals"

interface UseApprovalsOptions {
  status: ApprovalStatus
  limit?: number
  /** Poll interval in ms. 0 disables polling. */
  pollMs?: number
  enabled?: boolean
}

interface UseApprovalsResult {
  rows: ApprovalRow[]
  loading: boolean
  error: string | null
  notConfigured: boolean
  refresh: () => Promise<void>
  /** Mutates one row locally — used for optimistic updates post-decide. */
  patchRow: (id: string, patch: Partial<ApprovalRow>) => void
}

/**
 * Polls `/api/v1/approvals?status=…`. Pending view polls every `pollMs` so
 * new requests surface without a reload; decided views don't bother.
 */
export function useApprovals(opts: UseApprovalsOptions): UseApprovalsResult {
  const { status, limit = 50, pollMs = 15000, enabled = true } = opts
  const [rows, setRows] = useState<ApprovalRow[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [notConfigured, setNotConfigured] = useState(false)
  const reqIdRef = useRef(0)

  const refresh = useCallback(async () => {
    if (!enabled) {
      // Disabled hooks must still drop the initial `loading=true` state
      // or consumers render an infinite spinner. Do NOT bump reqIdRef
      // here — we're not actually firing a request.
      setLoading(false)
      return
    }
    const reqId = ++reqIdRef.current
    setLoading(true)
    setError(null)
    try {
      const res = await fetch(`/api/v1/approvals?status=${status}&limit=${limit}`)
      if (reqIdRef.current !== reqId) return
      if (res.status === 404) {
        setRows([])
        setNotConfigured(true)
        return
      }
      setNotConfigured(false)
      if (!res.ok) {
        // Prefer the backend's `{error: "..."}` message over a bare
        // HTTP code so operators see the real cause (e.g. "workspace
        // required", "forbidden") instead of "HTTP 400".
        let msg = `HTTP ${res.status}`
        try {
          const body = (await res.json()) as { error?: unknown }
          if (body && typeof body.error === "string") msg = body.error
        } catch {
          // fall through with the HTTP-status fallback
        }
        setError(msg)
        return
      }
      const json = await res.json()
      const parsed = approvalListResponseSchema.safeParse(json)
      if (reqIdRef.current !== reqId) return
      if (!parsed.success) {
        // Surface schema-validation failures instead of silently
        // pretending the response was empty — otherwise a backend
        // shape regression is invisible in the UI.
        setRows([])
        setError("Malformed response from /api/v1/approvals")
        return
      }
      setRows(parsed.data.rows)
    } catch {
      if (reqIdRef.current === reqId) setError("Network error")
    } finally {
      if (reqIdRef.current === reqId) setLoading(false)
    }
  }, [enabled, status, limit])

  useEffect(() => {
    refresh()
  }, [refresh])

  useEffect(() => {
    if (!enabled || !pollMs || status !== "pending") return
    const id = setInterval(() => {
      refresh()
    }, pollMs)
    return () => clearInterval(id)
  }, [enabled, pollMs, status, refresh])

  const patchRow = useCallback((id: string, patch: Partial<ApprovalRow>) => {
    setRows((prev) => prev.map((r) => (r.id === id ? { ...r, ...patch } : r)))
  }, [])

  return { rows, loading, error, notConfigured, refresh, patchRow }
}

/**
 * POST `/api/v1/approvals/{id}/decide`. Returns the decision response or
 * throws so callers can rollback optimistic state.
 */
export async function decideApproval(
  id: string,
  decision: "approved" | "denied",
  comment: string,
): Promise<{ status: string; decided_by?: string }> {
  const res = await fetch(`/api/v1/approvals/${encodeURIComponent(id)}/decide`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ status: decision, comment }),
  })
  if (!res.ok) {
    throw new Error(`HTTP ${res.status}`)
  }
  const json = await res.json()
  const parsed = approvalDecideResponseSchema.safeParse(json)
  if (!parsed.success) {
    throw new Error("Malformed response")
  }
  return parsed.data
}
