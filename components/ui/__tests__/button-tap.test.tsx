import { describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { MoreHorizontal } from "lucide-react"

import { readFileSync } from "node:fs"
import { join } from "node:path"

import { Button } from "../button"

// The coarse sizes live in globals.css, keyed on data-tap, so Button itself
// stays on the control tokens (lib/__tests__/control-tokens-coarse.test.ts).
const css = readFileSync(join(process.cwd(), "app/globals.css"), "utf8")
const source = readFileSync(join(process.cwd(), "components/ui/button.tsx"), "utf8")

// Phone pilot (MOBIL-DOPORUCENI 2a, PILOT-TELEFON-STAV row 9): a compact
// control is a real 44 × 44 button on a coarse pointer with a smaller drawn
// inner, not an invisible pseudo-area that can overlap its neighbour.
describe("Button tap", () => {
  it("is the button itself that grows to 44 × 44 on a coarse pointer", () => {
    render(<Button tap variant="ghost" size="icon-sm" aria-label="More actions"><MoreHorizontal /></Button>)
    const btn = screen.getByRole("button", { name: "More actions" })
    expect(btn).toHaveAttribute("data-tap", "")
    expect(css).toMatch(/button\[data-tap\]\s*\{\s*min-height:\s*44px;\s*min-width:\s*44px;/)
    expect(source).not.toMatch(/coarse:/)
    // No pseudo-element hit area.
    expect(btn.className).not.toMatch(/after:/)
  })

  it("draws the variant on a smaller inner that keeps 32 px on a coarse pointer", () => {
    render(<Button tap variant="ghost" size="icon-sm" aria-label="More actions"><MoreHorizontal /></Button>)
    const btn = screen.getByRole("button", { name: "More actions" })
    const inner = btn.querySelector("[data-slot=button-inner]") as HTMLElement
    expect(inner).not.toBeNull()
    expect(css).toMatch(/button\[data-tap\]\[data-size\^="icon"\] > \[data-slot="button-inner"\]\s*\{\s*width:\s*calc\(var\(--spacing\) \* 8\);/)
    expect(inner.querySelector("svg")).not.toBeNull()
    // The outer has no fill of its own: the drawn shape is the inner.
    expect(btn.className).not.toContain("bg-primary-strong")
  })

  it("keeps a text button's inner 32 px tall on a coarse pointer", () => {
    render(<Button tap size="sm">Save</Button>)
    const inner = screen.getByRole("button", { name: "Save" }).querySelector("[data-slot=button-inner]") as HTMLElement
    expect(css).toMatch(/button\[data-tap\] > \[data-slot="button-inner"\]\s*\{\s*height:\s*calc\(var\(--spacing\) \* 8\);/)
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
