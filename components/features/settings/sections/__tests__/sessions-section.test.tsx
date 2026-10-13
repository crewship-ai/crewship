import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { SessionsSection } from "../sessions-section"

// Settings › Sessions & tokens. These cases lived in profile-section.test.tsx
// until the card moved out of ProfileSection into its own section; they test
// the same card, unchanged.

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ refresh: vi.fn().mockResolvedValue(undefined) }),
}))

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
  },
}))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

beforeEach(() => {
  cleanup()
  apiFetch.mockReset()
  toastSuccess.mockReset()
  toastError.mockReset()
})

// Revoking a CLI token used to be unable to fail: the handler awaited
// apiFetch without checking res.ok and wrapped the whole thing in
// `catch { /* ignore */ }`. apiFetch resolves on a 403/500 rather than
// throwing, so a refused delete closed the dialog and re-fetched the list
// exactly like a successful one — the token stayed, and nothing said why.
describe("SessionsSection — revoking a CLI token", () => {
  const TOKEN = {
    id: "tok-1",
    name: "ci-deploy",
    created_at: "2026-07-20T10:00:00Z",
    last_used_at: "2026-07-27T09:00:00Z",
    tier: "STANDARD" as const,
  }

  function mockTokens(deleteStatus: number) {
    apiFetch.mockImplementation(async (url: string) => {
      if (url.includes("/api/v1/auth/cli-tokens/")) {
        return jsonResponse(deleteStatus >= 400 ? { error: "token is already revoked" } : {}, deleteStatus)
      }
      if (url.includes("/api/v1/auth/cli-tokens")) return jsonResponse({ data: [TOKEN] })
      if (url.includes("/api/v1/auth/sessions")) return jsonResponse([])
      return jsonResponse({})
    })
  }

  async function openRevokeDialog() {
    render(<SessionsSection />)
    await screen.findByText("ci-deploy")
    fireEvent.click(screen.getByRole("button", { name: /revoke ci-deploy/i }))
    return screen.findByRole("button", { name: /^revoke$/i })
  }

  it("confirms when the server accepts the revoke", async () => {
    mockTokens(200)
    fireEvent.click(await openRevokeDialog())
    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
  })

  it("says so when the server refuses, instead of looking like it worked", async () => {
    mockTokens(403)
    fireEvent.click(await openRevokeDialog())
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastSuccess).not.toHaveBeenCalled()
    // The token is still there — the list must not imply otherwise.
    expect(screen.getByText("ci-deploy")).toBeTruthy()
  })

  it("reports a network failure rather than swallowing it", async () => {
    apiFetch.mockImplementation(async (url: string) => {
      if (url.includes("/api/v1/auth/cli-tokens/")) throw new Error("offline")
      if (url.includes("/api/v1/auth/cli-tokens")) return jsonResponse({ data: [TOKEN] })
      if (url.includes("/api/v1/auth/sessions")) return jsonResponse([])
      return jsonResponse({})
    })
    fireEvent.click(await openRevokeDialog())
    await waitFor(() => expect(toastError).toHaveBeenCalled())
  })
})

// The card promises "everything that can sign in as you right now". A
// revoked token cannot, so listing them inline contradicted the heading —
// and on a real account seven struck-through rows pushed the two live ones
// out of view, on a screen whose entire job is spotting live access.
describe("SessionsSection — revoked CLI tokens", () => {
  const ACTIVE = { id: "a1", name: "ci-deploy", created_at: "2026-07-20T10:00:00Z", tier: "STANDARD" as const }
  const REVOKED = [
    { id: "r1", name: "old-laptop", created_at: "2026-07-01T10:00:00Z", revoked_at: "2026-07-10T10:00:00Z", tier: "STANDARD" as const },
    { id: "r2", name: "stale-ci", created_at: "2026-07-02T10:00:00Z", revoked_at: "2026-07-11T10:00:00Z", tier: "STANDARD" as const },
  ]

  function mockWith(tokens: unknown[]) {
    apiFetch.mockImplementation(async (url: string) => {
      if (url.includes("/api/v1/auth/cli-tokens")) return jsonResponse({ data: tokens })
      if (url.includes("/api/v1/auth/sessions")) return jsonResponse([])
      return jsonResponse({})
    })
  }

  it("keeps revoked tokens out of the list by default", async () => {
    mockWith([ACTIVE, ...REVOKED])
    render(<SessionsSection />)

    expect(await screen.findByText("ci-deploy")).toBeTruthy()
    expect(screen.queryByText("old-laptop")).toBeNull()
    expect(screen.queryByText("stale-ci")).toBeNull()
  })

  it("still admits they exist, with a count", async () => {
    mockWith([ACTIVE, ...REVOKED])
    render(<SessionsSection />)
    // Hiding is not the same as pretending — "did my revoke stick?" has to
    // stay answerable without a trip to the audit log.
    expect(await screen.findByRole("button", { name: /2 revoked/i })).toBeTruthy()
  })

  it("reveals them on request", async () => {
    mockWith([ACTIVE, ...REVOKED])
    render(<SessionsSection />)

    fireEvent.click(await screen.findByRole("button", { name: /2 revoked/i }))
    expect(await screen.findByText("old-laptop")).toBeTruthy()
    expect(screen.getByText("stale-ci")).toBeTruthy()
  })

  it("shows no disclosure when nothing has been revoked", async () => {
    mockWith([ACTIVE])
    render(<SessionsSection />)

    await screen.findByText("ci-deploy")
    expect(screen.queryByRole("button", { name: /revoked/i })).toBeNull()
  })

  it("does not call an all-revoked account empty", async () => {
    mockWith(REVOKED)
    render(<SessionsSection />)
    // "No tokens yet" would be wrong: there are tokens, they are just dead.
    await waitFor(() => expect(screen.queryByText(/no tokens yet/i)).toBeNull())
    expect(await screen.findByRole("button", { name: /2 revoked/i })).toBeTruthy()
  })
})

// A restricted session (#2861) may revoke but not issue: "New token" is a
// route off its allowlist.
describe("SessionsSection — restricted session", () => {
  function mockEmpty() {
    apiFetch.mockImplementation(async (url: string) => {
      if (url.includes("/api/v1/auth/cli-tokens")) return jsonResponse({ data: [] })
      if (url.includes("/api/v1/auth/sessions")) return jsonResponse([])
      return jsonResponse({})
    })
  }

  it("offers New token to a trusted session", async () => {
    mockEmpty()
    render(<SessionsSection />)
    expect(await screen.findByRole("button", { name: /new token/i })).toBeTruthy()
  })

  it("does not offer New token to a restricted one", async () => {
    mockEmpty()
    render(<SessionsSection restricted />)
    await screen.findByText(/no tokens yet/i)
    expect(screen.queryByRole("button", { name: /new token/i })).toBeNull()
  })
})
