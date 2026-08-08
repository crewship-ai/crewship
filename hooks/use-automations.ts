"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import type { Automation } from "@/lib/automations"

// The workspace's automation rules, read-only.
//
// `GET /api/v1/automations` is workspace-scoped from the request context and
// takes no filters, so this hook fetches the whole set once and both callers
// (the routine detail, the issue detail) narrow it client-side with the
// predicates in lib/automations.ts. At the scale the table is designed for —
// tens of rules, capped by an ADMIN-only write path — that is cheaper than two
// bespoke server-side filters and keeps the two pages agreeing about what a
// rule means.
//
// Read-only on purpose: there is no automation management UI, and inventing
// half of one behind a routine page is worse than pointing at the CLI, which
// is the whole write surface. See docs/guides/automations.
//
// A failure returns an empty list and an error string rather than throwing.
// Both surfaces treat automations as a secondary fact: a page that cannot say
// what can start a routine must still render the routine.
export function useAutomations(workspaceId: string | null | undefined) {
  const [automations, setAutomations] = useState<Automation[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setAutomations([])
      return
    }
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch("/api/v1/automations", { signal: controller.signal })
      if (controller.signal.aborted) return
      if (!res.ok) {
        // 403 is the ordinary case for a member on an ADMIN-gated surface, not
        // a fault. Either way the list is empty and the page carries on.
        setError(`automations: ${res.status}`)
        setAutomations([])
        return
      }
      const data = await res.json()
      if (controller.signal.aborted) return
      setAutomations(Array.isArray(data?.automations) ? data.automations : [])
    } catch (e) {
      if (controller.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
      setAutomations([])
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    refresh()
    return () => {
      abortRef.current?.abort()
    }
  }, [refresh])

  return { automations, loading, error, refresh }
}
