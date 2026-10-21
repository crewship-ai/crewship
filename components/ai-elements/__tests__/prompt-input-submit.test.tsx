import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { PromptInputSubmit } from "../prompt-input/prompt-input-menu"

describe("PromptInputSubmit stop states", () => {
  it("stops a streaming reply", () => {
    const onStop = vi.fn()
    render(<PromptInputSubmit status="streaming" onStop={onStop} />)
    fireEvent.click(screen.getByRole("button", { name: "Stop" }))
    expect(onStop).toHaveBeenCalledTimes(1)
  })
  it("says Stopping and does not stop again until the server confirms", () => {
    const onStop = vi.fn()
    render(<PromptInputSubmit status="streaming" stopping onStop={onStop} />)
    const button = screen.getByRole("button", { name: "Stopping…" })
    expect(button).toHaveAttribute("aria-disabled", "true")
    expect(button).not.toBeDisabled() // keeps focus where the person pressed Stop
    fireEvent.click(button)
    expect(onStop).not.toHaveBeenCalled()
  })
  it("a blocked send does not mark Stop unavailable", () => {
    const onStop = vi.fn()
    render(<PromptInputSubmit status="streaming" aria-disabled aria-describedby="why" onStop={onStop} />)
    const button = screen.getByRole("button", { name: "Stop" })
    expect(button).not.toHaveAttribute("aria-disabled")
    expect(button).not.toHaveAttribute("aria-describedby")
    fireEvent.click(button)
    expect(onStop).toHaveBeenCalledTimes(1)
  })
})
