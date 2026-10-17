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

  // COM-02 (#3481): the one kit control replaces the local copies, so it
  // carries what they needed — a count (Dashboard window filters) and a
  // reason on a segment (Calendar's Repeating, Issues' display hints).
  it("shows a count after the label without changing the segment's name", () => {
    render(<Segmented ariaLabel="Show" value="all" options={[{ value: "all", label: "All", count: 12 }, { value: "failed", label: "Failed", count: 3 }]} onChange={() => {}} />)
    const failed = screen.getByRole("radio", { name: "Failed" })
    expect(failed).toHaveTextContent("Failed3")
    expect(failed.querySelector("[data-slot=segmented-count]")).toHaveTextContent("3")
  })

  it("explains a segment with a title, also when it is disabled", () => {
    render(
      <Segmented
        ariaLabel="When"
        value="once"
        options={[{ value: "once", label: "One time" }, { value: "repeat", label: "Repeating", disabled: true, title: "Managers and above can add repeating schedules." }]}
        onChange={() => {}}
      />,
    )
    expect(screen.getByRole("radio", { name: "Repeating" })).toHaveAttribute("title", "Managers and above can add repeating schedules.")
  })

  it("passes a stable test id and extra classes through", () => {
    render(<Segmented ariaLabel="Range" value="w" options={[{ value: "w", label: "Week" }]} onChange={() => {}} className="ml-auto" />)
    expect(screen.getByRole("radiogroup")).toHaveAttribute("data-slot", "segmented")
    expect(screen.getByRole("radiogroup")).toHaveClass("ml-auto")
  })
})
