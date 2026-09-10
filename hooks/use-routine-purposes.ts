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
    const queue = missing.filter(
      ([slug, hash]) => !cache.current.has(JSON.stringify([workspaceId, slug, hash])),
    )
    const worker = async () => {
      while (!controller.signal.aborted && queue.length) {
        const [slug, hash] = queue.shift()!
        const key = JSON.stringify([workspaceId, slug, hash])
        try {
          const response = await apiFetch(
            `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}`,
            { signal: controller.signal },
          )
          if (!response.ok) throw new Error("Purpose unavailable")
          const routine = await response.json()
          if (controller.signal.aborted) return
          if (routine.definition_hash && routine.definition_hash !== hash)
            throw new Error("Recipe changed while loading its purpose")
          const purpose = deriveRoutinePurpose(routine.definition)
          cache.current.set(key, purpose)
          setSummaries((current) => ({ ...current, [key]: purpose }))
        } catch {
          if (!controller.signal.aborted)
            setSummaries((current) => ({ ...current, [key]: "Purpose unavailable." }))
        }
      }
    }
    for (let i = 0; i < Math.min(4, missing.length); i++) void worker()
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
