/**
 * Crew links — the model every view reads. Pure, framework-free.
 *
 * The server stores a link as one row per crew pair (crew_connections):
 * `unidirectional` from → to, or `bidirectional`, plus shared-file access per
 * direction (forward = what `from` may do with `to`'s files, reverse = the
 * other way). People think in DIRECTIONS instead: "may Engineering hand work
 * to Ops, and may it read Ops' shared files?". directionsOf() is that view;
 * the crew page, the All links list and the Matrix all render from it, as one
 * ladder per direction: Off, Work only, View files, Deliver files.
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

// ── The ladder ─────────────────────────────────────────────────────────────
//
// One ladder per direction: Off, Work only, View files, Deliver files. Each
// step includes the ones before it. The server keeps two facts per direction
// (does the direction exist, and the file access of the one handing work);
// the ladder is their one-word reading, and planLevels() is the way back.

export type Level = "off" | "work" | "view" | "deliver"

export const LEVELS: readonly Level[] = ["off", "work", "view", "deliver"]
/** Servers that do not report file access can only say Off or Work only. */
export const WORK_LEVELS: readonly Level[] = ["off", "work"]

export const LEVEL_LABEL: Record<Level, string> = {
  off: "Off",
  work: "Work only",
  view: "View files",
  deliver: "Deliver files",
}

/** What each step allows, for the "What each level allows" card. */
export const LEVEL_ALLOWS: Record<Exclude<Level, "off">, string> = {
  work: "Assign tasks and message the other crew’s agents",
  view: "Also read the other crew’s shared folder",
  deliver: "Also write into its incoming folder, nowhere else",
}

export const levelRank = (l: Level) => LEVELS.indexOf(l)

export const fileAccessOf = (l: Level): FileAccess => (l === "view" ? "read" : l === "deliver" ? "read_write" : "none")
export const levelOfFiles = (f: FileAccess | null | undefined): Level => (f === "read" ? "view" : f === "read_write" ? "deliver" : "work")

/** One direction as a sentence a person can check at a glance. */
export function levelSentence(from: string, to: string, level: Level): string {
  if (level === "off") return `${from} cannot hand work to ${to}`
  if (level === "work") return `Assigns tasks and messages ${to}’s agents`
  if (level === "view") return `Assigns tasks, and reads ${to}’s shared files`
  return `Assigns tasks, reads ${to}’s shared files and delivers into them`
}

export interface PairLevels {
  /** What `self` may do with the other crew: its direction out. */
  out: Level
  /** What the other crew may do with `self`. */
  in: Level
  /** The row says inactive: shown muted, both directions read as Off. */
  inactive: boolean
}

/** The file access held by the crew handing work, as the row stores it. */
function fileOf(conn: Connection, requester: string): FileAccess | null {
  return (conn.from_crew_id === requester ? conn.forward_file_access : conn.reverse_file_access) ?? null
}

/** A stored pair read as two ladders, from `self`'s side. */
export function pairLevels(conn: Connection | undefined, self: string): PairLevels {
  if (!conn) return { out: "off", in: "off", inactive: false }
  if (conn.status === "inactive") return { out: "off", in: "off", inactive: true }
  const two = conn.direction === "bidirectional"
  const outs = two || conn.from_crew_id === self
  const ins = two || conn.to_crew_id === self
  const other = conn.from_crew_id === self ? conn.to_crew_id : conn.from_crew_id
  return {
    out: outs ? levelOfFiles(fileOf(conn, self)) : "off",
    in: ins ? levelOfFiles(fileOf(conn, other)) : "off",
    inactive: false,
  }
}

export type Side = "out" | "in"

export interface LevelPlan {
  /** The pair state (seen from the first crew) to reach; null when only file levels change. */
  pair: PairState | null
  /** The last direction goes: the whole link is removed, after asking. */
  unlink: boolean
  /** Directions that need their file level set after the pair exists. */
  files: { side: Side; level: FileAccess }[]
}

const stateOfLevels = (l: { out: Level; in: Level }) => combinePair(l.out !== "off", l.in !== "off")

