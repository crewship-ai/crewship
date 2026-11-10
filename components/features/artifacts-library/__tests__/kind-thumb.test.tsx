// #5154: catalog cards draw an illustration of the kind, never the artifact's content.
// Until a safe per-version thumbnail exists, the illustration says so on its face.
import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"
import { KIND_ORDER, KINDS } from "../kinds"
import { KindThumb } from "../kind-thumb"

afterEach(cleanup)

describe("KindThumb is a labelled fallback, not a preview", () => {
  it.each(KIND_ORDER)("%s says it is an illustration of the kind", kind => {
    const { container } = render(<KindThumb kind={kind} />)
    const thumb = container.querySelector(`[data-thumb="${kind}"]`)!
    expect(thumb).toHaveAttribute("data-thumb-fallback", "kind")
    expect(thumb).toHaveTextContent(`${KINDS[kind].label} · no preview`)
    // The label is real text for sighted readers; assistive tech gets the card's own name instead.
    expect(thumb).toHaveAttribute("aria-hidden", "true")
  })

  it("draws nothing from the artifact: no image, frame, script or remote resource", () => {
    const { container } = render(<>{KIND_ORDER.map(kind => <KindThumb key={kind} kind={kind} />)}<KindThumb kind="design" mode="library" /><KindThumb kind="docs" mode="canvas" /></>)
    expect(container.querySelectorAll("img,iframe,script,object,embed,video,[src],[srcdoc]")).toHaveLength(0)
  })

  it("an empty item says so instead", () => {
    render(<KindThumb kind="docs" empty />)
    expect(screen.getByText("Nothing here yet")).toBeInTheDocument()
    expect(screen.queryByText(/no preview/)).toBeNull()
  })
})
