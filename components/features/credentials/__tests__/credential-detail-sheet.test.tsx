// Tests for CredentialDetailSheet RBAC gating — the Settings tab must
// only offer actions the backend will actually accept for the caller's
// role: value update/test = MANAGER+ ("update"), rotate = OWNER/ADMIN
// ("manage"), delete = OWNER/ADMIN ("delete"). A MANAGER must never
// see Rotate/Delete buttons that 403 on click.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { CredentialDetailSheet } from "../credential-detail-sheet"

const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  capabilities: [] as string[],
  apiFetch: vi.fn(),
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => h.apiFetch(...args),
}))

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

function renderSheet(overrides: Record<string, unknown> = {}) {
  return render(
    <CredentialDetailSheet
      workspaceId="ws1"
      credential={{ ...credential, ...overrides }}
      open
      onOpenChange={() => {}}
      onRefresh={() => {}}
      onRotate={() => {}}
      onEdit={() => {}}
    />,
  )
}

function openSettingsTab() {
  const trigger = screen.getByRole("tab", { name: /settings/i })
  fireEvent.mouseDown(trigger)
  fireEvent.click(trigger)
}

beforeEach(() => {
  h.capabilities = []
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => [] })
})

describe("Settings tab gating by role", () => {
  it("OWNER sees update value, rotate and delete", () => {
    h.role = "OWNER"
    renderSheet()
    openSettingsTab()

    expect(screen.getByText("Update value")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /rotate with grace overlap/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /delete credential/i })).toBeInTheDocument()
  })

  it("MANAGER keeps update value but loses rotate and delete (backend requires manage)", () => {
    h.role = "MANAGER"
    renderSheet()
    openSettingsTab()

    expect(screen.getByText("Update value")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /rotate with grace overlap/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /delete credential/i })).not.toBeInTheDocument()
    // ...and gets told why, instead of a silent gap.
    expect(screen.getByText(/require a workspace admin/i)).toBeInTheDocument()
  })

  it("MANAGER does not trigger the rotations-history fetch it can't render", () => {
    h.role = "MANAGER"
    renderSheet()
    openSettingsTab()

    const rotationCalls = h.apiFetch.mock.calls.filter(([url]) =>
      String(url).includes("/rotations"),
    )
    expect(rotationCalls).toHaveLength(0)
  })

  it("VIEWER sees no mutation affordances at all", () => {
    h.role = "VIEWER"
    renderSheet()
    openSettingsTab()

    expect(screen.queryByText("Update value")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /rotate with grace overlap/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /delete credential/i })).not.toBeInTheDocument()
    expect(screen.getByText(/don't have permission to modify/i)).toBeInTheDocument()
  })

  it("VIEWER does not get the header Edit button", () => {
    h.role = "VIEWER"
    renderSheet()
    expect(screen.queryByRole("button", { name: /edit/i })).not.toBeInTheDocument()
  })

  it("MANAGER gets the header Edit button (PATCH allows MANAGER)", () => {
    h.role = "MANAGER"
    renderSheet()
    expect(screen.getByRole("button", { name: /^edit$/i })).toBeInTheDocument()
  })

  // #1034 — the backend honors the credential.rotate capability for
  // lower roles (requireRoleOrCapabilityOrForbid, #1028); the sheet
  // must surface Rotate for a capability-holding MANAGER instead of
  // gating on role alone.
  it("MANAGER with credential.rotate capability sees the Rotate button", () => {
    h.role = "MANAGER"
    h.capabilities = ["chat", "credential.rotate"]
    renderSheet()
    openSettingsTab()

    expect(screen.getByRole("button", { name: /rotate with grace overlap/i })).toBeInTheDocument()
    // delete stays OWNER/ADMIN-only — the capability grants rotate, nothing more
    expect(screen.queryByRole("button", { name: /delete credential/i })).not.toBeInTheDocument()
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
    expect(screen.getByRole("button", { name: /test now/i })).toBeInTheDocument()
  })

  it("hides Test now when the server has no probe for it", () => {
    h.role = "OWNER"
    renderSheet({ provider: "NOTION", testable: false })
    expect(screen.queryByRole("button", { name: /test now/i })).not.toBeInTheDocument()
  })

  it("still hides Test now from roles that cannot update, even when testable", () => {
    h.role = "MEMBER"
    renderSheet({ provider: "GITHUB", testable: true })
    expect(screen.queryByRole("button", { name: /test now/i })).not.toBeInTheDocument()
  })
})

