import { describe, it, expect, afterEach } from "vitest"
import { render, cleanup, screen } from "@testing-library/react"

import { Input } from "../input"
import { Textarea } from "../textarea"
import { Select, SelectTrigger, SelectValue } from "../select"

afterEach(cleanup)

// One text size for every form control (text-control, 13px — the size of the
// label beside it). Input used to carry `text-base md:text-sm`: the `md:`
// variant beat any caller's size on desktop, so a field read 14px next to a
// 13px label while the select beside it read 12px (Settings › General).
describe("form controls share one text size", () => {
  it.each([
    ["Input", () => <Input aria-label="x" />],
    ["Textarea", () => <Textarea aria-label="x" />],
    ["SelectTrigger", () => <Select><SelectTrigger aria-label="x"><SelectValue placeholder="p" /></SelectTrigger></Select>],
  ])("%s uses text-control and no breakpoint size", (_, el) => {
    render(el())
    const node = screen.getByLabelText("x")
    expect(node.className).toContain("text-control")
    expect(node.className).not.toMatch(/(^|\s)(md:)?text-(sm|base)(\s|$)/)
  })
})

import { cn } from "@/lib/utils"

describe("cn keeps text-control apart from colours", () => {
  it("treats text-control as a size, not a colour", () => {
    expect(cn("text-control text-muted-foreground")).toBe("text-control text-muted-foreground")
    expect(cn("text-control", "text-xs")).toBe("text-xs")
  })
})
