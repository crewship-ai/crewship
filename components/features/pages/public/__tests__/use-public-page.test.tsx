/**
 * Fetching a public page (PRD `docs/prd/pages.md` §7.3.1, §7.3.3).
 *
 * The load-bearing assertions here are about what the REQUEST carries, not
 * about what the hook returns:
 *
 *   · no credentials, ever — §7.3.1 makes this surface session-less, and
 *     `apiFetch` would both attach the session and bounce a 401 to /login,
 *     which is a login screen a reader with no account cannot use;
 *   · the password in the body and never in the URL (§7.3.3);
 *   · the password is never persisted — no cookie, no storage — because
 *     inventing a session here is inventing the thing §7.3.1 says this surface
 *     does not have.
 */
import { describe, it, expect, vi } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"

import { usePublicPage, publicPagePath, publicPageUnlockPath } from "../use-public-page"

const TOKEN = "EXAMPLE-not-a-real-token-for-tests-00000000"

const DOC = {
  slug: "uzaverka",
  name: "Uzávěrka",
  panels: [],
  generated_at: "2026-08-12T13:00:00Z",
  expires_at: "2026-09-11T13:00:00Z",
  show_provenance: false,
}

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response
}

describe("usePublicPage", () => {
  it("fetches the token's document without credentials", async () => {
    const fetchImpl = vi.fn(async () => jsonResponse(200, DOC))
    const { result } = renderHook(() => usePublicPage(TOKEN, fetchImpl))

    await waitFor(() => expect(result.current.status).toBe("ready"))
    expect(result.current.page?.slug).toBe("uzaverka")

    const [url, init] = fetchImpl.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(publicPagePath(TOKEN))
    expect(init.credentials).toBe("omit")
    // No Authorization header, no workspace header: this surface has neither.
    expect(JSON.stringify(init.headers ?? {})).not.toMatch(/authorization|workspace/i)
  })

  it("treats a 401 as a password prompt and not as a login", async () => {
    const fetchImpl = vi.fn(async () => jsonResponse(401, { password_required: true }))
    const { result } = renderHook(() => usePublicPage(TOKEN, fetchImpl))

    await waitFor(() => expect(result.current.status).toBe("password"))
    expect(result.current.page).toBeNull()
    // Nothing here may navigate: hooks/use-auth.tsx turns a terminal auth
    // state into window.location.replace("/login"), which is why this hook
    // never goes through apiFetch.
    expect(window.location.pathname).not.toBe("/login")
  })

  it("posts the password in the body, never in the URL", async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(401, { password_required: true }))
      .mockResolvedValueOnce(jsonResponse(200, DOC))

    const { result } = renderHook(() => usePublicPage(TOKEN, fetchImpl))
    await waitFor(() => expect(result.current.status).toBe("password"))

    await act(async () => {
      await result.current.submit("uzaverka-2026")
    })

    const [url, init] = fetchImpl.mock.calls[1] as [string, RequestInit]
    expect(url).toBe(publicPageUnlockPath(TOKEN))
    expect(url).not.toContain("uzaverka-2026")
    expect(init.method).toBe("POST")
    expect(init.credentials).toBe("omit")
    expect(JSON.parse(String(init.body))).toEqual({ password: "uzaverka-2026" })
    await waitFor(() => expect(result.current.status).toBe("ready"))
  })

  it("keeps the refusal wording the server chose", async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(401, { password_required: true }))
      .mockResolvedValueOnce(
        jsonResponse(401, { error: "that link and password do not match", password_required: true }),
      )

    const { result } = renderHook(() => usePublicPage(TOKEN, fetchImpl))
    await waitFor(() => expect(result.current.status).toBe("password"))
    await act(async () => {
      await result.current.submit("wrong")
    })

    expect(result.current.status).toBe("password")
    expect(result.current.message).toBe("that link and password do not match")
    // The hook must not persist the attempt anywhere. A public page has no
    // session, and a password in storage is a session with extra steps.
    expect(document.cookie).toBe("")
    for (const store of [window.localStorage, window.sessionStorage]) {
      // happy-dom's Storage exposes its methods as own keys, so the assertion
      // is on what was STORED rather than on the key count.
      expect(store.getItem("password")).toBeNull()
      expect(store.getItem("crewship-public-page")).toBeNull()
      for (let i = 0; i < store.length; i++) {
        const key = store.key(i)
        expect(key === null ? "" : String(store.getItem(key))).not.toContain("wrong")
      }
    }
  })

  it("reports an unavailable link without guessing why", async () => {
    const fetchImpl = vi.fn(async () => jsonResponse(404, { error: "this link is not available" }))
    const { result } = renderHook(() => usePublicPage(TOKEN, fetchImpl))

    await waitFor(() => expect(result.current.status).toBe("unavailable"))
    expect(result.current.message).toBe("this link is not available")
    expect(result.current.page).toBeNull()
  })

  it("surfaces the 429 without losing the prompt", async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(401, { password_required: true }))
      .mockResolvedValueOnce(
        jsonResponse(429, { error: "this link has been opened too many times recently; try again shortly" }),
      )

    const { result } = renderHook(() => usePublicPage(TOKEN, fetchImpl))
    await waitFor(() => expect(result.current.status).toBe("password"))
    await act(async () => {
      await result.current.submit("guess")
    })
    expect(result.current.status).toBe("password")
    expect(result.current.message).toMatch(/too many times/)
  })

  it("does nothing until the token is known", () => {
    const fetchImpl = vi.fn(async () => jsonResponse(200, DOC))
    const { result } = renderHook(() => usePublicPage(null, fetchImpl))
    expect(fetchImpl).not.toHaveBeenCalled()
    expect(result.current.status).toBe("loading")
  })
})
