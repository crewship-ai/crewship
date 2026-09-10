"use client"

import { useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { routineInputSpecs, type RoutineInputSpec } from "@/lib/routine-inputs"
import { InputsForm } from "./routine-run-inputs-dialog"

/** Recurring presets use the same fields and conversion as a manual start.
 * Pinned schedules read their archive; a missing archive never falls back. */
export function RoutinePresetForm({
  workspaceId,
  slug,
  version,
  initialInputs,
  submitting,
  onCancel,
  onSave,
  submitLabel = "Save inputs",
  allowDraft = false,
}: {
  workspaceId: string
  slug: string
  version?: number
  initialInputs?: Record<string, unknown>
  submitting?: boolean
  onCancel: () => void
  onSave: (inputs: Record<string, unknown>) => void
  submitLabel?: string
  allowDraft?: boolean
}) {
  const [source, setSource] = useState("published")
  const [retry, setRetry] = useState(0)
  const [loaded, setLoaded] = useState<{
    key: string
    specs: RoutineInputSpec[]
    revision?: number
  } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const key = `${workspaceId}:${slug}:${version ?? "live"}:${source}`
  useEffect(() => {
    const controller = new AbortController()
    setLoaded(null)
    setError(null)
    void (async () => {
      try {
        const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}`
        const suffix = version ? `/versions/${version}` : source === "draft" ? "/draft" : ""
        const res = await apiFetch(base + suffix, { signal: controller.signal })
        if (!res.ok)
          throw new Error(
            "Could not load the recipe's input schema. Retry before saving this preset.",
          )
        const data = await res.json()
        if (source === "draft" && !version && !data.id)
          throw new Error("There is no saved draft to review.")
        const definition =
          source === "draft" && !version ? data.document?.definition : data.definition
        if (!definition || typeof definition !== "object")
          throw new Error("This recipe's input schema is unavailable.")
        if (!controller.signal.aborted)
          setLoaded({
            key,
            specs: routineInputSpecs(definition),
            revision: source === "draft" ? data.revision : undefined,
          })
      } catch (e) {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e))
      }
    })()
    return () => controller.abort()
  }, [workspaceId, slug, version, source, key, retry])

  const specs = loaded?.key === key ? loaded.specs : null
  const withSavedValues = specs?.map((spec) =>
    Object.hasOwn(initialInputs ?? {}, spec.name)
      ? { ...spec, default: initialInputs![spec.name] }
      : spec,
  )
  const save = (inputs: Record<string, unknown>) => {
    // Keep legacy keys that this schema does not describe. Editing a preset
    // must not silently erase information consumed by advanced expressions.
    const names = new Set(specs?.map((spec) => spec.name))
    const extra = Object.fromEntries(
      Object.entries(initialInputs ?? {}).filter(([name]) => !names.has(name)),
    )
    onSave({ ...extra, ...inputs })
  }
  return (
    <div className="space-y-3">
      {allowDraft && !version && (
        <div>
          <label htmlFor="preset-schema" className="text-sm font-medium">
            Input schema
          </label>
          <select
            id="preset-schema"
            className="mt-1 w-full rounded-md border bg-card p-2 text-sm"
            value={source}
            disabled={submitting}
            onChange={(e) => setSource(e.target.value)}
          >
            <option value="published">Published recipe</option>
            <option value="draft">Saved draft</option>
          </select>
        </div>
      )}
      {version && (
        <p className="text-xs text-muted-foreground">Inputs for pinned version {version}.</p>
      )}
      {source === "draft" && loaded?.revision && (
        <p className="text-xs text-muted-foreground">
          Draft revision {loaded.revision}. Saving inputs changes the schedule now; publishing the
          recipe remains a separate action. Pause the schedule first if these inputs are
          incompatible with the published recipe.
        </p>
      )}
      {error ? (
        <p role="alert" className="text-sm text-destructive">
          {error}{" "}
          <button type="button" className="underline" onClick={() => setRetry((n) => n + 1)}>
            Retry
          </button>
        </p>
      ) : (
        !specs && (
          <p role="status" className="text-sm text-muted-foreground">
            Loading inputs…
          </p>
        )
      )}
      {withSavedValues && (
        <InputsForm
          key={key}
          inputs={withSavedValues}
          submitting={submitting}
          onCancel={onCancel}
          onRun={save}
          submitLabel={submitLabel}
        />
      )}
      {!withSavedValues && (
        <button
          type="button"
          disabled={submitting}
          onClick={onCancel}
          className="rounded-md border px-3 py-2 text-sm"
        >
          Cancel
        </button>
      )}
      {initialInputs && Object.keys(initialInputs).length > 0 && (
        <details className="text-xs text-muted-foreground">
          <summary>Stored preset · JSON</summary>
          <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap">
            {JSON.stringify(initialInputs, null, 2)}
          </pre>
        </details>
      )}
    </div>
  )
}
