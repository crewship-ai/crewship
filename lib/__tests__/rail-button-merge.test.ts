import { describe, it, expect } from "vitest"
import { cn } from "@/lib/utils"

// The rail tile is sized by its button in icon mode. The shadcn base carries
// `group-data-[collapsible=icon]:w-8! h-8!`; the rail overrides them. A
// `size-9!` did NOT replace them (the tile then overflowed a 29px button and
// sat off-centre), so the override names w and h itself.
describe("rail button override", () => {
  it("replaces the icon-mode width and height, not adds to them", () => {
    const out = cn("group-data-[collapsible=icon]:w-8! group-data-[collapsible=icon]:h-8!", "group-data-[collapsible=icon]:w-9! group-data-[collapsible=icon]:h-9!")
    expect(out).toBe("group-data-[collapsible=icon]:w-9! group-data-[collapsible=icon]:h-9!")
  })
})
