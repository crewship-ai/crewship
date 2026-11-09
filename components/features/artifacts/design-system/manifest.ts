/**
 * Design System artifact manifest: a project artifact whose file manifest holds
 * `design-system.json`. Storage, versions, restore and sharing are the existing
 * artifact VersionStore; this module only admits the token file for a
 * read-only preview. Every value is a closed literal (hex color, integer pixel,
 * plain font-family list, plain text) so nothing from the file can become a CSS
 * expression, URL or markup when rendered.
 *
 * Two schema versions are read. Schema 1 holds one value per colour. Schema 2
 * adds `themes` (1–8, the first is the default), per-theme colour values, a
 * usage note per colour and a group per text style. Schema 1 files are read
 * exactly as before; nothing upgrades a file, and a newer schema is reported
 * as newer rather than shown in part.
 */

export const DESIGN_SYSTEM_FILE = "design-system.json"
export const DESIGN_SYSTEM_MAX_BYTES = 32 * 1024
export const DESIGN_SYSTEM_MAX_TOKENS = 128
export const DESIGN_SYSTEM_MAX_PX = 4096
export const DESIGN_SYSTEM_MAX_THEMES = 8
/** The newest `schema_version` this client reads. */
export const DESIGN_SYSTEM_SCHEMA = 2
const MAX_COMPONENTS = 64

export interface DesignToken<T> { name: string; value: T }
export interface DesignTheme { id: string; name: string }
/**
 * `value` is the default (first) theme's colour; `values` holds every theme's,
 * aliases resolved (empty in schema 1). `aliases` names the token a theme's
 * value was spelled as, `{other}`, when it was an alias.
 */
export interface DesignColor { name: string; value: string; values: Record<string, string>; aliases: Record<string, string>; usage: string | null }
export interface DesignTypeStyle { name: string; fontFamily: string | null; size: number; weight: number | null; lineHeight: number | null; group: string | null }
export interface DesignComponent { name: string; description: string | null }
export interface DesignSystem {
  schemaVersion: 1 | 2
  name: string | null
  themes: DesignTheme[]
  colors: DesignColor[]
  typography: DesignTypeStyle[]
  spacing: DesignToken<number>[]
  radii: DesignToken<number>[]
  components: DesignComponent[]
}

/** The colour in a theme; the default value for schema 1 or a theme the file does not name. */
export function colorIn(color: DesignColor, theme: string | null): string {
  return theme !== null && Object.hasOwn(color.values, theme) ? color.values[theme] : color.value
}

type Json = Record<string, unknown>
const object = (value: unknown): value is Json => value !== null && typeof value === "object" && !Array.isArray(value)
const only = (value: Json, required: string[], optional: string[] = []) =>
  required.every(key => Object.hasOwn(value, key)) && Object.keys(value).every(key => required.includes(key) || optional.includes(key))
const tokenName = (name: string) => name.length <= 48 && /^[a-z][a-z0-9-]*$/.test(name) && name !== "constructor" && name !== "prototype"
const text = (value: unknown, max: number): value is string => typeof value === "string" && value.trim().length > 0 && value.length <= max && !/[\u0000-\u001f\u007f-\u009f]/.test(value)
const pixels = (value: unknown, min: number): value is number => Number.isSafeInteger(value) && (value as number) >= min && (value as number) <= DESIGN_SYSTEM_MAX_PX

function group<T>(value: unknown, admit: (name: string, raw: unknown) => T | null): T[] | null {
  if (!object(value) || Object.keys(value).length > DESIGN_SYSTEM_MAX_TOKENS) return null
  const result: T[] = []
  for (const [name, raw] of Object.entries(value)) {
    const token = tokenName(name) ? admit(name, raw) : null
    if (token === null) return null
    result.push(token)
  }
  return result
}

const hex = (value: unknown): value is string => typeof value === "string" && /^#[0-9a-fA-F]{6}$/.test(value)

function themes(value: unknown): DesignTheme[] | null {
  if (!Array.isArray(value) || value.length < 1 || value.length > DESIGN_SYSTEM_MAX_THEMES) return null
  const ids = new Set<string>(), result: DesignTheme[] = []
  for (const item of value) {
    if (!object(item) || !only(item, ["id", "name"]) || typeof item.id !== "string" || !tokenName(item.id) || ids.has(item.id) || !text(item.name, 40)) return null
    ids.add(item.id)
    result.push({ id: item.id, name: item.name })
  }
  return result
}

