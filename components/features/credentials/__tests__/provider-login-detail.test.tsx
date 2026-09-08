// The seat's own sections on a credential page (PRD provider-logins §6.1,
// wireframe LoginDetail), and its action row. What matters: null quota is
// said, not drawn; a seat without a refresh flow says what to do instead;
// the refresh token is shown SEALED and nothing else; Refresh now is
// `crewship credential refresh` behind a button, 409 included; a
// subscription never offers Test.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { ProviderLoginActions, ProviderLoginCards } from "../provider-login-detail"
import type { LoginCredential, ProviderLogin } from "@/lib/credentials/provider-logins"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
vi.mock("sonner", () => ({ toast: h.toast }))

const DAY = 24 * 3600 * 1000

function login(over: Partial<ProviderLogin> = {}): ProviderLogin {
  return {
    mode: "subscription",
    provider: "OPENAI",
    plan: "plus",
    plan_label: "ChatGPT Plus",
    owner_user_id: "u2",
    owner_email: "jana@unify.cz",
    // A minute of slack: the fixture's clock runs a few ms ahead of the render's.
    expires_at: new Date(Date.now() + 2 * DAY + 4 * 3600_000 + 60_000).toISOString(),
    refresh: { supported: true, status: "ok", last_at: new Date(Date.now() - 8 * DAY).toISOString(), next_at: new Date(Date.now() + DAY).toISOString(), error: null },
    quota: { window_5h_pct: 100, window_weekly_pct: 71, resets_at: new Date(Date.now() + 2 * 3600_000).toISOString() },
    delivery: { kind: "file", target: ".codex/auth.json" },
    pays_for: { agents: 1, crews: 0 },
    ...over,
  }
}

function seat(over: Partial<ProviderLogin> = {}): LoginCredential & { login: ProviderLogin } {
  return { id: "cred_1", name: "ChatGPT Plus · jana", provider: "OPENAI", status: "ACTIVE", login: login(over) }
}

function ok(body: unknown, status = 200) {
  return { ok: true, status, json: async () => body } as unknown as Response
}
function fail(status: number, body: unknown = {}) {
  return { ok: false, status, json: async () => body } as unknown as Response
}

beforeEach(() => {
  h.apiFetch.mockReset()
  h.toast.success.mockReset()
  h.toast.error.mockReset()
  h.toast.info.mockReset()
})

function renderCards(credential = seat(), over: Partial<React.ComponentProps<typeof ProviderLoginCards>> = {}) {
  const onLoginChange = vi.fn()
  render(<ProviderLoginCards workspaceId="ws1" credential={credential} canUpdate onLoginChange={onLoginChange} accountId="acct_7f3a" {...over} />)
  return { onLoginChange }
}

describe("Seat", () => {
  it("names the owner, the plan and its source, the account and the billing", () => {
    renderCards()
    expect(screen.getByText("jana@unify.cz")).toBeInTheDocument()
    expect(screen.getByText("ChatGPT Plus")).toBeInTheDocument()
    expect(screen.getByText("(from token claim)")).toBeInTheDocument()
    expect(screen.getByText("acct_7f3a")).toBeInTheDocument()
    expect(screen.getByText("flat-rate · no per-call $")).toBeInTheDocument()
  })

  it("a metered key is billed per token and its plan is pay-as-you-go", () => {
    renderCards(seat({ mode: "api_key", plan: null, plan_label: null, delivery: { kind: "env", target: "OPENAI_API_KEY" } }), { accountId: null })
    expect(screen.getByText("pay-as-you-go")).toBeInTheDocument()
    expect(screen.getByText("metered · per token through the sidecar")).toBeInTheDocument()
    expect(screen.getByText("not reported")).toBeInTheDocument()
  })
})

describe("Quota", () => {
  it("draws both windows and says when they reset", () => {
    renderCards()
    expect(screen.getByRole("progressbar", { name: /5-hour window/i })).toHaveAttribute("aria-valuenow", "100")
    expect(screen.getByRole("progressbar", { name: /weekly window/i })).toHaveAttribute("aria-valuenow", "71")
    expect(screen.getByText(/At limit — resets \d{1,2}:\d{2}/)).toBeInTheDocument()
    expect(screen.getByText(/Wait for the reset or assign a different account/i)).toBeInTheDocument()
  })

  it("says 'not readable for this provider' when the server sent null — no bar, no number", () => {
    renderCards(seat({ quota: null }))
    expect(screen.getByText(/Usage limits are not reported/i)).toBeInTheDocument()
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument()
  })
})

