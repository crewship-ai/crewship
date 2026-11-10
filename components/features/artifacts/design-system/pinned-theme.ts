import type { SlidePreviewTheme } from "@/lib/slides/model"
import { DESIGN_SYSTEM_FILE, parseDesignSystem } from "./manifest"

/*
 * A consumer project renders from its pinned Library copy (#5248), never from
 * the live Library. The CSS is derived here from the pinned design-system.json
 * and the pinned font bytes each time; it is never stored. Every value comes
 * from a closed literal (hex colour, integer pixel, plain family list) or a
 * strict base64 body, so nothing can close the style element or load from the
 * network.
 */

export interface PinnedFile { path: string; media_type: string; status: "copied" | "licence" | "too_large"; family: string; weight: number; style: string; content: string }

const FONT_FORMAT: Record<string, string> = { "font/woff2": "woff2", "font/woff": "woff", "font/ttf": "truetype", "font/otf": "opentype" }
const FAMILY = /^[A-Za-z0-9][A-Za-z0-9 _-]{0,63}$/
const BASE64 = /^[A-Za-z0-9+/]*={0,2}$/
const STATUS = new Set(["copied", "licence", "too_large"])

export function parsePinnedFiles(raw: unknown): PinnedFile[] | null {
  if (typeof raw !== "object" || raw === null || !Array.isArray((raw as { files?: unknown }).files)) return null
  const out: PinnedFile[] = []
  for (const item of (raw as { files: unknown[] }).files) {
    if (typeof item !== "object" || item === null) return null
    const f = item as Record<string, unknown>
    if (typeof f.path !== "string" || typeof f.media_type !== "string" || typeof f.status !== "string" || !STATUS.has(f.status) || typeof f.content !== "string") return null
    out.push({ path: f.path, media_type: f.media_type, status: f.status as PinnedFile["status"], family: typeof f.family === "string" ? f.family : "", weight: Number.isSafeInteger(f.weight) ? f.weight as number : 0, style: typeof f.style === "string" ? f.style : "", content: f.content })
  }
  return out
}

/** The copied fonts that declare a family, with a source that cannot escape CSS. */
function faces(files: readonly PinnedFile[]) {
  return files.flatMap(file => {
    const format = FONT_FORMAT[file.media_type]
    if (file.status !== "copied" || !format || !FAMILY.test(file.family) || !file.content || !BASE64.test(file.content)) return []
    const weight = Number.isSafeInteger(file.weight) && file.weight >= 100 && file.weight <= 900 && file.weight % 100 === 0 ? String(file.weight) : "400"
    const style = file.style === "italic" ? "italic" : "normal"
    return [{ family: file.family, source: `url(data:${file.media_type};base64,${file.content})`, format, weight, style }]
  })
}

/**
 * Custom properties for the pinned system's default theme, plus a face per
 * copied font: `--ds-color-<name>`, `--ds-space-<name>`, `--ds-radius-<name>`,
 * `--ds-font-<name>` and `--ds-font-size-<name>`. Null when the copy cannot be
 * read (it never renders part of a file).
 */
export function pinnedThemeCss(tokens: string, files: readonly PinnedFile[]): string | null {
  const system = parseDesignSystem([{ path: DESIGN_SYSTEM_FILE, content: tokens }])
  if (!system) return null
  const vars: string[] = []
  for (const color of system.colors) vars.push(`--ds-color-${color.name}:${color.value};`)
  for (const space of system.spacing) vars.push(`--ds-space-${space.name}:${space.value}px;`)
  for (const radius of system.radii) vars.push(`--ds-radius-${radius.name}:${radius.value}px;`)
  for (const style of system.typography) {
    if (style.fontFamily) vars.push(`--ds-font-${style.name}:${style.fontFamily};`)
    vars.push(`--ds-font-size-${style.name}:${style.size}px;`)
  }
  const fontFaces = faces(files).map(face => `@font-face{font-family:"${face.family}";src:${face.source} format("${face.format}");font-weight:${face.weight};font-style:${face.style};font-display:swap}`)
  const css = `${fontFaces.join("")}:root{${vars.join("")}}`
  // Defence in depth: nothing derived above can contain markup.
  return /[<>]/.test(css) ? null : css
}

/** Put the style before authored styles, after an actual leading doctype. */
export function withPinnedTheme(html: string, css: string | null): string {
  if (!css || /[<>]/.test(css)) return html
  const style = `<style data-library-pin>${css}</style>`
  // A style before <html> opens the implicit head. Keep a real doctype
  // ahead of it so it doesn't put the preview into quirks mode. Commented
  // tags are never treated as an insertion point.
  const doctype = /^(?:\s|<!--[\s\S]*?-->)*<!doctype\s[^>]*>/i.exec(html)
  const index = doctype?.[0].length ?? 0
  return html.slice(0, index) + style + html.slice(index)
}

export const SLIDES_LIBRARY_FILE = "slides-library.json"

/** Resolve the author's optional preview bindings against the current pin. */
export function pinnedSlideTheme(tokens: string | undefined, files: readonly { path: string; content: string }[]): SlidePreviewTheme | undefined {
  const bindings = files.filter(file => file.path === SLIDES_LIBRARY_FILE)
  if (!tokens || bindings.length !== 1 || bindings[0].content.length > 1024) return undefined
  const system = parseDesignSystem([{ path: DESIGN_SYSTEM_FILE, content: tokens }])
  if (!system) return undefined
  let raw: unknown
  try { raw = JSON.parse(bindings[0].content) } catch { return undefined }
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return undefined
  const config = raw as Record<string, unknown>
  if (config.schema_version !== 1 || Object.keys(config).some(key => !["schema_version", "background", "text_color", "typography"].includes(key))) return undefined
  for (const key of ["background", "text_color", "typography"]) {
    if (Object.hasOwn(config, key) && (typeof config[key] !== "string" || !/^[a-z][a-z0-9-]{0,47}$/.test(config[key]))) return undefined
  }
  const background = system.colors.find(token => token.name === config.background)?.value
  const textColor = system.colors.find(token => token.name === config.text_color)?.value
  const style = system.typography.find(token => token.name === config.typography)
  if (!background && !textColor && !style) return undefined
  const family = style?.fontFamily?.split(",")[0].trim()
  return { background, textColor, typography: style ? { size: style.size, family: family && FAMILY.test(family) ? family : undefined, weight: style.weight ?? undefined } : undefined }
}

export interface SlideFontFace { family: string; alias: string; source: string; weight: string; style: string }

/**
 * Pinned faces for a Slides deck, registered in the app document under an
 * alias per deck so a pinned "Inter" never replaces the app's own fonts or
 * another deck's.
 */
export function slideFontFaces(files: readonly PinnedFile[], deck: string): SlideFontFace[] {
  // Hex encodes segment boundaries and distinctions such as "A B"/"A-B".
  const encode = (value: string) => Array.from(value, char => char.codePointAt(0)!.toString(16).padStart(6, "0")).join("")
  const scope = encode(deck)
  return faces(files).map(face => ({ family: face.family, alias: `crewship-pin-${scope}-${encode(face.family)}`, source: face.source, weight: face.weight, style: face.style }))
}
