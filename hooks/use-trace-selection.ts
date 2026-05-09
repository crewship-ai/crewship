"use client"

import { useCallback } from "react"
import { useRouter, useSearchParams, usePathname } from "next/navigation"

// useTraceSelection — single source of truth for "which run is the
// canvas focused on" and "which step is the side panel focused on".
//
// State lives in the URL so a copied link reproduces the exact view
// the user is looking at (`/activity?run=prn_X&step=fetch`). This
// matches the Trigger.dev / Temporal pattern: trace IDs are linkable.
export function useTraceSelection() {
  const router = useRouter()
  const pathname = usePathname()
  const params = useSearchParams()

  const runId = params.get("run") || null
  const stepId = params.get("step") || null

  const setRunId = useCallback(
    (next: string | null) => {
      const sp = new URLSearchParams(params.toString())
      if (next) sp.set("run", next)
      else sp.delete("run")
      // Selecting a different run drops the previously-focused step;
      // a step id from one run isn't meaningful in another.
      sp.delete("step")
      router.replace(`${pathname}?${sp.toString()}`, { scroll: false })
    },
    [params, pathname, router],
  )

  const setStepId = useCallback(
    (next: string | null) => {
      const sp = new URLSearchParams(params.toString())
      if (next) sp.set("step", next)
      else sp.delete("step")
      router.replace(`${pathname}?${sp.toString()}`, { scroll: false })
    },
    [params, pathname, router],
  )

  return { runId, stepId, setRunId, setStepId }
}
