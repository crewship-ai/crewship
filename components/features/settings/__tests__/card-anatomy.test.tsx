import { afterEach, describe, expect, it } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import { Building } from "lucide-react"
import { SettingsCard, controlHeight, settingsControl } from "../shared"

afterEach(cleanup)

// The Harbor 2 card anatomy at the Small text size: 32 px fields and a 68 px
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

  it("pads the card header 14 px around a 32 px icon tile, as v67 does", () => {
    render(<SettingsCard title="Identity" icon={Building}><p>row</p></SettingsCard>)
    const region = screen.getByRole("region", { name: "Identity" })
    const header = region.querySelector("[data-slot=settings-card-header]")!
    // v67 `.scard header`, PAR-088: 14 px padding (py-4 = 0.92rem), no floor;
    // 68 px with a subtitle. It was a 50 px floor before the parity audit.
    expect(header.className.split(" ")).toContain("py-4")
    expect(header.className).not.toContain("min-h-")
    const tile = header.querySelector(".icon-tile")!
    expect(tile.className.split(" ")).toContain("size-[var(--control-md)]")
    expect(tile.className.split(" ")).not.toContain("h-8")
  })
})
