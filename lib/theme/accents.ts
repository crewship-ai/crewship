/**
 * Accent themes — the one brand colour the whole UI is tinted with.
 *
 * Where things live:
 *   - app/styles/accents.css  the colours: one light and one dark block per
 *                             accent, each defining every BRAND_TOKENS entry.
 *   - this file               the list the picker offers, the storage key, and
 *                             the pre-paint boot script.
 *   - app/globals.css         maps the brand tokens onto the app tokens
 *                             (--primary, --ring, selection, blue chips …).
 *                             Nothing else in the app names an accent colour.
 *
 * To add an accent: add a light and a dark block to accents.css and a row
 * here. lib/theme/__tests__/accents.test.ts checks the two agree and
 * lib/__tests__/theme-contrast.test.ts holds every accent to WCAG AA.
 *
 * Status hues (green, red, amber) are never accents: they already mean done,
 * failed and needs-attention, and a brand in the same hue would blur that.
 */

export const ACCENTS = [
  { id: "blue", label: "Blue", swatch: "#1E7BFE" },
  { id: "sky", label: "Sky", swatch: "#38BDF8" },
  { id: "teal", label: "Teal", swatch: "#0EA5C4" },
  { id: "indigo", label: "Indigo", swatch: "#6366F1" },
  { id: "violet", label: "Violet", swatch: "#8B5CF6" },
  { id: "fuchsia", label: "Fuchsia", swatch: "#D946EF" },
  { id: "pink", label: "Pink", swatch: "#EC4899" },
  { id: "graphite", label: "Graphite", swatch: "#64748B" },
] as const

export type AccentId = (typeof ACCENTS)[number]["id"]

/**
 * Style accents — the accent a style brings with it (lib/theme/appearance-style.ts).
 * Each has light and dark blocks in accents.css like the picker's accents, and
 * passes the same contrast gate, but none is offered in the picker: they are
 * hidden, reachable only by choosing the style that carries them. Ids are
 * neutral colour names.
 */
export const STYLE_ACCENTS = [
  { id: "clay", swatch: "#AF5739" },
  { id: "ink", swatch: "#0D0D0D" },
  { id: "periwinkle", swatch: "#5B67CC" },
  { id: "amethyst", swatch: "#6E49CB" },
  { id: "ultramarine", swatch: "#4160FA" },
  { id: "sapphire", swatch: "#0051C3" },
  { id: "ash", swatch: "#2A2A2A" },
  { id: "jet", swatch: "#000000" },
  { id: "cornflower", swatch: "#1E72C5" },
  { id: "azure", swatch: "#0073BF" },
  { id: "rose", swatch: "#C63B69" },
  { id: "lupine", swatch: "#6058F7" },
  { id: "emerald", swatch: "#007A5A" },
  { id: "jade", swatch: "#1F8054" },
  { id: "bluejay", swatch: "#0969DA" },
  { id: "grape", swatch: "#8B3DFF" },
  { id: "lavender", swatch: "#6C5FC7" },
  { id: "salmon", swatch: "#BD4949" },
  { id: "peacock", swatch: "#1F7B87" },
  { id: "flame", swatch: "#C8420C" },
  { id: "electric", swatch: "#0068F1" },
  { id: "vermilion", swatch: "#C14921" },
  { id: "hyacinth", swatch: "#6B53F7" },
  { id: "azurite", swatch: "#2469E6" },
  { id: "heliotrope", swatch: "#7356ED" },
  { id: "byzantium", swatch: "#6856F2" },
  { id: "capri", swatch: "#0A73BF" },
  { id: "ultraviolet", swatch: "#6E3FF3" },
  { id: "blurple", swatch: "#5461E8" },
  { id: "blaze", swatch: "#C74300" },
  { id: "royal", swatch: "#632CA6" },
  { id: "steel", swatch: "#3B6CD0" },
  { id: "marine", swatch: "#0C66E4" },
  { id: "dodger", swatch: "#006DE3" },
  { id: "mauve", swatch: "#6750A4" },
  { id: "horizon", swatch: "#005FB8" },
  { id: "bluebird", swatch: "#2D70C2" },
  { id: "persimmon", swatch: "#C4471B" },
  { id: "aqua", swatch: "#29769E" },
  { id: "cadet", swatch: "#0F6CBD" },
  { id: "cinnabar", swatch: "#C94000" },
] as const

export type StyleAccentId = (typeof STYLE_ACCENTS)[number]["id"]

export const DEFAULT_ACCENT: AccentId = "blue"

/** localStorage key; per browser, like the light/dark choice. */
export const ACCENT_STORAGE_KEY = "crewship-accent"

/** Every custom property an accent block must define (without the `--`). */
export const BRAND_TOKENS = [
  "brand", // fills, focus ring, selection edge; also text on the ground
  "brand-foreground", // text on a --brand fill
  "brand-ink", // links and brand text on tints (--primary-hover)
  "brand-strong", // filled primary button, holds white text at AA
  "brand-strong-hover",
  "brand-glow", // the primary button's coloured shadow
  "brand-tint", // opaque fill behind brand-ink text (blue chips)
] as const

