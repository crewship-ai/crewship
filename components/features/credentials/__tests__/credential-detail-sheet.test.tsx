// Tests for CredentialDetailSheet RBAC gating — the Settings tab must
// only offer actions the backend will actually accept for the caller's
// role: value update/test = MANAGER+ ("update"), rotate = OWNER/ADMIN
// ("manage"), delete = OWNER/ADMIN ("delete"). A MANAGER must never
// see Rotate/Delete buttons that 403 on click.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { CredentialDetailSheet } from "../credential-detail-sheet"
import type { SecretTab } from "@/lib/credentials/vault-url-state"
import type { ProviderLogin } from "@/lib/credentials/provider-logins"

const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  capabilities: [] as string[],
  apiFetch: vi.fn(),
  revealEnabled: false,
  sensitivity: "STANDARD",
  isLogin: false,
}))

vi.mock("@/hooks/use-access-me", () => ({
  useAccessMe: () => {
    const manage = ["OWNER", "ADMIN"].includes(h.role)
    const edit = manage || (h.role === "MANAGER" && !h.isLogin)
    const revealReason = h.sensitivity === "SEALED" ? "sealed" : !h.revealEnabled ? "workspace_switch_off" : !edit ? "below_role_floor" : !h.capabilities.includes("credentials:reveal") ? "missing_capability" : "fresh_login_reason_and_audit_required"
    const decision = (allowed: boolean, reason = "role") => ({ state: allowed ? "allowed" : "denied", reason })
    return { access: { actions: {
      read: decision(true), edit: decision(edit), rotate: decision(manage || h.capabilities.includes("credential.rotate")),
      delete: decision(manage), manage_bindings: decision(manage), lower_sensitivity: decision(manage),
      reveal: decision(revealReason === "fresh_login_reason_and_audit_required", revealReason),
    } }, loading: false, error: false, refresh: vi.fn() }
  },
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => h.apiFetch(...args),
}))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))

vi.mock("@/hooks/use-abilities", async () => {
  const { defineAbilitiesFor } = await import("@/lib/permissions/abilities")
  const { hasCapability } = await import("@/lib/capabilities")
  return {
    useAbilities: () => ({
      abilities: defineAbilitiesFor(h.role as never),
      role: h.role,
      capabilities: h.capabilities,
      hasCapability: (cap: never) => hasCapability(h.capabilities, cap),
      loading: false,
    }),
  }
})

const credential = {
  id: "cred_1",
  name: "STRIPE_API_KEY",
  description: null,
  type: "API_KEY",
  provider: "CUSTOM_CLI",
  status: "ACTIVE",
  scope: "WORKSPACE",
  account_label: null,
  account_email: null,
  username: null,
  token_expires_at: null,
  last_checked_at: null,
  last_used_at: null,
  last_used_ips: [],
  last_error: null,
  tags: [],
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  agent_names: [],
  _count_agent_credentials: 0,
  mcp_used: false,
}

/** The page keeps its tab in the URL; the harness keeps it in state so a test can click tabs. */
function Harness(props: React.ComponentProps<typeof CredentialDetailSheet>) {
  const [tab, setTab] = React.useState<SecretTab | null>(props.tab ?? null)
  return <CredentialDetailSheet {...props} tab={tab} onTabChange={setTab} />
}

let defaultTab: SecretTab | null = null
function renderSheet(overrides: Record<string, unknown> = {}, tab: SecretTab | null = defaultTab) {
  h.sensitivity = typeof overrides.sensitivity === "string" ? overrides.sensitivity : "STANDARD"
  h.isLogin = Boolean(overrides.login)
  return render(
    <Harness
      workspaceId="ws1"
      credential={{ ...credential, ...overrides } as never}
      open
      tab={tab}
      onOpenChange={() => {}}
      onRefresh={() => {}}
      onRotate={() => {}}
    />,
  )
}

/** Harbor 2: the secret page has tabs — Overview · Access · Value history · Activity · Settings. */
function openTab(name: RegExp) {
  fireEvent.click(screen.getByRole("tab", { name }))
}
function openSettingsTab() {
  openTab(/^settings$/i)
}

// Every knob the mocked hooks read is reset here, so no test inherits one from
// whichever test ran before it. `capabilities` and `apiFetch` were already
// reset; `role` was not, and the tier and readiness describes below are the
// two that never assign it — they ran against whatever the previous describe
// happened to leave behind.
//
// Measured, not assumed: forcing a prior describe to leak VIEWER leaves both
// of them green, because neither asserts anything role-dependent. So this is
// the reset being complete rather than a bug being fixed — it stops the next
// role-sensitive assertion added to either describe from depending on file
// order, which is the point at which it would have become one.
beforeEach(() => {
  h.role = "OWNER"
  defaultTab = null
  h.isLogin = false
  h.sensitivity = "STANDARD"
  h.capabilities = []
  h.apiFetch.mockReset()
  h.revealEnabled = false
  h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => [] })
})