/** What a move from one pair of ladders to another asks of the server. */
export function planLevels(cur: { out: Level; in: Level }, next: { out: Level; in: Level }): LevelPlan {
  const from = stateOfLevels(cur)
  const to = stateOfLevels(next)
  if (to === "none") return { pair: from === "none" ? null : "none", unlink: from !== "none", files: [] }
  const files: LevelPlan["files"] = []
  for (const side of ["out", "in"] as const) {
    if (next[side] !== "off" && next[side] !== cur[side]) files.push({ side, level: fileAccessOf(next[side]) })
  }
  return { pair: to === from ? null : to, unlink: false, files }
}

/** One ladder moved: the other keeps its step. */
export const planChange = (cur: { out: Level; in: Level }, side: Side, level: Level): LevelPlan => planLevels(cur, { ...cur, [side]: level })

// ── Rows for the lists ─────────────────────────────────────────────────────

/** One link as a list row: A hands to B at `ab`, B hands to A at `ba`. */
export interface LinkRow {
  conn: Connection
  a: Crew
  b: Crew
  ab: Level
  ba: Level
  inactive: boolean
}

export function linkRows(crews: Crew[], connections: Connection[]): LinkRow[] {
  const byId = new Map(crews.map((c) => [c.id, c]))
  const rows: LinkRow[] = []
  for (const conn of connections) {
    const a = byId.get(conn.from_crew_id)
    const b = byId.get(conn.to_crew_id)
    if (!a || !b) continue
    const l = pairLevels(conn, a.id)
    rows.push({ conn, a, b, ab: l.out, ba: l.in, inactive: l.inactive })
  }
  return rows.sort((x, y) => byName(x.a, y.a) || byName(x.b, y.b))
}

export const canDeliver = (r: LinkRow) => r.ab === "deliver" || r.ba === "deliver"
export const rowAtLevel = (r: LinkRow, level: CrewLinkFilters["level"]) => level === "" || r.ab === level || r.ba === level

/** The crews linked to `id`, each once. */
export function partnersOf(id: string, connections: Connection[]): string[] {
  const out = new Set<string>()
  for (const c of connections) {
    if (c.from_crew_id === id) out.add(c.to_crew_id)
    else if (c.to_crew_id === id) out.add(c.from_crew_id)
  }
  return [...out]
}

export function partnerCounts(connections: Connection[]): Map<string, number> {
  const sets = new Map<string, Set<string>>()
  const add = (k: string, v: string) => { const s = sets.get(k); if (s) s.add(v); else sets.set(k, new Set([v])) }
  for (const c of connections) { add(c.from_crew_id, c.to_crew_id); add(c.to_crew_id, c.from_crew_id) }
  return new Map([...sets].map(([k, s]) => [k, s.size]))
}

/** Most linked first, then by name. */
export function byMostLinked(counts: Map<string, number>) {
  return (a: Crew, b: Crew) => (counts.get(b.id) ?? 0) - (counts.get(a.id) ?? 0) || byName(a, b)
}

/** The crews a panel names: the most linked of those given, never more than `n`. */
export const mostLinked = (crews: Crew[], connections: Connection[], n = 8): Crew[] => {
  const counts = partnerCounts(connections)
  return crews.filter((c) => (counts.get(c.id) ?? 0) > 0).sort(byMostLinked(counts)).slice(0, n)
}

/** A crew's partners with the two levels, most recently named first by name. */
export interface Partner {
  crew: Crew
  conn: Connection
  levels: PairLevels
}

export function partnersWithLevels(self: string, crews: Crew[], connections: Connection[]): Partner[] {
  const byId = new Map(crews.map((c) => [c.id, c]))
  const out: Partner[] = []
  for (const conn of connections) {
    if (conn.from_crew_id !== self && conn.to_crew_id !== self) continue
    const crew = byId.get(conn.from_crew_id === self ? conn.to_crew_id : conn.from_crew_id)
    if (crew) out.push({ crew, conn, levels: pairLevels(conn, self) })
  }
  return out.sort((x, y) => byName(x.crew, y.crew))
}

/** Per-crew figures for the All crews table. */
export function crewFigures(id: string, connections: Connection[]) {
  let out = 0
  let into = 0
  const partners = new Set<string>()
  for (const conn of connections) {
    if (conn.from_crew_id !== id && conn.to_crew_id !== id) continue
    partners.add(conn.from_crew_id === id ? conn.to_crew_id : conn.from_crew_id)
    const l = pairLevels(conn, id)
    if (l.out !== "off") out += 1
    if (l.in !== "off") into += 1
  }
  return { partners: partners.size, out, in: into }
}

