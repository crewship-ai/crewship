import * as React from "react"
import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { LayoutList } from "lucide-react"

import { Segmented } from "../segmented"

const OPTIONS = [
  { value: "list", label: "List", icon: LayoutList },
  { value: "board", label: "Board" },
  { value: "timeline", label: "Timeline", disabled: true },
  { value: "calendar", label: "Calendar" },
] as const

function Controlled({ onChange }: { onChange?: (v: string) => void }) {
  const [value, setValue] = React.useState<string>("list")
  return <Segmented ariaLabel="View" value={value} options={OPTIONS} onChange={(v) => { setValue(v); onChange?.(v) }} />
}

describe("Segmented", () => {
  it("is a named radio group with one checked segment", () => {
    render(<Controlled />)
    const group = screen.getByRole("radiogroup", { name: "View" })
    expect(group).toBeInTheDocument()
    expect(screen.getByRole("radio", { name: "List" })).toHaveAttribute("aria-checked", "true")
    expect(screen.getByRole("radio", { name: "Board" })).toHaveAttribute("aria-checked", "false")
  })

  it("selects on click and does not re-fire for the current segment", () => {
    const onChange = vi.fn()
    render(<Controlled onChange={onChange} />)
    fireEvent.click(screen.getByRole("radio", { name: "List" }))
    expect(onChange).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("radio", { name: "Board" }))
    expect(onChange).toHaveBeenCalledWith("board")
    expect(screen.getByRole("radio", { name: "Board" })).toHaveAttribute("aria-checked", "true")
  })

  it("keeps only the selected segment in the tab order and moves with the arrows, skipping disabled ones", () => {
    const onChange = vi.fn()
    render(<Controlled onChange={onChange} />)
    const radios = screen.getAllByRole("radio")
    expect(radios.map((r) => r.tabIndex)).toEqual([0, -1, -1, -1])
    fireEvent.keyDown(radios[0], { key: "ArrowRight" })
    expect(onChange).toHaveBeenLastCalledWith("board")
    expect(document.activeElement).toBe(radios[1])
    fireEvent.keyDown(radios[1], { key: "ArrowRight" })
    expect(onChange).toHaveBeenLastCalledWith("calendar")
    fireEvent.keyDown(radios[3], { key: "ArrowRight" })
    expect(onChange).toHaveBeenLastCalledWith("list")
    fireEvent.keyDown(radios[0], { key: "End" })
    expect(onChange).toHaveBeenLastCalledWith("calendar")
    fireEvent.keyDown(radios[3], { key: "Home" })
    expect(onChange).toHaveBeenLastCalledWith("list")
    expect(radios[2]).toBeDisabled()
  })

  it("stretches across the width when asked", () => {
    render(<Segmented ariaLabel="Range" value="w" options={[{ value: "w", label: "Week" }, { value: "m", label: "Month" }]} onChange={() => {}} fill />)
    expect(screen.getByRole("radiogroup")).toHaveClass("w-full")
    expect(screen.getByRole("radio", { name: "Week" })).toHaveClass("flex-1")
  })
})
