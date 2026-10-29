import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch, AUTH_EVENT, _resetRefreshInflightForTesting } from "../api-fetch"

// An expired workbook editor session is answered with
// 409 {"error":"editor_session_expired"}, not a 401: apiFetch must hand it to
// the editor host untouched, without a token refresh, a replay or the
// app-wide session-expired signal, in cookie and in bearer (desktop) mode.

const editorExpired = () =>
  new Response(JSON.stringify({ error: "editor_session_expired" }), {
    status: 409,
    headers: { "Content-Type": "application/json" },
  })

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

describe("apiFetch and an expired editor session", () => {
  for (const mode of ["cookie", "bearer"] as const) {
    it(`passes 409 editor_session_expired through in ${mode} mode`, async () => {
      if (mode === "bearer") {
        window.__CREWSHIP_SERVER_BASE__ = "https://crewship.example.com"
        window.__CREWSHIP_TOKEN__ = "crewship_cli_abc"
      }
      const fetchMock = vi.fn().mockResolvedValue(editorExpired())
      vi.stubGlobal("fetch", fetchMock)
      const expired = vi.fn()
      window.addEventListener(AUTH_EVENT, expired)
      try {
        const res = await apiFetch("/api/v1/pages/@p/workbook/edits", {
          method: "POST",
          body: JSON.stringify({ session_id: "ws_x" }),
        })
        expect(res.status).toBe(409)
        expect(await res.json()).toEqual({ error: "editor_session_expired" })
        expect(fetchMock).toHaveBeenCalledTimes(1)
        expect(expired).not.toHaveBeenCalled()
      } finally {
        window.removeEventListener(AUTH_EVENT, expired)
      }
    })
  }
})
