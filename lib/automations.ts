// Automations, as the two pages that live next to them need to read them.
//
// An automation watches ONE journal event type and, on a match, parks a
// deferred run of a routine (internal/automation). There is no management
// screen for them — `crewship automation …` is the write surface — which
// makes the read side here load-bearing rather than decorative: a routine
// that a rule can start, on a page that does not say so, is a routine whose
// behaviour is unexplainable from the product.
//
// Everything below is derived from `GET /api/v1/automations`, which returns
// the whole workspace's rules unfiltered. Filtering client-side is the right
// call at this size (a workspace has tens of rules, not thousands) and it
// keeps both surfaces on ONE cached fetch instead of two bespoke endpoints.

/** Mirrors `automation.Matcher` (internal/automation/types.go). */
export interface AutomationMatcher {
  crew_ids?: string[]
  agent_ids?: string[]
  mission_ids?: string[]
  severities?: string[]
  payload_equals?: Record<string, unknown>
}

/** Mirrors `automation.Action`. `routine_slug` is the routine that gets parked. */
export interface AutomationAction {
  routine_slug: string
  inputs?: Record<string, unknown>
}

/** Mirrors `automation.Automation` — one stored rule, as the API returns it. */
export interface Automation {
  id: string
  workspace_id: string
  name: string
  enabled: boolean
  event_type: string
  matcher: AutomationMatcher
  action_kind: string
  action: AutomationAction
  debounce_seconds: number
  max_per_hour: number
  created_by?: string
  created_at: string
  updated_at: string
}

/**
 * The journal entry types that carry a mission id, and can therefore be
 * emitted BY an issue.
 *
 * This is a whitelist rather than a "does the matcher exclude it" test alone,
 * and the distinction matters. The journal has ~117 entry types; the vast
 * majority (`container.metrics`, `llm.call`, `network.egress`, …) carry no
 * mission at all, so a rule watching one with an empty matcher would pass an
 * exclusion-only test and be listed on every issue in the workspace as
 * something that "could fire here". It could not.
 *
 * Kept in the same order as internal/journal/types.go so a diff between the
 * two is a one-line read. Adding a type here is safe; omitting one costs a
 * rule its mention on the issue page, which is why the list errs wide —
 * `approval.*` and `peer.escalation` are included because an issue's run is
 * what raises them.
 */
export const ISSUE_SCOPED_EVENT_TYPES: readonly string[] = [
  "mission.created",
  "mission.assigned",
  "mission.status_change",
  "mission.comment",
  "assignment.created",
  "assignment.running",
  "assignment.completed",
  "assignment.failed",
  "assignment.cancelled",
  "task.delegated",
  "agent.mentioned",
  "peer.escalation",
  "approval.requested",
  "approval.granted",
  "approval.denied",
  "approval.timeout",
  "approval.cancelled",
]

const ISSUE_SCOPED = new Set(ISSUE_SCOPED_EVENT_TYPES)

/**
 * Enabled first, then by name.
 *
 * A rule that is switched off still belongs on the page — "why did nothing
 * happen" is answered by seeing it there, greyed — but it must never be the
 * first thing read, or the page implies a trigger that is not armed.
 */
function byLiveness(a: Automation, b: Automation): number {
  if (a.enabled !== b.enabled) return a.enabled ? -1 : 1
  return (a.name || "").localeCompare(b.name || "")
}

/** The rules that can start `slug`. */
export function automationsForRoutine(list: Automation[], slug: string): Automation[] {
  if (!slug) return []
  return list.filter((a) => a?.action?.routine_slug === slug).sort(byLiveness)
}

/**
 * The rules an event from this issue could set off.
 *
 * Two of the matcher's five fields are decidable from an issue alone:
 *
 *   mission_ids  the issue's own id
 *   crew_ids     the crew the issue belongs to
 *
 * Both must be satisfied — the matcher's own semantics are AND across
 * populated fields, and an OR here would put every crew-scoped rule on every
 * issue in that crew.
 *
 * The other three are deliberately NOT evaluated. `agent_ids` names the agent
 * that EMITS the entry, which is not the assignee and is not knowable before
 * the fact; `severities` and `payload_equals` describe an event that has not
 * happened yet. Treating them as exclusions would hide rules that really do
 * fire here — the expensive direction of the error — so they narrow the
 * outcome without narrowing the list, and the UI says as much.
 */
export function automationsForIssue(
  list: Automation[],
  issue: { missionId: string; crewId?: string | null },
): Automation[] {
  return list
    .filter((a) => {
      if (!a || !ISSUE_SCOPED.has(a.event_type)) return false
      const m = a.matcher ?? {}
      if (m.mission_ids?.length && !m.mission_ids.includes(issue.missionId)) return false
      if (m.crew_ids?.length && !(issue.crewId && m.crew_ids.includes(issue.crewId))) return false
      return true
    })
    .sort(byLiveness)
}

/**
 * The `crewship` verbs a routine's definition acts with, sorted and unique.
 *
 * A `crewship` step is the difference between a routine that reads the board
 * and one that writes to it (internal/pipeline/crewship_step.go), so this is
 * a fact the routine page owes its reader.
 *
 * Walks foreach bodies as well as the top level. A routine that files one
 * issue per row of a report keeps its only write inside the loop; a
 * top-level-only scan would report it as read-only, which is the exact
 * inversion of the fact being surfaced.
 */
export function crewshipActionsInDefinition(definition: unknown): string[] {
  const found = new Set<string>()
  walk((definition as { steps?: unknown })?.steps, found, 0)
  return [...found].sort()
}

// Depth cap: the DSL nests foreach bodies, and a definition arrives from the
// server as opaque JSON. A cycle cannot occur in parsed JSON, but a
// pathologically deep one should still not blow the stack on a render path.
const MAX_WALK_DEPTH = 12

function walk(steps: unknown, found: Set<string>, depth: number): void {
  if (depth > MAX_WALK_DEPTH || !Array.isArray(steps)) return
  for (const raw of steps) {
    const st = raw as { type?: string; action?: string; foreach?: { steps?: unknown } }
    if (!st || typeof st !== "object") continue
    if (st.type === "crewship" && typeof st.action === "string" && st.action.trim() !== "") {
      found.add(st.action)
    }
    if (st.foreach) walk(st.foreach.steps, found, depth + 1)
  }
}
