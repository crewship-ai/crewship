import { describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { MoreHorizontal } from "lucide-react"

import { Button } from "../button"

// Phone pilot (MOBIL-DOPORUCENI 2a, PILOT-TELEFON-STAV row 9): a compact
// control is a real 44 × 44 button on a coarse pointer with a smaller drawn
// inner, not an invisible pseudo-area that can overlap its neighbour.
describe("Button tap", () => {
  it("is the button itself that grows to 44 × 44 on a coarse pointer", () => {
    render(<Button tap variant="ghost" size="icon-sm" aria-label="More actions"><MoreHorizontal /></Button>)
    const btn = screen.getByRole("button", { name: "More actions" })
    expect(btn).toHaveAttribute("data-tap", "")
    expect(btn.className).toContain("coarse:min-h-[44px]")
    expect(btn.className).toContain("coarse:min-w-[44px]")
    // No pseudo-element hit area.
    expect(btn.className).not.toMatch(/after:/)
  })

  it("draws the variant on a smaller inner that keeps 32 px on a coarse pointer", () => {
    render(<Button tap variant="ghost" size="icon-sm" aria-label="More actions"><MoreHorizontal /></Button>)
    const btn = screen.getByRole("button", { name: "More actions" })
    const inner = btn.querySelector("[data-slot=button-inner]") as HTMLElement
    expect(inner).not.toBeNull()
    expect(inner.className).toContain("coarse:size-8")
    expect(inner.querySelector("svg")).not.toBeNull()
    // The outer has no fill of its own: the drawn shape is the inner.
    expect(btn.className).not.toContain("bg-primary-strong")
  })

  it("keeps a text button's inner 32 px tall on a coarse pointer", () => {
    render(<Button tap size="sm">Save</Button>)
    const inner = screen.getByRole("button", { name: "Save" }).querySelector("[data-slot=button-inner]") as HTMLElement
    expect(inner.className).toContain("coarse:h-8")
    expect(inner.className).toContain("bg-primary-strong")
  })

  it("still clicks and disables as a button", () => {
    const onClick = vi.fn()
    const { rerender } = render(<Button tap onClick={onClick}>Go</Button>)
    fireEvent.click(screen.getByRole("button", { name: "Go" }))
    expect(onClick).toHaveBeenCalledTimes(1)
    rerender(<Button tap disabled onClick={onClick}>Go</Button>)
    expect(screen.getByRole("button", { name: "Go" })).toBeDisabled()
  })

  it("leaves a button without tap exactly as before", () => {
    render(<Button size="sm">Plain</Button>)
    const btn = screen.getByRole("button", { name: "Plain" })
    expect(btn.querySelector("[data-slot=button-inner]")).toBeNull()
    expect(btn).not.toHaveAttribute("data-tap")
  })
})
