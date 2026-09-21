import { isRecord } from "@/lib/routine-step-describe"
import { routineHooks, type Step } from "@/lib/routine-steps-layout"

/** Approval steps retain their actual paths, including nested loops and hooks. */
export function approvalSteps(definition: Record<string, unknown>): { step: Step; path: (number | string)[] }[] {
  const result: { step: Step; path: (number | string)[] }[] = []
  function visit(value: unknown, path: (number | string)[]) {
    if (!isRecord(value)) return
    if (value.type === "wait" && isRecord(value.wait) && value.wait.kind === "approval") {
      result.push({ step: value, path })
    }
    if (isRecord(value.foreach) && Array.isArray(value.foreach.steps)) {
      value.foreach.steps.forEach((child, i) => visit(child, [...path, "foreach", "steps", i]))
    }
  }
  if (Array.isArray(definition.steps)) definition.steps.forEach((step, i) => visit(step, ["steps", i]))
  for (const { key, step } of routineHooks(definition)) visit(step, ["hooks", key])
  return result
}
