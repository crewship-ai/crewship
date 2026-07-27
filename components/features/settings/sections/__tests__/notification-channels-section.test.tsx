import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
import { useState } from "react"
import { NotificationChannelsSection } from "../notification-channels-section"
import type { NotificationChannel } from "@/hooks/use-notification-channels"

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
  },
}))

// The switch under test (~line 407) fires PATCH /notification-channels/:id
// through this hook's `patch`. Mirror the real hook's contract exactly:
// `patch` only ever reflects a change in `channels` AFTER a successful
// write (there's no optimistic flip before the round-trip resolves) — so
// a rejected patch must leave `enabled` untouched.
let initialChannels: NotificationChannel[] = []
const patchMock = vi.fn()
const sendTestMock = vi.fn()

vi.mock("@/hooks/use-notification-channels", () => ({
  useNotificationChannels: () => {
    const [channels, setChannels] = useState<NotificationChannel[]>(initialChannels)
    return {
      channels,
      loading: false,
      error: null,
      create: vi.fn(),
      remove: vi.fn(),
      sendTest: sendTestMock,
      patch: async (id: string, body: Record<string, unknown>) => {
        await patchMock(id, body)
        setChannels((cur) => cur.map((c) => (c.id === id ? { ...c, ...body } : c)))
      },
    }
  },
}))

function channel(overrides: Partial<NotificationChannel> = {}): NotificationChannel {
  return {
    id: "c1",
    workspace_id: "ws1",
    type: "email",
    to: "ops@example.com",
    events: ["failed"],
    enabled: false,
    ...overrides,
  }
}

describe("NotificationChannelsSection — per-channel enable/disable switch", () => {
  beforeEach(() => {
    cleanup()
    toastSuccess.mockReset()
    toastError.mockReset()
    patchMock.mockReset()
    sendTestMock.mockReset()
    initialChannels = []
  })

  it("toasts success naming the channel when the switch is toggled on", async () => {
    initialChannels = [channel({ enabled: false })]
    patchMock.mockResolvedValue(undefined)
    render(<NotificationChannelsSection workspaceId="ws1" />)

    fireEvent.click(screen.getByRole("switch", { name: "Enable channel" }))

    await waitFor(() => expect(patchMock).toHaveBeenCalledWith("c1", { enabled: true }))
    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const [title, opts] = toastSuccess.mock.calls[0]
    expect(String(title)).toMatch(/enabled/i)
    expect(JSON.stringify(opts)).toContain("ops@example.com")
    expect(screen.getByRole("switch")).toHaveAttribute("aria-checked", "true")
  })

  it("toasts an error and does not flip the switch when the PATCH fails", async () => {
    initialChannels = [channel({ enabled: false })]
    patchMock.mockRejectedValue(new Error("workspace locked"))
    render(<NotificationChannelsSection workspaceId="ws1" />)

    const sw = screen.getByRole("switch", { name: "Enable channel" })
    fireEvent.click(sw)

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    const [title, opts] = toastError.mock.calls[0]
    expect(String(title)).toMatch(/failed/i)
    expect(JSON.stringify(opts)).toContain("workspace locked")
    // Must not be left showing the new (unconfirmed) state.
    expect(sw).toHaveAttribute("aria-checked", "false")
  })
})
