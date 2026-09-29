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
