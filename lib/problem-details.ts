// Shared readers for RFC 7807 Problem Details bodies returned by a refused
// run (HTTP 422). The run gate reports what's missing as a machine-readable
// string array under an extension member (`missing_integrations`,
// `missing_credentials`, …); this is the ONE place that array is parsed so
// every "what's missing?" toast reads it the same way.

/** extractProblemStringList pulls a string-array extension member out of a
 *  parsed Problem Details body (or any object). Returns a de-duplicated,
 *  trimmed, string-only list; `[]` when the field is absent or malformed —
 *  callers use a non-empty result to switch from the generic "run failed"
 *  toast to an actionable, name-the-missing-thing block. */
export function extractProblemStringList(body: unknown, field: string): string[] {
  if (!body || typeof body !== "object") return []
  const raw = (body as Record<string, unknown>)[field]
  if (!Array.isArray(raw)) return []
  const seen = new Set<string>()
  const out: string[] = []
  for (const item of raw) {
    if (typeof item !== "string") continue
    const value = item.trim()
    if (!value || seen.has(value)) continue
    seen.add(value)
    out.push(value)
  }
  return out
}

/** extractProblemDetail reads the human-readable `detail` member of a parsed
 *  Problem Details body, or undefined when absent — so a toast can prefer the
 *  server's specific message over a generic fallback. */
export function extractProblemDetail(body: unknown): string | undefined {
  if (!body || typeof body !== "object") return undefined
  const detail = (body as Record<string, unknown>).detail
  return typeof detail === "string" ? detail : undefined
}
