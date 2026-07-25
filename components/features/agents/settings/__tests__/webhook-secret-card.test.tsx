import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { WebhookSecretCard } from "../webhook-secret-card"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

let role = "ADMIN"
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true }, role, loading: false }),
}))

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...a: unknown[]) => toastSuccess(...a),
    error: (...a: unknown[]) => toastError(...a),
  },
}))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

// The test environment has no native confirm(); install one we control.
const confirmMock = vi.fn(() => true)

beforeEach(() => {
  apiFetch.mockReset()
  toastSuccess.mockReset()
  toastError.mockReset()
  role = "ADMIN"
  confirmMock.mockReturnValue(true)
  Object.defineProperty(window, "confirm", { value: confirmMock, configurable: true, writable: true })
})

describe("<WebhookSecretCard> — #1378 item 3", () => {
  it("POSTs the rotate endpoint and reveals the show-once secret", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ webhook_secret: "whsec_abc123", rotated_at: "now" }))
    render(<WebhookSecretCard agentId="agent_1" />)

    fireEvent.click(screen.getByRole("button", { name: /rotate/i }))

    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    expect(apiFetch).toHaveBeenCalledWith(
      "/api/v1/agents/agent_1/webhook-secret/rotate",
      expect.objectContaining({ method: "POST" }),
    )
    expect(screen.getByTestId("webhook-secret-value")).toHaveTextContent("whsec_abc123")
    // The contract that makes this dangerous to miss must be on screen.
    expect(screen.getByText(/cannot be retrieved again/i)).toBeInTheDocument()
  })

  it("aborts without calling the API when the confirm is declined", () => {
    confirmMock.mockReturnValue(false)
    render(<WebhookSecretCard agentId="agent_1" />)
    fireEvent.click(screen.getByRole("button", { name: /rotate/i }))
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("surfaces the server error and reveals nothing on failure", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ error: "Forbidden" }, 403))
    render(<WebhookSecretCard agentId="agent_1" />)
    fireEvent.click(screen.getByRole("button", { name: /rotate/i }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("Forbidden"))
    expect(screen.queryByTestId("webhook-secret-value")).toBeNull()
  })

  it("disables rotation for a member", () => {
    role = "MEMBER"
    render(<WebhookSecretCard agentId="agent_1" />)
    expect(screen.getByRole("button", { name: /rotate/i })).toBeDisabled()
    expect(screen.getByText(/requires a manager or admin/i)).toBeInTheDocument()
  })
})
