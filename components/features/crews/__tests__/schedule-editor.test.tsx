import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { ScheduleEditor } from "../schedule-editor"

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
  },
}))

describe("ScheduleEditor", () => {
  beforeEach(() => {
    cleanup()
    toastSuccess.mockReset()
    toastError.mockReset()
  })

  it("toasts a confirmation when the enable/disable toggle succeeds", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(
      <ScheduleEditor cron="0 9 * * 1-5" prompt="do things" enabled={false} onSave={onSave} />,
    )

    fireEvent.click(screen.getByRole("button", { pressed: false }))

    await waitFor(() => expect(onSave).toHaveBeenCalledWith({ cron: "0 9 * * 1-5", prompt: "do things", enabled: true }))
    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    expect(toastError).not.toHaveBeenCalled()
  })

  it("on a rejected toggle, toasts an error and reverts the switch instead of leaving it showing the new state", async () => {
    const onSave = vi.fn().mockRejectedValue(new Error("network down"))
    render(
      <ScheduleEditor cron="0 9 * * 1-5" prompt="do things" enabled={false} onSave={onSave} />,
    )

    const toggle = screen.getByRole("button", { pressed: false })
    fireEvent.click(toggle)

    await waitFor(() => expect(onSave).toHaveBeenCalled())
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastSuccess).not.toHaveBeenCalled()

    // The switch must report back to "off" (aria-pressed=false) — not stay
    // stuck showing the flipped-on state the failed write never committed.
    expect(screen.getByRole("button", { pressed: false })).toBeTruthy()
    expect(screen.queryByRole("button", { pressed: true })).toBeNull()
  })
})
