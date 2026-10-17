import * as React from "react"
import { describe, it, expect } from "vitest"
import { render, screen, within } from "@testing-library/react"

import { ConfirmDialog, ConsequenceList, type Consequence } from "@/components/ui/confirm-dialog"

// COM-11 (#3481): the lost / warn / kept list is the kit's confirm shape, and
// pages outside a ConfirmDialog (the Guest invitation on /join, CreateSurface
// reviews) composed it by hand. One component draws it everywhere.
const ITEMS: Consequence[] = [
  { tone: "lost", text: "Its bindings are removed" },
  { tone: "warn", text: "Running sessions stop" },
  { tone: "kept", text: "Runs stay 30 days" },
]

describe("ConsequenceList", () => {
  it("draws one line per consequence, toned", () => {
    render(<ConsequenceList items={ITEMS} />)
    const list = screen.getByRole("list", { name: "What happens" })
    expect(within(list).getAllByRole("listitem").map((li) => li.getAttribute("data-tone"))).toEqual(["lost", "warn", "kept"])
  })

  it("says the tone in words, not by colour and icon alone", () => {
    render(<ConsequenceList items={ITEMS} />)
    const [lost, warn, kept] = screen.getAllByRole("listitem")
    expect(lost).toHaveTextContent("Lost: Its bindings are removed")
    expect(warn).toHaveTextContent("Warning: Running sessions stop")
    expect(kept).toHaveTextContent("Kept: Runs stay 30 days")
  })

  it("renders nothing for no consequences", () => {
    const { container } = render(<ConsequenceList items={[]} />)
    expect(container).toBeEmptyDOMElement()
  })

  it("is what ConfirmDialog shows", () => {
    render(<ConfirmDialog open onOpenChange={() => {}} title="Delete?" consequences={ITEMS} confirmLabel="Delete" onConfirm={() => {}} />)
    expect(screen.getByRole("list", { name: "What happens" })).toHaveAttribute("data-slot", "consequences")
  })
})
