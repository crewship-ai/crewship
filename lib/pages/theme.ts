/**
 * The Crewship palette — what every Page wears until a workspace admin picks
 * something else (Settings › General › Pages appearance). It is the product's
 * own dark theme: brand blue on the near-black ground, the same values as
 * app/globals.css `.dark`. A workspace that never customised stores `{}` and
 * so always follows this, including future changes to it. The Page starter's
 * CSS fallbacks (tools/pages-build/starter/src/style.css) repeat these values
 * for a Page built with no palette at all.
 */
export const DEFAULT_PAGE_THEME = {
  accent: "#1e7bfe", background: "#0a0b0e", surface: "#121318",
  text: "#ededf2", muted: "#9295a3", border: "#24262e",
} as const
export type PageTheme = { -readonly [K in keyof typeof DEFAULT_PAGE_THEME]: string }
export function normalizePageTheme(value: unknown): PageTheme {
  const result: PageTheme = { ...DEFAULT_PAGE_THEME }
  if (!value || typeof value !== "object") return result
  for (const key of Object.keys(result) as (keyof PageTheme)[]) {
    const color = (value as Record<string, unknown>)[key]
    if (typeof color === "string" && /^#[0-9a-f]{6}$/i.test(color)) result[key] = color
  }
  return result
}
/**
 * Starting palettes offered in Settings. The first is the default. Every one
 * keeps text and secondary text at WCAG AA (4.5:1) on background and cards
 * (lib/pages/__tests__/theme.test.ts).
 */
export const PAGE_THEME_PRESETS: { id: string; label: string; theme: PageTheme }[] = [
  { id: "crewship", label: "Crewship", theme: { ...DEFAULT_PAGE_THEME } },
  { id: "crewship-light", label: "Crewship light", theme: { accent: "#0e6be8", background: "#f7f8fa", surface: "#ffffff", text: "#0f1733", muted: "#566079", border: "#e3e7ee" } },
  { id: "midnight", label: "Midnight", theme: { accent: "#6ea8ff", background: "#0b1220", surface: "#141e33", text: "#e6edf7", muted: "#9fb0c8", border: "#27344f" } },
  { id: "paper", label: "Paper", theme: { accent: "#1d4ed8", background: "#fbfaf7", surface: "#ffffff", text: "#1c2430", muted: "#566072", border: "#e7e3da" } },
]

export function samePageTheme(a: PageTheme, b: PageTheme): boolean {
  return (Object.keys(DEFAULT_PAGE_THEME) as (keyof PageTheme)[]).every((k) => a[k].toLowerCase() === b[k].toLowerCase())
}

/** The preset a palette matches, or null for a custom one. */
export function pageThemePreset(theme: PageTheme): (typeof PAGE_THEME_PRESETS)[number] | null {
  return PAGE_THEME_PRESETS.find((p) => samePageTheme(p.theme, theme)) ?? null
}

export function colorContrast(a: string, b: string) {
  const luminance = (hex: string) => {
    const rgb = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255).map(v => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4)
    return rgb[0] * .2126 + rgb[1] * .7152 + rgb[2] * .0722
  }
  const x = luminance(a), y = luminance(b)
  return (Math.max(x, y) + .05) / (Math.min(x, y) + .05)
}

const relativeLuminance = (hex: string) => {
  const rgb = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255).map(v => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4)
  return rgb[0] * .2126 + rgb[1] * .7152 + rgb[2] * .0722
}

/**
 * The CSS custom properties a Page application receives for a palette —
 * the same ones tools/pages-build/sdk.ts applyTheme sets on <html>, including
 * the black-or-white text colour on the accent. The settings preview paints
 * from this, so what an admin previews is what a Page renders.
 */
export function pageThemeVars(theme: PageTheme): Record<`--crewship-page-${keyof PageTheme | "on-accent"}`, string> & { colorScheme: "light" | "dark" } {
  const t = normalizePageTheme(theme)
  return {
    "--crewship-page-accent": t.accent,
    "--crewship-page-background": t.background,
    "--crewship-page-surface": t.surface,
    "--crewship-page-text": t.text,
    "--crewship-page-muted": t.muted,
    "--crewship-page-border": t.border,
    "--crewship-page-on-accent": relativeLuminance(t.accent) > .179 ? "#000000" : "#ffffff",
    // sdk.ts uses the plain (non-linearised) weighting for the scheme.
    colorScheme: [1, 3, 5].map(i => parseInt(t.background.slice(i, i + 2), 16) / 255).reduce((sum, v, i) => sum + v * [.2126, .7152, .0722][i], 0) > .5 ? "light" : "dark",
  }
}
