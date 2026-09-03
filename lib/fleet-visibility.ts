/**
 * Which agents belong on the client's fleet screen.
 *
 * The onboarding guide is an agent row like any other — `_crewship-setup-guide`
 * — living in a crew of kind "setup" that GET /crews deliberately never lists.
 * GET /agents does return it, so /crews showed "Crewship Guide · crew —" as the
 * first row of the roster while the explorer, which walks crews, never did
 * (docs/ux/audit-fleet.md §2). One rule, applied once where the page fetches,
 * so the roster, the explorer and the sub-bar count agree.
 */

export interface FleetAgentLike {
  slug: string
  crew_id: string | null
}

export function isInternalAgent(agent: FleetAgentLike, crews: ReadonlyArray<{ id: string }>): boolean {
  if (agent.slug.startsWith("_")) return true
  if (agent.crew_id == null) return false
  return !crews.some((c) => c.id === agent.crew_id)
}

/** The agents to show. Returns the input array itself when nothing is hidden. */
export function visibleFleetAgents<T extends FleetAgentLike>(agents: T[], crews: ReadonlyArray<{ id: string }>): T[] {
  const visible = agents.filter((a) => !isInternalAgent(a, crews))
  return visible.length === agents.length ? agents : visible
}
