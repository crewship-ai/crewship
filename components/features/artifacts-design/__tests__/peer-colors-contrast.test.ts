import { describe, expect, it } from "vitest"
import { contrast, hexToRgb } from "@/lib/theme/derive-accent"
import { DESIGN_PEER_COLORS } from "../use-design"

// Peer avatars (design-editor presence stack) and canvas cursor labels draw
// white text directly on the peer colour, so each palette entry must meet
// WCAG AA for normal text (4.5:1) against #ffffff.
describe("design peer colours", () => {
  it.each(DESIGN_PEER_COLORS)("%s keeps >= 4.5:1 contrast with white labels", color => {
    expect(contrast(hexToRgb(color), hexToRgb("#ffffff"))).toBeGreaterThanOrEqual(4.5)
  })
})