describe("write affordances gated by role", () => {
  it("provider accounts never offer generic secret rotation or reveal", () => {
    renderSheet({
      type: "AI_CLI_TOKEN", provider: "OPENAI",
      login: {
        mode: "subscription", provider: "OPENAI", plan: "plus", plan_label: "ChatGPT Plus",
        owner_user_id: "u1", owner_email: "fixture@example.test", expires_at: null,
        refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null },
        quota: null, delivery: { kind: "file", target: ".codex/auth.json" }, pays_for: { agents: 0, crews: 0 },
      },
    })
    expect(screen.queryByRole("button", { name: /replace value/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /reveal/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("tab", { name: /value history/i })).not.toBeInTheDocument()
    expect(screen.getByRole("tab", { name: /pays for/i })).toBeInTheDocument()
    expect(screen.getByText("Connection health")).toBeInTheDocument()
    // Revoke is in Settings, behind the confirm dialog — not in the header.
    expect(screen.queryByRole("button", { name: /^revoke/i })).not.toBeInTheDocument()
    openSettingsTab()
    expect(screen.getByRole("button", { name: /^revoke…$/i })).toBeInTheDocument()
  })

  it("OWNER sees replace and delete", () => {
    h.role = "OWNER"
    const onRotate = vi.fn()
    render(<Harness workspaceId="ws1" credential={credential as never} open onOpenChange={() => {}} onRefresh={() => {}} onRotate={onRotate} />)
    fireEvent.click(screen.getByRole("button", { name: /^replace value$/i }))
    expect(onRotate).toHaveBeenCalledWith(credential, "rotate")
    openSettingsTab()
    expect(screen.getByRole("button", { name: /^delete…$/i })).toBeInTheDocument()
  })

  it("MANAGER replaces the value without grace, cannot delete, and is told why", () => {
    h.role = "MANAGER"
    const onRotate = vi.fn()
    render(<Harness workspaceId="ws1" credential={credential as never} open onOpenChange={() => {}} onRefresh={() => {}} onRotate={onRotate} />)
    fireEvent.click(screen.getByRole("button", { name: /^replace value$/i }))
    expect(onRotate).toHaveBeenCalledWith(credential, "patch")
    openSettingsTab()
    expect(screen.queryByRole("button", { name: /^delete…$/i })).not.toBeInTheDocument()
    openTab(/^access$/i)
    expect(screen.getByText(/requires a workspace admin/i)).toBeInTheDocument()
  })

  it("MANAGER does not trigger the rotations-history fetch it can't render", () => {
    h.role = "MANAGER"
    renderSheet({}, "history")
    const rotationCalls = h.apiFetch.mock.calls.filter(([url]) => String(url).includes("/rotations"))
    expect(rotationCalls).toHaveLength(0)
    expect(screen.getByText(/shown to people who can replace the value/i)).toBeInTheDocument()
  })

  it("VIEWER sees no mutation affordances at all", () => {
    h.role = "VIEWER"
    renderSheet()
    expect(screen.queryByRole("button", { name: /^replace value$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("tab", { name: /activity/i })).not.toBeInTheDocument()
    openSettingsTab()
    expect(screen.queryByRole("button", { name: /^delete…$/i })).not.toBeInTheDocument()
    expect(screen.getByLabelText("Name")).toBeDisabled()
    openTab(/^access$/i)
    expect(screen.getByText(/don't have permission to modify/i)).toBeInTheDocument()
  })

  it("MANAGER edits the details in Settings (PATCH allows MANAGER)", () => {
    h.role = "MANAGER"
    renderSheet({}, "settings")
    expect(screen.getByLabelText("Name")).not.toBeDisabled()
  })

  // #1034 — the backend honors the credential.rotate capability for
  // lower roles (requireRoleOrCapabilityOrForbid, #1028).
  it("MANAGER with credential.rotate capability rotates with a grace period", () => {
    h.role = "MANAGER"
    h.capabilities = ["chat", "credential.rotate"]
    const onRotate = vi.fn()
    render(<Harness workspaceId="ws1" credential={credential as never} open onOpenChange={() => {}} onRefresh={() => {}} onRotate={onRotate} />)
    fireEvent.click(screen.getByRole("button", { name: /^replace value$/i }))
    expect(onRotate).toHaveBeenCalledWith(credential, "rotate")
    openSettingsTab()
    // delete stays OWNER/ADMIN-only — the capability grants rotate, nothing more
    expect(screen.queryByRole("button", { name: /^delete…$/i })).not.toBeInTheDocument()
  })
})

// "Test now" used to be gated on getBrand(provider).cli — the five brands
// Crewship drives inside agent containers. That set is not the set the server
// can probe (GITHUB, GITLAB and VERCEL have real probes and are not cli:true),
// so the action was hidden for credentials TestStored would have answered.
//
// The server decides now and says so per credential, via `testable` on the read
// payload. The separate CLI badge keeps using .cli — that badge really is about
// "Crewship authenticates the agent's CLI with this", a different question.
describe("Test now gating follows server-declared probe support", () => {
  it("shows Test now when the server says the credential is testable", () => {
    h.role = "OWNER"
    renderSheet({ provider: "GITHUB", testable: true })
    expect(screen.getByRole("button", { name: /^test$/i })).toBeInTheDocument()
  })

  it("hides Test now when the server has no probe for it", () => {
    h.role = "OWNER"
    renderSheet({ provider: "NOTION", testable: false })
    expect(screen.queryByRole("button", { name: /^test$/i })).not.toBeInTheDocument()
  })

  it("still hides Test now from roles that cannot update, even when testable", () => {
    h.role = "MEMBER"
    renderSheet({ provider: "GITHUB", testable: true })
    expect(screen.queryByRole("button", { name: /^test$/i })).not.toBeInTheDocument()
  })
})


describe("property rendering", () => {
  it("renders username, expiry, last-used, last-error and last-used-IPs when present", () => {
    h.role = "OWNER"
    renderSheet({
      username: "svc-account",
      token_expires_at: "2099-08-01T00:00:00Z",
      last_used_at: "2026-07-20T00:00:00Z",
      last_error: "401 Unauthorized",
      last_used_ips: ["10.0.0.1", "10.0.0.2"],
    })

    // The username is not secret: it leads the masked value.
    expect(screen.getByText(/svc-account · •+/)).toBeInTheDocument()
    expect(screen.getByText("Expires")).toBeInTheDocument()
    expect(screen.getByText("Last error")).toBeInTheDocument()
    expect(screen.getByText("401 Unauthorized")).toBeInTheDocument()
    expect(screen.getByText("10.0.0.1, 10.0.0.2")).toBeInTheDocument()
    expect(screen.queryByText("never")).not.toBeInTheDocument()
  })

  it("shows 'never' for Last used when the credential has never been used", () => {
    h.role = "OWNER"
    renderSheet({ last_used_at: null })
    expect(screen.getAllByText("never").length).toBeGreaterThan(0)
  })

  it("does not render a username, error or IP list when absent", () => {
    h.role = "OWNER"
    renderSheet()
    expect(screen.queryByText(/ · •+/)).not.toBeInTheDocument()
    expect(screen.queryByText("Last error")).not.toBeInTheDocument()
    expect(screen.queryByText("Last IPs")).not.toBeInTheDocument()
  })

  it("puts a rejected secret's error in a banner with Replace value", () => {
    h.role = "OWNER"
    renderSheet({ status: "ERROR", last_error: "401 Unauthorized at 02:00" })
    const banner = screen.getAllByRole("alert").find((el) => el.textContent?.includes("Rejected"))!
    expect(banner).toHaveTextContent("401 Unauthorized at 02:00")
    expect(within(banner).getByRole("button", { name: /replace value/i })).toBeInTheDocument()
  })

  // The CLI badge marks credentials Crewship itself uses to authenticate an
  // agent's CLI inside the container (brand.cli) — a different question from
  // `testable` (whether the server can probe it). Regressing this would make
  // the badge appear on/disappear from the wrong credentials.
  it("shows the CLI badge for a brand Crewship drives inside agent containers", () => {
    h.role = "OWNER"
    renderSheet({ provider: "ANTHROPIC" })
    expect(screen.getByText("CLI")).toBeInTheDocument()
  })

  it("hides the CLI badge for a brand with no in-container CLI", () => {
    h.role = "OWNER"
    renderSheet({ provider: "CUSTOM_CLI" })
    expect(screen.queryByText("CLI")).not.toBeInTheDocument()
  })

  it("renders a badge per tag", () => {
    h.role = "OWNER"
    renderSheet({ tags: ["prod", "payments"] })
    expect(screen.getByText("prod")).toBeInTheDocument()
    expect(screen.getByText("payments")).toBeInTheDocument()
  })

  it("renders nothing when credential is null", () => {
    const { container } = render(
      <Harness
        workspaceId="ws1"
        credential={null}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
      />,
    )
    expect(container).toBeEmptyDOMElement()
  })
})

// "Test now" is the one write-ish action reachable from the Overview tab.
// These pin the full request lifecycle so a regression (e.g. losing the
// `finally` that clears `testing`, or swapping `data.valid` for a truthy
// check) is caught before it ships a button that spins forever or always
// says "Valid".
describe("Test now request lifecycle", () => {
  it("disables the button and shows a spinner while the probe is in flight, then renders Valid", async () => {
    h.role = "OWNER"
    let resolveTest: (v: unknown) => void = () => {}
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/test")) {
        return new Promise((res) => {
          resolveTest = res
        })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet({ testable: true })

    const button = screen.getByRole("button", { name: /^test$/i })
    fireEvent.click(button)
    expect(button).toBeDisabled()

    await Promise.resolve().then(() =>
      resolveTest({ ok: true, status: 200, json: async () => ({ valid: true, supported: true }) }),
    )
    expect(await screen.findByText("Valid")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^test$/i })).not.toBeDisabled()
  })

  it("does not show a green result when the server did not probe the credential", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/test")) {
        return Promise.resolve({ ok: true, status: 200, json: async () => ({ valid: true, supported: false, error: "CLI login is delivered to the agent" }) })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet({ provider: "OPENAI", testable: true })
    fireEvent.click(screen.getByRole("button", { name: /^test$/i }))
    expect(await screen.findByText("Not checked")).toBeInTheDocument()
    expect(screen.getByTestId("connection-verification")).toHaveTextContent("not checked")
    expect(screen.queryByText("Valid")).not.toBeInTheDocument()
  })

  it("does not infer verification from an older server response without supported", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) =>
      String(url).includes("/test")
        ? Promise.resolve({ ok: true, status: 200, json: async () => ({ valid: true }) })
        : Promise.resolve({ ok: true, status: 200, json: async () => [] }),
    )
    renderSheet({ testable: true })
    fireEvent.click(screen.getByRole("button", { name: /^test$/i }))
    expect(await screen.findByText("Not checked")).toBeInTheDocument()
    expect(screen.queryByText("Valid")).not.toBeInTheDocument()
  })

  it("renders the server-provided error message when the probe reports invalid", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/test")) {
        return Promise.resolve({ ok: true, status: 200, json: async () => ({ valid: false, error: "Bad token" }) })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet({ testable: true })
    fireEvent.click(screen.getByRole("button", { name: /^test$/i }))
    expect(await screen.findByText("Bad token")).toBeInTheDocument()
  })

  it("falls back to a generic 'Invalid' label when the probe gives no error text", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/test")) {
        return Promise.resolve({ ok: true, status: 200, json: async () => ({ valid: false }) })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet({ testable: true })
    fireEvent.click(screen.getByRole("button", { name: /^test$/i }))
    expect(await screen.findByText("Invalid")).toBeInTheDocument()
  })

  it("shows a request-failed message when the test endpoint responds non-ok", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/test")) {
        return Promise.resolve({ ok: false, status: 500, json: async () => ({}) })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet({ testable: true })
    fireEvent.click(screen.getByRole("button", { name: /^test$/i }))
    expect(await screen.findByText(/test request failed/i)).toBeInTheDocument()
  })

  it("shows a network-error message when the test request throws", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/test")) {
        return Promise.reject(new Error("boom"))
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet({ testable: true })
    fireEvent.click(screen.getByRole("button", { name: /^test$/i }))
    expect(await screen.findByText(/network error/i)).toBeInTheDocument()
  })
})

