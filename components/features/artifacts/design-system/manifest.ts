/**
 * Design System artifact manifest: a project artifact whose file manifest holds
 * `design-system.json`. Storage, versions, restore and sharing are the existing
 * artifact VersionStore; this module only admits the token file for a
 * read-only preview. Every value is a closed literal (hex color, integer pixel,
 * plain font-family list) so nothing from the file can become a CSS expression,
 * URL or markup when rendered.
 */

export const DESIGN_SYSTEM_FILE = "design-system.json"
export const DESIGN_SYSTEM_MAX_BYTES = 32 * 1024
export const DESIGN_SYSTEM_MAX_TOKENS = 128
export const DESIGN_SYSTEM_MAX_PX = 4096
const MAX_COMPONENTS = 64

export interface DesignToken<T> { name: string; value: T }
export interface DesignTypeStyle { name: string; fontFamily: string | null; size: number; weight: number | null; lineHeight: number | null }
export interface DesignComponent { name: string; description: string | null }
export interface DesignSystem {
  name: string | null
  colors: DesignToken<string>[]
  typography: DesignTypeStyle[]
  spacing: DesignToken<number>[]
  radii: DesignToken<number>[]
  components: DesignComponent[]
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

function typeStyle(name: string, raw: unknown): DesignTypeStyle | null {
  if (!object(raw) || !only(raw, ["size"], ["font_family", "weight", "line_height"])) return null
  const { size, font_family: family, weight, line_height: lineHeight } = raw
  if (!Number.isSafeInteger(size) || (size as number) < 1 || (size as number) > 512) return null
  if (family !== undefined && !(typeof family === "string" && /^[A-Za-z0-9 ,'"-]{1,120}$/.test(family))) return null
  if (weight !== undefined && !(Number.isSafeInteger(weight) && (weight as number) >= 100 && (weight as number) <= 900 && (weight as number) % 100 === 0)) return null
  if (lineHeight !== undefined && !(typeof lineHeight === "number" && Number.isFinite(lineHeight) && lineHeight >= 0.5 && lineHeight <= 4)) return null
  return { name, fontFamily: (family as string | undefined) ?? null, size: size as number, weight: (weight as number | undefined) ?? null, lineHeight: (lineHeight as number | undefined) ?? null }
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

/** Fails closed: any unknown field, ambiguous key or non-literal value rejects the whole file. */
export function parseDesignSystem(files: readonly { path: string; content: string }[]): DesignSystem | null {
  const candidates = files.filter(file => file.path === DESIGN_SYSTEM_FILE)
  if (candidates.length !== 1) return null
  const source = candidates[0].content
  if (new TextEncoder().encode(source).byteLength > DESIGN_SYSTEM_MAX_BYTES) return null
  let value: unknown
  try { value = JSON.parse(source) } catch { return null }
  if (!object(value) || value.schema_version !== 1 || !only(value, ["schema_version", "colors", "typography", "spacing", "radii"], ["name", "components"])) return null
  if (value.name !== undefined && !text(value.name, 80)) return null
  const spelled = [...source.matchAll(/"(?:\\.|[^"\\])*"\s*:/g)].length
  if (spelled !== keyCount(value)) return null
  const colors = group(value.colors, (name, raw) => typeof raw === "string" && /^#[0-9a-fA-F]{6}$/.test(raw) ? { name, value: raw.toLowerCase() } : null)
  const typography = group(value.typography, typeStyle)
  const spacing = group(value.spacing, (name, raw) => pixels(raw, 0) ? { name, value: raw } : null)
  const radii = group(value.radii, (name, raw) => pixels(raw, 0) ? { name, value: raw } : null)
  const parts = components(value.components)
  if (!colors || !typography || !spacing || !radii || !parts) return null
  return { name: (value.name as string | undefined) ?? null, colors, typography, spacing, radii, components: parts }
}
