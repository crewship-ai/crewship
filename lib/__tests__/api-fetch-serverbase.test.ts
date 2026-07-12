import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch, _resetRefreshInflightForTesting } from "../api-fetch"

// Desktop-shell (server-base) integration for apiFetch: the configured
// server base is the ONE extra origin the guard admits, relative paths get
// prefixed, and bearer mode swaps cookie credentials for an Authorization
// header with no refresh cycle. Same-origin cookie behavior is owned (and
// locked) by api-fetch.test.ts / api-fetch.cov.test.ts — those must keep
// passing untouched; this file only covers the new mode.

function clearShellGlobals() {
  delete (window as Window).__CREWSHIP_SERVER_BASE__
  delete (window as Window).__CREWSHIP_TOKEN__
}

beforeEach(() => {
  clearShellGlobals()
  _resetRefreshInflightForTesting()
})
afterEach(() => {
  clearShellGlobals()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe("apiFetch with a configured server base", () => {
  it("accepts an absolute URL on the configured base origin", async () => {
    window.__CREWSHIP_SERVER_BASE__ = "https://crewship.example.com"
    const fetchMock = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }))
    vi.stubGlobal("fetch", fetchMock)

    const res = await apiFetch("https://crewship.example.com/api/v1/agents")
    expect(res.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("still rejects origins that are neither page nor base", async () => {
    window.__CREWSHIP_SERVER_BASE__ = "https://crewship.example.com"
    await expect(apiFetch("https://evil.example.com/api/v1/x")).rejects.toThrow(/cross-origin/)
  })

  it("prefixes relative /api paths with the base", async () => {
    window.__CREWSHIP_SERVER_BASE__ = "https://crewship.example.com"
    const fetchMock = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }))
    vi.stubGlobal("fetch", fetchMock)

    await apiFetch("/api/v1/agents")
    expect(fetchMock.mock.calls[0][0]).toBe("https://crewship.example.com/api/v1/agents")
  })

  it("bearer mode: Authorization header, credentials omit, no refresh on 401", async () => {
    window.__CREWSHIP_SERVER_BASE__ = "https://crewship.example.com"
    window.__CREWSHIP_TOKEN__ = "crewship_cli_abc"
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify({ error: "session_expired" }), { status: 401 }))
    vi.stubGlobal("fetch", fetchMock)

    const res = await apiFetch("/api/v1/agents")
    expect(res.status).toBe(401)
    // Exactly one fetch — no /api/auth/token/refresh round-trip (bearer
    // tokens don't rotate; a refresh POST would 401 and double the noise).
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const init = fetchMock.mock.calls[0][1]
    expect(init.credentials).toBe("omit")
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer crewship_cli_abc")
  })

  it("cookie mode against a remote base keeps credentials include + refresh path", async () => {
    window.__CREWSHIP_SERVER_BASE__ = "https://crewship.example.com"
    const fetchMock = vi
      .fn()
      // original request 401s with a refreshable reason…
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: "session_expired" }), { status: 401 }))
      // …refresh succeeds…
      .mockResolvedValueOnce(new Response("{}", { status: 200 }))
      // …retry lands.
      .mockResolvedValueOnce(new Response("{}", { status: 200 }))
    vi.stubGlobal("fetch", fetchMock)

    const res = await apiFetch("/api/v1/agents")
    expect(res.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    // The refresh POST must target the remote base, not the page origin.
    expect(fetchMock.mock.calls[1][0]).toBe("https://crewship.example.com/api/auth/token/refresh")
    expect(fetchMock.mock.calls[1][1]).toMatchObject({ credentials: "include" })
  })
})
