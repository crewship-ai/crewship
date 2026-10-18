import { describe, it, expect } from "vitest"
import { existsSync, readFileSync } from "node:fs"
import path from "node:path"

// COM-08 / COM-09: the setup uses the kit's Note and ChoiceTile and the one
// CLI pairing component (shared with Connect CLI); it keeps no copies.
const ROOT = path.resolve(__dirname, "../../../..")
const read = (f: string) => readFileSync(path.join(ROOT, f), "utf8")

describe("onboarding uses the shared kit", () => {
  it("has no pairing block of its own", () => {
    expect(existsSync(path.join(ROOT, "components/features/onboarding/pairing-block.tsx"))).toBe(false)
    expect(read("app/(onboarding)/onboarding/page.tsx")).toContain('from "@/components/layout/cli-pairing"')
  })
  it("setup-card defines no Note or ChoiceTile of its own", () => {
    const src = read("components/features/onboarding/setup-card.tsx")
    expect(src).not.toMatch(/export function (SetupNote|ChoiceTile)\b/)
  })
  it("the page and the template picker take them from the kit", () => {
    for (const f of ["app/(onboarding)/onboarding/page.tsx", "components/features/onboarding/template-picker.tsx"]) {
      const src = read(f)
      expect(src, f).not.toMatch(/\b(SetupNote|ChoiceTile)\b[^\n]*from "[^"]*setup-card"|setup-card"[^\n]*\b(SetupNote)\b/)
      expect(src, f).toContain('@/components/ui/choice-tile')
    }
    expect(read("app/(onboarding)/onboarding/page.tsx")).toContain('@/components/ui/note')
  })
})
