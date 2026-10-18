/**
 * Where every object lives — the one map behind every cross-link (README §5).
 *
 * Screens used to spell these by hand and drifted: /agents/<id> (dead),
 * /orchestration/issues (dead), /crews/<id> (dead), unscoped /issues from a
 * crew. A link built here cannot point at a route that does not exist, and
 * when a route moves it moves in one place.
 */
const enc = encodeURIComponent

export type EntityRef =
  | { kind: "crew"; slug: string; tab?: string }
  | { kind: "agent"; slug: string; tab?: string }
  | { kind: "chat"; agentSlug: string; sessionId?: string }
  | { kind: "issue"; identifier: string }
  /** Ids, not slugs: /issues and GET /issues match crew_id / assignee_id against ids. */
  | { kind: "issues"; crewId?: string; assigneeId?: string; status?: string }
  | { kind: "routine"; slug: string; tab?: string }
  | { kind: "routines"; crewSlug?: string }
  | { kind: "run"; runId: string; pipelineSlug?: string }
  | { kind: "journal"; crewSlug?: string; agentSlug?: string; missionId?: string; traceId?: string }
  | { kind: "page"; slug: string }
  | { kind: "credential"; id: string }
  | { kind: "credentials"; crewSlug?: string }
  | { kind: "inbox"; itemId?: string; itemKind?: string; agentSlug?: string; attention?: "approvals" | "run-alerts" | "schedule-alerts" }
  | { kind: "spend"; crewId?: string }
  | { kind: "integrations"; tab?: "notifications" | "tools"; section?: string; server?: string }
  | { kind: "skill"; id: string; tab?: string }
  | { kind: "skills"; agentId?: string; crewId?: string }

function withQuery(path: string, params: Record<string, string | undefined>): string {
  const qs = Object.entries(params)
    .filter((entry): entry is [string, string] => typeof entry[1] === "string" && entry[1].length > 0)
    .map(([k, v]) => `${enc(k)}=${enc(v)}`)
    .join("&")
  return qs ? `${path}?${qs}` : path
}

export function entityHref(ref: EntityRef): string {
  switch (ref.kind) {
    case "crew":
      return withQuery("/crews", { crew: ref.slug, tab: ref.tab })
    case "agent":
      return withQuery("/crews", { agent: ref.slug, tab: ref.tab })
    case "chat":
      return withQuery(`/chat/${enc(ref.agentSlug)}`, { session: ref.sessionId })
    case "issue":
      return `/issues/${enc(ref.identifier)}`
    case "issues":
      // The keys the Issues page owns (hooks/use-issue-filters.ts) — a
      // `?crew=` / `?assignee=` link used to land on the unfiltered list.
      return withQuery("/issues", { crew_id: ref.crewId, assignee_id: ref.assigneeId, status: ref.status })
    case "routine":
      return withQuery("/routines", { slug: ref.slug, tab: ref.tab })
    case "routines":
      return withQuery("/routines", { crew: ref.crewSlug })
    case "run":
      return withQuery("/activity", { run: ref.runId, pipeline: ref.pipelineSlug })
    case "journal":
      // The Journal is Activity now (F-02): straight to the list, in its own
      // keys. Activity reads a slug or an issue identifier as readily as an
      // id (it resolves them through the workspace lookup).
      return withQuery("/activity", { crew: ref.crewSlug, agent: ref.agentSlug, mission: ref.missionId, run: ref.traceId })
    case "page":
      return `/pages/${enc(ref.slug)}`
    case "credential":
      return withQuery("/credentials", { id: ref.id })
    case "credentials":
      return withQuery("/credentials", { crew: ref.crewSlug })
    case "inbox":
      return withQuery("/inbox", { item: ref.itemId, kind: ref.itemKind, agent: ref.agentSlug, attention: ref.attention })
    case "spend":
      // Spend is one view for the whole workspace; it has no crew filter.
      return "/activity?mode=spend"
    case "integrations":
      return withQuery("/integrations", { tab: ref.tab, section: ref.section, server: ref.server })
    case "skill":
      return withQuery("/skills", { skill: ref.id, tab: ref.tab })
    case "skills":
      return withQuery("/skills", { agent: ref.agentId, crew: ref.crewId })
  }
}
