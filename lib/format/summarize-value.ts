// summarizeValue — short, human-readable preview of an arbitrary
// JSON value, with a soft length cap.
//
// Three places used to ship near-identical implementations
// (json-viewer table cells, build-trace-graph edge previews,
// extract-artifacts file previews); they had drifted on the limit
// (80 vs 100 chars) and on whether to wrap strings in quotes. This
// is the single canonical version.

interface SummarizeOptions {
  /** Max length of the returned preview string. Default 80.
   * Truncation appends a single ellipsis. */
  maxChars?: number
  /** When true, wrap string values in double quotes — useful for
   * edge previews where it disambiguates `"foo"` (a JSON string)
   * from `foo` (a key). Default false. */
  quoteStrings?: boolean
}

export function summarizeValue(v: unknown, opts: SummarizeOptions = {}): string {
  // Clamp to a sane minimum: with maxChars <= 0 the truncation math
  // (`slice(0, max - 1) + "…"`) collapses or returns the whole value
  // unchanged, which silently breaks the cap contract. Caller can
  // still pass huge values to disable truncation effectively.
  const max = Math.max(8, opts.maxChars ?? 80)
  const truncate = (text: string, limit = max) =>
    text.length > limit ? text.slice(0, limit - 1) + "…" : text
  if (v === undefined) return ""
  if (v === null) return "null"
  if (typeof v === "string") {
    const truncated = truncate(v, opts.quoteStrings ? max - 2 : max)
    return opts.quoteStrings ? `"${truncated}"` : truncated
  }
  if (typeof v === "number" || typeof v === "boolean") return truncate(String(v))
  // Objects + arrays — compact JSON, capped.
  try {
    return truncate(JSON.stringify(v) ?? String(v))
  } catch {
    return truncate(String(v))
  }
}
