/**
 * Base palettes — the neutral ground the accent sits on: page background,
 * cards, sidebar, borders and muted text. The accent tints; the base is
 * everything else.
 *
 * Where things live (same split as the accents):
 *   - app/styles/bases.css     the colours: one light and one dark block per
 *                              non-default base, each defining every
 *                              BASE_TOKENS entry. Cool is globals.css itself.
 *   - this file                the list the picker offers, the storage key and
 *                              the <html> attribute.
 *   - lib/theme/appearance.ts  the store, sync and the pre-paint boot script.
 *
 * lib/__tests__/theme-base-contrast.test.ts holds every base × accent pair to
 * the same WCAG AA floors as theme-contrast.test.ts holds the default ground,
 * and checks every base block defines every token.
 */

export const BASES = [
  // Swatches are the picker's preview: light ground/card, dark ground/card.
  { id: "cool", label: "Cool", description: "Blue-tinted greys", light: ["#F4F7FB", "#FFFFFF"], dark: ["#0A0B0E", "#121318"] },
  { id: "neutral", label: "Neutral", description: "Pure greys", light: ["#F7F7F8", "#FFFFFF"], dark: ["#0B0B0C", "#141416"] },
  { id: "warm", label: "Warm", description: "Stone and paper", light: ["#F7F5F2", "#FFFFFF"], dark: ["#0D0B0A", "#161412"] },
  { id: "black", label: "Black", description: "True black in dark mode", light: ["#FAFAFA", "#FFFFFF"], dark: ["#000000", "#0B0B0C"] },
] as const

export type BaseId = (typeof BASES)[number]["id"]

export const DEFAULT_BASE: BaseId = "cool"

/** localStorage key; per browser, like the accent. */
export const BASE_STORAGE_KEY = "crewship-base"

/** Every custom property a base block must define (without the `--`). */
export const BASE_TOKENS = [
  "background",
  "foreground",
  "card",
  "card-foreground",
  "popover",
  "popover-foreground",
  "muted",
  "muted-foreground",
  "muted-foreground-soft",
  "accent",
  "accent-foreground",
  "border",
  "input",
  "control-border",
  "sidebar",
  "sidebar-foreground",
  "sidebar-accent",
  "sidebar-border",
  "surface-subtle",
  "surface-raised",
  "scrollbar-thumb",
  "scrollbar-thumb-hover",
] as const

const IDS: readonly string[] = BASES.map((b) => b.id)

export function isBaseId(value: unknown): value is BaseId {
  return typeof value === "string" && IDS.includes(value)
}

/** Cool is the absence of the attribute: globals.css already is Cool. */
export function applyBaseAttr(id: BaseId): void {
  if (id === DEFAULT_BASE) delete document.documentElement.dataset.base
  else document.documentElement.dataset.base = id
}

/**
 * Test helper: the tokens one base defines for one mode, parsed from
 * bases.css text. Selectors are written in exactly this form there.
 */
export function baseBlock(css: string, id: BaseId, mode: "light" | "dark"): Map<string, string> {
  const selector = mode === "dark" ? `:root.dark[data-base="${id}"]` : `:root[data-base="${id}"]`
  const bare = css.replace(/\/\*[\s\S]*?\*\//g, "")
  const blocks = [...bare.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
  const found = blocks.find(([, selectors]) => selectors.split(",").map((s) => s.trim()).includes(selector))
  const tokens = new Map<string, string>()
  if (!found) return tokens
  for (const m of found[2].matchAll(/--([a-z-]+):\s*([^;]+);/g)) tokens.set(m[1], m[2].trim())
  return tokens
}