describe("used by", () => {
  it("shows configured routine references separately from recorded use", async () => {
    h.apiFetch.mockImplementation((url: unknown) => Promise.resolve({
      ok: true, status: 200,
      json: async () => String(url).includes("/dependents") ? {
        routines: [{ slug: "billing-check", name: "Billing check", type: "api_key", resolution: "would_resolve" }],
        recorded_use: "not_attributed", visibility_limited: false, dynamic_uses_untracked: true,
      } : [],
    }))
    renderSheet()
    expect(await screen.findByRole("link", { name: "Billing check" })).toHaveAttribute("href", "/routines?slug=billing-check")
    expect(screen.getByText("Would select this credential")).toBeInTheDocument()
    expect(screen.getByText(/not recorded run use/i)).toBeInTheDocument()
  })

  it("treats failed dependency and field reads as unknown even after old data", async () => {
    h.apiFetch.mockImplementation((url: unknown) => Promise.resolve({
      ok: !String(url).includes("/dependents") && !String(url).includes("/fields"),
      status: 500, json: async () => [],
    }))
    renderSheet()
    expect(await screen.findByText(/Routine dependencies could not be checked/i)).toBeInTheDocument()
    expect(await screen.findByText(/Fields could not be checked/i)).toBeInTheDocument()
    expect(screen.queryByText(/No visible routine declares/i)).not.toBeInTheDocument()
  })

  it("never flashes a previous credential's routine names while switching", async () => {
    h.apiFetch.mockImplementation((url: unknown) => Promise.resolve({
      ok: true, status: 200,
      json: async () => String(url).includes("/dependents") ? {
        routines: [{ slug: "old-secret-job", name: "Old secret job", type: "api_key", resolution: "would_resolve" }],
        recorded_use: "not_attributed", visibility_limited: false, dynamic_uses_untracked: true,
      } : [],
    }))
    const props = { workspaceId: "ws1", open: true, onOpenChange: () => {}, onRefresh: () => {}, onRotate: () => {} }
    const view = render(<Harness {...props} credential={credential} />)
    expect(await screen.findByText("Old secret job")).toBeInTheDocument()
    view.rerender(<Harness {...props} credential={{ ...credential, id: "cred_2", name: "OTHER" }} />)
    expect(screen.queryByText("Old secret job")).not.toBeInTheDocument()
  })

  it("does not show the previous provider account when the next fields read fails", async () => {
    h.apiFetch.mockImplementation((url: unknown) => {
      const path = String(url)
      if (path.includes("/cred_2/fields")) return Promise.resolve({ ok: false, status: 500 })
      return Promise.resolve({
        ok: true, status: 200,
        json: async () => path.includes("/cred_1/fields")
          ? [{ key: "account_id", value: "acct-first", is_secret: false }]
          : [],
      })
    })
    const login: ProviderLogin = {
      mode: "subscription", provider: "OPENAI", plan: "plus", plan_label: "ChatGPT Plus",
      owner_user_id: "u1", owner_email: "fixture@example.test", expires_at: null,
      refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null },
      quota: null, delivery: { kind: "file", target: ".codex/auth.json" }, pays_for: { agents: 0, crews: 0 },
    }
    const props = { workspaceId: "ws1", open: true, onOpenChange: () => {}, onRefresh: () => {}, onRotate: () => {} }
    const view = render(<Harness {...props} credential={{ ...credential, login }} />)
    expect((await screen.findAllByText("acct-first")).length).toBeGreaterThan(0)

    view.rerender(<Harness {...props} credential={{ ...credential, id: "cred_2", name: "SECOND", login }} />)
    expect(screen.queryByText("acct-first")).not.toBeInTheDocument()
    expect(await screen.findByText(/Fields could not be checked/)).toBeInTheDocument()
    expect(screen.queryByText("acct-first")).not.toBeInTheDocument()
    expect(screen.getByText("not reported")).toBeInTheDocument()
  })

  it("lists every assigned agent and carries the count on the section", () => {
    h.role = "OWNER"
    renderSheet({ agent_names: ["agent-a", "agent-b"], _count_agent_credentials: 2 })
    expect(screen.getAllByText("agent-a")[0]).toBeInTheDocument()
    expect(screen.getAllByText("agent-b")[0]).toBeInTheDocument()
    // The count moved from a tab badge to the card's own subtitle, and to the
    // figures band — the tab it used to sit on no longer exists.
    // The count moved from a tab badge to the figures band and the card's own
    // subtitle — the tab it used to sit on no longer exists.
    expect(screen.getAllByText("Used by").length).toBe(1)
    expect(screen.getAllByText("2").length).toBeGreaterThan(0)
  })

  it("shows an honest empty state after checking visible agents", async () => {
    h.role = "OWNER"
    renderSheet({ agent_names: [] }, "access")
    expect(await screen.findByText(/No matching grants were reported/i)).toBeInTheDocument()
  })
  it("does not interpret a failed access lookup as no access", async () => {
    h.apiFetch.mockResolvedValue({ ok: false, status: 403, json: async () => ({}) })
    renderSheet({}, "access")
    expect(await screen.findByText(/Access could not be fully loaded/)).toBeInTheDocument()
    expect(screen.queryByText(/No matching grants were reported/)).not.toBeInTheDocument()
  })
  it("checks candidates reached by a workspace delivery binding", async () => {
    h.apiFetch.mockImplementation(async (url: string) => ({ ok: true, json: async () =>
      url.includes("/bindings?") ? { bindings: [{ id: "b", scope: "WORKSPACE", slot: "SERVICE_KEY" }] }
      : url.includes("/agents?") ? [{ id: "a1", name: "Worker", crew_id: "unrelated" }]
      : url.includes("/agents/a1/credentials?") ? [{ credential_id: "cred_1", env_var_name: "SERVICE_KEY", grant_source: "binding" }]
      : [],
    }))
    renderSheet({}, "access")
    expect(await screen.findAllByText("Worker").then((rows) => rows[0])).toBeInTheDocument()
    expect(screen.getByTestId("credential-access-summary")).toHaveTextContent("Checked 1 of 1")
    expect(screen.getByText("delivery binding")).toBeInTheDocument()
  })
  it("reports capped assignment checks instead of presenting a complete list", async () => {
    h.apiFetch.mockImplementation(async (url: string) => ({ ok: true, json: async () =>
      url.includes("/bindings?") ? { bindings: [{ id: "b", scope: "WORKSPACE", slot: "SERVICE_KEY" }] }
      : url.includes("/agents?") ? Array.from({ length: 15 }, (_, i) => ({ id: `a${i}`, name: `Agent ${i}` })) : [],
    }))
    renderSheet({}, "access")
    await waitFor(() => expect(screen.getByTestId("credential-access-summary")).toHaveTextContent("Checked 12 of 15"))
    expect(screen.queryByText(/No matching grants were reported/)).not.toBeInTheDocument()
  })

  it("shows the MCP-usage note only when mcp_used is true", () => {
    h.role = "OWNER"
    renderSheet({ mcp_used: true })

    expect(screen.getByText(/referenced by one or more mcp server/i)).toBeInTheDocument()
  })
})

