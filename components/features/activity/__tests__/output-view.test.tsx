import { describe, it, expect } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { OutputWithRawToggle } from "../output-view"

// OutputWithRawToggle (#851) — the copy-paste-fidelity switch. Its own
// contract is *when* the toggle appears and that it flips; the actual
// markdown/code/JSON rendering is covered by analyzeOutput's unit tests.
// JSON content is used here because JSONViewer is shiki-free and mounts
// cleanly in happy-dom.

describe("OutputWithRawToggle", () => {
  it("offers a raw toggle for renderable content and flips its label", () => {
    render(<OutputWithRawToggle value={'{"status":"ok"}'} />)
    const toggle = screen.getByRole("button", { name: /raw/i })
    expect(toggle).toBeTruthy()
    expect(toggle.getAttribute("aria-pressed")).toBe("false")

    fireEvent.click(toggle)
    // After flipping to raw, the switch inverts to "rendered" and the
    // verbatim source is shown for copy-paste.
    const back = screen.getByRole("button", { name: /rendered/i })
    expect(back.getAttribute("aria-pressed")).toBe("true")
    expect(screen.getByText('{"status":"ok"}')).toBeTruthy()
  })

  it("shows no toggle for plain text (nothing to toggle)", () => {
    render(<OutputWithRawToggle value="just a short scalar" />)
    expect(screen.queryByRole("button", { name: /raw/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /rendered/i })).toBeNull()
    expect(screen.getByText("just a short scalar")).toBeTruthy()
  })

  it("shows no toggle for an empty value", () => {
    render(<OutputWithRawToggle value="" emptyLabel="No input." />)
    expect(screen.queryByRole("button", { name: /raw/i })).toBeNull()
  })
})
