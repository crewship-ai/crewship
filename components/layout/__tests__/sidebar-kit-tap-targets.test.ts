import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"

// Source-level, like setup-chrome.test.ts: the property is that every
// interactive piece of the sidebar kit carries the class the coarse-pointer
// rule targets, and that the rule exists under `pointer: coarse`, not under a
// viewport width. Measured 26–29px on a phone before this (audit A §2).
const KIT = readFileSync(join(process.cwd(), "components", "layout", "sidebar-kit.tsx"), "utf8")
const CSS = readFileSync(join(process.cwd(), "app", "globals.css"), "utf8")

describe("sidebar kit tap targets", () => {
  it("tags every interactive kit element with kit-tap", () => {
    for (const fn of ["SidebarSearch", "SidebarFilterButton", "SidebarFacetOption", "SidebarViewButton", "SidebarCollapseButton", "SidebarRow", "SidebarActiveChip", "SidebarSection"]) {
      const from = KIT.indexOf(`export function ${fn}(`)
      expect(from, `${fn} missing`).toBeGreaterThan(-1)
      const body = KIT.slice(from, KIT.indexOf("\nexport function", from + 10) === -1 ? undefined : KIT.indexOf("\nexport function", from + 10))
      expect(body, `${fn} has no kit-tap class`).toMatch(/kit-tap/)
    }
  })

  it("raises them to 44px only under a coarse pointer", () => {
    const at = CSS.indexOf(".kit-tap {")
    expect(at).toBeGreaterThan(-1)
    const guard = CSS.lastIndexOf("@media", at)
    expect(CSS.slice(guard, at)).toMatch(/pointer:\s*coarse/)
    expect(CSS.slice(at, at + 80)).toMatch(/min-height:\s*44px/)
  })
})
