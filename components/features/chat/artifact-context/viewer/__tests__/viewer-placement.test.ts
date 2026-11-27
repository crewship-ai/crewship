import { describe, expect, it } from "vitest"
import { viewerAsideClass } from "../viewer-placement"

describe("viewerAsideClass", () => {
  it("takes the whole width when expanded", () => {
    expect(viewerAsideClass({ expanded: true, overlay: true })).toContain("w-full")
    expect(viewerAsideClass({ expanded: true, overlay: true })).not.toContain("max-lg:absolute")
  })

  it("lies over 78% of the chat below lg with the flag on", () => {
    const cls = viewerAsideClass({ expanded: false, overlay: true }).split(" ")
    expect(cls).toEqual(expect.arrayContaining(["w-[var(--chat-panel-width,45%)]", "max-lg:absolute", "max-lg:right-0", "max-lg:w-[78%]", "max-lg:min-w-0"]))
  })

  it("keeps the shared row at every width with the flag off", () => {
    const cls = viewerAsideClass({ expanded: false, overlay: false })
    // The divider beside it sets the width; 45% until it was moved.
    expect(cls).toContain("w-[var(--chat-panel-width,45%)]")
    expect(cls).not.toContain("max-lg:")
  })
})

// An expanded Sheet takes the surface, the chat stays as a narrow column that can be folded.
import { chatColumnClass } from "../viewer-placement"
describe("an expanded Sheet beside a narrow chat", () => {
  it("lets the Sheet take the rest of the row beside the chat", () => {
    const cls = viewerAsideClass({ expanded: true, overlay: true, beside: true }).split(" ")
    expect(cls).toContain("flex-1"); expect(cls).not.toContain("w-full"); expect(cls).not.toContain("shrink-0")
  })
  it("keeps the chat as a narrow column, or folds it", () => {
    expect(chatColumnClass({ hidden: false, narrow: true }).split(" ")).toEqual(expect.arrayContaining(["w-[22rem]", "shrink-0"]))
    expect(chatColumnClass({ hidden: false, narrow: true })).not.toContain("flex-1")
    expect(chatColumnClass({ hidden: true, narrow: true }).split(" ")).toContain("hidden")
    expect(chatColumnClass({ hidden: false, narrow: false }).split(" ")).toContain("flex-1")
  })
})
