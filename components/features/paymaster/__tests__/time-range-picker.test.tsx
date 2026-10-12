import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { TimeRangePicker } from "@/components/features/paymaster/time-range-picker"
import type { PaymasterRange } from "@/lib/types/paymaster"
afterEach(cleanup)
it.each<PaymasterRange>(["1h", "24h", "7d", "30d"])("announces %s and sends the exact backend range token", value => {
  const change = vi.fn()
  render(<TimeRangePicker value={value} onChange={change} className="custom-range" />)
  const group = screen.getByRole("radiogroup", { name: "Time range" })
  expect(group).toHaveClass("custom-range")
  expect(within(group).getByRole("radio", { name: value })).toHaveAttribute("aria-checked", "true")
  expect(within(group).getAllByRole("radio")).toHaveLength(4)
  for (const next of ["1h", "24h", "7d", "30d"]) {
    const button = within(group).getByRole("radio", { name: next })
    expect(button).toHaveAttribute("aria-checked", String(next === value))
    fireEvent.click(button)
    expect(change).toHaveBeenLastCalledWith(next)
  }
})