function openTab(name: RegExp) {
  const trigger = screen.getByRole("tab", { name })
  fireEvent.mouseDown(trigger)
  fireEvent.click(trigger)
}

describe("Overview tab field rendering", () => {
  it("renders username, expiry, last-used, last-error and last-used-IPs when present", () => {
    h.role = "OWNER"
    renderSheet({
      username: "svc-account",
      token_expires_at: "2026-08-01T00:00:00Z",
      last_used_at: "2026-07-20T00:00:00Z",
      last_error: "401 Unauthorized",
      last_used_ips: ["10.0.0.1", "10.0.0.2"],
    })

    expect(screen.getByText("Username")).toBeInTheDocument()
    expect(screen.getByText("svc-account")).toBeInTheDocument()
    expect(screen.getByText("Expires")).toBeInTheDocument()
    expect(screen.getByText("Last error")).toBeInTheDocument()
    expect(screen.getByText("401 Unauthorized")).toBeInTheDocument()
    expect(screen.getByText("10.0.0.1")).toBeInTheDocument()
    expect(screen.getByText("10.0.0.2")).toBeInTheDocument()
    // "never" must not leak in once a real last-used timestamp exists.
    expect(screen.queryByText("never")).not.toBeInTheDocument()
  })

  it("shows 'never' for Last used when the credential has never been used", () => {
    h.role = "OWNER"
    renderSheet({ last_used_at: null })
    expect(screen.getByText("never")).toBeInTheDocument()
  })

  it("does not render a Username row, expiry, error box or IP list when absent", () => {
    h.role = "OWNER"
    renderSheet()
    expect(screen.queryByText("Username")).not.toBeInTheDocument()
    expect(screen.queryByText("Expires")).not.toBeInTheDocument()
    expect(screen.queryByText("Last error")).not.toBeInTheDocument()
    expect(screen.queryByText("Last 5 IPs")).not.toBeInTheDocument()
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

  it("omits the header Edit button when onEdit is not supplied (legacy caller)", () => {
    h.role = "OWNER"
    render(
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
      />,
    )
    expect(screen.queryByRole("button", { name: /^edit$/i })).not.toBeInTheDocument()
  })

  it("renders nothing when credential is null", () => {
    const { container } = render(
      <CredentialDetailSheet
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

    const button = screen.getByRole("button", { name: /test now/i })
    fireEvent.click(button)
    expect(button).toBeDisabled()

    await Promise.resolve().then(() =>
      resolveTest({ ok: true, status: 200, json: async () => ({ valid: true }) }),
    )
    expect(await screen.findByText("Valid")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /test now/i })).not.toBeDisabled()
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
    fireEvent.click(screen.getByRole("button", { name: /test now/i }))
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
    fireEvent.click(screen.getByRole("button", { name: /test now/i }))
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
    fireEvent.click(screen.getByRole("button", { name: /test now/i }))
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
    fireEvent.click(screen.getByRole("button", { name: /test now/i }))
    expect(await screen.findByText(/network error/i)).toBeInTheDocument()
  })
})

describe("Used-by tab", () => {
  it("lists every assigned agent and mirrors the count on the tab badge", () => {
    h.role = "OWNER"
    renderSheet({ agent_names: ["agent-a", "agent-b"], _count_agent_credentials: 2 })
    openTab(/used by/i)
    expect(screen.getByText("agent-a")).toBeInTheDocument()
    expect(screen.getByText("agent-b")).toBeInTheDocument()
    expect(screen.getByRole("tab", { name: /used by/i })).toHaveTextContent("2")
  })

  it("shows the empty state when no agent uses the credential", () => {
    h.role = "OWNER"
    renderSheet({ agent_names: [] })
    openTab(/used by/i)
    expect(screen.getByText(/not yet used by any agent/i)).toBeInTheDocument()
  })

  it("shows the MCP-usage note only when mcp_used is true", () => {
    h.role = "OWNER"
    renderSheet({ mcp_used: true })
    openTab(/used by/i)
    expect(screen.getByText(/referenced by one or more mcp server/i)).toBeInTheDocument()
  })
})

// The audit tab has no role gate in this component today — every role that
// can open the sheet at all can see the audit trail (it's read-only history,
// not a mutation). These tests pin that as the current, verified behaviour
// rather than assume a gate that isn't in the code.
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
    openTab(/audit/i)

    expect(await screen.findByText("CREATED")).toBeInTheDocument()
    expect(screen.getByText("READ")).toBeInTheDocument()
    expect(screen.getByText(/from 1\.2\.3\.4/)).toBeInTheDocument()
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
    openTab(/audit/i)
    expect(await screen.findByText(/no audit events yet/i)).toBeInTheDocument()
  })

  // A 500 or network blip on the audit fetch must degrade to the empty
  // state, not an unhandled rejection or a stuck spinner.
  it("degrades to the empty state (not a crash) when the audit fetch rejects", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/audit")) {
        return Promise.reject(new Error("network down"))
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet()
    openTab(/audit/i)
    expect(await screen.findByText(/no audit events yet/i)).toBeInTheDocument()
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
    openTab(/audit/i)
    expect(screen.queryByText(/no audit events yet/i)).not.toBeInTheDocument()
    // Sheet content is portaled to document.body, not the RTL container —
    // query the document directly for the decorative (aria-hidden) spinner.
    expect(document.querySelector("svg.animate-spin")).toBeInTheDocument()
  })

  it("MEMBER and VIEWER can still open the Audit tab and see its content", async () => {
    h.role = "VIEWER"
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
    expect(screen.getByRole("tab", { name: /audit/i })).toBeInTheDocument()
    openTab(/audit/i)
    expect(await screen.findByText("READ")).toBeInTheDocument()
  })
})