/** One theme's spelling: a literal or an alias `{other-token}`. */
type ColorSpec = { hex: string } | { alias: string }
const aliasName = (value: unknown): string | null => {
  const match = typeof value === "string" ? /^\{([a-z][a-z0-9-]*)\}$/.exec(value) : null
  return match && tokenName(match[1]) ? match[1] : null
}
function spec(value: unknown, themed: boolean): ColorSpec | null {
  if (hex(value)) return { hex: value.toLowerCase() }
  const alias = themed ? aliasName(value) : null
  return alias ? { alias } : null
}

/**
 * Schema 1: `"#rrggbb"`. Schema 2 also `"{other}"` (an alias) or
 * `{value: {<theme id>: "#rrggbb" | "{other}"}, usage?}`; a theme the value
 * leaves out takes the first theme's. Aliases resolve per theme afterwards.
 */
function colorSpec(raw: unknown, themed: DesignTheme[] | null): { specs: Record<string, ColorSpec> | ColorSpec; usage: string | null } | null {
  const plain = spec(raw, Boolean(themed))
  if (plain) return { specs: plain, usage: null }
  if (!themed || !object(raw) || !only(raw, ["value"], ["usage"]) || !object(raw.value)) return null
  const values = raw.value, ids = new Set(themed.map(theme => theme.id))
  if (!Object.hasOwn(values, themed[0].id) || !Object.keys(values).every(id => ids.has(id))) return null
  const specs: Record<string, ColorSpec> = {}
  for (const [id, value] of Object.entries(values)) {
    const parsed = spec(value, true)
    if (!parsed) return null
    specs[id] = parsed
  }
  if (raw.usage !== undefined && !text(raw.usage, 280)) return null
  return { specs, usage: (raw.usage as string | undefined) ?? null }
}

const MAX_ALIAS_DEPTH = 16

/** Resolves every colour in every theme; an unknown target, a cycle or a chain over 16 rejects the file. */
function resolveColors(raws: { name: string; specs: Record<string, ColorSpec> | ColorSpec; usage: string | null }[], themed: DesignTheme[] | null): DesignColor[] | null {
  const byName = new Map(raws.map(raw => [raw.name, raw]))
  const first = themed?.[0].id
  const specIn = (raw: typeof raws[number], theme: string | undefined): ColorSpec => {
    if ("hex" in raw.specs || "alias" in raw.specs) return raw.specs as ColorSpec
    const specs = raw.specs as Record<string, ColorSpec>
    return theme !== undefined && Object.hasOwn(specs, theme) ? specs[theme] : specs[first!]
  }
  const resolve = (name: string, theme: string | undefined): { hex: string; alias: string | null } | null => {
    let current = byName.get(name), alias: string | null = null
    for (let depth = 0; current && depth <= MAX_ALIAS_DEPTH; depth++) {
      const step = specIn(current, theme)
      if ("hex" in step) return { hex: step.hex, alias }
      alias ??= step.alias
      current = byName.get(step.alias)
    }
    return null
  }
  const result: DesignColor[] = []
  for (const raw of raws) {
    const values: Record<string, string> = {}, aliases: Record<string, string> = {}
    for (const theme of themed ?? [{ id: undefined }]) {
      const resolved = resolve(raw.name, theme.id)
      if (!resolved) return null
      if (theme.id !== undefined) { values[theme.id] = resolved.hex; if (resolved.alias) aliases[theme.id] = resolved.alias }
      else values[""] = resolved.hex
    }
    const value = themed ? values[first!] : values[""]
    result.push({ name: raw.name, value, values: themed ? values : {}, aliases, usage: raw.usage })
  }
  return result
}

