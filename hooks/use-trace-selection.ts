"use client"

import { useCallback } from "react"
import { useRouter, useSearchParams, usePathname } from "next/navigation"

// useTraceSelection — single source of truth for "which run is the
// canvas focused on" and "which step is the side panel focused on".
//
// State lives in the URL so a copied link reproduces the exact view
// the user is looking at (`/activity?run=prn_X&step=fetch`). Trace
// IDs are linkable as a first-class deep-link surface.
export function useTraceSelection() {
  const router = useRouter()
  const pathname = usePathname()
  const params = useSearchParams()

  const runId = params.get("run") || null
  const stepId = params.get("step") || null

  // Push for run/step selection so browser Back walks the trail of
  // recent picks instead of bouncing the user out of /activity. We
  // don't push when CLEARING a selection — that's idempotent state
  // hygiene rather than a user-meaningful navigation.
  const setRunId = useCallback(
    (next: string | null) => {
      const sp = new URLSearchParams(params.toString())
      if (next) sp.set("run", next)
      else sp.delete("run")
      // Selecting a different run drops the previously-focused step;
      // a step id from one run isn't meaningful in another.
      sp.delete("step")
      const url = `${pathname}?${sp.toString()}`
      if (next) router.push(url, { scroll: false })
      else router.replace(url, { scroll: false })
    },
    [params, pathname, router],
  )

  const setStepId = useCallback(
    (next: string | null) => {
      const sp = new URLSearchParams(params.toString())
      if (next) sp.set("step", next)
      else sp.delete("step")
      const url = `${pathname}?${sp.toString()}`
      if (next) router.push(url, { scroll: false })
      else router.replace(url, { scroll: false })
    },
    [params, pathname, router],
  )

  return { runId, stepId, setRunId, setStepId }
}
