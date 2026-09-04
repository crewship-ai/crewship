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
  | { kind: "issues"; crewSlug?: string; assigneeSlug?: string; status?: string }
  | { kind: "routine"; slug: string; tab?: string }
  | { kind: "routines"; crewSlug?: string }
  | { kind: "run"; runId: string; pipelineSlug?: string }
  | { kind: "journal"; crewSlug?: string; agentSlug?: string; missionId?: string; traceId?: string }
  | { kind: "page"; slug: string }
  | { kind: "credential"; id: string }
  | { kind: "credentials"; crewSlug?: string }
  | { kind: "inbox"; itemId?: string; itemKind?: string; agentSlug?: string }
  | { kind: "spend"; crewId?: string }
  | { kind: "integrations"; tab?: "notifications" | "tools"; section?: string; server?: string }

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
      return withQuery("/issues", { crew: ref.crewSlug, assignee: ref.assigneeSlug, status: ref.status })
    case "routine":
      return withQuery("/routines", { slug: ref.slug, tab: ref.tab })
    case "routines":
      return withQuery("/routines", { crew: ref.crewSlug })
    case "run":
      return withQuery("/activity", { run: ref.runId, pipeline: ref.pipelineSlug })
    case "journal":
      // /journal reads crew_id / agent_id (the server resolves a slug as
      // readily as an id — resolveJournalRefs), so these are the keys the
      // page owns rather than aliases it has to learn.
      return withQuery("/journal", { crew_id: ref.crewSlug, agent_id: ref.agentSlug, mission_id: ref.missionId, trace_id: ref.traceId })
    case "page":
      return `/pages/${enc(ref.slug)}`
    case "credential":
      return withQuery("/credentials", { id: ref.id })
    case "credentials":
      return withQuery("/credentials", { crew: ref.crewSlug })
    case "inbox":
      return withQuery("/inbox-v2", { item: ref.itemId, kind: ref.itemKind, agent: ref.agentSlug })
    case "spend":
      return withQuery("/paymaster", { crew: ref.crewId })
    case "integrations":
      return withQuery("/integrations", { tab: ref.tab, section: ref.section, server: ref.server })
  }
}
