import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { TriggersTab } from "../triggers-tab"

// Stub apiFetch so we exercise the real GET (agent info) → POST (rotate)
// round-trip against the agents_webhook_secret.go contract.
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

let canUpdate = true
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({
    abilities: { can: () => canUpdate },
    role: canUpdate ? "ADMIN" : "MEMBER",
    loading: false,
  }),
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

const AGENT = {
  schedule_cron: null,
  schedule_prompt: null,
  schedule_enabled: false,
  schedule_last_run: null,
  schedule_next_run: null,
  webhook_secret_set: false,
  crew_id: "crew-1",
  slug: "webhook-bot",
}

function mockRoutes(opts: { secretSet?: boolean; rotateStatus?: number } = {}) {
  apiFetch.mockImplementation(async (url: string) => {
    if (url.includes("/webhook-secret/rotate")) {
      if ((opts.rotateStatus ?? 200) >= 400) {
        return jsonResponse({ error: "forbidden" }, opts.rotateStatus)
      }
      return jsonResponse({ webhook_secret: "whsec_deadbeef", rotated_at: "2026-07-23T00:00:00Z" })
    }
    if (url.includes("/api/v1/agents/")) {
      return jsonResponse({ ...AGENT, webhook_secret_set: opts.secretSet ?? false })
    }
    throw new Error(`unexpected fetch: ${url}`)
  })
}

describe("TriggersTab webhook secret rotation (#1378)", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
    canUpdate = true
  })

  it("labels the button 'Generate secret' when no secret is set", async () => {
    mockRoutes({ secretSet: false })
    render(<TriggersTab agentId="a1" workspaceId="ws1" />)
    const btn = await screen.findByTestId("webhook-rotate-secret")
    expect(btn).toHaveTextContent(/generate secret/i)
  })

  it("labels the button 'Rotate secret' when a secret is already set", async () => {
    mockRoutes({ secretSet: true })
    render(<TriggersTab agentId="a1" workspaceId="ws1" />)
    const btn = await screen.findByTestId("webhook-rotate-secret")
    expect(btn).toHaveTextContent(/rotate secret/i)
  })

  it("POSTs to the rotate endpoint and reveals the show-once secret", async () => {
    mockRoutes({ secretSet: false })
    render(<TriggersTab agentId="a1" workspaceId="ws1" />)

    fireEvent.click(await screen.findByTestId("webhook-rotate-secret"))

    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const postCall = apiFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === "POST")
    expect(postCall).toBeTruthy()
    const [postUrl] = postCall as [string, RequestInit]
    expect(postUrl).toContain("/api/v1/agents/a1/webhook-secret/rotate")
    expect(postUrl).toContain("workspace_id=ws1")

    const reveal = await screen.findByTestId("webhook-secret-reveal")
    expect(reveal).toHaveTextContent("whsec_deadbeef")
    // After minting, the button flips to the rotate label.
    expect(screen.getByTestId("webhook-rotate-secret")).toHaveTextContent(/rotate secret/i)
  })

  it("dismisses the revealed secret", async () => {
    mockRoutes({ secretSet: false })
    render(<TriggersTab agentId="a1" workspaceId="ws1" />)

    fireEvent.click(await screen.findByTestId("webhook-rotate-secret"))
    await screen.findByTestId("webhook-secret-reveal")
    fireEvent.click(screen.getByRole("button", { name: /dismiss/i }))
    await waitFor(() =>
      expect(screen.queryByTestId("webhook-secret-reveal")).not.toBeInTheDocument(),
    )
  })

  it("toasts an error and reveals nothing when rotation is forbidden", async () => {
    mockRoutes({ secretSet: true, rotateStatus: 403 })
    render(<TriggersTab agentId="a1" workspaceId="ws1" />)

    fireEvent.click(await screen.findByTestId("webhook-rotate-secret"))
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(screen.queryByTestId("webhook-secret-reveal")).not.toBeInTheDocument()
  })

  it("hides the rotate control from a caller who cannot update the agent", async () => {
    canUpdate = false
    mockRoutes({ secretSet: true })
    render(<TriggersTab agentId="a1" workspaceId="ws1" />)
    // Wait for load to settle, then assert the control is absent.
    await screen.findByText(/signature header required/i)
    expect(screen.queryByTestId("webhook-rotate-secret")).not.toBeInTheDocument()
  })
})
