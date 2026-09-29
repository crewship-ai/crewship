/**
 * Crew links — the model every view reads. Pure, framework-free.
 *
 * The server stores a link as one row per crew pair (crew_connections):
 * `unidirectional` from → to, or `bidirectional`, plus shared-file access per
 * direction (forward = what `from` may do with `to`'s files, reverse = the
 * other way). People think in DIRECTIONS instead: "may Engineering hand work
 * to Ops, and may it read Ops' shared files?". directionsOf() is that view;
 * the By crew list, the All links table and the Matrix all render from it.
 *
 * A link never grants shell, credentials or private agent homes.
 */

export interface Crew {
  id: string
  name: string
  slug: string
  color?: string | null
  /** The icon picked in Crews & Agents — a crew looks the same everywhere. */
  icon?: string | null
  description?: string | null
  /** "restricted", or anything else (the server writes "free") for open. */
  network_mode?: string | null
  container_memory_mb?: number | null
  container_cpus?: number | null
  runtime_image?: string | null
  avatar_style?: string | null
  _count?: { agents?: number; members?: number }
}

/** The fields of GET /api/v1/agents the profile and the crew list read. */
export interface CrewAgent {
  id: string
  name: string
  slug: string
  crew_id: string | null
  status?: string
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
}

/** none = no access; read = view `/crew/shared`; read_write = view and deliver
 *  into the other crew's incoming folder. */
export type FileAccess = "none" | "read" | "read_write"

export interface Connection {
  id: string
  from_crew_id: string
  to_crew_id: string
  direction: string
  status: string
  forward_file_access?: FileAccess
  reverse_file_access?: FileAccess
  access_version?: number
}

/** A pair seen from one crew: none, it hands work (out), it receives work
 *  (in), or both ways. */
export type PairState = "none" | "out" | "in" | "both"

export interface Direction {
  from: string
  to: string
  /** What `from` may do with `to`'s shared files; null when the server does
   *  not report file access (older servers). */
  files: FileAccess | null
  connection: Connection
}

export const FILE_LABELS: Record<FileAccess, string> = {
  none: "No files",
  read: "View",
  read_write: "View + deliver",
}

export const dirKey = (from: string, to: string) => `${from}>${to}`

/** Every direction in which one crew may hand work to another. */
export function directionsOf(connections: Connection[]): Map<string, Direction> {
  const out = new Map<string, Direction>()
  for (const c of connections) {
    out.set(dirKey(c.from_crew_id, c.to_crew_id), { from: c.from_crew_id, to: c.to_crew_id, files: c.forward_file_access ?? null, connection: c })
    if (c.direction === "bidirectional") {
      out.set(dirKey(c.to_crew_id, c.from_crew_id), { from: c.to_crew_id, to: c.from_crew_id, files: c.reverse_file_access ?? null, connection: c })
    }
  }
  return out
}

/** The stored row for a pair, in whichever orientation it was written. */
export function connectionBetween(connections: Connection[], a: string, b: string): Connection | undefined {
  return connections.find((c) => (c.from_crew_id === a && c.to_crew_id === b) || (c.from_crew_id === b && c.to_crew_id === a))
}

/** How crew `self` sees a stored row. */
export function stateOf(conn: Connection | undefined, self: string): PairState {
  if (!conn) return "none"
  if (conn.direction === "bidirectional") return "both"
  return conn.from_crew_id === self ? "out" : "in"
}

export function combinePair(out: boolean, inn: boolean): PairState {
  return out && inn ? "both" : out ? "out" : inn ? "in" : "none"
}

export function splitPair(state: PairState): { out: boolean; in: boolean } {
  return { out: state === "out" || state === "both", in: state === "in" || state === "both" }
}

export function crewLinkStats(crews: Crew[], connections: Connection[]) {
  const dirs = [...directionsOf(connections).values()]
  const linked = new Set(connections.flatMap((c) => [c.from_crew_id, c.to_crew_id]))
  return {
    links: connections.length,
    directions: dirs.length,
    delivering: dirs.filter((d) => d.files === "read_write").length,
    alone: crews.filter((c) => !linked.has(c.id)).length,
  }
}

/** One direction as a sentence a person can check at a glance. */
export function directionSentence(from: string, to: string, files: FileAccess | null): string {
  if (files === "read") return `${from} can hand work to ${to} and read its shared files`
  if (files === "read_write") return `${from} can hand work to ${to}, read its shared files and deliver into them`
  return `${from} can hand work to ${to}`
}

/** Names two or more crews share — show their slug so they can be told apart. */
export function duplicateNames(crews: Crew[]): Set<string> {
  const seen = new Set<string>()
  const dup = new Set<string>()
  for (const c of crews) (seen.has(c.name) ? dup : seen).add(c.name)
  return dup
}

export const byName = (a: Crew, b: Crew) => a.name.localeCompare(b.name) || a.slug.localeCompare(b.slug)

// ── What the side panel filters on ─────────────────────────────────────────

export interface CrewLinkFilters {
  /** Linked = in at least one link. */
  links: "all" | "linked" | "none"
  /** Crews with agents, or without (collectors, samplers). "" = any. */
  agents: "" | "with" | "none"
  net: "" | "restricted" | "open"
  q: string
}

export const DEFAULT_CREW_LINK_FILTERS: CrewLinkFilters = { links: "all", agents: "", net: "", q: "" }

export const isOpenNetwork = (c: Crew) => c.network_mode != null && c.network_mode !== "restricted"

/** The crew's agents when the list loaded, else the count the crew row carries. */
export function agentCount(c: Crew, agents: CrewAgent[] | undefined): number | null {
  if (agents) return agents.length
  return c._count?.agents ?? null
}

export function crewMatches(c: Crew, f: CrewLinkFilters, linked: boolean, agents: CrewAgent[] | undefined): boolean {
  if (f.links === "linked" && !linked) return false
  if (f.links === "none" && linked) return false
  if (f.agents) {
    const n = agentCount(c, agents)
    if (n == null || (f.agents === "with") !== n > 0) return false
  }
  if (f.net && (c.network_mode == null || (f.net === "open") !== isOpenNetwork(c))) return false
  const q = f.q.trim().toLowerCase()
  if (q && !`${c.name} ${c.slug}`.toLowerCase().includes(q) && !(agents ?? []).some((a) => a.name.toLowerCase().includes(q))) return false
  return true
}

/** Facets in use (the search is shown on its own). */
export const activeFacetCount = (f: CrewLinkFilters) => Number(f.links !== "all") + Number(Boolean(f.agents)) + Number(Boolean(f.net))

/** "4 GB · 2 CPU" — null when the crew row does not say. */
export function crewResources(c: Crew): string | null {
  if (!c.container_memory_mb) return null
  const mem = c.container_memory_mb >= 1024 ? `${+(c.container_memory_mb / 1024).toFixed(1)} GB` : `${c.container_memory_mb} MB`
  return c.container_cpus ? `${mem} · ${c.container_cpus} CPU` : mem
}

/** "mcr.microsoft.com/devcontainers/javascript-node:22@sha256:…" → "javascript-node:22". */
export function shortImage(image: string | null | undefined): string | null {
  if (!image) return null
  const noDigest = image.split("@")[0]
  return noDigest.slice(noDigest.lastIndexOf("/") + 1) || null
}
