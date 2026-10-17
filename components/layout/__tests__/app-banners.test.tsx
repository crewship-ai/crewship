import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { AppBannerProvider, pickBanner } from "../app-banners"
import { OfflineBanner } from "../page-state"
import { RealtimeStatusBanner } from "../realtime-status-banner"
import { RuntimeBanner } from "../runtime-banner"
import { DISMISS_KEY_PREFIX, UpdateBanner } from "../update-banner"
import { apiFetch } from "@/lib/api-fetch"

// Harbor 2 G09: the app has three banners — realtime, runtime, update — and
// shows at most one at a time, highest priority first. A page's own offline
// banner joins the same queue so "Reconnecting…" is never said twice.

const realtime = vi.hoisted(() => ({ status: "connected" as string, retry: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtime: () => realtime }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)

let runtimeAvailable = true
let version: { current: string; latest: string | null; newer: boolean; url: string | null } = { current: "0.8.0", latest: "0.9.0", newer: true, url: "https://example.test/notes" }

const flush = async () => { await act(async () => { await Promise.resolve(); await Promise.resolve() }) }

beforeEach(() => {
  vi.useFakeTimers()
  realtime.status = "connected"
  realtime.retry.mockReset()
  runtimeAvailable = true
  version = { current: "0.8.0", latest: "0.9.0", newer: true, url: "https://example.test/notes" }
  fetchMock.mockReset()
  fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url === "/api/v1/system/runtime") return new Response(JSON.stringify({ available: runtimeAvailable }))
    if (url === "/api/v1/system/version") return new Response(JSON.stringify(version))
    return new Response("{}", { status: 404 })
  })
  // vitest.setup.ts stubs localStorage with no-op mocks; give it a memory.
  const store = new Map<string, string>()
  vi.mocked(localStorage.getItem).mockImplementation((key: string) => store.get(key) ?? null)
  vi.mocked(localStorage.setItem).mockImplementation((key: string, value: string) => { store.set(key, String(value)) })
})
afterEach(() => { cleanup(); vi.useRealTimers() })

function Shell({ page }: { page?: React.ReactNode }) {
  return (
    <AppBannerProvider>
      <RealtimeStatusBanner />
      <RuntimeBanner />
      <UpdateBanner />
      {page}
    </AppBannerProvider>
  )
}

const shown = () => screen.queryAllByRole("status").map((el) => el.getAttribute("data-state"))

