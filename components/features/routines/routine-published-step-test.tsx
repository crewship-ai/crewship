"use client"

import { useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { RoutineTestWorkspace } from "./routine-test-workspace"

/** Checks the displayed published recipe; never silently substitutes its draft. */
export function RoutinePublishedStepTest({ workspaceId, definition, stepId, authorCrewId }: {
  workspaceId: string
  definition: Record<string, unknown>
  stepId: string
  authorCrewId?: string | null
}) {
  const pending = useRef<AbortController | null>(null)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ passed: boolean; details: string } | null>(null)
  useEffect(() => () => { pending.current?.abort() }, [])
  const validate = async () => {
    if (pending.current) return
    const controller = new AbortController()
    pending.current = controller
    setBusy(true)
    setResult(null)
    try {
      const response = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/test_run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        signal: controller.signal,
        body: JSON.stringify({ definition, sample_inputs: {}, ...(authorCrewId ? { author_crew_id: authorCrewId } : {}) }),
      })
      const data = await response.json()
      if (controller.signal.aborted) return
      const passed = response.ok && data.status === "DRY_RUN_OK"
      setResult({ passed, details: passed
        ? "The published recipe passed its structural checks. No agents, scripts or HTTP calls were run."
        : typeof data.error === "string" ? data.error : "The server did not confirm a successful recipe check." })
    } catch (error) {
      if (!controller.signal.aborted) setResult({ passed: false, details: error instanceof Error ? error.message : "Recipe check failed." })
    } finally {
      if (!controller.signal.aborted) {
        pending.current = null
        setBusy(false)
      }
    }
  }
  return <RoutineTestWorkspace workspaceId={workspaceId} definition={definition} stepId={stepId}
    busy={busy} result={result} onValidate={() => void validate()} parseError={null} />
}