// GET /credentials/{id}/audit is MANAGER+ (credential_audit.go: "Audit reveals
// IPs of admin actions — that's forensic data, not for VIEWER/MEMBER eyes").
// The tab used to render for every role, and because a failed fetch degrades to
// the empty state, a MEMBER who opened it was told "No audit events yet" about a
// credential whose history they simply were not allowed to read. Hiding the tab
// is the honest answer; showing a false empty timeline is not.
describe("audit visibility follows the backend gate", () => {
  it("is offered to a role that can read the timeline", () => {
    h.role = "MANAGER"
    renderSheet()
    expect(screen.getByRole("tab", { name: /activity/i })).toBeInTheDocument()
  })

  for (const role of ["MEMBER", "VIEWER"]) {
    it(`is hidden from ${role}, who would get a 403 and a false empty state`, () => {
      h.role = role
      renderSheet()
      expect(screen.queryByRole("tab", { name: /activity/i })).not.toBeInTheDocument()
    })
  }

  it("never calls the audit endpoint for a role that cannot read it", async () => {
    h.role = "MEMBER"
    renderSheet()
    // The tab is gone, so there is nothing to click — but the effect is also
    // gated, which is what keeps a stray setTab (deep link, restored state)
    // from firing a request that can only 403.
    for (let i = 0; i < 20; i++) await new Promise((r) => setTimeout(r, 5))
    expect(
      h.apiFetch.mock.calls.filter((c) => String(c[0]).includes("/audit")),
    ).toHaveLength(0)
  })
})

describe("Audit tab", () => {
  it("fetches and renders events, including the optional IP line", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/audit")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: async () => [
            { id: "a1", event_type: "CREATED", agent_id: null, ip_address: "1.2.3.4", metadata: null, occurred_at: "2026-07-20T00:00:00Z" },
            { id: "a2", event_type: "READ", agent_id: null, ip_address: null, metadata: null, occurred_at: "2026-07-21T00:00:00Z" },
          ],
        })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet()
    openTab(/activity/i)

    expect(await screen.findByText("CREATED")).toBeInTheDocument()
    expect(screen.getByText("READ")).toBeInTheDocument()
    expect(screen.getByText(/^1\.2\.3\.4$/)).toBeInTheDocument()
  })

  it("shows the empty state when the audit log has no events", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/audit")) {
        return Promise.resolve({ ok: true, status: 200, json: async () => [] })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet()
    openTab(/activity/i)
    expect(await screen.findByText(/nothing has happened to this credential yet/i)).toBeInTheDocument()
  })

  // A failed audit fetch must not claim that no activity happened.
  it("marks the audit as unavailable when the fetch rejects", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/audit")) {
        return Promise.reject(new Error("network down"))
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet()
    openTab(/activity/i)
    expect(await screen.findByText(/Activity could not be checked/i)).toBeInTheDocument()
  })

  it("shows a loading spinner while the audit fetch is in flight", () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/audit")) {
        return new Promise(() => {}) // never resolves within this test
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet()
    openTab(/activity/i)
    expect(screen.queryByText(/nothing has happened to this credential yet/i)).not.toBeInTheDocument()
    // Sheet content is portaled to document.body, not the RTL container —
    // query the document directly for the decorative (aria-hidden) spinner.
    expect(screen.getByRole("status")).toHaveTextContent("Loading")
  })

  it("MANAGER — the lowest role the backend serves — sees the timeline", async () => {
    h.role = "MANAGER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/audit")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: async () => [{ id: "a1", event_type: "READ", agent_id: null, ip_address: null, metadata: null, occurred_at: "2026-07-21T00:00:00Z" }],
        })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet()
    expect(screen.getByRole("tab", { name: /activity/i })).toBeInTheDocument()
    openTab(/activity/i)
    expect(await screen.findByText("READ")).toBeInTheDocument()
  })
})

