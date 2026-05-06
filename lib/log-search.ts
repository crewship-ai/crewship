import type { JournalEntry } from "@/lib/types/journal"

/**
 * Predicate built from a user-typed search string. Supports three syntaxes:
 *
 *   /pattern/[flags]   → case-insensitive regex on summary + entry_type
 *   key:value          → field-scoped substring match (key in:
 *                          type, severity/sev, agent_id/agent, crew_id/crew,
 *                          mission_id/mission, trace_id/trace, or any
 *                          payload key)
 *   foo bar            → all-tokens substring match on summary + entry_type
 *
 * Free-text and key:value tokens AND together; bare regex form short-circuits.
 * Returns `null` for an empty/whitespace query so callers can skip filtering.
 */
export function buildMatcher(q: string): ((e: JournalEntry) => boolean) | null {
  const trimmed = q.trim()
  if (!trimmed) return null

  // /regex/ form
  let textForFallback = trimmed
  const rx = trimmed.match(/^\/(.+)\/([imsx]*)$/)
  if (rx) {
    try {
      const re = new RegExp(rx[1], rx[2] || "i")
      return (e) => re.test(e.summary || "") || re.test(e.entry_type)
    } catch {
      // Invalid regex — treat the inside as a free-text token so the
      // user still sees something while they fix the pattern.
      textForFallback = rx[1]
    }
  }

  const tokens = textForFallback.split(/\s+/)
  const kv: Array<[string, string]> = []
  const free: string[] = []
  for (const t of tokens) {
    const m = t.match(/^([a-z_]+):(.+)$/i)
    if (m) kv.push([m[1].toLowerCase(), m[2].toLowerCase()])
    else free.push(t.toLowerCase())
  }

  return (e) => {
    const hay = `${e.summary || ""} ${e.entry_type}`.toLowerCase()
    for (const tok of free) {
      if (!hay.includes(tok)) return false
    }
    if (kv.length > 0) {
      for (const [k, v] of kv) {
        const value = readField(e, k)
        if (!value || !String(value).toLowerCase().includes(v)) return false
      }
    }
    return true
  }
}

function readField(e: JournalEntry, k: string): unknown {
  switch (k) {
    case "type": return e.entry_type
    case "sev":
    case "severity": return e.severity
    case "agent":
    case "agent_id": return e.agent_id
    case "crew":
    case "crew_id": return e.crew_id
    case "mission":
    case "mission_id": return e.mission_id
    case "trace":
    case "trace_id": return e.trace_id
    default: return e.payload ? e.payload[k] : undefined
  }
}
