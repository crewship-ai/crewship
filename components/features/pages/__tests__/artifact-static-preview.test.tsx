import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { ArtifactStaticPreview } from "../artifact-static-preview"

afterEach(() => { cleanup(); vi.restoreAllMocks() })
const rect = { x: 10, y: 20, left: 10, top: 20, right: 810, bottom: 520, width: 800, height: 500, toJSON: () => ({}) }
describe("static viewport comment capture", () => {
  it("waits for the reset iframe to load and captures bounded viewport coordinates", () => {
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(rect)
    const pick = vi.fn()
    render(<ArtifactStaticPreview document="<p>Report</p>" revision={1} picking onPick={pick} />)
    const button = screen.getByRole("button", { name: "Choose comment point in preview" })
    expect(button).toBeDisabled()
    fireEvent.load(screen.getByTitle("Artifact static preview"))
    fireEvent.click(button, { detail: 1, clientX: 130, clientY: 110 })
    expect(pick).toHaveBeenCalledWith({ schema_version: 1, type: "point", coordinate_space: "viewport", x: 120, y: 90, viewport_width: 800, viewport_height: 500 })
    expect(screen.getByTitle("Artifact static preview")).toHaveAttribute("sandbox", "")
  })
  it("does not relocate a point onto a different viewport or revision", () => {
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(rect)
    const point = { revision: 1, anchor: { schema_version: 1 as const, type: "point" as const, coordinate_space: "viewport" as const, x: 120, y: 90, viewport_width: 1000, viewport_height: 500 } }
    const view = render(<ArtifactStaticPreview document="<p>Report</p>" revision={1} picking={false} shown={point} onPick={vi.fn()} />)
    fireEvent.load(screen.getByTitle("Artifact static preview"))
    expect(screen.getByText(/viewport differs/)).toBeTruthy()
    expect(screen.queryByLabelText("Comment point")).toBeNull()
    view.rerender(<ArtifactStaticPreview document="<p>Changed</p>" revision={2} picking={false} shown={point} onPick={vi.fn()} />)
    expect(screen.queryByLabelText("Comment point")).toBeNull()
    expect(screen.queryByText(/Preview restarted/)).toBeNull()
  })
})
