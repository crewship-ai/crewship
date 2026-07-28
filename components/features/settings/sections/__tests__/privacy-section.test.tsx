import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { PrivacySection } from "../privacy-section"

// Same pattern as privileged-credentials-card.test.tsx: drive the component
// through its real fetch path with a stubbed apiFetch so we exercise the
// actual GET/PUT/DELETE round-trip against the peer-consent + peer-cards
// contract, not a mocked-out version of the component's own logic.
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
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

const CONSENT_IN = { user_id: "u1", workspace_id: "ws1", opted_out: false }
const CONSENT_OUT = {
  user_id: "u1",
  workspace_id: "ws1",
  opted_out: true,
  opted_out_at: "2026-07-20T10:00:00Z",
}

const CARDS = [
  {
    id: "c1",
    agent_id: "a1",
    agent_slug: "researcher",
    user_slug: "pavel",
    bytes: 42,
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-20T10:00:00Z",
    content: "Prefers concise answers.",
  },
]

function mockPrivacy({
  consent = CONSENT_IN,
  cards = CARDS as typeof CARDS,
  consentStatus = 200,
  cardsStatus = 200,
  putStatus = 200,
  deleteStatus = 200,
}: {
  consent?: typeof CONSENT_IN | typeof CONSENT_OUT
  cards?: typeof CARDS
  consentStatus?: number
  cardsStatus?: number
  putStatus?: number
  deleteStatus?: number
} = {}) {
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes("/api/v1/users/me/peer-consent")) {
      if (init?.method === "PUT") {
        if (putStatus >= 400) return jsonResponse({ error: "nope" }, putStatus)
        const body = JSON.parse(String(init.body)) as { opted_out: boolean }
        return jsonResponse({ ...consent, opted_out: body.opted_out })
      }
      if (consentStatus >= 400) return jsonResponse({ error: "nope" }, consentStatus)
      return jsonResponse(consent)
    }
    if (url.includes("/api/v1/users/me/peer-cards")) {
      if (init?.method === "DELETE") {
        if (deleteStatus >= 400) return jsonResponse({ error: "nope" }, deleteStatus)
        return jsonResponse({})
      }
      if (cardsStatus >= 400) return jsonResponse({ error: "nope" }, cardsStatus)
      return jsonResponse({ peers: cards })
    }
    throw new Error(`unexpected fetch: ${url}`)
  })
}

describe("PrivacySection", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
  })

  it("renders the opted-in state by default", async () => {
    mockPrivacy({ consent: CONSENT_IN })
    render(<PrivacySection workspaceId="ws1" />)

    expect(await screen.findByText(/Opted in/i)).toBeTruthy()
    // Every load sends the workspace header the peer-consent/peer-cards
    // contract requires.
    const [, getInit] = apiFetch.mock.calls[0] as [string, RequestInit]
    expect((getInit.headers as Record<string, string>)["X-Workspace-ID"]).toBe("ws1")
  })

  it("flipping opt-out PUTs the new state and toasts a confirmation", async () => {
    mockPrivacy({ consent: CONSENT_IN })
    render(<PrivacySection workspaceId="ws1" />)

    const optOutBtn = await screen.findByRole("button", { name: /opt out/i })
    fireEvent.click(optOutBtn)

    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const putCall = apiFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT")
    expect(putCall).toBeTruthy()
    const [putUrl, putInit] = putCall as [string, RequestInit]
    expect(putUrl).toContain("/api/v1/users/me/peer-consent")
    expect(JSON.parse(String(putInit.body))).toEqual({ opted_out: true })
    expect((putInit.headers as Record<string, string>)["X-Workspace-ID"]).toBe("ws1")
  })

  it("opting back in PUTs opted_out=false and toasts a confirmation", async () => {
    mockPrivacy({ consent: CONSENT_OUT })
    render(<PrivacySection workspaceId="ws1" />)

    const optInBtn = await screen.findByRole("button", { name: /opt back in/i })
    fireEvent.click(optInBtn)

    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const putCall = apiFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT")
    expect(JSON.parse(String((putCall as [string, RequestInit])[1].body))).toEqual({ opted_out: false })
  })

  it("renders peer cards with their content", async () => {
    mockPrivacy()
    render(<PrivacySection workspaceId="ws1" />)

    expect(await screen.findByText("researcher")).toBeTruthy()
    expect(screen.getByText("Prefers concise answers.")).toBeTruthy()
  })

  it("shows an empty state when there are no peer cards", async () => {
    mockPrivacy({ cards: [] })
    render(<PrivacySection workspaceId="ws1" />)

    expect(await screen.findByText(/no peer cards/i)).toBeTruthy()
    // Nothing to delete → no "Delete all" affordance.
    expect(screen.queryByRole("button", { name: /delete all/i })).toBeNull()
  })

  it("deleting all peer cards goes through a confirm dialog, DELETEs, and toasts", async () => {
    mockPrivacy()
    render(<PrivacySection workspaceId="ws1" />)

    const deleteBtn = await screen.findByRole("button", { name: /delete all/i })
    fireEvent.click(deleteBtn)

    // Destructive action is gated behind the shared AlertDialog, not
    // window.confirm — the DELETE must not fire until the dialog is
    // explicitly confirmed.
    const confirmBtn = await screen.findByRole("button", { name: /^delete$/i })
    expect(apiFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === "DELETE")).toBeUndefined()
    fireEvent.click(confirmBtn)

    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const delCall = apiFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === "DELETE")
    expect(delCall).toBeTruthy()
    const [delUrl, delInit] = delCall as [string, RequestInit]
    expect(delUrl).toContain("/api/v1/users/me/peer-cards")
    expect((delInit.headers as Record<string, string>)["X-Workspace-ID"]).toBe("ws1")
  })

  it("surfaces a load error instead of showing stale/incorrect consent state", async () => {
    mockPrivacy({ consentStatus: 500 })
    render(<PrivacySection workspaceId="ws1" />)

    await waitFor(() => expect(screen.getByText(/failed/i)).toBeTruthy())
    // Must not silently render an opted-in/opted-out claim it can't back up.
    expect(screen.queryByText(/Opted in/i)).toBeNull()
    expect(screen.queryByText(/Opted out/i)).toBeNull()
  })
})
