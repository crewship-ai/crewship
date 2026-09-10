import { describeStep } from "@/lib/routine-step-describe"
type Row = Record<string, unknown>
export interface RoutineDataSource {
  value: string
  label: string
  stepId?: string
  valueType: string
}
const object = (value: unknown): Row =>
  value && typeof value === "object" && !Array.isArray(value) ? (value as Row) : {}
const rows = (value: unknown): Row[] =>
  Array.isArray(value)
    ? value.filter((v): v is Row => !!v && typeof v === "object" && !Array.isArray(v))
    : []
const safeID = /^[A-Za-z_][A-Za-z0-9_]*$/
const references = (step: Row) =>
  [...JSON.stringify(step).matchAll(/steps\.([a-zA-Z0-9_-]+)/g)].map((m) => m[1])

/** Mirror the existing scheduler switch; choosing data must not opt a linear
 * recipe into concurrency or disable auto-derived dependencies. */
function graphMode(definition: Row): "linear" | "auto" | "explicit" {
  const steps = rows(definition.steps)
  if (definition.parallelism === "off") return "linear"
  if (definition.parallelism === "auto")
    return steps.some((s) => s.type === "call_pipeline") ? "linear" : "auto"
  return steps.some((s) => Array.isArray(s.needs) && s.needs.length > 0) ? "explicit" : "linear"
}

export function routineDataSources(
  definition: Row,
  targetId: string,
  acceptedTypes?: string[],
): RoutineDataSource[] {
  const steps = rows(definition.steps)
  const targetIndex = steps.findIndex((s) => s.id === targetId)
  const mode = graphMode(definition)
  const dependsOnTarget = (id: string, seen = new Set<string>()): boolean => {
    if (id === targetId) return true
    if (seen.has(id)) return false
    seen.add(id)
    const step = steps.find((s) => s.id === id)
    if (!step) return false
    const explicit = Array.isArray(step.needs)
      ? step.needs.filter((v): v is string => typeof v === "string")
      : []
    return [...explicit, ...references(step)].some((parent) => dependsOnTarget(parent, seen))
  }
  const inputs = rows(definition.inputs)
  const sources: RoutineDataSource[] = inputs
    .filter((i) => typeof i.name === "string" && safeID.test(i.name))
    .map((i) => ({
      value: `{{ inputs.${i.name} }}`,
      label: `Provided information → ${i.label || i.name} · ${i.type || "unspecified type"}`,
      valueType: String(i.type || "unknown"),
    }))
  steps.forEach((step, index) => {
    if (
      typeof step.id !== "string" ||
      !safeID.test(step.id) ||
      targetIndex < 0 ||
      dependsOnTarget(step.id) ||
      (mode === "linear" && index >= targetIndex)
    )
      return
    const schema = object(object(step.validation).schema)
    const type = typeof schema.type === "string" ? schema.type : "string"
    sources.push({
      value: `{{ steps.${step.id}.output }}`,
      label: `Step result → ${describeStep(step, 1).title} · ${schema.type ? `declared ${type}` : "output text"}`,
      stepId: step.id,
      valueType: type,
    })
    const properties = object(schema.properties)
    for (const [key, raw] of Object.entries(properties)) {
      const property = object(raw)
      if (!safeID.test(key) || typeof property.type !== "string") continue
      sources.push({
        value: `{{ steps.${step.id}.output.${key} }}`,
        label: `Step result → ${describeStep(step, 1).title} → ${property.title || key} · declared ${property.type}`,
        stepId: step.id,
        valueType: property.type,
      })
    }
  })
  return acceptedTypes
    ? sources.filter((source) => acceptedTypes.includes(source.valueType))
    : sources
}

/** Returns a step patch, retaining unknown configuration and author dependencies. */
export function routineSourcePatch(
  definition: Row,
  target: Row,
  source: RoutineDataSource,
  key: string,
  nested?: string,
  append = false,
): Row {
  const previous = nested ? object(target[nested])[key] : target[key]
  const text = append && previous ? `${String(previous)}\n${source.value}` : source.value
  const patch: Row = nested
    ? { [nested]: { ...object(target[nested]), [key]: text } }
    : { [key]: text }
  const mode = graphMode(definition)
  const existing = Array.isArray(target.needs) ? target.needs : []
  // In auto mode with no author needs, the new reference is derived together
  // with the existing ones. Adding one explicit need would suppress the others.
  if (source.stepId && (mode === "explicit" || (mode === "auto" && existing.length > 0)))
    patch.needs = [...new Set([...existing, source.stepId])]
  return patch
}
