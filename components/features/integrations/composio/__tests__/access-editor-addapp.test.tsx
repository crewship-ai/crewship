// Tests for AddAppMenu — the "+ Add app" affordance inside the AccessEditor
// dialog, rewritten as a Popover + Command (cmdk) combobox so it portals out of
// the scrollable dialog (no more scroll-jail / clipping) and supports keyboard
// search. Controlled open state lets us assert content without driving Radix
// open via a pointer event in happy-dom.

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { AddAppMenu } from "../access-editor"
import type { Toolkit } from "../types"

const toolkits: Toolkit[] = [
  { slug: "youtube" },
  { slug: "gmail" },
  { slug: "googlecalendar" },
]

describe("AddAppMenu", () => {
  it("renders one selectable item per addable toolkit when open", () => {
    render(
      <AddAppMenu
        toolkits={toolkits}
        onPick={vi.fn()}
        open
        onOpenChange={vi.fn()}
      />,
    )
    expect(screen.getByText("Youtube")).toBeDefined()
    expect(screen.getByText("Gmail")).toBeDefined()
    expect(screen.getByText("Googlecalendar")).toBeDefined()
  })

  it("does not render the list while closed", () => {
    render(
      <AddAppMenu
        toolkits={toolkits}
        onPick={vi.fn()}
        open={false}
        onOpenChange={vi.fn()}
      />,
    )
    expect(screen.queryByText("Gmail")).toBeNull()
  })

  it("picking an item calls onPick with that toolkit and closes", () => {
    const onPick = vi.fn()
    const onOpenChange = vi.fn()
    render(
      <AddAppMenu
        toolkits={toolkits}
        onPick={onPick}
        open
        onOpenChange={onOpenChange}
      />,
    )
    fireEvent.click(screen.getByText("Gmail"))
    expect(onPick).toHaveBeenCalledWith(expect.objectContaining({ slug: "gmail" }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it("filters the list from the search box and commits via keyboard", () => {
    const onPick = vi.fn()
    const onOpenChange = vi.fn()
    render(
      <AddAppMenu
        toolkits={toolkits}
        onPick={onPick}
        open
        onOpenChange={onOpenChange}
      />,
    )
    const input = screen.getByPlaceholderText("Search apps…")
    fireEvent.input(input, { target: { value: "mail" } })
    expect(screen.queryByText("Youtube")).toBeNull()
    expect(screen.getByText("Gmail")).toBeDefined()

    fireEvent.keyDown(input, { key: "ArrowDown" })
    fireEvent.keyDown(input, { key: "Enter" })
    expect(onPick).toHaveBeenCalledWith(expect.objectContaining({ slug: "gmail" }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it("disables the trigger when there is nothing to add", () => {
    render(
      <AddAppMenu
        toolkits={[]}
        onPick={vi.fn()}
        open={false}
        onOpenChange={vi.fn()}
        disabled
      />,
    )
    expect(screen.getByRole("button", { name: /Add app/i })).toBeDisabled()
  })
})