/** The matrix draws at most this many crews. */
export const MATRIX_LIMIT = 12

/** The most linked crews among those the shown links join. */
export function matrixCrews(rows: LinkRow[], connections: Connection[], limit = MATRIX_LIMIT): { crews: Crew[]; total: number } {
  const seen = new Map<string, Crew>()
  for (const r of rows) { seen.set(r.a.id, r.a); seen.set(r.b.id, r.b) }
  const counts = partnerCounts(connections)
  const all = [...seen.values()].sort(byMostLinked(counts))
  return { crews: all.slice(0, limit), total: all.length }
}

// ── Views and the URL ──────────────────────────────────────────────────────

export type CrewLinksView = "crew" | "all" | "deliver" | "crews" | "alone" | "matrix"

/** ?view= and the old ?links=: `all` and `matrix` keep working, ?links=linked|none maps to All crews / Working alone. */
export function parseView(view: string | null, links: string | null): CrewLinksView {
  if (view === "all" || view === "deliver" || view === "crews" || view === "alone" || view === "matrix") return view
  if (links === "none") return "alone"
  if (links === "linked") return "crews"
  return "crew"
}

// ── The audit trail of one crew's links ────────────────────────────────────

export interface LinkAuditEntry {
  id: string
  action: string
  at: string
  actor: string | null
  previous: Record<string, unknown> | null
  current: Record<string, unknown> | null
}

const str = (v: unknown) => (typeof v === "string" ? v : "")

/** The two crews an audit row is about, from the state it recorded. */
export function entryCrews(e: LinkAuditEntry): [string, string] | null {
  const s = e.current && str(e.current.from_crew_id) ? e.current : e.previous
  if (!s || !str(s.from_crew_id) || !str(s.to_crew_id)) return null
  return [str(s.from_crew_id), str(s.to_crew_id)]
}

/** One audit row in the words of this page, or null when it cannot be read. */
export function describeLinkChange(e: LinkAuditEntry, nameOf: (id: string) => string): string | null {
  const pair = entryCrews(e)
  if (!pair) return null
  const [a, b] = [nameOf(pair[0]), nameOf(pair[1])]
  if (e.action === "crew_link.create") return `Linked ${a} and ${b}`
  if (e.action === "crew_link.delete") return `Unlinked ${a} and ${b}`
  if (e.action === "crew_link.file_access") {
    const cur = e.current ?? {}
    const prev = e.previous ?? {}
    const parts: string[] = []
    const one = (key: "forward_file_access" | "reverse_file_access", from: string, to: string) => {
      if (str(cur[key]) && str(cur[key]) !== str(prev[key])) {
        parts.push(`${from} → ${to}: ${LEVEL_LABEL[levelOfFiles(str(prev[key]) as FileAccess)]} → ${LEVEL_LABEL[levelOfFiles(str(cur[key]) as FileAccess)]}`)
      }
    }
    one("forward_file_access", a, b)
    one("reverse_file_access", b, a)
    return parts.length ? parts.join(" · ") : `Changed file access between ${a} and ${b}`
  }
  return `Changed how ${a} and ${b} work together`
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
  /** Crews with agents, or without (collectors, samplers). "" = any. */
  agents: "" | "with" | "none"
  net: "" | "restricted" | "open"
  q: string
  /** All links only: links where either direction is at this level. "" = any. */
  level: "" | "deliver" | "view" | "work"
}

export const DEFAULT_CREW_LINK_FILTERS: CrewLinkFilters = { agents: "", net: "", q: "", level: "" }

export const isOpenNetwork = (c: Crew) => c.network_mode != null && c.network_mode !== "restricted"

/** The crew's agents when the list loaded, else the count the crew row carries. */
export function agentCount(c: Crew, agents: CrewAgent[] | undefined): number | null {
  if (agents) return agents.length
  return c._count?.agents ?? null
}

export function crewMatches(c: Crew, f: CrewLinkFilters, agents: CrewAgent[] | undefined): boolean {
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
export const activeFacetCount = (f: CrewLinkFilters) => Number(Boolean(f.agents)) + Number(Boolean(f.net))

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