const IDS: readonly string[] = ACCENTS.map((a) => a.id)
const STYLE_IDS: readonly string[] = STYLE_ACCENTS.map((a) => a.id)

export function isAccentId(value: unknown): value is AccentId {
  return typeof value === "string" && IDS.includes(value)
}

/**
 * The workspace's own brand colour (Settings › General › Pages appearance ›
 * Brand accent) as the accent. Not a preset: it has no block in accents.css.
 * Its tokens are derived from the admin's colour at runtime
 * (lib/theme/derive-accent.ts) and painted by a <style> element that
 * components/layout/workspace-accent.tsx keeps current. While no workspace
 * colour is known, data-accent="workspace" matches no block and the bare
 * :root palette — Blue — shows.
 */
export const WORKSPACE_ACCENT = "workspace"
export type AccentChoice = AccentId | StyleAccentId | typeof WORKSPACE_ACCENT

export function isAccentChoice(value: unknown): value is AccentChoice {
  return value === WORKSPACE_ACCENT || isAccentId(value) || (typeof value === "string" && STYLE_IDS.includes(value))
}

/** localStorage copy of the last derived workspace stylesheet, so the boot
 *  script can paint it before the workspace list has loaded. */
export const WORKSPACE_ACCENT_CSS_KEY = "crewship-accent-workspace-css"
export const WORKSPACE_ACCENT_STYLE_ID = "crewship-workspace-accent"

/** Paint an accent now and remember it. The default is written too, so an
 *  explicit "Blue" survives a future change of default. */
export function applyAccent(id: AccentChoice): void {
  document.documentElement.dataset.accent = id
  try {
    localStorage.setItem(ACCENT_STORAGE_KEY, id)
  } catch {
    // Private mode or blocked storage: the choice lasts for this page only.
  }
}

/** The stored accent, or the default when nothing valid is stored. */
export function readStoredAccent(): AccentChoice {
  try {
    const stored = localStorage.getItem(ACCENT_STORAGE_KEY)
    return isAccentChoice(stored) ? stored : DEFAULT_ACCENT
  } catch {
    return DEFAULT_ACCENT
  }
}

/**
 * A cached workspace stylesheet is only ever our own output — two rules of
 * custom-property declarations — and is checked to be exactly that before
 * the boot script injects it: nothing that can load a URL or nest a rule.
 */
export const WORKSPACE_ACCENT_CSS_PATTERN = /^:root\[data-accent="workspace"\]\{(--brand[a-z-]*:(#[0-9A-F]{6}|rgb\(\d{1,3} \d{1,3} \d{1,3} \/ [0-9.]{1,4}\));){7}\}:root\.dark\[data-accent="workspace"\]\{(--brand[a-z-]*:(#[0-9A-F]{6}|rgb\(\d{1,3} \d{1,3} \d{1,3} \/ [0-9.]{1,4}\));){7}\}$/

/**
 * Inline in <head> (app/layout.tsx) so the stored accent is on <html> before
 * first paint — otherwise every load flashes Blue. Only a registered id is
 * ever written to the attribute; "workspace" also re-injects its cached,
 * pattern-checked stylesheet.
 */
export const ACCENT_BOOT_SCRIPT = `(function(){try{var s=localStorage,a=s.getItem(${scriptJSON(
  ACCENT_STORAGE_KEY,
)});if(a===${scriptJSON(WORKSPACE_ACCENT)}){var c=s.getItem(${scriptJSON(WORKSPACE_ACCENT_CSS_KEY)});if(c&&${WORKSPACE_ACCENT_CSS_PATTERN.toString()}.test(c)){var e=document.createElement("style");e.id=${scriptJSON(
  WORKSPACE_ACCENT_STYLE_ID,
)};e.textContent=c;document.head.appendChild(e)}document.documentElement.setAttribute("data-accent",a)}else if(${scriptJSON([...IDS, ...STYLE_IDS])}.indexOf(a)>-1)document.documentElement.setAttribute("data-accent",a)}catch(e){}})()`

/** JSON that stays inert inside an inline <script>: no "</script>", no line
 *  separators that end a JS string. */
function scriptJSON(value: unknown): string {
  const escapes: Record<string, string> = { "<": "\\u003c", ">": "\\u003e", "/": "\\u002f", "\u2028": "\\u2028", "\u2029": "\\u2029" }
  return JSON.stringify(value).replace(/[<>/\u2028\u2029]/g, (c) => escapes[c])
}

/**
 * Test helper: the brand tokens one accent defines for one mode, parsed from
 * accents.css text. Selectors are written in exactly this form there.
 */
export function accentBlock(css: string, id: AccentId | StyleAccentId, mode: "light" | "dark"): Map<string, string> {
  const selector = mode === "dark" ? `:root.dark[data-accent="${id}"]` : `:root[data-accent="${id}"]`
  const bare = css.replace(/\/\*[\s\S]*?\*\//g, "")
  const blocks = [...bare.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
  const found = blocks.find(([, selectors]) => selectors.split(",").map((s) => s.trim()).includes(selector))
  const tokens = new Map<string, string>()
  if (!found) return tokens
  for (const m of found[2].matchAll(/--([a-z-]+):\s*([^;]+);/g)) tokens.set(m[1], m[2].trim())
  return tokens
}
