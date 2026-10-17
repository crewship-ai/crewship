import { afterEach, describe, expect, it } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import { Building } from "lucide-react"
import { SettingsCard, controlHeight, settingsControl } from "../shared"

afterEach(cleanup)

// The Harbor 2 card anatomy at the Small text size: 32 px fields and a 50 px
// card header. `--spacing` is 0.23rem, so the spacing scale (h-8 ≈ 27 px at
// Small) cannot express either; both have to come from the control token
// (--control-md, 32 px at Small) or a rem value derived from it, so they
// still scale with Standard and Large. Measured in a browser by
// e2e/settings-h2.spec.ts › T06 card anatomy.
describe("settings card anatomy", () => {
  it("sizes a settings field from the control token, not the spacing scale", () => {
    for (const cls of [controlHeight, settingsControl]) {
      expect(cls.split(" ")).toContain("h-[var(--control-md)]")
      expect(cls.split(" ")).not.toContain("h-8")
    }
  })

  it("gives the card header a 50 px floor around a 32 px icon tile", () => {
    render(<SettingsCard title="Identity" icon={Building}><p>row</p></SettingsCard>)
    const region = screen.getByRole("region", { name: "Identity" })
    const header = region.querySelector("[data-slot=settings-card-header]")!
    // 50 px at the 14.857 px root of the Small text size.
    expect(header.className.split(" ")).toContain("min-h-[3.3654rem]")
    const tile = header.querySelector(".icon-tile")!
    expect(tile.className.split(" ")).toContain("size-[var(--control-md)]")
    expect(tile.className.split(" ")).not.toContain("h-8")
  })
})
