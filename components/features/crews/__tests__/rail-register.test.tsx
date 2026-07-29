import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"

// =============================================================================
// The rail has its own type register, and it stays a NAMED one.
//
// A detail card is read; a rail is scanned. In a card the text IS the content,
// so it carries the content sizes. In a rail the text is a label on a target
// you are already aiming at — you know which agent you want, and the portrait
// finds it faster than the name does. So the rail sizes down, and the row
// belongs to the face instead of competing with it.
//
// The thing worth guarding is not the pixel value: it is that the value lives
// in globals.css under a name. Twelve different ways to write "small text"
// happened because each surface spelled its own out, and the next surface
// copied the number without the reason. So: no hardcoded font size in the
// rail, at all.
// =============================================================================

const read = (p: string) => readFileSync(join(process.cwd(), p), "utf8")

describe("crews rail type register", () => {
  it("defines the register in globals.css, with both steps", () => {
    const css = read("app/globals.css")
    expect(css).toMatch(/\.type-nav\s*\{[^}]*font-size:\s*0\.8125rem/)
    expect(css).toMatch(/\.type-nav-sub\s*\{[^}]*font-size:\s*0\.6875rem/)
  })

  it("uses the register in the rail rather than raw sizes", () => {
    const rail = read("components/features/crews/crews-explorer.tsx")
    expect(rail).toContain("type-nav")
    expect(rail).not.toMatch(/text-\[\d/)
    expect(rail).not.toMatch(/\btext-(xs|sm|base|lg)\b/)
  })

  it("keeps the portrait out of the shrink", () => {
    // Variant E proposed 24px portraits along with the smaller type. The type
    // came down; the face did not. 24px is where a line-drawing style stops
    // being a face, which is the thing the avatar work had just fixed.
    const rail = read("components/features/crews/crews-explorer.tsx")
    expect(rail).toContain("h-8 w-8 rounded-lg shrink-0")
    expect(rail).not.toContain("h-6 w-6 rounded-lg shrink-0")
  })
})
