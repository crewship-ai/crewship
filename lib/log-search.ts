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

/**
 * Pull server-bindable structured tokens out of a search string so
 * filters that map to query params (`agent_id`, `crew_id`, `trace_id`,
 * `entry_type`, `severity`, `actor_type`, `priority`) can be pushed to
 * the backend instead of being narrowed client-side. Tokens that don't
 * match a server-side key (free text, payload keys, regexes) stay in
 * `clientQuery` and feed the client-side matcher.
 *
 * Why split: client-side narrowing on top of the 5,000-row buffer cap
 * silently drops matches when the user wants something rare (e.g.
 * `agent:viktor severity:error` → may have zero hits in the last 5k
 * even when there are matches further back). Sending the structured
 * filters to the backend reads from the full table.
 *
 * Recognised keys (left side of `key:value`):
 *   type, severity (or sev), actor (or actor_type),
 *   priority, agent (or agent_id), crew (or crew_id),
 *   trace (or trace_id)
 *
 * Multiple values for the same key (e.g. `severity:warn severity:error`)
 * are coalesced into a CSV. Unknown keys (like `payload.tool_name:Bash`,
 * which the client matcher does support) are NOT consumed here — they
 * fall back into `clientQuery`.
 */
export interface StructuredQuery {
  /** What to send to the server alongside other filter params. */
  serverParams: {
    entry_type?: string
    severity?: string
    actor_type?: string
    priority?: string
    agent_id?: string
    crew_id?: string
    trace_id?: string
  }
  /** What's left after server-bound tokens are removed. Feeds buildMatcher. */
  clientQuery: string
}

const SERVER_KEY_ALIAS: Record<string, keyof StructuredQuery["serverParams"]> = {
  type: "entry_type",
  severity: "severity",
  sev: "severity",
  actor: "actor_type",
  actor_type: "actor_type",
  priority: "priority",
  agent: "agent_id",
  agent_id: "agent_id",
  crew: "crew_id",
  crew_id: "crew_id",
  trace: "trace_id",
  trace_id: "trace_id",
}

export function parseStructuredQuery(q: string): StructuredQuery {
  const out: StructuredQuery = { serverParams: {}, clientQuery: "" }
  const trimmed = q.trim()
  if (!trimmed) return out

  // /regex/ form is purely client-side — pass through untouched.
  if (/^\/(.+)\/([imsx]*)$/.test(trimmed)) {
    out.clientQuery = trimmed
    return out
  }

  const tokens = trimmed.split(/\s+/)
  const remaining: string[] = []
  const collected: Record<string, string[]> = {}

  for (const t of tokens) {
    const m = t.match(/^([a-z_]+):(.+)$/i)
    if (!m) {
      remaining.push(t)
      continue
    }
    const key = m[1].toLowerCase()
    const value = m[2]
    const serverKey = SERVER_KEY_ALIAS[key]
    if (!serverKey) {
      // Unknown key (e.g. payload.tool_name) — leave it for the
      // client matcher, which understands payload keys.
      remaining.push(t)
      continue
    }
    if (!collected[serverKey]) collected[serverKey] = []
    collected[serverKey].push(value)
  }

  for (const [k, values] of Object.entries(collected)) {
    out.serverParams[k as keyof StructuredQuery["serverParams"]] = values.join(",")
  }
  out.clientQuery = remaining.join(" ")
  return out
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
