import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

import * as ComboboxKit from "../combobox"

// COM-13 (#3481): one searchable picker for dialogs — the Hand off "Who"
// list (Issues), the routine dialogs' native <select>s — instead of local
// copies. A button that names the choice, a searchable list in a popover.
type Opt = { value: string; label: string; description?: string; keywords?: string[]; disabled?: boolean }
type Props = {
  value: string | null
  onChange: (value: string) => void
  options: Opt[]
  ariaLabel: string
  placeholder?: string
  searchPlaceholder?: string
  emptyText?: string
}
function Combobox(props: Props) {
  const C = (ComboboxKit as unknown as { Combobox?: React.ComponentType<Props> }).Combobox
  if (!C) throw new Error("Combobox is not exported")
  return <C {...props} />
}

const PEOPLE: Opt[] = [
  { value: "u1", label: "Alex Novak", description: "Owner", keywords: ["alex@acme.test"] },
  { value: "a1", label: "Scout", description: "Agent · Research" },
  { value: "a2", label: "Quill", description: "Agent · Writing", disabled: true },
]

function Stateful({ onChange = () => {} }: { onChange?: (v: string) => void }) {
  const [value, setValue] = React.useState<string | null>(null)
  return (
    <Combobox
      ariaLabel="Who"
      placeholder="Pick someone"
      searchPlaceholder="Find a person or agent…"
      value={value}
      options={PEOPLE}
      onChange={(v) => { setValue(v); onChange(v) }}
    />
  )
}

describe("Combobox (COM-13)", () => {
  beforeEach(() => {
    cleanup()
    // jsdom lacks what cmdk's list calls; environment only.
    Element.prototype.scrollIntoView ??= vi.fn()
    globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver
  })

  it("is a named combobox showing the placeholder until something is chosen", () => {
    render(<Stateful />)
    const trigger = screen.getByRole("combobox", { name: "Who" })
    expect(trigger).toHaveAttribute("aria-expanded", "false")
    expect(trigger).toHaveTextContent("Pick someone")
  })

  it("opens a searchable list, picks, closes and names the choice", async () => {
    const onChange = vi.fn()
    render(<Stateful onChange={onChange} />)
    fireEvent.click(screen.getByRole("combobox", { name: "Who" }))
    const search = await screen.findByPlaceholderText("Find a person or agent…")
    fireEvent.change(search, { target: { value: "scout" } })
    const list = screen.getByRole("listbox")
    expect(within(list).getAllByRole("option").map((o) => o.textContent)).toEqual(["ScoutAgent · Research"])
    fireEvent.click(within(list).getByRole("option", { name: /Scout/ }))
    expect(onChange).toHaveBeenCalledWith("a1")
    expect(screen.queryByRole("listbox")).toBeNull()
    expect(screen.getByRole("combobox", { name: "Who" })).toHaveTextContent("Scout")
  })

  it("finds an option by its keywords", async () => {
    render(<Stateful />)
    fireEvent.click(screen.getByRole("combobox", { name: "Who" }))
    fireEvent.change(await screen.findByPlaceholderText("Find a person or agent…"), { target: { value: "alex@acme" } })
    expect(within(screen.getByRole("listbox")).getAllByRole("option").map((o) => o.textContent)).toEqual(["Alex NovakOwner"])
  })

  it("marks the current choice and does not pick a disabled option", async () => {
    const onChange = vi.fn()
    render(<Combobox ariaLabel="Who" value="u1" options={PEOPLE} onChange={onChange} />)
    fireEvent.click(screen.getByRole("combobox", { name: "Who" }))
    const list = await screen.findByRole("listbox")
    expect(within(list).getByRole("option", { name: /Alex Novak/ })).toHaveAttribute("data-checked", "true")
    const quill = within(list).getByRole("option", { name: /Quill/ })
    expect(quill).toHaveAttribute("aria-disabled", "true")
    fireEvent.click(quill)
    expect(onChange).not.toHaveBeenCalled()
  })

  it("says so when nothing matches", async () => {
    render(<Combobox ariaLabel="Who" value={null} options={PEOPLE} onChange={() => {}} emptyText="No one by that name." />)
    fireEvent.click(screen.getByRole("combobox", { name: "Who" }))
    fireEvent.change(await screen.findByRole("combobox", { name: /search/i }), { target: { value: "zzz" } })
    expect(screen.getByText("No one by that name.")).toBeInTheDocument()
  })
})
