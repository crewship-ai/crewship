import { describe, expect, it } from "vitest"
import { viewerAsideClass } from "../viewer-placement"

describe("viewerAsideClass", () => {
  it("takes the whole width when expanded", () => {
    expect(viewerAsideClass({ expanded: true, overlay: true })).toContain("w-full")
    expect(viewerAsideClass({ expanded: true, overlay: true })).not.toContain("max-lg:absolute")
  })

  it("lies over 78% of the chat below lg with the flag on", () => {
    const cls = viewerAsideClass({ expanded: false, overlay: true }).split(" ")
    expect(cls).toEqual(expect.arrayContaining(["w-[45%]", "max-lg:absolute", "max-lg:right-0", "max-lg:w-[78%]", "max-lg:min-w-0"]))
  })

  it("keeps the shared row at every width with the flag off", () => {
    const cls = viewerAsideClass({ expanded: false, overlay: false })
    expect(cls).toContain("w-[45%]")
    expect(cls).not.toContain("max-lg:")
  })
})
