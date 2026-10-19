// Triggers: one list over three sources (Harbor 2, I-TRIG).
//
//   routine  pipeline webhooks — the only source with call receipts and a count
//   agent    an agent's webhook secret (/webhooks/{crew}/{agent}/trigger)
//   page     page webhooks — tokens per page, read per page
//
// A count of calls exists for routine triggers (fire_count / receipts) and for
// agent triggers (the webhook delivery ledger, once its summary has loaded).
// Page triggers write neither, so they say "calls not counted" rather than a
// zero they do not know.

import type { IncomingEndpoint, IncomingKind, IncomingTarget } from "./incoming-model"

export type TriggerView = "all" | "failing" | "off"

export interface TriggerRow {
  id: string
  name: string
  kind: IncomingKind
  target: IncomingTarget
  /** "GitHub signature" | "Crewship signature" | "Token in the URL" */
  checks: string
  enabled: boolean
  failing: boolean
  /** Routine and agent triggers; null = not counted for this source. */
  calls: number | null
  last: string | null
  endpoint?: IncomingEndpoint
}

export interface PageWebhookLite {
  id: string
  name?: string
  enabled?: boolean
  last_used_at?: string | null
}

export function checksLabel(e: IncomingEndpoint): string {
  if (e.routine?.ingress_profile === "github") return "GitHub signature"
  return "Crewship signature"
}

export function triggerRows(
  endpoints: IncomingEndpoint[],
  pages: { target: IncomingTarget; hooks: PageWebhookLite[] }[] = [],
  /** Calls per agent id from the delivery ledger; undefined until it has loaded. */
  agentCalls?: ReadonlyMap<string, number>,
): TriggerRow[] {
  const rows: TriggerRow[] = endpoints.map((e) => ({
    id: e.id,
    name: e.name,
    kind: e.target.kind,
    target: e.target,
    checks: checksLabel(e),
    enabled: e.enabled,
    failing: e.routine?.last_status === "FAILED",
    calls: e.routine ? e.routine.fire_count : e.target.kind === "agent" && agentCalls ? (agentCalls.get(e.target.id) ?? 0) : null,
    last: e.last ?? null,
    endpoint: e,
  }))
  for (const p of pages)
    for (const h of p.hooks)
      rows.push({
        id: h.id,
        name: h.name || p.target.name,
        kind: "page",
        target: p.target,
        checks: "Token in the URL",
        enabled: h.enabled !== false,
        failing: false,
        calls: null,
        last: h.last_used_at ?? null,
      })
  return rows
}

export function triggerCounts(rows: TriggerRow[]) {
  const counted = rows.filter((r) => r.calls !== null)
  return {
    total: rows.length,
    calls: counted.reduce((n, r) => n + (r.calls ?? 0), 0),
    countedSources: counted.length,
    failing: rows.filter((r) => r.failing).length,
    off: rows.filter((r) => !r.enabled).length,
  }
}

export function filterTriggers(
  rows: TriggerRow[],
  f: { view: TriggerView; starts: IncomingKind | null; target: string | null; checks: string | null; search: string },
): TriggerRow[] {
  const q = f.search.trim().toLowerCase()
  return rows.filter((r) => {
    if (f.view === "failing" && !r.failing) return false
    if (f.view === "off" && r.enabled) return false
    if (f.starts && r.kind !== f.starts) return false
    if (f.target && r.target.id !== f.target && r.target.slug !== f.target) return false
    if (f.checks && r.checks !== f.checks) return false
    if (q && !`${r.name} ${r.target.name} ${r.target.slug} ${r.checks}`.toLowerCase().includes(q)) return false
    return true
  })
}
