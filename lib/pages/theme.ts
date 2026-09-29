/** Shared appearance is optional: custom Pages may use or override these tokens. */
export const DEFAULT_PAGE_THEME = {
  accent: "#9fe5bd", background: "#0c1410", surface: "#121c16",
  text: "#edf5ef", muted: "#a3b6aa", border: "#2a3b30",
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
