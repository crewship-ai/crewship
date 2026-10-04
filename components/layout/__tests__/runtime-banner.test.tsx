import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { RuntimeBanner } from "../runtime-banner"
import { apiFetch } from "@/lib/api-fetch"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const response = (available: boolean) => new Response(JSON.stringify({ available }))
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => { resolve = r })
  return { resolve, promise }
}
const flush = async () => { await act(async () => { await Promise.resolve() }) }
beforeEach(() => { vi.useFakeTimers(); fetchMock.mockReset() })
afterEach(() => { cleanup(); vi.useRealTimers() })

describe("container runtime banner", () => {
  it("shows actionable installation guidance and can be dismissed until runtime recovers", async () => {
    fetchMock.mockResolvedValue(response(false))
    render(<RuntimeBanner />)
    await flush()
    expect(screen.getByText(/Agents cannot run/)).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Install Docker" })).toHaveAttribute("href", "https://docs.docker.com/get-docker/")
    expect(screen.getByRole("link", { name: "Admin Settings" })).toHaveAttribute("href", "/admin")
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }))
    fetchMock.mockResolvedValue(response(false))
    await act(async () => { vi.advanceTimersByTime(30_000) })
    expect(screen.queryByText(/Agents cannot run/)).not.toBeInTheDocument()
    fetchMock.mockResolvedValue(response(true))
    fireEvent.focus(window)
    await flush()
    fetchMock.mockResolvedValue(response(false))
    fireEvent.focus(window)
    await flush()
    expect(screen.getByText(/Agents cannot run/)).toBeInTheDocument()
    expect(fetchMock.mock.calls.every(([url]) => url === "/api/v1/system/runtime")).toBe(true)
  })
  it.each(["http", "transport", "json"])("keeps existing warning through a %s failure and retries on focus", async (failure) => {
    fetchMock.mockResolvedValueOnce(response(false))
    render(<RuntimeBanner />)
    await flush()
    if (failure === "http") fetchMock.mockResolvedValueOnce(new Response("denied", { status: 403 }))
    else if (failure === "json") fetchMock.mockResolvedValueOnce(new Response("broken"))
    else fetchMock.mockRejectedValueOnce(new Error("offline"))
    fireEvent.focus(window)
    await flush()
    expect(screen.getByText(/Agents cannot run/)).toBeInTheDocument()
    fetchMock.mockResolvedValueOnce(response(true))
    fireEvent.focus(window)
    await flush()
    expect(screen.queryByText(/Agents cannot run/)).not.toBeInTheDocument()
  })
  it("stops polling and focus refreshes on unmount", async () => {
    fetchMock.mockResolvedValueOnce(response(true))
    const { unmount } = render(<RuntimeBanner />)
    await flush()
    unmount()
    fireEvent.focus(window)
    await act(async () => { vi.advanceTimersByTime(90_000) })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
  it("does not let an old offline result undo a newer successful focus check", async () => {
    const old = deferred<Response>()
    fetchMock.mockReturnValueOnce(old.promise).mockResolvedValueOnce(response(true))
    render(<RuntimeBanner />)
    fireEvent.focus(window)
    await flush()
    await act(async () => { old.resolve(response(false)) })
    expect(screen.queryByText(/Agents cannot run/)).not.toBeInTheDocument()
  })
  it("ignores stale response bodies after a newer check has completed", async () => {
    const body = deferred<{ available: boolean }>()
    fetchMock.mockResolvedValueOnce({ ok: true, json: () => body.promise } as Response)
      .mockResolvedValueOnce(response(true))
    render(<RuntimeBanner />)
    await flush()
    fireEvent.focus(window)
    await flush()
    await act(async () => { body.resolve({ available: false }) })
    expect(screen.queryByText(/Agents cannot run/)).not.toBeInTheDocument()
  })
})
