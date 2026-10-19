/**
 * Which appearance values the user chose themselves (WSD-02).
 *
 * A value is explicit only when the user changed it — from the top-bar menu
 * or Settings › Appearance. Every other value is inherited: the workspace's
 * default look, else the built-in default. Sync (hooks/use-appearance-sync)
 * writes explicit values only, and "Use workspace default" clears the mark.
 *
 * This is a per-browser set of field names. The values themselves stay where
 * the pickers and the boot scripts already read them; the set only says which
 * of them came from the user. `style` stands for the style and its knobs.
 */

import { APPEARANCE_EXPLICIT_STORAGE_KEY, APPEARANCE_INHERITED_STORAGE_KEY, emitAppearanceChange } from "@/lib/theme/appearance"

export { APPEARANCE_EXPLICIT_STORAGE_KEY, APPEARANCE_INHERITED_STORAGE_KEY }

export const EXPLICIT_FIELDS = ["theme", "accent", "style", "density", "motion", "textSize", "sidebar", "avatarAnimation"] as const
export type ExplicitField = (typeof EXPLICIT_FIELDS)[number]

/** The fields a workspace default can supply. Not "Animated avatars"
 *  (#3789): whether avatars move is the viewer's own comfort setting, like
 *  Reduce motion, so it is never inherited and "Use workspace default" keeps it. */
export const INHERITABLE_FIELDS = ["theme", "accent", "style"] as const satisfies readonly ExplicitField[]

const memory = new Map<string, string>()

function isField(v: unknown): v is ExplicitField {
  return typeof v === "string" && (EXPLICIT_FIELDS as readonly string[]).includes(v)
}

function readKey(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return memory.get(key) ?? null
  }
}

function writeKey(key: string, fields: Set<ExplicitField>): void {
  const raw = JSON.stringify(EXPLICIT_FIELDS.filter((f) => fields.has(f)))
  memory.set(key, raw)
  try {
    localStorage.setItem(key, raw)
  } catch {
    // Blocked storage: the marks last for this page only.
  }
}

export function parseFieldList(raw: string | null): ExplicitField[] {
  if (!raw) return []
  try {
    const v: unknown = JSON.parse(raw)
    if (!Array.isArray(v)) return []
    return EXPLICIT_FIELDS.filter((f) => v.includes(f))
  } catch {
    return []
  }
}

/** Canonical JSON of the explicit set (fixed order): the snapshot for useSyncExternalStore. */
export function readExplicitRaw(): string {
  return JSON.stringify(parseFieldList(readKey(APPEARANCE_EXPLICIT_STORAGE_KEY)))
}

export function readExplicit(): Set<ExplicitField> {
  return new Set(parseFieldList(readKey(APPEARANCE_EXPLICIT_STORAGE_KEY)))
}

export function isExplicit(field: ExplicitField): boolean {
  return readExplicit().has(field)
}

export function readInherited(): Set<ExplicitField> {
  return new Set(parseFieldList(readKey(APPEARANCE_INHERITED_STORAGE_KEY)))
}

/** Records that a field's current value did (or no longer does) come from a workspace default. */
export function readInheritedRaw(): string {
  return JSON.stringify(parseFieldList(readKey(APPEARANCE_INHERITED_STORAGE_KEY)))
}

export function setInherited(field: ExplicitField, on: boolean): void {
  const set = readInherited()
  if (set.has(field) === on) return
  if (on) set.add(field)
  else set.delete(field)
  writeKey(APPEARANCE_INHERITED_STORAGE_KEY, set)
}

/** The user chose this value. */
export function markExplicit(field: ExplicitField): void {
  if (!isField(field)) return
  setInherited(field, false)
  const set = readExplicit()
  if (set.has(field)) return
  set.add(field)
  writeKey(APPEARANCE_EXPLICIT_STORAGE_KEY, set)
  emitAppearanceChange()
}

/** Forget the user's choice for these fields; they inherit again (the
 *  workspace default, else the built-in value). */
export function clearExplicit(fields: readonly ExplicitField[]): void {
  const set = readExplicit()
  for (const f of fields) {
    setInherited(f, true)
    set.delete(f)
  }
  writeKey(APPEARANCE_EXPLICIT_STORAGE_KEY, set)
  emitAppearanceChange()
}

/** Puts both sets back exactly as they were (Undo in Settings › Appearance). */
export function restoreMarks(explicit: ReadonlySet<ExplicitField>, inherited: ReadonlySet<ExplicitField>): void {
  writeKey(APPEARANCE_INHERITED_STORAGE_KEY, new Set(inherited))
  writeKey(APPEARANCE_EXPLICIT_STORAGE_KEY, new Set(explicit))
  emitAppearanceChange()
}
