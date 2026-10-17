import { describe, it, expect, vi } from "vitest"
import * as React from "react"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

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

  it("can open with focus on the sheet, on a given element, or left where it was", async () => {
    const { unmount } = render(<Peek open onOpenChange={() => {}} title="Item" onNext={() => {}} initialFocus="content" />)
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("dialog")))
    unmount()

    function WithRef() {
      const ref = React.useRef<HTMLButtonElement>(null)
      return <Peek open onOpenChange={() => {}} title="Item" onNext={() => {}} initialFocus={ref}><button ref={ref}>Approve</button></Peek>
    }
    const second = render(<WithRef />)
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Approve" })))
    second.unmount()

    const outside = document.createElement("button")
    document.body.appendChild(outside)
    outside.focus()
    render(<Peek open onOpenChange={() => {}} title="Item" onNext={() => {}} initialFocus="none" />)
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.getByRole("dialog").contains(document.activeElement)).toBe(false)
    outside.remove()
  })
})
