import { contrast, hexToRgb, oklchToRgb, rgbToHex, rgbToOklch } from "@/lib/theme/derive-accent"

// Peer avatars and canvas cursor labels draw white text on the colour each
// peer announces in its Yjs awareness state. That colour comes from another
// client (possibly an older bundle or a hand-crafted frame), so the editor
// makes it readable when it renders instead of trusting the sender.
const WHITE = hexToRgb("#ffffff")
const MIN_CONTRAST = 4.5
const FALLBACK = "#2563eb"
// Earlier palette entries below AA keep the same hue one Tailwind step darker.
const DARKER: Record<string, string> = { "#059669": "#047857", "#ea580c": "#c2410c" }

/** Returns a fill on which white label text meets WCAG AA (4.5:1). */
export function readablePeerFill(color: string): string {
 const hex = color.trim().toLowerCase()
 if (!/^#[0-9a-f]{6}$/.test(hex)) return FALLBACK
 if (DARKER[hex]) return DARKER[hex]
 if (contrast(hexToRgb(hex), WHITE) >= MIN_CONTRAST) return hex
 const [, chroma, hue] = rgbToOklch(hexToRgb(hex))
 for (let lightness = rgbToOklch(hexToRgb(hex))[0]; lightness > 0; lightness -= 0.01) {
  const candidate = rgbToHex(oklchToRgb(lightness, chroma, hue)).toLowerCase()
  if (contrast(hexToRgb(candidate), WHITE) >= MIN_CONTRAST) return candidate
 }
 return "#000000"
}
