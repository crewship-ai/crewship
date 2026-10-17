import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

import { Segmented } from "../issues-display-options"

describe("Segmented", () => {
  const options = [
    { id: "list" as const, label: "List" },
    { id: "board" as const, label: "Board" },
  ]

  it("is a radio group with one option checked", () => {
    const onChange = vi.fn()
    render(<Segmented label="View" options={options} value="list" onChange={onChange} />)
    expect(screen.getByRole("radiogroup", { name: "View" })).toBeInTheDocument()
    expect(screen.getByRole("radio", { name: "List" })).toHaveAttribute("aria-checked", "true")
    fireEvent.click(screen.getByRole("radio", { name: "Board" }))
    expect(onChange).toHaveBeenCalledWith("board")
  })

  // The shell's coarse-pointer rule grows buttons to 44 px but leaves
  // role="radio" at its drawn size (it is meant for drawn radios). These are
  // buttons a finger presses in the top bar, so they opt into the kit's
  // touch target explicitly (the Issues List | Board switch measured 21 px
  // at 1024 px with a coarse pointer).
  it("every option carries the kit touch target", () => {
    render(<Segmented label="View" options={options} value="list" onChange={() => {}} />)
    for (const radio of screen.getAllByRole("radio")) expect(radio).toHaveClass("kit-tap")
  })
})
