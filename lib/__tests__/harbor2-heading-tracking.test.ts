import { describe, expect, it } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"

// PAR-008: v67 sets card and section titles at -0.005em. The old -0.02em
// pulled word spaces shut ("Auditlog", "Recenthistory").

const css = readFileSync(path.resolve(__dirname, "../../app/globals.css"), "utf8")

describe("heading tracking", () => {
  it("sets h1, h2 and h3 at -0.005em", () => {
    const i = css.indexOf("h1, h2, h3 {")
    expect(i).toBeGreaterThan(-1)
    expect(css.slice(i, css.indexOf("}", i))).toMatch(/letter-spacing:\s*-0\.005em/)
  })

  it("keeps the section and panel roles at the same tracking", () => {
    for (const sel of [".type-section", ".type-group-head", ".type-dialog-title"]) {
      const i = css.indexOf(`${sel} {`)
      expect(i, sel).toBeGreaterThan(-1)
      expect(css.slice(i, css.indexOf("}", i)), sel).toMatch(/letter-spacing:\s*-0\.005em/)
    }
  })
})
