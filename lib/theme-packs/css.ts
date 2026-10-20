import type { PackFont, ThemePack } from "./types"

// The stylesheet for the theme packs, generated from the pack files into
// app/styles/packs.generated.css (a test keeps the two in step; regenerate with
// UPDATE_PACKS_CSS=1 pnpm vitest run lib/theme-packs). It is a static file
// because the app's CSP allows stylesheets from 'self' only.

const LATIN = "U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+0304,U+0308,U+0329,U+2000-206F,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD"
const LATIN_EXT =
  "U+0100-02BA,U+02BD-02C5,U+02C7-02CC,U+02CE-02D7,U+02DD-02FF,U+0304,U+0308,U+0329,U+1D00-1DBF,U+1E00-1E9F,U+1EF2-1EFF,U+2020,U+20A0-20AB,U+20AD-20C0,U+2113,U+2C60-2C7F,U+A720-A7FF"

function fontFaces(font: PackFont): string {
  return font.faces
    .flatMap((f) => [
      [f.latin, LATIN] as const,
      ...(f.latinExt ? [[f.latinExt, LATIN_EXT] as const] : []),
    ].map(([file, range]) => `@font-face {\n  font-family: "${font.family}";\n  font-style: normal;\n  font-weight: ${f.weight};\n  font-display: swap;\n  src: url(${file}) format("woff2");\n  unicode-range: ${range};\n}`))
    .join("\n")
}

const font0 = (p: ThemePack) => p.displayFont!.faces[0].weight
const fontLast = (p: ThemePack) => p.displayFont!.faces[p.displayFont!.faces.length - 1].weight
const stack = (font: PackFont) => `"${font.family}", ${font.fallback}`
const decls = (tokens: Record<string, string>) => Object.entries(tokens).map(([k, v]) => `  --${k}: ${v};`).join("\n")

function packBlock(p: ThemePack): string {
  const sel = `[data-style="${p.id}"]`
  const out: string[] = [`/* ${p.name} (${p.category}) */`]
  if (p.textFont) out.push(fontFaces(p.textFont))
  if (p.displayFont) out.push(fontFaces(p.displayFont))
  if (p.schemes.includes("light") && p.tokens.light) out.push(`${sel}:not(.dark) {\n${decls(p.tokens.light)}\n}`)
  if (p.schemes.includes("dark") && p.tokens.dark) out.push(`.dark${sel} {\n${decls(p.tokens.dark)}\n}`)

  out.push(`${sel} {\n  --radius-k: ${p.radiusK};\n  --pack-tilt: ${p.motion.postcardTilt}deg;\n}`)
  // Fonts are set on the root with one more class of specificity, because the base --font-sans
  // is declared after the stylesheet imports and would win a tie.
  const fonts: string[] = []
  if (p.textFont) fonts.push(`  --font-sans: ${stack(p.textFont)};`)
  if (p.displayFont) fonts.push(`  --font-display: ${stack(p.displayFont)};`)
  if (fonts.length) out.push(`:root${sel} {\n${fonts.join("\n")}\n}`)

  if (p.displayFont) {
    out.push(
      `/* Page titles and big numbers only; everything else keeps the text face. */\n` +
        `${sel} :is(h1[class*="text-title"], h1.text-display, [data-slot="settings-page-head"] :is(h1, h2), [data-slot="drill-head"] h2, .type-title, .type-page-metric, .pack-display) {\n  font-family: var(--font-display);\n  font-weight: ${font0(p)};\n  text-transform: uppercase;\n  letter-spacing: 0.01em;\n}\n` +
        `${sel} :is(h1.text-display, .type-page-metric, .pack-display) {\n  font-weight: ${fontLast(p)};\n  letter-spacing: 0;\n}`,
    )
  }
  if (p.motion.postcardTilt !== 0) {
    out.push(
      `/* Overview cards only. Rows in a list never wear .pack-postcard. */\n` +
        `${sel} .pack-postcard:nth-child(odd) {\n  transform: rotate(calc(var(--pack-tilt) * -1));\n}\n` +
        `${sel} .pack-postcard:nth-child(even) {\n  transform: rotate(var(--pack-tilt));\n}`,
    )
  }
  out.push(`${sel} .pack-glow {\n  box-shadow: var(--pack-glow);\n}`)
  if (p.motion.glowPulse) {
    out.push(
      `@keyframes pack-glow-pulse-${p.id} {\n  0%, 100% { box-shadow: var(--pack-glow); }\n  50% { box-shadow: 0 0 0 1px rgb(255 95 162 / 0.25), 0 0 8px rgb(255 95 162 / 0.12); }\n}\n` +
        `${sel} .pack-pulse {\n  animation: pack-glow-pulse-${p.id} 3.2s ease-in-out infinite;\n}\n` +
        `@media (prefers-reduced-motion: reduce) {\n  :root:not([data-motion="full"])${sel} .pack-pulse {\n    animation: none;\n  }\n}\n` +
        `:root[data-motion="reduce"]${sel} .pack-pulse {\n  animation: none;\n}`,
    )
  }
  return out.join("\n")
}

export function packsCss(packs: ThemePack[]): string {
  return (
    `/* GENERATED from lib/theme-packs/*.ts. Do not edit by hand: change the pack file,\n` +
    ` * then run UPDATE_PACKS_CSS=1 pnpm vitest run lib/theme-packs.\n` +
    ` * Packs never set brand, logo or sail tokens; the Crewship sail stays blue. */\n\n` +
    packs.map(packBlock).join("\n\n") +
    "\n"
  )
}
