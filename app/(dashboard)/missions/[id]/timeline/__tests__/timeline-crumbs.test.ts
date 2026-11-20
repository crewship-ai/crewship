import { readFileSync, existsSync } from "node:fs"
import path from "node:path"
import { describe, expect, it } from "vitest"

// #3361: the timeline's breadcrumb linked "Missions" to /missions/<id>, a page
// that does not exist (the Missions UI is retired), so it was a 404.
describe("mission timeline breadcrumb", () => {
  it("links only to pages that exist", () => {
    const src = readFileSync(path.resolve(__dirname, "../mission-timeline-client.tsx"), "utf8")
    expect(src).not.toMatch(/href=\{`\/missions\/\$\{missionId\}`\}/)
    expect(existsSync(path.resolve(__dirname, "../../page.tsx"))).toBe(false)
  })
})
