import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
import { useState } from "react"
import { NotificationPrefsSection } from "../notification-prefs-section"
import type { NotificationChannel } from "@/hooks/use-notification-channels"
import type { PrefCell } from "@/hooks/use-notification-prefs"

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
  },
}))

const testChannels: NotificationChannel[] = [
  {
    id: "c1",
    workspace_id: "ws1",
    type: "email",
    to: "ops@example.com",
    events: ["failed"],
    enabled: true,
  },
]

vi.mock("@/hooks/use-notification-channels", () => ({
  useNotificationChannels: () => ({
    channels: testChannels,
    loading: false,
    error: null,
    sendTest: vi.fn(),
  }),
}))

// setCell (~line 219 matrix cell, ~line 167 mute button) already rolls the
// optimistic write back to the pre-click cells on a rejected PUT — mirror
// that real contract here so these tests prove the component surfaces it
// via a toast rather than re-implementing the rollback themselves.
let initialCells: PrefCell[] = []
const setCellMock = vi.fn()

vi.mock("@/hooks/use-notification-prefs", () => ({
  useNotificationPrefs: () => {
    const [cells, setCells] = useState<PrefCell[]>(initialCells)
    return {
      cells,
      loading: false,
      error: null,
      setCell: async (cell: PrefCell) => {
        const prev = cells
        setCells((cur) => {
          const idx = cur.findIndex((c) => c.category === cell.category && c.channel_id === cell.channel_id)
          if (idx === -1) return [...cur, cell]
          const next = [...cur]
          next[idx] = cell
          return next
        })
        try {
          await setCellMock(cell)
        } catch (e) {
          setCells(prev)
          throw e
        }
      },
    }
  },
}))

describe("NotificationPrefsSection — matrix cell + mute button", () => {
  beforeEach(() => {
    cleanup()
    toastSuccess.mockReset()
    toastError.mockReset()
    setCellMock.mockReset()
    initialCells = []
  })

  it("toasts success naming the category and channel when a matrix cell is toggled on", async () => {
    setCellMock.mockResolvedValue(undefined)
    render(<NotificationPrefsSection workspaceId="ws1" />)

    fireEvent.click(screen.getByRole("button", { name: /Approval needed on email/ }))

    await waitFor(() =>
      expect(setCellMock).toHaveBeenCalledWith({ category: "agents.approval", channel_id: "c1", state: "immediate" }),
    )
    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const [title, opts] = toastSuccess.mock.calls[0]
    expect(String(title)).toMatch(/approval/i)
    expect(JSON.stringify(opts)).toContain("ops@example.com")
  })

  it("toasts an error and leaves the cell showing its old state when the write fails", async () => {
    setCellMock.mockRejectedValue(new Error("category not allowed"))
    render(<NotificationPrefsSection workspaceId="ws1" />)

    const cellBtn = screen.getByRole("button", { name: /Approval needed on email/ })
    fireEvent.click(cellBtn)

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    const [, opts] = toastError.mock.calls[0]
    expect(JSON.stringify(opts)).toContain("category not allowed")
    // Rolled back — must not be left showing "immediate".
    expect(cellBtn).toHaveAttribute("aria-pressed", "false")
  })

  it("toasts success naming the channel when the mute button is toggled", async () => {
    setCellMock.mockResolvedValue(undefined)
    render(<NotificationPrefsSection workspaceId="ws1" />)

    fireEvent.click(screen.getByRole("button", { name: "Mute channel" }))

    await waitFor(() =>
      expect(setCellMock).toHaveBeenCalledWith({ category: "*", channel_id: "c1", state: "immediate" }),
    )
    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const [title, opts] = toastSuccess.mock.calls[0]
    expect(String(title)).toMatch(/mute/i)
    expect(JSON.stringify(opts)).toContain("ops@example.com")
  })

  it("toasts an error and does not leave the channel showing muted when the mute write fails", async () => {
    setCellMock.mockRejectedValue(new Error("nope"))
    render(<NotificationPrefsSection workspaceId="ws1" />)

    fireEvent.click(screen.getByRole("button", { name: "Mute channel" }))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    // Rolled back — the button must still read "Mute channel", not "Unmute channel".
    expect(screen.getByRole("button", { name: "Mute channel" })).toBeTruthy()
  })
})
