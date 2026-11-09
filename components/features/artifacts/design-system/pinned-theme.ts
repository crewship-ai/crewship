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

/** The screen with the pinned theme as the first style in its head, so the project's own CSS still wins. */
export function withPinnedTheme(html: string, css: string | null): string {
  if (!css) return html
  const style = `<style data-library-pin>${css}</style>`
  const head = /<head(\s[^>]*)?>/i.exec(html)
  return head ? html.slice(0, head.index + head[0].length) + style + html.slice(head.index + head[0].length) : style + html
}

export interface SlideFontFace { family: string; alias: string; source: string; weight: string; style: string }

/**
 * Pinned faces for a Slides deck, registered in the app document under an
 * alias per deck so a pinned "Inter" never replaces the app's own fonts or
 * another deck's.
 */
export function slideFontFaces(files: readonly PinnedFile[], deck: string): SlideFontFace[] {
  const scope = deck.replace(/[^A-Za-z0-9_-]/g, "").slice(0, 64)
  return faces(files).map(face => ({ family: face.family, alias: `crewship-pin-${scope}-${face.family.replace(/ /g, "-")}`, source: face.source, weight: face.weight, style: face.style }))
}