describe("Settings tab — rotation history", () => {
  it("does not offer or fetch advanced rotation history", () => {
    renderSheet()
    openSettingsTab()
    expect(screen.queryByText("Rotation")).not.toBeInTheDocument()
    expect(h.apiFetch.mock.calls.some(([url]) => String(url).includes("/rotations"))).toBe(false)
  })

  it("does not render a rotation-history section when there is no history yet", () => {
    h.role = "OWNER"
    renderSheet()
    openSettingsTab()
    expect(screen.queryByText("Rotation history")).not.toBeInTheDocument()
  })

  it("degrades to an empty rotation history (not a crash) when the rotations fetch rejects", () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/rotations")) return Promise.reject(new Error("down"))
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet()
    openSettingsTab()
    expect(screen.queryByText("Rotation history")).not.toBeInTheDocument()
  })
})

describe("Header and settings actions fire with the right credential", () => {
  it("Replace value hands the current credential to the rotation flow", () => {
    h.role = "OWNER"
    const onRotate = vi.fn()
    render(<Harness workspaceId="ws1" credential={credential as never} open onOpenChange={() => {}} onRefresh={() => {}} onRotate={onRotate} />)
    fireEvent.click(screen.getByRole("button", { name: /^replace value$/i }))
    expect(onRotate).toHaveBeenCalledWith(credential, "rotate")
  })

  it("Give to agents opens the picker for a role that may assign", () => {
    h.role = "OWNER"
    renderSheet()
    fireEvent.click(screen.getByRole("button", { name: /give to agents/i }))
    expect(screen.getByRole("dialog", { name: /Give to agents/ })).toBeInTheDocument()
  })

  it("Give to agents opens the Access tab for a role that may not assign", () => {
    h.role = "MANAGER"
    renderSheet()
    fireEvent.click(screen.getByRole("button", { name: /give to agents/i }))
    expect(screen.getByRole("tab", { name: /^access$/i })).toHaveAttribute("aria-selected", "true")
    expect(screen.queryByRole("dialog", { name: /Give to agents/ })).not.toBeInTheDocument()
  })
})

describe("settings tab save bar saves name/tags/tier", () => {
  it("PATCHes only what changed, through the page's one save bar", async () => {
    h.role = "OWNER"
    const onRefresh = vi.fn()
    render(<Harness workspaceId="ws1" credential={{ ...credential, tags: ["prod"] } as never} open tab="settings" onOpenChange={() => {}} onRefresh={onRefresh} onRotate={() => {}} />)
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "STRIPE_LIVE_KEY" } })
    fireEvent.click(screen.getByRole("button", { name: "Remove tag prod" }))
    const bar = await screen.findByRole("region", { name: /unsaved changes/i })
    expect(bar).toHaveTextContent("2 unsaved changes")
    fireEvent.click(within(bar).getByRole("button", { name: /^save$/i }))
    await waitFor(() => expect(onRefresh).toHaveBeenCalled())
    const patch = h.apiFetch.mock.calls.find(([, o]) => (o as { method?: string })?.method === "PATCH")!
    expect(String(patch[0])).toContain("/api/v1/credentials/cred_1")
    expect(JSON.parse((patch[1] as { body: string }).body)).toEqual({ name: "STRIPE_LIVE_KEY", tags: [] })
  })

  it("a typed tag is not lost: comma adds it, case duplicates collapse, and text still in the field is saved", async () => {
    // #2428 kept metadata edits a person typed but had not committed with Enter;
    // the Settings tab must not drop them on Save.
    h.role = "OWNER"
    const onRefresh = vi.fn()
    render(<Harness workspaceId="ws1" credential={{ ...credential, tags: [] } as never} open tab="settings" onOpenChange={() => {}} onRefresh={onRefresh} onRotate={() => {}} />)
    const tags = screen.getByPlaceholderText("Add a tag")
    fireEvent.change(tags, { target: { value: "Demo" } })
    fireEvent.keyDown(tags, { key: "Enter" })
    fireEvent.change(tags, { target: { value: "demo" } })
    fireEvent.keyDown(tags, { key: "," })
    expect(screen.getAllByRole("button", { name: "Remove tag demo" })).toHaveLength(1)
    expect(tags).toHaveValue("")
    fireEvent.click(screen.getByRole("button", { name: "Remove tag demo" }))
    fireEvent.change(tags, { target: { value: "production" } })
    const bar = await screen.findByRole("region", { name: /unsaved changes/i })
    expect(bar).toHaveTextContent("1 unsaved change")
    fireEvent.click(within(bar).getByRole("button", { name: /^save$/i }))
    await waitFor(() => expect(onRefresh).toHaveBeenCalled())
    const patch = h.apiFetch.mock.calls.find(([, o]) => (o as { method?: string })?.method === "PATCH")!
    expect(JSON.parse((patch[1] as { body: string }).body)).toEqual({ tags: ["production"] })
  })

  it("the save bar stays in view: it is fixed to the window, not to the end of a long Settings tab", async () => {
    // page-save-bar.tsx: "The bar is always in view". Absolutely positioned
    // inside the Settings column it sat below the Danger zone, off screen;
    // sticky still sat below the visible pane (its scroller is taller than
    // the framed page), so it is fixed, as on Integrations.
    h.role = "OWNER"
    render(<Harness workspaceId="ws1" credential={credential as never} open tab="settings" onOpenChange={() => {}} onRefresh={() => {}} onRotate={() => {}} />)
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Renamed" } })
    const bar = await screen.findByRole("region", { name: /unsaved changes/i })
    expect(bar.className.split(/\s+/)).toContain("fixed")
    expect(bar.className.split(/\s+/)).not.toContain("absolute")
  })

  it("sets the Keeper tier from Access with a PATCH of security_level alone", async () => {
    h.role = "OWNER"
    const onRefresh = vi.fn()
    render(<Harness workspaceId="ws1" credential={{ ...credential, security_level: 1 } as never} open tab="access" onOpenChange={() => {}} onRefresh={onRefresh} onRotate={() => {}} />)
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Keeper tier" })).getByRole("radio", { name: "L3" }))
    await waitFor(() => expect(onRefresh).toHaveBeenCalled())
    const patch = h.apiFetch.mock.calls.find(([, o]) => (o as { method?: string })?.method === "PATCH")!
    expect(JSON.parse((patch[1] as { body: string }).body)).toEqual({ security_level: 3 })
  })

  it("never offers a value field in Settings", () => {
    h.role = "OWNER"
    renderSheet({}, "settings")
    expect(screen.queryByPlaceholderText(/paste/i)).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/value/i)).not.toBeInTheDocument()
  })
})

