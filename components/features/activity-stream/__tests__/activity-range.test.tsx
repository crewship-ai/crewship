import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { ActivityRange, RangePicker } from "../activity-range"

const NOW = new Date(2026, 9, 15, 8, 16).getTime()

afterEach(() => cleanup())

describe("ActivityRange", () => {
  it("is one joined group: shorter, the range with its dates, longer", () => {
    const onChange = vi.fn()
    render(<ActivityRange value={{ key: "7d" }} onChange={onChange} now={NOW} />)
    const group = screen.getByRole("group", { name: "Time range" })
    expect(group).toHaveTextContent("Last 7 days")
    expect(group).toHaveTextContent("8 – 15 Oct")
    fireEvent.click(screen.getByRole("button", { name: "Shorter range" }))
    expect(onChange).toHaveBeenLastCalledWith({ key: "today" })
    fireEvent.click(screen.getByRole("button", { name: "Longer range" }))
    expect(onChange).toHaveBeenLastCalledWith({ key: "30d" })
  })

  it("disables the arrow at either end", () => {
    const { rerender } = render(<ActivityRange value={{ key: "30d" }} onChange={vi.fn()} now={NOW} />)
    expect(screen.getByRole("button", { name: "Longer range" })).toBeDisabled()
    rerender(<ActivityRange value={{ key: "1h" }} onChange={vi.fn()} now={NOW} />)
    expect(screen.getByRole("button", { name: "Shorter range" })).toBeDisabled()
  })

  it("is a single chip on a phone", () => {
    render(<ActivityRange value={{ key: "today" }} onChange={vi.fn()} now={NOW} compact />)
    expect(screen.queryByRole("group", { name: "Time range" })).toBeNull()
    expect(screen.getByRole("button", { name: /Today/ })).toHaveTextContent("15 Oct")
  })
})

describe("RangePicker", () => {
  it("applies a preset on click and on its key", () => {
    const onApply = vi.fn()
    render(<RangePicker value={{ key: "7d" }} now={NOW} onApply={onApply} onCancel={vi.fn()} />)
    expect(screen.getByRole("option", { name: /Last 7 days/ })).toHaveAttribute("aria-selected", "true")
    fireEvent.click(screen.getByRole("option", { name: /Yesterday/ }))
    expect(onApply).toHaveBeenLastCalledWith({ key: "yday" })
    const root = screen.getByRole("listbox", { name: "Presets" })
    fireEvent.keyDown(root, { key: "3" })
    fireEvent.keyDown(root, { key: "0" })
    fireEvent.keyDown(root, { key: "d" })
    expect(onApply).toHaveBeenLastCalledWith({ key: "30d" })
    fireEvent.keyDown(root, { key: "w" })
    expect(onApply).toHaveBeenLastCalledWith({ key: "week" })
  })

  it("applies From / To as a custom range, and shows the time zone", () => {
    const onApply = vi.fn()
    render(<RangePicker value={{ key: "7d" }} now={NOW} onApply={onApply} onCancel={vi.fn()} />)
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2026-10-01T00:00" } })
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-10-03T12:00" } })
    fireEvent.click(screen.getByRole("button", { name: "Apply" }))
    expect(onApply).toHaveBeenLastCalledWith({
      key: "custom",
      from: new Date(2026, 9, 1).getTime(),
      to: new Date(2026, 9, 3, 12).getTime(),
    })
    expect(screen.getByTitle("Times are shown in this time zone")).toHaveTextContent(Intl.DateTimeFormat().resolvedOptions().timeZone)
  })

  it("Cancel leaves the range alone", () => {
    const onApply = vi.fn()
    const onCancel = vi.fn()
    render(<RangePicker value={{ key: "7d" }} now={NOW} onApply={onApply} onCancel={onCancel} />)
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(onCancel).toHaveBeenCalled()
    expect(onApply).not.toHaveBeenCalled()
  })
})
