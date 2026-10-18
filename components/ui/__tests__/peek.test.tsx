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

  // Kit `returnFocus`: a Peek has no trigger, so Radix would drop focus to the
  // page on close. With the option it goes back to what opened it.
  describe("returnFocus", () => {
    function Harness({ returnFocus }: { returnFocus?: boolean }) {
      const [open, setOpen] = React.useState(false)
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>row</button>
          <Peek open={open} onOpenChange={setOpen} title="Item" initialFocus="content" returnFocus={returnFocus}><p>Body</p></Peek>
        </>
      )
    }
    async function openAndClose() {
      render(<Harness returnFocus />)
      const opener = screen.getByRole("button", { name: "row" })
      opener.focus()
      fireEvent.click(opener)
      expect(document.activeElement).toBe(screen.getByRole("dialog"))
      fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" })
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
      await waitFor(() => expect(document.activeElement).toBe(opener))
    }

    it("puts focus back on the element that opened it, on close", async () => {
      await openAndClose()
    })

    it("does the same as the phone bottom sheet (390 px, useIsMobile true): focuses the sheet on open, returns on close", async () => {
      mobile = true
      try {
        await openAndClose()
      } finally {
        mobile = false
      }
    })

    it("does not move focus if the person has moved it elsewhere meanwhile", async () => {
      render(<Harness returnFocus />)
      const opener = screen.getByRole("button", { name: "row" })
      opener.focus()
      fireEvent.click(opener)
      const other = document.createElement("input")
      document.body.appendChild(other)
      fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" })
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
      other.focus()
      await new Promise((r) => setTimeout(r, 10))
      expect(document.activeElement).toBe(other)
      other.remove()
    })
  })
})
