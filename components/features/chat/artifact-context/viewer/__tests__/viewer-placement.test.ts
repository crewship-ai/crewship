import { describe, expect, it } from "vitest"
import { viewerAsideClass } from "../viewer-placement"

describe("viewerAsideClass", () => {
  it("takes the whole width when expanded", () => {
    expect(viewerAsideClass({ expanded: true, overlay: true })).toContain("w-full")
    expect(viewerAsideClass({ expanded: true, overlay: true })).not.toContain("max-[1179px]:absolute")
  })

  it("lies over 78% of the chat below 1180px with the flag on", () => {
    const cls = viewerAsideClass({ expanded: false, overlay: true }).split(" ")
    expect(cls).toEqual(expect.arrayContaining(["w-[45%]", "max-[1179px]:absolute", "max-[1179px]:right-0", "max-[1179px]:w-[78%]", "max-[1179px]:min-w-0"]))
  })

  it("keeps the shared row at every width with the flag off", () => {
    const cls = viewerAsideClass({ expanded: false, overlay: false })
    expect(cls).toContain("w-[45%]")
    expect(cls).not.toContain("max-[1179px]:")
  })
})