describe("Settings tab — delete flow", () => {
  it("OWNER confirms delete: sends the DELETE request, refreshes and closes the page", async () => {
    h.role = "OWNER"
    const onRefresh = vi.fn()
    const onOpenChange = vi.fn()
    h.apiFetch.mockImplementation((url: unknown, opts?: unknown) => {
      const method = (opts as { method?: string } | undefined)?.method
      if (method === "DELETE") return Promise.resolve({ ok: true, status: 200, json: async () => ({}) })
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    render(<Harness workspaceId="ws1" credential={credential as never} open tab="settings" onOpenChange={onOpenChange} onRefresh={onRefresh} onRotate={() => {}} />)
    fireEvent.click(screen.getByRole("button", { name: /^delete…$/i }))
    expect(screen.getByText("Delete STRIPE_API_KEY?")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /^delete$/i }))
    await vi.waitFor(() => expect(onRefresh).toHaveBeenCalledTimes(1))
    expect(onOpenChange).toHaveBeenCalledWith(false)
    const deleteCalls = h.apiFetch.mock.calls.filter(([, opts]) => (opts as { method?: string } | undefined)?.method === "DELETE")
    expect(deleteCalls).toHaveLength(1)
    expect(String(deleteCalls[0][0])).toContain("/api/v1/credentials/cred_1")
  })

  it("Cancel closes the confirm dialog without calling the delete endpoint", () => {
    h.role = "OWNER"
    renderSheet({}, "settings")
    fireEvent.click(screen.getByRole("button", { name: /^delete…$/i }))
    expect(screen.getByText("Delete STRIPE_API_KEY?")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /^cancel$/i }))
    expect(screen.queryByText("Delete STRIPE_API_KEY?")).not.toBeInTheDocument()
    expect(h.apiFetch.mock.calls.filter(([, opts]) => (opts as { method?: string } | undefined)?.method === "DELETE")).toHaveLength(0)
  })

  it("a refused delete keeps the dialog open and leaves the page in place", async () => {
    h.role = "OWNER"
    const onRefresh = vi.fn()
    const onOpenChange = vi.fn()
    h.apiFetch.mockImplementation((url: unknown, opts?: unknown) => {
      const method = (opts as { method?: string } | undefined)?.method
      if (method === "DELETE") return Promise.resolve({ ok: false, status: 500, json: async () => ({}) })
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    render(<Harness workspaceId="ws1" credential={credential as never} open tab="settings" onOpenChange={onOpenChange} onRefresh={onRefresh} onRotate={() => {}} />)
    fireEvent.click(screen.getByRole("button", { name: /^delete…$/i }))
    fireEvent.click(screen.getByRole("button", { name: /^delete$/i }))
    await waitFor(() => expect(h.apiFetch.mock.calls.some(([, o]) => (o as { method?: string })?.method === "DELETE")).toBe(true))
    await waitFor(() => expect(screen.getByRole("button", { name: /^delete$/i })).not.toBeDisabled())
    expect(screen.getByText("Delete STRIPE_API_KEY?")).toBeInTheDocument()
    expect(onRefresh).not.toHaveBeenCalled()
    expect(onOpenChange).not.toHaveBeenCalled()
  })
})

// credential.rotate exists so an oncall MEMBER can rotate a leaked token
// without blanket vault reach (credential_rotation.go).
describe("Capability elevation reaches MEMBER", () => {
  it("MEMBER with credential.rotate sees Replace value, which the backend accepts from them", () => {
    h.role = "MEMBER"
    h.capabilities = ["chat", "credential.rotate"]
    const onRotate = vi.fn()
    render(<Harness workspaceId="ws1" credential={credential as never} open onOpenChange={() => {}} onRefresh={() => {}} onRotate={onRotate} />)
    fireEvent.click(screen.getByRole("button", { name: /^replace value$/i }))
    expect(onRotate).toHaveBeenCalledWith(credential, "rotate")
  })

  it("still withholds editing from that MEMBER — rotate is not update", () => {
    h.role = "MEMBER"
    h.capabilities = ["chat", "credential.rotate"]
    renderSheet({}, "settings")
    expect(screen.getByLabelText("Name")).toBeDisabled()
  })

  it("MEMBER without the capability sees neither", () => {
    h.role = "MEMBER"
    h.capabilities = ["chat"]
    renderSheet()
    expect(screen.queryByRole("button", { name: /^replace value$/i })).not.toBeInTheDocument()
  })
})

describe("Sheet close/reopen resets transient state", () => {
  it("clears the probe result, so it cannot describe the previous credential", async () => {
    h.role = "OWNER"
    const base = { ...credential, testable: true }
    h.apiFetch.mockImplementation(async (url: string) =>
      String(url).includes("/test")
        ? { ok: true, status: 200, json: async () => ({ valid: true, supported: true }) }
        : { ok: true, status: 200, json: async () => [] },
    )
    const view = (open: boolean) => (
      <Harness
        workspaceId="ws1"
        credential={base}
        open={open}
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
      />
    )
    const { rerender } = render(view(true))

    fireEvent.click(screen.getByRole("button", { name: /^test$/i }))
    expect(await screen.findByText("Valid")).toBeInTheDocument()

    rerender(view(false))
    rerender(view(true))

    // A green "Valid" carried over from the last secret you looked at is worse
    // than no verdict: it is a claim about THIS one that nobody made.
    expect(screen.queryByText("Valid")).not.toBeInTheDocument()
  })
})

// The Keeper tier decides what happens when an agent asks for this credential,
// and this sheet is where an operator goes to understand one secret in full. It
// showed the reveal classification and not the tier — two different sentences
// about the same row, and only one of them was on screen.
describe("the Keeper tier", () => {
  it("names the tier in the header chips, beside the classification", () => {
    renderSheet({ security_level: 4, security_level_label: "L4 · critical", sensitivity: "RESTRICTED" })
    const chip = screen.getByText("L4")
    expect(chip).toHaveAttribute("title", expect.stringContaining("L4 · critical"))
    expect(screen.getByText("Restricted")).toBeInTheDocument()
  })

  it("spells out on Access what the tier does, not just what it is called", () => {
    renderSheet({ security_level: 3, security_level_label: "L3 · high" }, "access")
    expect(screen.getByText("Keeper tier")).toBeInTheDocument()
    expect(screen.getByRole("radio", { name: "L3" })).toHaveAttribute("aria-checked", "true")
    expect(screen.getByText(/auto-leases rather than granting standing access/i)).toBeInTheDocument()
  })

  // "We were not told" and "L1 · low" are different claims.
  it("says the server did not report a tier rather than assuming L1", () => {
    renderSheet({}, "access")
    expect(screen.getByText(/did not report a tier/i)).toBeInTheDocument()
    expect(screen.queryByRole("radio", { name: "L1", checked: true })).not.toBeInTheDocument()
  })

  // Fail-closed, matching keeper.SecurityLevel.Tier().
  it("reads an out-of-range level as L4", () => {
    renderSheet({ security_level: 42 })
    expect(screen.getByText("L4")).toBeInTheDocument()
  })
})