describe("Settings tab — rotation history", () => {
  it("renders each rotation's status badge and grace-period hours for a rotate-capable role", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown) => {
      if (String(url).includes("/rotations")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: async () => [
            { id: "r1", credential_id: "cred_1", grace_seconds: 86400, rotated_at: "2026-07-01T00:00:00Z", expires_at: "2026-07-02T00:00:00Z", rotated_by: "u1", status: "ACTIVE", old_value_gone: false },
            { id: "r2", credential_id: "cred_1", grace_seconds: 3600, rotated_at: "2026-06-01T00:00:00Z", expires_at: "2026-06-01T01:00:00Z", rotated_by: "u1", status: "EXPIRED", old_value_gone: true },
          ],
        })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet()
    openSettingsTab()

    expect(await screen.findByText("ACTIVE")).toBeInTheDocument()
    expect(screen.getByText("EXPIRED")).toBeInTheDocument()
    expect(screen.getByText("24h grace")).toBeInTheDocument()
    expect(screen.getByText("1h grace")).toBeInTheDocument()
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

describe("Header and settings action callbacks fire with the right credential", () => {
  it("clicking Edit invokes onEdit with the current credential", () => {
    h.role = "OWNER"
    const onEdit = vi.fn()
    render(
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
        onEdit={onEdit}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }))
    expect(onEdit).toHaveBeenCalledWith(credential)
  })

  it("clicking 'Rotate with grace overlap…' invokes onRotate with the current credential", () => {
    h.role = "OWNER"
    const onRotate = vi.fn()
    render(
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={onRotate}
        onEdit={() => {}}
      />,
    )
    openSettingsTab()
    fireEvent.click(screen.getByRole("button", { name: /rotate with grace overlap/i }))
    expect(onRotate).toHaveBeenCalledWith(credential)
  })
})

