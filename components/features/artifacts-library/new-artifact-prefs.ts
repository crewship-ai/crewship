import { KIND_ORDER, KINDS, type ArtifactKind } from "./kinds"

/**
 * What the New artifact button remembers per person: pinned kinds (first in its
 * menu, keys 1–5), the kinds made recently (the main button makes the newest
 * one) and whether the Home guide was dismissed. Stored as user preferences, so
 * they follow the person across devices and are never shared with others.
 */
export const PIN_MAX = 5
const RECENT_MAX = 4

/** The editor and file types a Craft kind opens, for search and the menu's second line. */
export const KIND_EDITOR: Partial<Record<ArtifactKind, { app: string; extensions: string }>> = {
  word: { app: "WordCraft", extensions: ".docx" },
  sheet: { app: "GridCraft", extensions: ".xlsx .csv" },
}

const isKind = (value: unknown): value is ArtifactKind => typeof value === "string" && (KIND_ORDER as readonly string[]).includes(value)

const unique = (list: readonly ArtifactKind[]) => [...new Set(list)]

/** The pins in force: what the person stored, or the first five kinds they can make until they change it. */
export function effectivePins(stored: readonly unknown[] | null | undefined, available: readonly ArtifactKind[]): ArtifactKind[] {
  if (Array.isArray(stored)) return unique(stored.filter(isKind)).slice(0, PIN_MAX)
  return KIND_ORDER.filter(kind => available.includes(kind)).slice(0, PIN_MAX)
}

export type PinResult = "pinned" | "unpinned" | "full" | "unavailable"

/** Star a kind: unpinning always works; pinning needs a free slot and a kind this person can make. */
export function togglePin(pins: readonly ArtifactKind[], kind: ArtifactKind, available: readonly ArtifactKind[]): { pins: readonly ArtifactKind[]; result: PinResult } {
  if (pins.includes(kind)) return { pins: pins.filter(entry => entry !== kind), result: "unpinned" }
  if (!available.includes(kind)) return { pins, result: "unavailable" }
  if (pins.length >= PIN_MAX) return { pins, result: "full" }
  return { pins: [...pins, kind], result: "pinned" }
}

export function movePin(pins: readonly ArtifactKind[], kind: ArtifactKind, delta: -1 | 1): ArtifactKind[] {
  const list = [...pins]
  const at = list.indexOf(kind)
  const to = at + delta
  if (at < 0 || to < 0 || to >= list.length) return list
  ;[list[at], list[to]] = [list[to], list[at]]
  return list
}

export function pushRecent(recent: readonly unknown[] | null | undefined, kind: ArtifactKind): ArtifactKind[] {
  const list = Array.isArray(recent) ? recent.filter(isKind) : []
  return [kind, ...list.filter(entry => entry !== kind)].slice(0, RECENT_MAX)
}

/** What the main button makes: the newest kind still available, else the first available one. */
export function lastKind(recent: readonly unknown[] | null | undefined, available: readonly ArtifactKind[]): ArtifactKind | undefined {
  const list = Array.isArray(recent) ? recent.filter(isKind) : []
  return list.find(kind => available.includes(kind)) ?? KIND_ORDER.find(kind => available.includes(kind))
}

export function matchesKind(kind: ArtifactKind, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  const def = KINDS[kind]
  const editor = KIND_EDITOR[kind]
  return [def.label, def.plural, def.noun, def.description, editor?.app, editor?.extensions].filter(Boolean).join(" ").toLowerCase().includes(q)
}