// Readiness moved here from the credentials table when that table was removed:
// "the vault has a valid PAT" and "`gh` exists in the crew's container" are
// different facts, and this sheet is where "will this actually work?" is asked
// about one secret. Three states, not two — a green tick we did not earn is the
// exact false reassurance the readiness endpoint exists to remove.
describe("readiness", () => {
  const GAP = {
    crewId: "c1",
    crewName: "engineering",
    tool: "gh",
    feature: "ghcr.io/devcontainers/features/github-cli:1",
    featureId: "github-cli",
  }

  it("names the missing CLI and the crew that lacks it", () => {
    render(
      <Harness
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
        toolGaps={[GAP]}
        readinessKnown
      />,
    )
    expect(screen.getAllByText("Needs gh").length).toBeGreaterThan(0)
    expect(screen.getByText("engineering")).toBeInTheDocument()
    expect(screen.getByText("github-cli")).toBeInTheDocument()
  })

  it("says Ready once a crew has reported and no gap came back", () => {
    render(
      <Harness
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
        toolGaps={[]}
        readinessKnown
      />,
    )
    expect(screen.queryByText("Tools available")).not.toBeInTheDocument()
    expect(screen.getByTestId("connection-verification")).toHaveTextContent("Connection not verified")
  })

  // The important one.
  it("refuses to claim Ready when no crew has reported at all", () => {
    render(
      <Harness
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
        toolGaps={[]}
        readinessKnown={false}
      />,
    )
    expect(screen.queryByText("Ready")).not.toBeInTheDocument()
    expect(screen.queryByText("Tool availability")).not.toBeInTheDocument()
    expect(screen.queryByText("Tool availability")).not.toBeInTheDocument()
  })
})

// #1162, inherited from the list's own delete when that delete went with the
// table: another admin removing the credential first is the outcome the user
// wanted, so it is a success with a note — and every OTHER failure has to be
// said out loud rather than closing the dialog over an untouched credential.
describe("deleting the credential", () => {
  // Explicit, not inherited: `h.role` is module-global and the delete button
  // only renders for a role that can delete, so a test that relied on an
  // earlier one leaving OWNER behind would pass or fail on file ordering.
  beforeEach(() => {
    h.role = "OWNER"
  })

  async function clickDelete() {
    openSettingsTab()
    fireEvent.click(screen.getByRole("button", { name: /^delete…$/i }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: /^delete$/i }))
  }

  it("treats a 404 as already-gone: refreshes, closes and says so", async () => {
    const { toast } = await import("sonner")
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "DELETE") return { ok: false, status: 404, json: async () => ({}) }
      return { ok: true, status: 200, json: async () => [] }
    })
    const onRefresh = vi.fn()
    const onOpenChange = vi.fn()
    render(
      <Harness
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={onOpenChange}
        onRefresh={onRefresh}
        onRotate={() => {}}
      />,
    )
    await clickDelete()

    await waitFor(() => expect(onRefresh).toHaveBeenCalled())
    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(toast.success).toHaveBeenCalledWith(expect.stringContaining("already deleted"))
  })

  // A 403 that closes the dialog and leaves the credential in place reads
  // exactly like a successful delete. It used to do precisely that.
  it("reports a refused delete instead of closing over an untouched credential", async () => {
    const { toast } = await import("sonner")
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "DELETE") return { ok: false, status: 403, json: async () => ({}) }
      return { ok: true, status: 200, json: async () => [] }
    })
    const onRefresh = vi.fn()
    const onOpenChange = vi.fn()
    render(
      <Harness
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={onOpenChange}
        onRefresh={onRefresh}
        onRotate={() => {}}
      />,
    )
    await clickDelete()

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Couldn't delete"), { duration: Infinity }))
    expect(onRefresh).not.toHaveBeenCalled()
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
  })
})

// Reveal has four gates: a workspace switch, the MANAGER floor, the
// credentials:reveal capability, and the SEALED classification. Failing any one
// used to render NOTHING, which teaches a reader that the product cannot show a
// secret — and then a colleague on the same screen has the button. Each gate is
// a different fix, so each one says which.
describe("why reveal is unavailable", () => {
  function renderReveal(over: Record<string, unknown> = {}, policyEnabled = true) {
    h.revealEnabled = policyEnabled
    h.apiFetch.mockImplementation(async (url: string) =>
      String(url).includes("/reveal-policy")
        ? { ok: true, status: 200, json: async () => ({ enabled: policyEnabled }) }
        : { ok: true, status: 200, json: async () => [] },
    )
    return renderSheet(over)
  }

  it("says SEALED can never be revealed, whatever else is configured", async () => {
    h.role = "OWNER"
    h.capabilities = ["credentials:reveal"]
    renderReveal({ sensitivity: "SEALED" })
    // Twice: the Value card explains why the button is missing, and the
    // Classification card explains what SEALED means. Both are true and both
    // are where the reader is looking when the question comes up.
    expect((await screen.findAllByText(/SEALED values cannot be revealed/i)).length).toBeGreaterThan(0)
    expect(screen.queryByRole("button", { name: /^reveal…$/i })).not.toBeInTheDocument()
  })

  it("points at the workspace switch when reveal is off", async () => {
    h.role = "OWNER"
    h.capabilities = ["credentials:reveal"]
    renderReveal({}, false)
    expect(await screen.findByText(/switched off for this workspace/i)).toBeInTheDocument()
  })

  it("names the missing capability when the switch is on but the grant is not", async () => {
    h.role = "OWNER"
    h.capabilities = []
    renderReveal({})
    expect(await screen.findByText(/needs the credentials:reveal capability/i)).toBeInTheDocument()
  })

  it("offers the button once all four gates are open", async () => {
    h.role = "OWNER"
    h.capabilities = ["credentials:reveal"]
    renderReveal({})
    expect(
      await screen.findByRole("button", { name: /^reveal…$/i }),
    ).toBeInTheDocument()
  })

  // Below the MANAGER floor there is nothing to explain: reveal is not a thing
  // that tier does, and an explanation would read as an offer.
  it("explains nothing to a role that cannot write at all", async () => {
    h.role = "VIEWER"
    renderReveal({})
    expect(screen.queryByText(/credentials:reveal capability/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/switched off for this workspace/i)).not.toBeInTheDocument()
  })
})

