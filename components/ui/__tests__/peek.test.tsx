import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

let mobile = false
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile }))

import { Peek } from "../peek"

describe("Peek", () => {
  it("is a titled side sheet with previous, next and open", () => {
    const prev = vi.fn(), next = vi.fn(), full = vi.fn()
    render(<Peek open onOpenChange={() => {}} title="Run #418" subtitle="Review pull requests" onPrev={prev} onNext={next} onOpenFull={full}><p>Body</p></Peek>)
    const dialog = screen.getByRole("dialog", { name: "Run #418" })
    expect(dialog).toHaveAttribute("data-slot", "peek")
    fireEvent.click(screen.getByRole("button", { name: "Next (J)" }))
    fireEvent.click(screen.getByRole("button", { name: "Previous (K)" }))
    fireEvent.click(screen.getByRole("button", { name: "Open full page (Enter)" }))
    expect([next, prev, full].map((f) => f.mock.calls.length)).toEqual([1, 1, 1])
  })

  it("J and K move between items, but not while typing", () => {
    const next = vi.fn(), prev = vi.fn()
    render(<Peek open onOpenChange={() => {}} title="Item" onNext={next} onPrev={prev}><input aria-label="Note" /></Peek>)
    const dialog = screen.getByRole("dialog")
    fireEvent.keyDown(dialog, { key: "j" })
    fireEvent.keyDown(dialog, { key: "k" })
    fireEvent.keyDown(screen.getByRole("textbox", { name: "Note" }), { key: "j" })
    expect(next).toHaveBeenCalledOnce()
    expect(prev).toHaveBeenCalledOnce()
  })

  it("comes from the bottom on a phone", () => {
    mobile = true
    render(<Peek open onOpenChange={() => {}} title="Item" />)
    expect(screen.getByRole("dialog")).toHaveClass("rounded-t-xl")
    mobile = false
  })
})
