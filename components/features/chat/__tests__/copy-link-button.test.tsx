import { act, fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { CopyLinkButton } from "../copy-link-button"

describe("copy link to this conversation", () => {
  it("says the link was copied, also where the label text is hidden", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true })
    render(<CopyLinkButton />)
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Copy link to this conversation" })) })
    expect(writeText).toHaveBeenCalledWith(window.location.href)
    expect(screen.getByRole("button", { name: "Link copied" })).toBeInTheDocument()
  })
  it("is a centred icon button on phones and a pill from sm up", () => {
    render(<CopyLinkButton />)
    const button = screen.getByRole("button", { name: "Copy link to this conversation" })
    expect(button).toHaveClass("justify-center", "rounded-md", "sm:rounded-full", "sm:border")
    expect(button).not.toHaveClass("rounded-full", "border")
  })
})