describe("Validity & refresh", () => {
  it("shows the expiry, last and next refresh, and the refresh token as SEALED", () => {
    renderCards()
    expect(screen.getByText(/^in 2 d 4 h/)).toBeInTheDocument()
    expect(screen.getByText("8d ago")).toBeInTheDocument()
    expect(screen.getByText(/^in 1d$/)).toBeInTheDocument()
    expect(screen.getByTestId("refresh-token-sealed")).toHaveTextContent("SEALED")
    expect(screen.getByText("never leaves the server")).toBeInTheDocument()
    expect(screen.getByTestId("login-status-at_limit")).toBeInTheDocument()
  })

  it("a seat with no refresh flow says to paste a new token before the expiry", () => {
    const expires = new Date(Date.now() + 200 * DAY).toISOString()
    renderCards(seat({
      provider: "ANTHROPIC",
      refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null },
      expires_at: expires,
      quota: null,
      delivery: { kind: "env", target: "CLAUDE_CODE_OAUTH_TOKEN" },
    }))
    expect(screen.getByText(/no refresh flow — paste a new token before/i)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /refresh now/i })).not.toBeInTheDocument()
    expect(screen.queryByTestId("refresh-token-sealed")).not.toBeInTheDocument()
  })

  it("needs re-login: the error, the sentence, the red tone", () => {
    renderCards(seat({ refresh: { supported: true, status: "needs_relogin", last_at: null, next_at: null, error: "invalid_grant" } }))
    expect(screen.getByText("invalid_grant")).toBeInTheDocument()
    expect(screen.getByText(/Use Re-login to reconnect it; existing assignments stay unchanged/i)).toBeInTheDocument()
    expect(screen.getByTestId("login-status-needs_relogin")).toBeInTheDocument()
  })

  it("Refresh now posts to the refresh endpoint and hands the new login up", async () => {
    const next = login({ expires_at: new Date(Date.now() + 10 * DAY).toISOString(), quota: null })
    h.apiFetch.mockResolvedValueOnce(ok({ login: next }))
    const { onLoginChange } = renderCards()
    fireEvent.click(screen.getByRole("button", { name: /refresh now/i }))
    await waitFor(() => expect(onLoginChange).toHaveBeenCalledWith(next))
    const [url, init] = h.apiFetch.mock.calls[0]
    expect(String(url)).toBe("/api/v1/credentials/cred_1/refresh?workspace_id=ws1")
    expect((init as { method?: string }).method).toBe("POST")
    expect(h.toast.success).toHaveBeenCalled()
  })

  it("a 409 means one is already running — said, not errored", async () => {
    h.apiFetch.mockResolvedValueOnce(fail(409, { error: "refresh in progress" }))
    const { onLoginChange } = renderCards()
    fireEvent.click(screen.getByRole("button", { name: /refresh now/i }))
    await waitFor(() => expect(h.toast.info).toHaveBeenCalledWith(expect.stringMatching(/already running/i)))
    expect(onLoginChange).not.toHaveBeenCalled()
    expect(h.toast.error).not.toHaveBeenCalled()
  })

  it("Refresh now is not offered to a reader who cannot update", () => {
    renderCards(seat(), { canUpdate: false })
    expect(screen.queryByRole("button", { name: /refresh now/i })).not.toBeInTheDocument()
  })
})

describe("Delivered to the agent as", () => {
  it("a file: the path, 0600, and that model policy is not enforced", () => {
    renderCards()
    expect(screen.getByText(".codex/auth.json")).toBeInTheDocument()
    expect(screen.getByText(/file · 0600/)).toBeInTheDocument()
    expect(screen.getByText(/model policy is not enforced in subscription mode/i)).toBeInTheDocument()
  })

  it("an env var: the variable, and that the key is metered", () => {
    renderCards(seat({ mode: "api_key", delivery: { kind: "env", target: "OPENAI_API_KEY" } }))
    expect(screen.getByText("$OPENAI_API_KEY")).toBeInTheDocument()
    expect(screen.getByText(/the key is metered and model policy applies/i)).toBeInTheDocument()
  })
})

describe("actions", () => {
  function renderActions(credential = seat(), over: Partial<React.ComponentProps<typeof ProviderLoginActions>> = {}) {
    const fns = { onAssign: vi.fn(), onTest: vi.fn(), onRelogin: vi.fn(), onRevoke: vi.fn() }
    render(
      <ProviderLoginActions
        credential={credential}
        testable
        testing={false}
        canUpdate
        canBind
        canDelete
        {...fns}
        {...over}
      />,
    )
    return fns
  }

  it("Assign · Re-login · Revoke, and no Test for a subscription", () => {
    const fns = renderActions()
    fireEvent.click(screen.getByRole("button", { name: /assign to agent/i }))
    fireEvent.click(screen.getByRole("button", { name: /re-login/i }))
    fireEvent.click(screen.getByRole("button", { name: /revoke/i }))
    expect(fns.onAssign).toHaveBeenCalled()
    expect(fns.onRelogin).toHaveBeenCalled()
    expect(fns.onRevoke).toHaveBeenCalled()
    expect(screen.queryByRole("button", { name: /^test$/i })).not.toBeInTheDocument()
  })

  it("Test appears for a metered key the server can probe", () => {
    const fns = renderActions(seat({ mode: "api_key" }))
    fireEvent.click(screen.getByRole("button", { name: /^test$/i }))
    expect(fns.onTest).toHaveBeenCalled()
  })

  it("each verb follows its own gate", () => {
    renderActions(seat({ mode: "api_key" }), { canBind: false, canDelete: false, canUpdate: false })
    expect(screen.queryByRole("button", { name: /assign to agent/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /revoke/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /re-login/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /^test$/i })).not.toBeInTheDocument()
  })
})