function typeStyle(name: string, raw: unknown, grouped: boolean): DesignTypeStyle | null {
  if (!object(raw) || !only(raw, ["size"], grouped ? ["font_family", "weight", "line_height", "group"] : ["font_family", "weight", "line_height"])) return null
  const { size, font_family: family, weight, line_height: lineHeight, group } = raw
  if (group !== undefined && !text(group, 40)) return null
  if (!Number.isSafeInteger(size) || (size as number) < 1 || (size as number) > 512) return null
  if (family !== undefined && !(typeof family === "string" && /^[A-Za-z0-9 ,'"-]{1,120}$/.test(family))) return null
  if (weight !== undefined && !(Number.isSafeInteger(weight) && (weight as number) >= 100 && (weight as number) <= 900 && (weight as number) % 100 === 0)) return null
  if (lineHeight !== undefined && !(typeof lineHeight === "number" && Number.isFinite(lineHeight) && lineHeight >= 0.5 && lineHeight <= 4)) return null
  return { name, fontFamily: (family as string | undefined) ?? null, size: size as number, weight: (weight as number | undefined) ?? null, lineHeight: (lineHeight as number | undefined) ?? null, group: (group as string | undefined) ?? null }
}

function components(value: unknown): DesignComponent[] | null {
  if (value === undefined) return []
  if (!Array.isArray(value) || value.length > MAX_COMPONENTS) return null
  const names = new Set<string>(), result: DesignComponent[] = []
  for (const item of value) {
    if (!object(item) || !only(item, ["name"], ["description"]) || !text(item.name, 80) || names.has(item.name)) return null
    if (item.description !== undefined && !text(item.description, 280)) return null
    names.add(item.name)
    result.push({ name: item.name, description: (item.description as string | undefined) ?? null })
  }
  return result
}

/** Counts decoded object keys; fewer than the source spells means JSON.parse merged a duplicate. */
function keyCount(value: unknown): number {
  if (Array.isArray(value)) return value.reduce((sum: number, item) => sum + keyCount(item), 0)
  if (!object(value)) return 0
  return Object.values(value).reduce((sum: number, item) => sum + keyCount(item), Object.keys(value).length)
}

export function hasDesignSystemFile(files: readonly { path: string }[]): boolean {
  return files.some(file => file.path === DESIGN_SYSTEM_FILE)
}

function source(files: readonly { path: string; content: string }[]): string | null {
  const candidates = files.filter(file => file.path === DESIGN_SYSTEM_FILE)
  if (candidates.length !== 1 || new TextEncoder().encode(candidates[0].content).byteLength > DESIGN_SYSTEM_MAX_BYTES) return null
  return candidates[0].content
}

/** The file's declared `schema_version` (a positive integer), valid or not; null when there is none to read. */
export function designSystemSchemaVersion(files: readonly { path: string; content: string }[]): number | null {
  const raw = source(files)
  if (raw === null) return null
  try {
    const value: unknown = JSON.parse(raw)
    return object(value) && Number.isSafeInteger(value.schema_version) && (value.schema_version as number) > 0 ? value.schema_version as number : null
  } catch { return null }
}

/** Why a file is not shown. A newer schema is named as such: nothing in it is lost, this client cannot read it. */
export function designSystemUnreadable(schema: number | null): string {
  return schema !== null && schema > DESIGN_SYSTEM_SCHEMA
    ? `This Design System uses format version ${schema}, which this version of Crewship cannot show. The file is unchanged.`
    : "This Design System file is invalid or uses an unsupported format."
}

/** Fails closed: any unknown field, ambiguous key or non-literal value rejects the whole file. */
export function parseDesignSystem(files: readonly { path: string; content: string }[]): DesignSystem | null {
  const raw = source(files)
  if (raw === null) return null
  let value: unknown
  try { value = JSON.parse(raw) } catch { return null }
  if (!object(value)) return null
  const v2 = value.schema_version === 2
  if (!(v2 || value.schema_version === 1)) return null
  if (!only(value, v2 ? ["schema_version", "themes", "colors", "typography", "spacing", "radii"] : ["schema_version", "colors", "typography", "spacing", "radii"], ["name", "components"])) return null
  if (value.name !== undefined && !text(value.name, 80)) return null
  const spelled = [...raw.matchAll(/"(?:\\.|[^"\\])*"\s*:/g)].length
  if (spelled !== keyCount(value)) return null
  const themed = v2 ? themes(value.themes) : null
  if (v2 && !themed) return null
  const specs = group(value.colors, (name, raw) => { const parsed = colorSpec(raw, themed); return parsed && { name, ...parsed } })
  const colors = specs && resolveColors(specs, themed)
  const typography = group(value.typography, (name, raw) => typeStyle(name, raw, v2))
  const spacing = group(value.spacing, (name, raw) => pixels(raw, 0) ? { name, value: raw } : null)
  const radii = group(value.radii, (name, raw) => pixels(raw, 0) ? { name, value: raw } : null)
  const parts = components(value.components)
  if (!colors || !typography || !spacing || !radii || !parts) return null
  return { schemaVersion: v2 ? 2 : 1, name: (value.name as string | undefined) ?? null, themes: themed ?? [], colors, typography, spacing, radii, components: parts }
}
