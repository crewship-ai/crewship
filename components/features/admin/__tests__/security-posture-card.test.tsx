import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import { SecurityPostureCard } from "../security-posture-card"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

const base = {
  environment: "prod",
  encryption_key_configured: true,
  plaintext_secrets_allowed: false,
  private_endpoints_ceiling: false,
  signup_open: false,
  oauth_configured: true,
  email_configured: true,
  rate_limit_disabled: false,
  rate_limit_effectively_disabled: false,
  warnings: [] as Array<{ key: string; severity: string; message: string }>,
}

beforeEach(() => apiFetch.mockReset())

describe("<SecurityPostureCard> — #1379", () => {
  it("renders an insecure state as insecure, not as a bare boolean", async () => {
    apiFetch.mockResolvedValue(
      jsonResponse({
        ...base,
        encryption_key_configured: false,
        plaintext_secrets_allowed: true,
        signup_open: true,
        warnings: [{ key: "plaintext_secrets_allowed", severity: "high", message: "credentials unencrypted at rest" }],
      }),
    )
    render(<SecurityPostureCard workspaceId="ws-1" />)

    // The point of the card is the glance — "true" would read as fine.
    expect(await screen.findByText("ALLOWED (insecure)")).toBeInTheDocument()
    expect(screen.getByText("NOT configured")).toBeInTheDocument()
    expect(screen.getByText("OPEN")).toBeInTheDocument()
    expect(screen.getByText(/credentials unencrypted at rest/)).toBeInTheDocument()
  })

  it("separates a rate-limit flag that production ignores from a real one", async () => {
    apiFetch.mockResolvedValue(
      jsonResponse({ ...base, rate_limit_disabled: true, rate_limit_effectively_disabled: false }),
    )
    render(<SecurityPostureCard workspaceId="ws-1" />)
    // Collapsing these would invent an exposure that the prod guard prevents.
    expect(await screen.findByText(/IGNORED in production/)).toBeInTheDocument()
    expect(screen.queryByText("DISABLED")).toBeNull()
  })

  it("shows a genuinely disabled limiter as DISABLED", async () => {
    apiFetch.mockResolvedValue(
      jsonResponse({ ...base, environment: "dev", rate_limit_disabled: true, rate_limit_effectively_disabled: true }),
    )
    render(<SecurityPostureCard workspaceId="ws-1" />)
    expect(await screen.findByText("DISABLED")).toBeInTheDocument()
  })

  it("gives an explicit all-clear rather than an empty area", async () => {
    apiFetch.mockResolvedValue(jsonResponse(base))
    render(<SecurityPostureCard workspaceId="ws-1" />)
    expect(await screen.findByText(/stands out/i)).toBeInTheDocument()
  })

  it("labels an unset environment", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ ...base, environment: "" }))
    render(<SecurityPostureCard workspaceId="ws-1" />)
    expect(await screen.findByText("(unset)")).toBeInTheDocument()
  })

  it("explains a 403 instead of showing an empty card", async () => {
    apiFetch.mockResolvedValue(jsonResponse({}, 403))
    render(<SecurityPostureCard workspaceId="ws-1" />)
    await waitFor(() => expect(screen.getByText(/requires an admin role/i)).toBeInTheDocument())
  })
})
