import { describe, it, expect, afterEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { StatusIndicator } from "../status-indicator"

describe("StatusIndicator", () => {
  afterEach(cleanup)

  it("renders the content text", () => {
    render(<StatusIndicator content="Thinking..." />)
    expect(screen.getByText("Thinking...")).toBeTruthy()
  })

  it("exposes the status to assistive tech with polite live region", () => {
    render(<StatusIndicator content="Working" />)
    const region = screen.getByRole("status")
    expect(region.getAttribute("aria-live")).toBe("polite")
  })

  // The face beside the turn is the working signal; the step line is quiet.
  it("draws no animated mark of its own", () => {
    render(<StatusIndicator content="Starting agent…" />)
    expect(screen.getByRole("status").querySelector("svg")).toBeNull()
  })

  it("updates the rendered text when content prop changes", () => {
    const { rerender } = render(<StatusIndicator content="first" />)
    expect(screen.getByText("first")).toBeTruthy()

    rerender(<StatusIndicator content="second" />)
    expect(screen.queryByText("first")).toBeNull()
    expect(screen.getByText("second")).toBeTruthy()
  })
})
