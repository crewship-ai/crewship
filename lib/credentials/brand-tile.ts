/**
 * The ground and glyph colour of a provider's app-icon tile (Harbor): the
 * brand's own colour behind a white mark, the way a phone home screen draws
 * it. Both are theme-independent — the tile carries its own ground, so it
 * reads the same on the dark and the light theme.
 */

const INK = "#0D0D0D"
const WHITE = "#FFFFFF"

const NAMED: Record<string, { background: string; glyph: string }> = {
  ANTHROPIC: { background: "#D97757", glyph: WHITE },
  CLAUDE_CODE: { background: "#D97757", glyph: WHITE },
  OPENAI: { background: INK, glyph: WHITE },
  OLLAMA: { background: WHITE, glyph: INK },
  GOOGLE: { background: "linear-gradient(135deg, #4285F4, #9B72CB, #D96570)", glyph: WHITE },
}

function luminance(hex: string): number {
  const m = /^#([0-9a-f]{6})$/i.exec(hex)
  if (!m) return 0
  const channels = [0, 2, 4].map((i) => parseInt(m[1].slice(i, i + 2), 16) / 255)
  const [r, g, b] = channels.map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

export function brandTileColors(key: string, hex: string): { background: string; glyph: string } {
  const named = NAMED[key]
  if (named) return named
  const l = luminance(hex)
  // A white brand (OpenCode, Z.AI) is a white mark on black in its own art.
  if (l > 0.9) return { background: INK, glyph: WHITE }
  if (l > 0.3) return { background: hex, glyph: INK }
  return { background: hex, glyph: WHITE }
}
