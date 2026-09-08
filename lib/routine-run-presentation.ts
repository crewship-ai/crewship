/** Presentation never treats a false/zero result as a failed execution. */
export function routineRunPresentation(run: { status?: string; outcome?: string }) {
  const status = (run.status ?? "").toLowerCase()
  if (run.outcome === "FAILED") return { label: "Result failed", tone: "destructive" as const }
  if (run.outcome === "NEEDS_HUMAN") return { label: "Needs your attention", tone: "warn" as const }
  if (["waiting", "paused"].includes(status)) return { label: "Waiting for a decision", tone: "warn" as const }
  if (["running", "queued"].includes(status)) return { label: status === "running" ? "Running" : "Queued", tone: "blue" as const }
  if (["failed", "error", "interrupted"].includes(status)) return { label: status === "interrupted" ? "Interrupted" : "Failed", tone: "destructive" as const }
  if (["completed", "success", "succeeded"].includes(status)) return { label: "Completed", tone: "success" as const }
  return { label: status === "cancelled" ? "Stopped" : status || "Recorded", tone: "default" as const }
}

/** Only the saved author's mapping may give a primitive result business meaning. */
export function routineResultLabel(output: string, definition: unknown): string | null {
  if (!definition || typeof definition !== "object") return null
  const specs = (definition as { outputs?: unknown }).outputs
  if (!Array.isArray(specs) || specs.length !== 1) return null
  const spec = specs[0]
  if (!spec || typeof spec !== "object" || !spec.value_labels || typeof spec.value_labels !== "object") return null
  let value: unknown
  try { value = JSON.parse(output) } catch { value = output }
  if (value !== null && typeof value === "object" && typeof spec.name === "string") value = (value as Record<string, unknown>)[spec.name]
  if (!["string", "number", "boolean"].includes(typeof value)) return null
  const key = String(value)
  return Object.hasOwn(spec.value_labels, key) && typeof spec.value_labels[key] === "string" ? spec.value_labels[key] : null
}
