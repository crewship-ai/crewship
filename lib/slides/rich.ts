import iconSet from "@/internal/slides/lucide_icons.json"

// Schema-3 (artifact rich profile) constants mirrored from internal/slides/rich.go.
// The faces are bundled under public/fonts and declared in slide-fonts.css; the
// icon geometry is the same committed file the Go PDF renderer embeds.
export const SLIDES_RICH_FONTS: Readonly<Record<string, readonly number[]>> = {
  "Go Regular": [400],
  "IBM Plex Sans": [400, 600, 700],
  "JetBrains Mono": [400, 600],
}
/** CSS fallback for the schema-3 faces while the bundled file loads (Go Regular stays as in schema 1). */
export const SLIDES_FONT_STACK: Readonly<Record<string, string>> = {
  "IBM Plex Sans": '"IBM Plex Sans", sans-serif',
  "JetBrains Mono": '"JetBrains Mono", monospace',
}
export const SLIDES_MAX_FONT_SIZE = 160
export const SLIDES_MAX_RICH_FONT_SIZE = 320
export const SLIDES_LETTER_SPACING = [-20, 100] as const
export const SLIDES_LINE_HEIGHT = [80, 300] as const
export const SLIDES_MAX_GRADIENT_STOPS = 8

export type IconNode = readonly [string, Readonly<Record<string, string>>]
const ICONS = (iconSet as unknown as { icons: Record<string, IconNode[]> }).icons
export const SLIDES_ICON_STROKE = (iconSet as { stroke_width: number }).stroke_width

export function richFontAllowed(family: unknown, weight: unknown = 400): boolean {
  return typeof family === "string" && Object.hasOwn(SLIDES_RICH_FONTS, family) && SLIDES_RICH_FONTS[family].includes(weight as number)
}
export function slideIconNodes(name: unknown): readonly IconNode[] | undefined {
  return typeof name === "string" && Object.hasOwn(ICONS, name) ? ICONS[name] : undefined
}

/** CSS linear-gradient geometry over a w×h box: 0deg points up, clockwise. */
export function gradientLine(angle: number, width: number, height: number) {
  const a = angle * Math.PI / 180, dx = Math.sin(a), dy = -Math.cos(a)
  const length = Math.abs(width * dx) + Math.abs(height * dy)
  return { x1: width / 2 - dx * length / 2, y1: height / 2 - dy * length / 2, x2: width / 2 + dx * length / 2, y2: height / 2 + dy * length / 2 }
}

/** Same arrowhead as the PDF renderer: length 3w+4, half width 1.5w+2. */
export function arrowhead(x1: number, y1: number, x2: number, y2: number, w: number) {
  const d = Math.hypot(x2 - x1, y2 - y1), ux = (x2 - x1) / d, uy = (y2 - y1) / d
  const length = Math.min(3 * w + 4, d), half = 1.5 * w + 2
  const bx = x2 - ux * length, by = y2 - uy * length
  return { points: `${x2},${y2} ${bx - uy * half},${by + ux * half} ${bx + uy * half},${by - ux * half}`, bx, by }
}
