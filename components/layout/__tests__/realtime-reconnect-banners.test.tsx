import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { AppBannerProvider } from "../app-banners"
import { RealtimeStatusBanner } from "../realtime-status-banner"
import { ReconnectBanner } from "@/components/features/chat/messages/reconnect-banner"
import type { WSReason, WSStatus } from "@/hooks/use-websocket"

// #3783: quiet first, louder only for a real outage, one indicator per screen.
//   0–4 s after the drop          nothing
//   retrying                      neutral "Reconnecting…"
//   planned restart (1012/1001)   neutral "Crewship is restarting · reconnecting…"
//   fast budget spent             warn "Can't reach Crewship · retrying every 30 s" + Retry now
//   browser offline               neutral "You're offline · Crewship will reconnect when you're back"
//   back                          gone, no "Reconnected"

const realtime = vi.hoisted(() => ({ status: "connected" as string, reason: null as string | null, retry: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtime: () => realtime }))

beforeEach(() => {
  vi.useFakeTimers()
  realtime.status = "connected"
  realtime.reason = null
  realtime.retry.mockReset()
})
afterEach(() => { cleanup(); vi.useRealTimers() })

const banners = () => screen.queryAllByRole("status")

function App({ chat }: { chat?: { status: WSStatus; reason: WSReason | null; queuedCount?: number } }) {
  return (
    <AppBannerProvider>
      <RealtimeStatusBanner />
      {chat && <ReconnectBanner {...chat} />}
    </AppBannerProvider>
  )
}

function drop(status: WSStatus, reason: WSReason | null, rerender: (ui: React.ReactElement) => void) {
  realtime.status = status
  realtime.reason = reason
  rerender(<App />)
}

describe("app realtime banner", () => {
  it("says nothing for the first 4 s, then a neutral Reconnecting…", () => {
    const { rerender } = render(<App />)
    drop("connecting", "retrying", rerender)
    act(() => { vi.advanceTimersByTime(3_900) })
    expect(banners()).toHaveLength(0)
    act(() => { vi.advanceTimersByTime(100) })
    const banner = screen.getByRole("status")
    expect(banner).toHaveTextContent("Reconnecting…")
    expect(banner).toHaveAttribute("data-tone", "neutral")
    expect(banner.textContent).not.toMatch(/Disconnected|Connection lost/)
    expect(screen.queryByRole("button", { name: "Retry now" })).toBeNull()
  })

  it("never shows a drop shorter than 4 s, and says nothing when it ends", () => {
    const { rerender } = render(<App />)
    drop("connecting", "retrying", rerender)
    act(() => { vi.advanceTimersByTime(3_000) })
    drop("connected", null, rerender)
    act(() => { vi.advanceTimersByTime(10_000) })
    expect(banners()).toHaveLength(0)
  })

  it("names a planned restart", () => {
    const { rerender } = render(<App />)
    drop("connecting", "restart", rerender)
    act(() => { vi.advanceTimersByTime(4_000) })
    expect(screen.getByRole("status")).toHaveTextContent("Crewship is restarting · reconnecting…")
    expect(screen.getByRole("status")).toHaveAttribute("data-tone", "neutral")
  })

  it("warns with Retry now only once the fast budget is spent", () => {
    const { rerender } = render(<App />)
    drop("connecting", "retrying", rerender)
    act(() => { vi.advanceTimersByTime(4_000) })
    drop("offline", "unreachable", rerender)
    const banner = screen.getByRole("status")
    expect(banner).toHaveTextContent("Can't reach Crewship · retrying every 30 s")
    expect(banner).toHaveAttribute("data-tone", "warn")
    fireEvent.click(screen.getByRole("button", { name: "Retry now" }))
    expect(realtime.retry).toHaveBeenCalledOnce()
  })

  it("tells a browser that is offline to wait for the network", () => {
    const { rerender } = render(<App />)
    drop("offline", "browser-offline", rerender)
    act(() => { vi.advanceTimersByTime(4_000) })
    const banner = screen.getByRole("status")
    expect(banner).toHaveTextContent("You're offline · Crewship will reconnect when you're back")
    expect(banner).toHaveAttribute("data-tone", "neutral")
    expect(screen.queryByRole("button", { name: "Retry now" })).toBeNull()
  })

  it("disappears when the socket is back, with no Reconnected", () => {
    const { rerender } = render(<App />)
    drop("offline", "unreachable", rerender)
    act(() => { vi.advanceTimersByTime(4_000) })
    expect(banners()).toHaveLength(1)
    drop("connected", null, rerender)
    expect(banners()).toHaveLength(0)
    expect(screen.queryByText(/Reconnected/)).toBeNull()
  })

  it("does not count the first connect as an outage", () => {
    const { rerender } = render(<App />)
    drop("connecting", null, rerender)
    act(() => { vi.advanceTimersByTime(10_000) })
    expect(banners()).toHaveLength(0)
  })
})

describe("chat reconnect banner", () => {
  it.each([
    ["connecting", "retrying", 0, "Reconnecting…", "neutral"],
    ["connecting", "retrying", 1, "Reconnecting… · 1 message queued", "neutral"],
    ["connecting", "retrying", 3, "Reconnecting… · 3 messages queued", "neutral"],
    ["connecting", "restart", 0, "Crewship is restarting · reconnecting…", "neutral"],
    ["offline", "unreachable", 0, "Can't reach Crewship · retrying every 30 s", "warn"],
    ["offline", "browser-offline", 0, "You're offline · Crewship will reconnect when you're back", "neutral"],
  ] as const)("%s/%s with %d queued after 4 s: %s", (status, reason, queuedCount, text, tone) => {
    render(<ReconnectBanner status={status} reason={reason} queuedCount={queuedCount} />)
    expect(banners()).toHaveLength(0)
    act(() => { vi.advanceTimersByTime(4_000) })
    const banner = screen.getByRole("status")
    expect(banner).toHaveTextContent(text)
    expect(banner).toHaveAttribute("data-tone", tone)
  })

  it.each([["connected"], ["connecting"], ["disconnected"], ["unavailable"]] as const)("stays hidden for %s without an outage", (status) => {
    render(<ReconnectBanner status={status} reason={null} queuedCount={2} />)
    act(() => { vi.advanceTimersByTime(10_000) })
    expect(banners()).toHaveLength(0)
  })

  it("shows one indicator on the chat screen when the app banner says the same", () => {
    const { rerender } = render(<App chat={{ status: "connecting", reason: "retrying" }} />)
    realtime.status = "connecting"
    realtime.reason = "retrying"
    rerender(<App chat={{ status: "connecting", reason: "retrying" }} />)
    act(() => { vi.advanceTimersByTime(4_000) })
    expect(banners()).toHaveLength(1)
    expect(screen.getAllByText(/Reconnecting…/)).toHaveLength(1)
  })
})
