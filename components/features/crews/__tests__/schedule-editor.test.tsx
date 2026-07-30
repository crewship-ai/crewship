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

  // Open the editor and type a new cron expression. Returns the input so the
  // assertions can read back what survived the save attempt.
  function startEditing(nextCron: string) {
    fireEvent.click(screen.getByRole("button", { name: "edit" }))
    const input = screen.getByPlaceholderText("0 9 * * 1-5") as HTMLInputElement
    fireEvent.change(input, { target: { value: nextCron } })
    return input
  }

  it("leaves edit mode and reports success when the save lands", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(
      <ScheduleEditor cron="0 9 * * 1-5" prompt="do things" enabled onSave={onSave} />,
    )

    startEditing("30 6 * * *")
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith({ cron: "30 6 * * *", prompt: "do things", enabled: true }),
    )
    // Back to the read-only view: the "edit" affordance is the tell.
    await waitFor(() => expect(screen.getByRole("button", { name: "edit" })).toBeTruthy())
    expect(toastError).not.toHaveBeenCalled()
  })

  it("does not claim success when the save is rejected: it surfaces the server's message and keeps the draft", async () => {
    // What a 500 (or a 403) arrives as once the caller checks res.ok — the
    // server's own wording, which distinguishes validation from permission.
    const onSave = vi.fn().mockRejectedValue(new Error("cron expression has 6 fields, expected 5"))
    render(
      <ScheduleEditor cron="0 9 * * 1-5" prompt="do things" enabled onSave={onSave} />,
    )

    const input = startEditing("0 9 * * 1-5 7")
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(onSave).toHaveBeenCalled())

    // 1. The failure is stated, in the server's words — not swallowed, not
    //    replaced by a generic "something went wrong".
    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith("cron expression has 6 fields, expected 5"),
    )
    expect(toastSuccess).not.toHaveBeenCalled()

    // 2. Nothing anywhere in the editor says the save worked.
    expect(screen.queryByText("Saved")).toBeNull()

    // 3. The edit is still there to retry — the editor stays open with the
    //    typed value intact, and the read-only view (which would show the
    //    stale server value as if it were current) is not rendered.
    expect(screen.queryByRole("button", { name: "edit" })).toBeNull()
    expect((screen.getByPlaceholderText("0 9 * * 1-5") as HTMLInputElement).value).toBe("0 9 * * 1-5 7")
    expect(input.value).toBe("0 9 * * 1-5 7")

    // 4. And it is re-submittable: Save is not left stuck in "Saving…".
    const save = screen.getByRole("button", { name: "Save" }) as HTMLButtonElement
    expect(save.disabled).toBe(false)
  })
})
