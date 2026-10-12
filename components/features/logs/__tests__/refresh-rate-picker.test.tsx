import { useState } from "react"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { RefreshRatePicker, refreshRateMs, type RefreshRate } from "../refresh-rate-picker"
afterEach(cleanup)
it.each([
  ["live", "Live", null], ["off", "Off", null], ["5s", "5s", 5000],
  ["10s", "10s", 10000], ["30s", "30s", 30000], ["1m", "1m", 60000],
] as const)("shows %s and converts it to polling interval %s", (value, label, interval) => {
  render(<RefreshRatePicker value={value} onChange={vi.fn()} />)
  expect(screen.getByRole("combobox", { name: "Refresh rate" })).toHaveTextContent(label)
  expect(refreshRateMs(value)).toBe(interval)
})
it("lets a user select each cadence through the real picker", async () => {
  const changed = vi.fn()
  function Harness() {
    const [value, setValue] = useState<RefreshRate>("live")
    return <RefreshRatePicker value={value} onChange={v => { changed(v); setValue(v) }} />
  }
  render(<Harness />)
  for (const [label, value] of [["Every 5s", "5s"], ["Every 10s", "10s"], ["Every 30s", "30s"], ["Every 1m", "1m"], ["Off", "off"], ["Live (SSE)", "live"]]) {
    fireEvent.keyDown(screen.getByRole("combobox"), { key: "ArrowDown" })
    fireEvent.click(await screen.findByRole("option", { name: label }))
    expect(changed).toHaveBeenLastCalledWith(value)
  }
})
it("does not invent a polling interval for an unknown persisted preference", () => {
  expect(refreshRateMs("future" as RefreshRate)).toBeNull()
})
