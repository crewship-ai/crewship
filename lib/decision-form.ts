import type { RoutineInputSpec } from "@/lib/routine-inputs"

export interface DecisionAction {
  id: string
  label: string
  approved: boolean
}
export interface DecisionForm {
  fields: RoutineInputSpec[]
  actions: DecisionAction[]
}
export interface DecisionAnswer {
  action_id: string
  data: Record<string, unknown>
}

export function isDecisionForm(value: unknown): value is DecisionForm {
  if (!value || typeof value !== "object") return false
  const form = value as Partial<DecisionForm>
  return (
    Array.isArray(form.fields) &&
    form.fields.every((f) => f && typeof f.name === "string" && typeof f.type === "string") &&
    Array.isArray(form.actions) &&
    form.actions.length >= 2 &&
    form.actions.every(
      (a) =>
        a &&
        typeof a.id === "string" &&
        typeof a.label === "string" &&
        typeof a.approved === "boolean",
    )
  )
}
