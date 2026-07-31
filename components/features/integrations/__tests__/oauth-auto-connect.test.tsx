import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { OAuthAutoConnect } from "../oauth-auto-connect"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function renderCard() {
  return render(
    <OAuthAutoConnect
      serverName="Linear"
      mcpURL="https://mcp.linear.app/sse"
      workspaceId="ws-1"
      authStatus="none"
      onCredentialCreated={vi.fn()}
    />,
  )
}

function connect() {
  fireEvent.click(screen.getByRole("button", { name: /connect with oauth/i }))
}

describe("OAuthAutoConnect", () => {
  const openSpy = vi.fn()

  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    openSpy.mockReset()
    vi.stubGlobal("open", openSpy)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("opens the consent window when discovery succeeds", async () => {
    openSpy.mockReturnValue({} as Window)
    apiFetch.mockResolvedValue(
      json(200, { status: "authorize", auth_url: "https://linear.app/oauth", credential_id: "cred-1" }),
    )
    renderCard()

    connect()

    await waitFor(() => expect(openSpy).toHaveBeenCalledWith("https://linear.app/oauth", "_blank", expect.any(String)))
    expect(screen.queryByRole("alert")).toBeNull()
  })

  // The defect: the response was parsed without checking res.ok, so the error
  // shown depended entirely on what the failure body happened to contain.
  it("reports a refused discovery in the server's words, not as an unknown error", async () => {
    apiFetch.mockResolvedValue(json(403, { error: "workspace admins only may add integrations" }))
    renderCard()

    connect()

    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "workspace admins only may add integrations",
      ),
    )
    expect(openSpy).not.toHaveBeenCalled()
  })

  // A gateway page, an empty 502, a truncated body — `res.json()` throws and
  // the whole thing was blamed on the network, which sends the operator to
  // check their wifi while the server is the thing that is down.
  it("does not blame the network for a server error with an unreadable body", async () => {
    apiFetch.mockResolvedValue(new Response("<html>502 Bad Gateway</html>", { status: 502 }))
    renderCard()

    connect()

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("502"))
    expect(screen.queryByText(/network error/i)).toBeNull()
    expect(openSpy).not.toHaveBeenCalled()
  })

  it("still reports a genuine transport failure as one", async () => {
    apiFetch.mockRejectedValue(new Error("Failed to fetch"))
    renderCard()

    connect()

    await waitFor(() => expect(screen.getByRole("alert").textContent).toMatch(/network error/i))
  })
})
