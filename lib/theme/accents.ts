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
  { id: "indigo", label: "Indigo", swatch: "#6366F1" },
  { id: "violet", label: "Violet", swatch: "#8B5CF6" },
  { id: "teal", label: "Teal", swatch: "#0EA5C4" },
  { id: "graphite", label: "Graphite", swatch: "#64748B" },
] as const

export type AccentId = (typeof ACCENTS)[number]["id"]

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

export function isAccentId(value: unknown): value is AccentId {
  return typeof value === "string" && IDS.includes(value)
}

/** Paint an accent now and remember it. The default is written too, so an
 *  explicit "Blue" survives a future change of default. */
export function applyAccent(id: AccentId): void {
  document.documentElement.dataset.accent = id
  try {
    localStorage.setItem(ACCENT_STORAGE_KEY, id)
  } catch {
    // Private mode or blocked storage: the choice lasts for this page only.
  }
}

/** The stored accent, or the default when nothing valid is stored. */
export function readStoredAccent(): AccentId {
  try {
    const stored = localStorage.getItem(ACCENT_STORAGE_KEY)
    return isAccentId(stored) ? stored : DEFAULT_ACCENT
  } catch {
    return DEFAULT_ACCENT
  }
}

/**
 * Inline in <head> (app/layout.tsx) so the stored accent is on <html> before
 * first paint — otherwise every load flashes Blue. Only a registered id is
 * ever written to the attribute.
 */
export const ACCENT_BOOT_SCRIPT = `(function(){try{var a=localStorage.getItem(${scriptJSON(
  ACCENT_STORAGE_KEY,
)});if(${scriptJSON(IDS)}.indexOf(a)>-1)document.documentElement.setAttribute("data-accent",a)}catch(e){}})()`

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
export function accentBlock(css: string, id: AccentId, mode: "light" | "dark"): Map<string, string> {
  const selector = mode === "dark" ? `:root.dark[data-accent="${id}"]` : `:root[data-accent="${id}"]`
  const bare = css.replace(/\/\*[\s\S]*?\*\//g, "")
  const blocks = [...bare.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
  const found = blocks.find(([, selectors]) => selectors.split(",").map((s) => s.trim()).includes(selector))
  const tokens = new Map<string, string>()
  if (!found) return tokens
  for (const m of found[2].matchAll(/--([a-z-]+):\s*([^;]+);/g)) tokens.set(m[1], m[2].trim())
  return tokens
}
