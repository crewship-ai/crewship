import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { DeviceSessions } from "../device-sessions"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { success: (...a: unknown[]) => toastSuccess(...a), error: (...a: unknown[]) => toastError(...a) },
}))

const CHROME_MAC = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
const SAFARI_IPHONE = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"

function sessionRow(over: Partial<Record<string, unknown>> = {}) {
  return {
    id: "s1",
    created_at: "2026-07-20T10:00:00Z",
    last_used_at: "2026-07-27T09:00:00Z",
    user_agent: CHROME_MAC,
    ip: "192.168.1.14",
    is_current: true,
    ...over,
  }
}

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body }
}

function mockList(sessions: unknown[]) {
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes("/revoke")) return ok({})
    if (url.includes("/api/v1/auth/sessions")) return ok(sessions)
    return ok({})
    void init
  })
}

describe("DeviceSessions", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
  })

  it("names devices instead of printing user-agent strings", async () => {
    mockList([sessionRow(), sessionRow({ id: "s2", user_agent: SAFARI_IPHONE, is_current: false, ip: "85.160.22.7" })])
    render(<DeviceSessions onSignOut={vi.fn()} />)

    expect(await screen.findByText("Chrome on macOS")).toBeTruthy()
    expect(screen.getByText("Safari on iPhone")).toBeTruthy()
    // The raw blob must not leak into the row.
    expect(screen.queryByText(/AppleWebKit/)).toBeNull()
  })

  it("marks the current device and offers sign-out rather than revoke for it", async () => {
    const onSignOut = vi.fn()
    mockList([sessionRow()])
    render(<DeviceSessions onSignOut={onSignOut} />)
    await screen.findByText("Chrome on macOS")

    expect(screen.getByText(/this device/i)).toBeTruthy()
    // Revoking your own session mid-session is the easiest mistake here, so
    // the current row gets the deliberate "Sign out" path instead.
    fireEvent.click(screen.getByRole("button", { name: /sign out/i }))
    expect(onSignOut).toHaveBeenCalled()
  })

  it("revokes another device and refreshes the list", async () => {
    mockList([sessionRow(), sessionRow({ id: "s2", user_agent: SAFARI_IPHONE, is_current: false })])
    render(<DeviceSessions onSignOut={vi.fn()} />)
    await screen.findByText("Safari on iPhone")

    fireEvent.click(screen.getByRole("button", { name: /revoke safari on iphone/i }))

    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith("/api/v1/auth/sessions/s2/revoke", expect.objectContaining({ method: "POST" })),
    )
    expect(toastSuccess).toHaveBeenCalled()
  })

  it("reports a failed revoke instead of silently dropping the row", async () => {
    apiFetch.mockImplementation(async (url: string) => {
      if (url.includes("/revoke")) return { ok: false, status: 500, json: async () => ({ error: "nope" }) }
      return ok([sessionRow(), sessionRow({ id: "s2", user_agent: SAFARI_IPHONE, is_current: false })])
    })
    render(<DeviceSessions onSignOut={vi.fn()} />)
    await screen.findByText("Safari on iPhone")

    fireEvent.click(screen.getByRole("button", { name: /revoke safari on iphone/i }))
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(screen.getByText("Safari on iPhone")).toBeTruthy()
  })

  it("hides 'sign out everywhere else' when this is the only session", async () => {
    mockList([sessionRow()])
    render(<DeviceSessions onSignOut={vi.fn()} />)
    await screen.findByText("Chrome on macOS")

    // Nothing to sign out of — the control would be a dead end.
    expect(screen.queryByRole("button", { name: /everywhere else/i })).toBeNull()
    expect(screen.getByText(/not signed in anywhere else/i)).toBeTruthy()
  })

  it("names the count in the bulk confirm rather than saying 'confirm'", async () => {
    mockList([
      sessionRow(),
      sessionRow({ id: "s2", user_agent: SAFARI_IPHONE, is_current: false }),
      sessionRow({ id: "s3", user_agent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:127.0) Gecko/20100101 Firefox/127.0", is_current: false }),
    ])
    render(<DeviceSessions onSignOut={vi.fn()} />)
    await screen.findByText("Safari on iPhone")

    fireEvent.click(screen.getByRole("button", { name: /everywhere else/i }))
    // "Sign out 2 devices" is confirmable; a bare "Confirm" is not.
    expect(await screen.findByRole("button", { name: /sign out 2 devices/i })).toBeTruthy()
  })

  it("says CLI tokens survive a bulk sign-out, because they do", async () => {
    mockList([sessionRow(), sessionRow({ id: "s2", user_agent: SAFARI_IPHONE, is_current: false })])
    render(<DeviceSessions onSignOut={vi.fn()} />)
    await screen.findByText("Safari on iPhone")

    fireEvent.click(screen.getByRole("button", { name: /everywhere else/i }))
    // Killing a CI token as a side effect of logging out a phone would be a
    // silent outage, so the dialog states the boundary explicitly.
    expect(await screen.findByText(/CLI tokens are not affected/i)).toBeTruthy()
  })

  it("keeps the other rows when one revoke in a bulk sign-out fails", async () => {
    apiFetch.mockImplementation(async (url: string) => {
      if (url.includes("/s2/revoke")) return { ok: false, status: 500, json: async () => ({}) }
      if (url.includes("/revoke")) return ok({})
      return ok([
        sessionRow(),
        sessionRow({ id: "s2", user_agent: SAFARI_IPHONE, is_current: false }),
        sessionRow({ id: "s3", user_agent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:127.0) Gecko/20100101 Firefox/127.0", is_current: false }),
      ])
    })
    render(<DeviceSessions onSignOut={vi.fn()} />)
    await screen.findByText("Safari on iPhone")

    fireEvent.click(screen.getByRole("button", { name: /everywhere else/i }))
    fireEvent.click(await screen.findByRole("button", { name: /sign out 2 devices/i }))

    // Partial failure must be reported as partial, not as success.
    await waitFor(() => expect(toastError).toHaveBeenCalled())
  })

  it("stays quiet on a 401 — the app is already signing you out", async () => {
    // apiFetch retries a 401 through /auth/token/refresh and only lets it
    // reach us when the refresh ALSO failed, i.e. the session is gone and
    // AuthProvider is mid-redirect to /login. "Couldn't load your sessions"
    // there is a lie dressed as an error: the endpoint is fine, the user is
    // signed out. It also invites a Retry that cannot succeed.
    apiFetch.mockResolvedValue({ ok: false, status: 401, json: async () => ({ error: "no_credentials" }) })
    const { container } = render(<DeviceSessions onSignOut={vi.fn()} />)

    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    expect(screen.queryByText(/couldn't load/i)).toBeNull()
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull()
    void container
  })

  it("surfaces a failed load instead of rendering an empty device list", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 500, json: async () => ({}) })
    render(<DeviceSessions onSignOut={vi.fn()} />)
    // An empty list here reads as "you are logged in nowhere", which is both
    // false and reassuring — the worst combination on a security screen.
    expect(await screen.findByText(/couldn't load/i)).toBeTruthy()
  })
})