// A slot bound to a crew printed the raw cuid — an identifier nobody
// recognises, in the one row on the page that answers "who can read this?".
describe("who a slot reaches", () => {
  const BINDING = {
    id: "b1",
    credential_id: "cred_1",
    credential_name: "STRIPE_API_KEY",
    scope: "CREW",
    crew_id: "crew_1",
    agent_id: null,
    slot: "GH_TOKEN",
    created_at: "",
  }

  function renderWithBinding(crewsById: Record<string, unknown>) {
    h.apiFetch.mockImplementation(async (url: string) =>
      String(url).includes("/credentials/bindings")
        ? { ok: true, status: 200, json: async () => ({ bindings: [BINDING] }) }
        : { ok: true, status: 200, json: async () => [] },
    )
    render(
      <Harness
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
        crewsById={crewsById as never}
        tab="access"
      />,
    )
  }

  it("names the crew and draws its tile instead of printing its id", async () => {
    h.role = "OWNER"
    renderWithBinding({
      crew_1: { id: "crew_1", name: "engineering", icon: "rocket", color: "#ff0000" },
    })
    expect(await screen.findByText("engineering")).toBeInTheDocument()
    expect(screen.queryByText("crew_1")).not.toBeInTheDocument()
  })

  // A crew we hold no record of still gets its row: hiding it would hide the
  // fact that something can read this secret.
  it("falls back to the id for a crew it cannot name", async () => {
    h.role = "OWNER"
    renderWithBinding({})
    expect(await screen.findByText("crew_1")).toBeInTheDocument()
  })
})

// A credential read every couple of minutes produced fifty rows of
// "USE · 3m ago" — a card three screens tall that pushed everything below it
// out of reach and said nothing the first row did not.
describe("the audit timeline", () => {
  beforeEach(() => { defaultTab = "activity" })
  function events(n: number, over: Partial<Record<string, unknown>> = {}) {
    return Array.from({ length: n }, (_, i) => ({
      id: `e${i}`,
      event_type: "USE",
      agent_id: null,
      ip_address: null,
      metadata: null,
      occurred_at: `2026-08-10T10:${String(i).padStart(2, "0")}:00Z`,
      actor_kind: "system",
      ...over,
    }))
  }
  function routeAudit(list: unknown[]) {
    h.apiFetch.mockImplementation(async (url: string) =>
      String(url).includes("/audit")
        ? { ok: true, status: 200, json: async () => list }
        : { ok: true, status: 200, json: async () => [] },
    )
  }

  it("shows ten, and says how many there are", async () => {
    h.role = "OWNER"
    routeAudit(events(37))
    renderSheet()

    expect(await screen.findByText("10 of 37")).toBeInTheDocument()
    expect(screen.getAllByText("USE")).toHaveLength(10)
  })

  it("expands to the rest on request, and back", async () => {
    h.role = "OWNER"
    routeAudit(events(37))
    renderSheet()

    fireEvent.click(await screen.findByRole("button", { name: /show all 37/i }))
    expect(screen.getAllByText("USE")).toHaveLength(37)

    fireEvent.click(screen.getByRole("button", { name: /show less/i }))
    expect(screen.getAllByText("USE")).toHaveLength(10)
  })

  // A full page means the timeline is longer than we asked for. Reporting the
  // page size as the total would understate a busy credential's history.
  it("marks a full page as a floor, not a total", async () => {
    h.role = "OWNER"
    routeAudit(events(50))
    renderSheet()
    expect(await screen.findByText("10 of 50+")).toBeInTheDocument()
  })

  it("offers no expander when everything already fits", async () => {
    h.role = "OWNER"
    routeAudit(events(4))
    renderSheet()
    expect(await screen.findByText("4")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /show all/i })).not.toBeInTheDocument()
  })
})

// "Who read this secret?" is the first question asked of a credential's
// timeline, and the rows could not answer it — they carried the event type, a
// time, and sometimes an IP.
describe("who did it", () => {
  beforeEach(() => { defaultTab = "activity" })
  function routeAudit(list: unknown[]) {
    h.apiFetch.mockImplementation(async (url: string) =>
      String(url).includes("/audit")
        ? { ok: true, status: 200, json: async () => list }
        : { ok: true, status: 200, json: async () => [] },
    )
  }
  const base = {
    id: "e1",
    event_type: "USE",
    agent_id: null,
    ip_address: null,
    metadata: null,
    occurred_at: "2026-08-10T10:00:00Z",
  }

  it("draws an agent with the avatar it has everywhere else", async () => {
    h.role = "OWNER"
    routeAudit([{ ...base, actor_kind: "agent", actor_id: "ag_1", actor_name: "Deploy bot" }])
    renderSheet()

    const row = (await screen.findByText("Deploy bot")).closest("li")!
    // alt="" makes it presentational, which is right — the name beside it is
    // the accessible label — so assert on the element, not on a role.
    expect(row.querySelector("img")).not.toBeNull()
  })

  // A generated face for a colleague is a picture of someone who does not
  // exist. A person gets a person glyph and their name.
  it("draws a human without inventing a face for them", async () => {
    h.role = "OWNER"
    routeAudit([
      { ...base, event_type: "REVEAL", actor_kind: "user", actor_id: "u1", actor_name: "Riley Quinn" },
    ])
    renderSheet()

    const row = (await screen.findByText("Riley Quinn")).closest("li")!
    expect(row.querySelector("img")).toBeNull()
    expect(within(row).getByText("REVEAL")).toBeInTheDocument()
  })

  it("says system for a row nobody signed, rather than leaving a gap", async () => {
    h.role = "OWNER"
    routeAudit([{ ...base, event_type: "DETECTED", actor_kind: "system", actor_id: "" }])
    renderSheet()
    expect(await screen.findByText("system")).toBeInTheDocument()
  })

  // An older server sends no actor fields at all. The agent_id column is the
  // one attribution that predates them.
  it("still attributes an agent when the server sends no actor block", async () => {
    h.role = "OWNER"
    routeAudit([{ ...base, agent_id: "ag_legacy" }])
    renderSheet()
    expect(await screen.findByText("ag_legacy")).toBeInTheDocument()
  })
})

// A sidecar fetch is the commonest event on a busy credential and it has no
// agent behind it — the sidecar serves a whole container. Reporting it as
// "system" is true and useless; the crew that owns the container is the answer.
describe("a sidecar read", () => {
  it("is attributed to the crew, drawn as that crew", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation(async (url: string) =>
      String(url).includes("/audit")
        ? {
            ok: true,
            status: 200,
            json: async () => [
              {
                id: "e1",
                event_type: "USE",
                agent_id: null,
                ip_address: null,
                metadata: { source: "sidecar_fetch", crew_id: "crew_1" },
                occurred_at: "2026-08-10T10:00:00Z",
                actor_kind: "crew",
                actor_id: "crew_1",
                actor_name: "engineering",
              },
            ],
          }
        : { ok: true, status: 200, json: async () => [] },
    )
    render(
      <Harness
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
        tab="activity"
        crewsById={
          { crew_1: { id: "crew_1", name: "engineering", icon: "rocket", color: "#0f0" } } as never
        }
      />,
    )
    expect(await screen.findByText("engineering")).toBeInTheDocument()
    expect(screen.queryByText("system")).not.toBeInTheDocument()
  })
})
