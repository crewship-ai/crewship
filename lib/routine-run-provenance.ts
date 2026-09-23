import { runProvenance } from "@/lib/run-provenance"

type ObjectValue = Record<string, unknown>
const object = (value: unknown): ObjectValue | null =>
  value !== null && typeof value === "object" && !Array.isArray(value) ? value as ObjectValue : null

// Read only declarations from the immutable definition returned with this run.
// Do not inspect prompt text, inputs, resolved values, or current HEAD.
export function declaredCredentialTypes(definition: unknown): string[] {
  const dsl = object(definition)
  if (!dsl) return []
  const types = new Set<string>()
  const add = (value: unknown) => {
    if (types.size < 50 && typeof value === "string" && /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(value)) types.add(value)
  }
  if (Array.isArray(dsl.credentials_required)) {
    for (const item of dsl.credentials_required) add(object(item)?.type)
  }
  const visit = (step: unknown, depth: number) => {
    if (depth > 8 || types.size >= 50) return
    const s = object(step)
    if (!s) return
    add(object(object(s.http)?.credential_ref)?.type)
    const children = object(s.foreach)?.steps
    if (Array.isArray(children)) children.slice(0, 100).forEach((child) => visit(child, depth + 1))
    const hooks = object(s.hooks)
    if (hooks) for (const key of ["before", "after"])
      if (hooks[key]) visit(hooks[key], depth + 1)
  }
  if (Array.isArray(dsl.steps)) dsl.steps.slice(0, 100).forEach((step) => visit(step, 0))
  const hooks = object(dsl.hooks)
  if (hooks) for (const key of ["before_all", "after_all", "on_failure"])
    if (hooks[key]) visit(hooks[key], 0)
  return [...types].sort()
}

export function routineRunOrigin(run: { triggered_via?: string; triggered_by_id?: string; metadata?: unknown }) {
  const metadata = object(run.metadata)
  const automationName = metadata?.automation_name
  return runProvenance({
    triggered_via: run.triggered_via || "unknown",
    triggered_by_id: run.triggered_by_id,
    automation_name: typeof automationName === "string" && automationName.trim() ? automationName : undefined,
  })
}