describe("app banner queue", () => {
  it("picks the highest priority, then the earliest claim", () => {
    expect(pickBanner([])).toBeNull()
    expect(pickBanner([{ id: "u", kind: "update", order: 0 }, { id: "r", kind: "runtime", order: 1 }])).toBe("r")
    expect(pickBanner([{ id: "p", kind: "offline", order: 0 }, { id: "rt", kind: "realtime", order: 1 }])).toBe("rt")
    expect(pickBanner([{ id: "a", kind: "offline", order: 2 }, { id: "b", kind: "offline", order: 1 }])).toBe("b")
  })

  it("only the highest-priority banner renders", async () => {
    runtimeAvailable = false
    render(<Shell />)
    await flush()
    expect(shown()).toEqual(["runtime"])
    expect(screen.queryByText(/is available/)).toBeNull()

    cleanup()
    realtime.status = "disconnected"
    const { rerender } = render(<Shell />)
    await flush()
    act(() => { vi.advanceTimersByTime(3000) })
    expect(shown()).toEqual(["offline"])
    expect(screen.getByText("Reconnecting…")).toBeInTheDocument()

    realtime.status = "connected"
    rerender(<Shell />)
    expect(shown()).toEqual(["reconnected"])
    act(() => { vi.advanceTimersByTime(2000) })
    // The realtime banner gives the slot back; the runtime warning returns.
    expect(shown()).toEqual(["runtime"])
  })

  it("the update notice waits for nothing else and is dismissed per version", async () => {
    const { unmount } = render(<Shell />)
    await flush()
    expect(shown()).toEqual(["update"])
    expect(screen.getByText(/0\.9\.0/).closest("[role=status]")).toHaveTextContent("Crewship 0.9.0 is available. You have 0.8.0.")
    expect(screen.getByRole("link", { name: "Release notes" })).toHaveAttribute("href", "https://example.test/notes")
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }))
    expect(shown()).toEqual([])
    expect(localStorage.getItem(DISMISS_KEY_PREFIX + "0.9.0")).toBe("1")
    unmount()

    render(<Shell />)
    await flush()
    expect(shown()).toEqual([])
    cleanup()

    version = { current: "0.8.0", latest: "0.10.0", newer: true, url: null }
    render(<Shell />)
    await flush()
    expect(shown()).toEqual(["update"])
  })

  it("a page's own offline banner steps aside while the app says the same", async () => {
    realtime.status = "disconnected"
    const page = <OfflineBanner since="14:02" onRetry={() => {}} />
    const { rerender } = render(<Shell page={page} />)
    await flush()
    // Before the app's 3 s delay the page's banner is the only one.
    expect(screen.getByText(/You see data from 14:02/)).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(3000) })
    expect(screen.queryByText(/You see data from 14:02/)).toBeNull()
    expect(screen.getAllByText("Reconnecting…")).toHaveLength(1)

    realtime.status = "connected"
    rerender(<Shell page={page} />)
    act(() => { vi.advanceTimersByTime(2000) })
    expect(screen.getByText(/You see data from 14:02/)).toBeInTheDocument()
  })

  it("the realtime banner names when the data was live, offers Retry now and does not pulse", async () => {
    vi.setSystemTime(new Date(2026, 9, 17, 14, 2))
    realtime.status = "error"
    render(<Shell />)
    await flush()
    act(() => { vi.advanceTimersByTime(3000) })
    const banner = screen.getByRole("status")
    expect(banner).toHaveTextContent("Connection lost. Live updates are paused. You see data from")
    expect(banner.textContent).toMatch(/14:02|2:02/)
    expect(banner.className).not.toMatch(/animate-pulse/)
    fireEvent.click(screen.getByRole("button", { name: "Retry now" }))
    expect(realtime.retry).toHaveBeenCalledOnce()
  })

  it("holds steady through retry attempts and only says Reconnected after an outage it announced", async () => {
    realtime.status = "disconnected"
    const { rerender } = render(<AppBannerProvider><RealtimeStatusBanner /></AppBannerProvider>)
    act(() => { vi.advanceTimersByTime(3000) })
    expect(shown()).toEqual(["offline"])
    for (const s of ["connecting", "disconnected", "connecting"]) {
      realtime.status = s
      rerender(<AppBannerProvider><RealtimeStatusBanner /></AppBannerProvider>)
      expect(shown()).toEqual(["offline"])
    }
    realtime.status = "connected"
    rerender(<AppBannerProvider><RealtimeStatusBanner /></AppBannerProvider>)
    expect(shown()).toEqual(["reconnected"])
    act(() => { vi.advanceTimersByTime(2000) })
    expect(shown()).toEqual([])

    // A blip shorter than 3 s is never announced, so nothing comes back.
    realtime.status = "disconnected"
    rerender(<AppBannerProvider><RealtimeStatusBanner /></AppBannerProvider>)
    act(() => { vi.advanceTimersByTime(1000) })
    realtime.status = "connected"
    rerender(<AppBannerProvider><RealtimeStatusBanner /></AppBannerProvider>)
    act(() => { vi.advanceTimersByTime(5000) })
    expect(shown()).toEqual([])
  })

  it("stays silent for a restricted session, which has no realtime", async () => {
    realtime.status = "unavailable"
    render(<AppBannerProvider><RealtimeStatusBanner /></AppBannerProvider>)
    act(() => { vi.advanceTimersByTime(10_000) })
    expect(shown()).toEqual([])
  })
})
