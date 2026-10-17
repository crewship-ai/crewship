import * as React from "react"
import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { Check } from "lucide-react"

import { Note } from "../note"
import { ChoiceTile } from "../choice-tile"

// COM-08 (#3481): the setup card (#3414) drew a quiet note and choice tiles
// locally; other dialogs need the same two pieces, so they live in the kit
// with the setup card's API.

describe("Note", () => {
  it("is a quiet info note by default, with its icon hidden from assistive technology", () => {
    render(<Note data-testid="n">Runs stay on this machine.</Note>)
    const note = screen.getByTestId("n")
    expect(note).toHaveAttribute("data-slot", "note")
    expect(note).toHaveAttribute("data-tone", "info")
    expect(note).toHaveTextContent("Runs stay on this machine.")
    expect(note.querySelector("svg")).toHaveAttribute("aria-hidden", "true")
  })

  it("takes a warn tone and passes role through", () => {
    render(<Note tone="warn" role="alert">Docker is not running.</Note>)
    expect(screen.getByRole("alert")).toHaveAttribute("data-tone", "warn")
  })
})

describe("ChoiceTile", () => {
  it("is a toggle button that reads the way it looks", () => {
    const onClick = vi.fn()
    const { rerender } = render(<ChoiceTile icon={Check} title="This computer" description="Runs agents here" active={false} onClick={onClick} />)
    const tile = screen.getByRole("button", { name: /This computer/ })
    expect(tile).toHaveAttribute("aria-pressed", "false")
    expect(tile).toHaveAttribute("data-slot", "choice-tile")
    fireEvent.click(tile)
    expect(onClick).toHaveBeenCalledOnce()
    rerender(<ChoiceTile icon={Check} title="This computer" active onClick={onClick} meta="2 GB" />)
    expect(screen.getByRole("button", { name: /This computer/ })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByText("2 GB")).toBeInTheDocument()
  })

  it("lets a leading node replace the glyph", () => {
    render(<ChoiceTile icon={Check} leading={<span data-testid="avatar" />} title="Ops" active={false} onClick={() => {}} />)
    expect(screen.getByTestId("avatar")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Ops" }).querySelector("svg")).toBeNull()
  })
})
