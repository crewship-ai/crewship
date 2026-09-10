"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { describeStep, isRecord } from "@/lib/routine-step-describe"
import type { Pipeline } from "./use-pipelines"

export function deriveRoutinePurpose(definition: unknown): string {
  if (!isRecord(definition) || !Array.isArray(definition.steps)) return "Purpose unavailable."
  const steps = definition.steps.filter(isRecord)
  if (!steps.length) return "No steps configured."
  const first = describeStep(steps[0], 1).title
  const last = describeStep(steps[steps.length - 1], steps.length).title
  return steps.length === 1 ? `${first}.` : `${first}. ${last}.`
}

/** Only recipes without a summary need their definition loaded. Shared by both lists. */
export function useRoutinePurposes(workspaceId: string, routines: Pipeline[]): Pipeline[] {
  const cache = useRef(new Map<string, string>())
  const [summaries, setSummaries] = useState<Record<string, string>>({})
  const missingKey = JSON.stringify(
    routines.filter((r) => !r.description?.trim()).map((r) => [r.slug, r.definition_hash]),
  )
  useEffect(() => {
    const controller = new AbortController()
    const missing: [string, string][] = JSON.parse(missingKey)
    for (const [slug, hash] of missing) {
      const key = JSON.stringify([workspaceId, slug, hash])
      if (cache.current.has(key)) continue
      void apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}`,
        { signal: controller.signal },
      )
        .then(async (response) => {
          if (!response.ok) throw new Error("Purpose unavailable")
          const routine = await response.json()
          if (controller.signal.aborted) return
          if (routine.definition_hash && routine.definition_hash !== hash)
            throw new Error("Recipe changed while loading its purpose")
          const purpose = deriveRoutinePurpose(routine.definition)
          cache.current.set(key, purpose)
          setSummaries((current) => ({ ...current, [key]: purpose }))
        })
        .catch(() => {
          if (!controller.signal.aborted)
            setSummaries((current) => ({ ...current, [key]: "Purpose unavailable." }))
        })
    }
    return () => controller.abort()
  }, [workspaceId, missingKey])
  return useMemo(
    () =>
      routines.map((routine) =>
        routine.description?.trim()
          ? routine
          : {
              ...routine,
              description:
                summaries[
                  JSON.stringify([workspaceId, routine.slug, routine.definition_hash])
                ] || "Loading purpose…",
            },
      ),
    [routines, workspaceId, summaries],
  )
}