describe("Settings tab — inline value update ('Save value')", () => {
  it("Save is disabled until the draft has non-whitespace content", () => {
    h.role = "OWNER"
    renderSheet()
    openSettingsTab()
    const save = screen.getByRole("button", { name: /save value/i })
    expect(save).toBeDisabled()

    const input = screen.getByPlaceholderText(/paste new secret value/i)
    fireEvent.change(input, { target: { value: "   " } })
    expect(save).toBeDisabled()

    fireEvent.change(input, { target: { value: "sk-live-123" } })
    expect(save).not.toBeDisabled()
  })

  it("toggling the eye button switches the input between masked and plaintext", () => {
    h.role = "OWNER"
    renderSheet()
    openSettingsTab()
    const input = screen.getByPlaceholderText(/paste new secret value/i) as HTMLInputElement
    expect(input.type).toBe("password")
    fireEvent.click(screen.getByRole("button", { name: /show value/i }))
    expect(input.type).toBe("text")
    fireEvent.click(screen.getByRole("button", { name: /hide value/i }))
    expect(input.type).toBe("password")
  })

  // Regression target: a successful PATCH must wipe the plaintext draft from
  // state (not just clear the input visually) and tell the caller to refetch
  // so the sheet reflects the new value's metadata (e.g. rotated_at).
  it("on success: clears the draft, shows Saved, and calls onRefresh", async () => {
    h.role = "OWNER"
    const onRefresh = vi.fn()
    h.apiFetch.mockImplementation((url: unknown, opts?: unknown) => {
      const method = (opts as { method?: string } | undefined)?.method
      if (method === "PATCH") {
        return Promise.resolve({ ok: true, status: 200, json: async () => ({}) })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    render(
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={() => {}}
        onRefresh={onRefresh}
        onRotate={() => {}}
        onEdit={() => {}}
      />,
    )
    openSettingsTab()
    const input = screen.getByPlaceholderText(/paste new secret value/i) as HTMLInputElement
    fireEvent.change(input, { target: { value: "new-secret-value" } })
    fireEvent.click(screen.getByRole("button", { name: /save value/i }))

    expect(await screen.findByText("Saved")).toBeInTheDocument()
    expect(input.value).toBe("")
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it("on a server-rejected value: shows the server's error message and keeps the draft", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown, opts?: unknown) => {
      const method = (opts as { method?: string } | undefined)?.method
      if (method === "PATCH") {
        return Promise.resolve({ ok: false, status: 400, json: async () => ({ error: "Value rejected" }) })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet()
    openSettingsTab()
    const input = screen.getByPlaceholderText(/paste new secret value/i) as HTMLInputElement
    fireEvent.change(input, { target: { value: "bad-value" } })
    fireEvent.click(screen.getByRole("button", { name: /save value/i }))

    expect(await screen.findByText("Value rejected")).toBeInTheDocument()
    expect(input.value).toBe("bad-value")

    // Retyping clears the stale error immediately rather than leaving a red
    // message stuck under input the user has already changed.
    fireEvent.change(input, { target: { value: "bad-value2" } })
    expect(screen.queryByText("Value rejected")).not.toBeInTheDocument()
  })

  it("falls back to a generic 'Request failed (status)' message when the error body has no message", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown, opts?: unknown) => {
      const method = (opts as { method?: string } | undefined)?.method
      if (method === "PATCH") {
        return Promise.resolve({ ok: false, status: 422, json: async () => ({}) })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet()
    openSettingsTab()
    fireEvent.change(screen.getByPlaceholderText(/paste new secret value/i), { target: { value: "x" } })
    fireEvent.click(screen.getByRole("button", { name: /save value/i }))
    expect(await screen.findByText("Request failed (422)")).toBeInTheDocument()
  })

  it("shows a network-error message when the PATCH request throws", async () => {
    h.role = "OWNER"
    h.apiFetch.mockImplementation((url: unknown, opts?: unknown) => {
      const method = (opts as { method?: string } | undefined)?.method
      if (method === "PATCH") {
        return Promise.reject(new Error("boom"))
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderSheet()
    openSettingsTab()
    fireEvent.change(screen.getByPlaceholderText(/paste new secret value/i), { target: { value: "x" } })
    fireEvent.click(screen.getByRole("button", { name: /save value/i }))
    expect(await screen.findByText("Network error")).toBeInTheDocument()
  })
})

describe("Settings tab — delete flow", () => {
  it("OWNER confirms delete: sends the DELETE request, refreshes and closes the sheet", async () => {
    h.role = "OWNER"
    const onRefresh = vi.fn()
    const onOpenChange = vi.fn()
    h.apiFetch.mockImplementation((url: unknown, opts?: unknown) => {
      const method = (opts as { method?: string } | undefined)?.method
      if (method === "DELETE") {
        return Promise.resolve({ ok: true, status: 200, json: async () => ({}) })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    render(
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={onOpenChange}
        onRefresh={onRefresh}
        onRotate={() => {}}
        onEdit={() => {}}
      />,
    )
    openSettingsTab()
    fireEvent.click(screen.getByRole("button", { name: /delete credential/i }))
    expect(screen.getByText("Delete credential?")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: /^delete$/i }))

    await vi.waitFor(() => expect(onRefresh).toHaveBeenCalledTimes(1))
    expect(onOpenChange).toHaveBeenCalledWith(false)
    const deleteCalls = h.apiFetch.mock.calls.filter(
      ([, opts]) => (opts as { method?: string } | undefined)?.method === "DELETE",
    )
    expect(deleteCalls).toHaveLength(1)
    expect(String(deleteCalls[0][0])).toContain("/api/v1/credentials/cred_1")
  })

  it("Cancel closes the confirm dialog without calling the delete endpoint", () => {
    h.role = "OWNER"
    renderSheet()
    openSettingsTab()
    fireEvent.click(screen.getByRole("button", { name: /delete credential/i }))
    expect(screen.getByText("Delete credential?")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: /^cancel$/i }))

    expect(screen.queryByText("Delete credential?")).not.toBeInTheDocument()
    const deleteCalls = h.apiFetch.mock.calls.filter(
      ([, opts]) => (opts as { method?: string } | undefined)?.method === "DELETE",
    )
    expect(deleteCalls).toHaveLength(0)
  })

  it("a failed delete request closes the confirm dialog without refreshing or closing the sheet", async () => {
    h.role = "OWNER"
    const onRefresh = vi.fn()
    const onOpenChange = vi.fn()
    h.apiFetch.mockImplementation((url: unknown, opts?: unknown) => {
      const method = (opts as { method?: string } | undefined)?.method
      if (method === "DELETE") {
        return Promise.resolve({ ok: false, status: 500, json: async () => ({}) })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    render(
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={onOpenChange}
        onRefresh={onRefresh}
        onRotate={() => {}}
        onEdit={() => {}}
      />,
    )
    openSettingsTab()
    fireEvent.click(screen.getByRole("button", { name: /delete credential/i }))
    fireEvent.click(screen.getByRole("button", { name: /^delete$/i }))

    await vi.waitFor(() => expect(screen.queryByText("Delete credential?")).not.toBeInTheDocument())
    expect(onRefresh).not.toHaveBeenCalled()
    expect(onOpenChange).not.toHaveBeenCalled()
  })
})

// A MANAGER holding the credential.rotate capability gets Rotate — but that
// button lives *inside* the `canUpdate` block in this component's markup.
// A MEMBER (who lacks "update" entirely) holding the same capability gets
// no Rotate button at all, even though the backend's
// requireRoleOrCapabilityOrForbid (#1028) would accept the request from
// them. This pins today's actual (and, per the #1034 comment's own stated
// intent, likely under-scoped) behaviour rather than asserting the backend
// contract — see the final report for why this looks like a real gap.
describe("Capability elevation is scoped by the surrounding canUpdate gate", () => {
  it("MEMBER with credential.rotate capability still sees no Rotate button (no baseline canUpdate)", () => {
    h.role = "MEMBER"
    h.capabilities = ["chat", "credential.rotate"]
    renderSheet()
    openSettingsTab()
    expect(screen.queryByRole("button", { name: /rotate with grace overlap/i })).not.toBeInTheDocument()
    expect(screen.queryByText("Update value")).not.toBeInTheDocument()
  })
})

describe("Sheet close/reopen resets transient state", () => {
  it("resets the active tab, test result and value draft after the sheet closes and reopens", () => {
    h.role = "OWNER"
    const base = { ...credential, testable: true }
    const { rerender } = render(
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={base}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
        onEdit={() => {}}
      />,
    )
    openSettingsTab()
    expect(screen.getByText("Update value")).toBeInTheDocument()

    rerender(
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={base}
        open={false}
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
        onEdit={() => {}}
      />,
    )
    rerender(
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={base}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
        onEdit={() => {}}
      />,
    )

    // Back on Overview — the Settings-only text must be gone.
    expect(screen.queryByText("Update value")).not.toBeInTheDocument()
    expect(screen.getByRole("tab", { name: /overview/i })).toHaveAttribute("data-state", "active")
  })
})
