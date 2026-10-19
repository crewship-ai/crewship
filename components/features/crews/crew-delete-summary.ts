const count = (n: number, one: string, many: string) => (n === 0 ? `no ${many}` : `${n} ${n === 1 ? one : many}`)

/**
 * The object line of Delete crew (v67: "3 agents · 2 routines · container
 * running"): what the person is about to take away, counted. A count that is
 * not known yet (null / undefined) is left out rather than shown as zero, so
 * the line never claims "no routines" for a request that has not answered.
 * `container`: its state, null = the crew has none, undefined = not read yet.
 */
export function crewDeleteSummary({ agents, routines, container }: { agents: number | null; routines: number | null; container?: string | null }): string {
  const parts: string[] = []
  if (agents != null) parts.push(count(agents, "agent", "agents"))
  if (routines != null) parts.push(count(routines, "routine", "routines"))
  if (container === null) parts.push("no container")
  else if (container) parts.push(`container ${container}`)
  return parts.join(" · ")
}
