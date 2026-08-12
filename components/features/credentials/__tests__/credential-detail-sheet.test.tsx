// Tests for CredentialDetailSheet RBAC gating — the Settings tab must
// only offer actions the backend will actually accept for the caller's
// role: value update/test = MANAGER+ ("update"), rotate = OWNER/ADMIN
// ("manage"), delete = OWNER/ADMIN ("delete"). A MANAGER must never
// see Rotate/Delete buttons that 403 on click.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { CredentialDetailSheet } from "../credential-detail-sheet"

const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  capabilities: [] as string[],
  apiFetch: vi.fn(),
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

/**
 * The sheet had five tabs and now has none — every section is on the page at
 * once, in the /issues shape. These stay as no-ops rather than being deleted
 * from ~40 call sites: what each test asserts is unchanged, and the name still
 * marks WHICH section it is about.
 */
function openSettingsTab() {
  /* no tabs: the section is already rendered */
}

beforeEach(() => {
  h.capabilities = []
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => [] })
})

describe("write affordances gated by role", () => {
  it("OWNER sees rotate and delete", () => {
    h.role = "OWNER"
    renderSheet()

    expect(screen.getByRole("button", { name: /rotate with grace overlap/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /delete credential/i })).toBeInTheDocument()
  })

  it("MANAGER loses rotate and delete (backend requires manage), and is told why", () => {
    h.role = "MANAGER"
    renderSheet()

    expect(screen.queryByRole("button", { name: /rotate with grace overlap/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /delete credential/i })).not.toBeInTheDocument()
    // ...instead of a silent gap.
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
    // Two now: the header action and the Value card's pointer to it, which
    // exists because replacing a value no longer has its own control here.
    expect(screen.getAllByRole("button", { name: /^edit$/i })).toHaveLength(2)
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

function openTab(_name: RegExp) {
  /* no tabs: the section is already rendered — see openSettingsTab */
}

describe("property rendering", () => {
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
    // Twice on purpose: once in the identity line under the name, once as a
    // property. The header says what this credential IS; the row is where you
    // read it off.
    expect(screen.getAllByText("svc-account").length).toBeGreaterThan(0)
    // Twice: the figures band and the property row.
    expect(screen.getAllByText("Expires").length).toBeGreaterThan(0)
    expect(screen.getByText("Last error")).toBeInTheDocument()
    expect(screen.getByText("401 Unauthorized")).toBeInTheDocument()
    expect(screen.getByText("10.0.0.1")).toBeInTheDocument()
    expect(screen.getByText("10.0.0.2")).toBeInTheDocument()
    // "never" must not leak in once a real last-used timestamp exists —
    // neither in the figures band nor in the properties.
    expect(screen.queryByText("never")).not.toBeInTheDocument()
  })

  it("shows 'never' for Last used when the credential has never been used", () => {
    h.role = "OWNER"
    renderSheet({ last_used_at: null })
    // The figures band and the property row both say it.
    expect(screen.getAllByText("never").length).toBeGreaterThan(0)
  })

  it("does not render a Username row, expiry row, error card or IP list when absent", () => {
    h.role = "OWNER"
    renderSheet()
    const props = within(screen.getByText("Properties").closest("[class*='rounded-xl']")!)
    expect(props.queryByText("Username")).not.toBeInTheDocument()
    // The figures band keeps a fixed set of slots and prints "—" for an
    // expiry that does not exist, the way the issue band prints "—" for no due
    // date. What must not appear is a PROPERTY row asserting one.
    expect(props.queryByText("Expires")).not.toBeInTheDocument()
    expect(screen.queryByText("Last error")).not.toBeInTheDocument()
    expect(screen.queryByText(/^Last \d+ IPs$/)).not.toBeInTheDocument()
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

describe("used by", () => {
  it("lists every assigned agent and carries the count on the section", () => {
    h.role = "OWNER"
    renderSheet({ agent_names: ["agent-a", "agent-b"], _count_agent_credentials: 2 })
    expect(screen.getByText("agent-a")).toBeInTheDocument()
    expect(screen.getByText("agent-b")).toBeInTheDocument()
    // The count moved from a tab badge to the card's own subtitle, and to the
    // figures band — the tab it used to sit on no longer exists.
    // The count moved from a tab badge to the figures band and the card's own
    // subtitle — the tab it used to sit on no longer exists.
    expect(screen.getAllByText("Used by").length).toBe(2)
    expect(screen.getAllByText("2").length).toBeGreaterThan(0)
  })

  it("shows the empty state when no agent uses the credential", () => {
    h.role = "OWNER"
    renderSheet({ agent_names: [] })
    openTab(/used by/i)
    expect(screen.getByText(/no agent holds this credential yet/i)).toBeInTheDocument()
  })

  it("shows the MCP-usage note only when mcp_used is true", () => {
    h.role = "OWNER"
    renderSheet({ mcp_used: true })
    openTab(/used by/i)
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
    expect(screen.getByText("Audit")).toBeInTheDocument()
  })

  for (const role of ["MEMBER", "VIEWER"]) {
    it(`is hidden from ${role}, who would get a 403 and a false empty state`, () => {
      h.role = role
      renderSheet()
      expect(screen.queryByText("Audit")).not.toBeInTheDocument()
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
    openTab(/audit/i)

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
    openTab(/audit/i)
    expect(await screen.findByText(/nothing has happened to this credential yet/i)).toBeInTheDocument()
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
    expect(await screen.findByText(/nothing has happened to this credential yet/i)).toBeInTheDocument()
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
    expect(screen.queryByText(/nothing has happened to this credential yet/i)).not.toBeInTheDocument()
    // Sheet content is portaled to document.body, not the RTL container —
    // query the document directly for the decorative (aria-hidden) spinner.
    expect(document.querySelector("svg.animate-spin")).toBeInTheDocument()
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
    expect(screen.getByText("Audit")).toBeInTheDocument()
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
    // The header action; the Value card's pointer is covered separately.
    fireEvent.click(screen.getAllByRole("button", { name: /^edit$/i })[0])
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

// The inline "Save value" editor is gone.
//
// One credential could have its secret replaced in three places on one screen:
// rotate, this input, and the Value field in the Edit dialog — and two of them
// issued the same PATCH. Rotate stays because it is a different operation (the
// old value keeps working through a grace window). The plain swap belongs where
// every other property of the credential is changed, and the form's own tests
// cover it (credential-form.test.tsx).
describe("changing the value", () => {
  it("offers no second value editor beside Rotate", () => {
    h.role = "OWNER"
    renderSheet()
    expect(screen.queryByPlaceholderText(/paste new secret value/i)).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /save value/i })).not.toBeInTheDocument()
  })

  // …but it must not become a dead end: a reader looking for "replace this
  // secret" has to be told where it went.
  it("points at Edit, and that link opens Edit with this credential", () => {
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
    expect(
      screen.getByText(/leave the field empty there to keep the existing one/i),
    ).toBeInTheDocument()
    fireEvent.click(screen.getAllByRole("button", { name: /^Edit$/ })[1])
    expect(onEdit).toHaveBeenCalledWith(expect.objectContaining({ id: credential.id }))
  })

  it("keeps Rotate, which is not the same operation", () => {
    h.role = "OWNER"
    renderSheet()
    expect(
      screen.getByRole("button", { name: /rotate and show the new value/i }),
    ).toBeInTheDocument()
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
// credential.rotate exists so an oncall MEMBER can rotate a leaked token
// without blanket vault reach — credential_rotation.go grants exactly that
// ("a MANAGER/MEMBER holding an explicit credential.rotate capability also
// passes"). The button used to live inside the `canUpdate &&` block, which a
// MEMBER never satisfies, so the dashboard hid the action from the one tier
// the grant was written for and told them they had no permission at all.
describe("Capability elevation reaches MEMBER", () => {
  it("MEMBER with credential.rotate sees Rotate, which the backend accepts from them", () => {
    h.role = "MEMBER"
    h.capabilities = ["chat", "credential.rotate"]
    renderSheet()
    openSettingsTab()
    expect(screen.getByRole("button", { name: /rotate with grace overlap/i })).toBeInTheDocument()
  })

  it("still withholds the value-rewrite flow from that MEMBER — rotate is not update", () => {
    h.role = "MEMBER"
    h.capabilities = ["chat", "credential.rotate"]
    renderSheet()
    openSettingsTab()
    // PATCH is MANAGER+ with no capability escape hatch, so surfacing the
    // inline rewrite here would be the mirror-image bug: a button that 403s.
    expect(screen.queryByText("Replace the value")).not.toBeInTheDocument()
  })

  it("MEMBER without the capability sees neither", () => {
    h.role = "MEMBER"
    h.capabilities = ["chat"]
    renderSheet()
    openSettingsTab()
    expect(screen.queryByRole("button", { name: /rotate with grace overlap/i })).not.toBeInTheDocument()
  })
})

describe("Sheet close/reopen resets transient state", () => {
  it("clears the probe result, so it cannot describe the previous credential", async () => {
    h.role = "OWNER"
    const base = { ...credential, testable: true }
    h.apiFetch.mockImplementation(async (url: string) =>
      String(url).includes("/test")
        ? { ok: true, status: 200, json: async () => ({ valid: true }) }
        : { ok: true, status: 200, json: async () => [] },
    )
    const view = (open: boolean) => (
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={base}
        open={open}
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
        onEdit={() => {}}
      />
    )
    const { rerender } = render(view(true))

    fireEvent.click(screen.getByRole("button", { name: /test now/i }))
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
  it("names the tier in the identity chips, beside the classification", () => {
    renderSheet({ security_level: 4, security_level_label: "L4 · critical" })
    // Two places, on purpose: a chip in the identity card so it is visible
    // without scrolling, and the card that explains what it costs.
    expect(screen.getAllByText("L4 · critical")).toHaveLength(2)
  })

  it("spells out on Overview what the tier does, not just what it is called", () => {
    renderSheet({ security_level: 3, security_level_label: "L3 · high" })
    expect(screen.getByText("Keeper tier")).toBeInTheDocument()
    expect(screen.getAllByText("L3 · high").length).toBeGreaterThan(0)
    // The blast radius the tier is FOR, and the consequence it imposes.
    expect(screen.getByText(/Admin access to real infrastructure/i)).toBeInTheDocument()
    expect(screen.getByText(/auto-leases rather than granting standing access/i)).toBeInTheDocument()
  })

  // "We were not told" and "L1 · low" are different claims, and defaulting to
  // the second would put a reassuring badge on a row nobody has classified.
  it("says the server did not report a tier rather than assuming L1", () => {
    renderSheet()
    expect(screen.getByText(/did not report a tier/i)).toBeInTheDocument()
    expect(screen.queryByText("L1 · low")).not.toBeInTheDocument()
  })

  // Fail-closed, matching keeper.SecurityLevel.Tier(): a stored level the table
  // does not define is guarded as critical, so the sheet must say critical.
  it("reads an out-of-range level as L4", () => {
    renderSheet({ security_level: 42 })
    expect(screen.getAllByText("L4 · critical").length).toBeGreaterThan(0)
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
      <CredentialDetailSheet
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
      <CredentialDetailSheet
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
    expect(screen.getAllByText("Ready").length).toBeGreaterThan(0)
  })

  // The important one.
  it("refuses to claim Ready when no crew has reported at all", () => {
    render(
      <CredentialDetailSheet
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
    expect(screen.getAllByText("Readiness unknown").length).toBeGreaterThan(0)
    expect(screen.getByText(/no crew has reported its tool inventory yet/i)).toBeInTheDocument()
  })
})

// #1162, inherited from the list's own delete when that delete went with the
// table: another admin removing the credential first is the outcome the user
// wanted, so it is a success with a note — and every OTHER failure has to be
// said out loud rather than closing the dialog over an untouched credential.
describe("deleting the credential", () => {
  async function clickDelete() {
    openSettingsTab()
    fireEvent.click(screen.getByRole("button", { name: /delete credential/i }))
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
      <CredentialDetailSheet
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
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={onOpenChange}
        onRefresh={onRefresh}
        onRotate={() => {}}
      />,
    )
    await clickDelete()

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Couldn't delete")))
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
    expect((await screen.findAllByText(/SEALED can never be revealed/i)).length).toBe(2)
    expect(screen.queryByRole("button", { name: /reveal the existing value/i })).not.toBeInTheDocument()
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
      await screen.findByRole("button", { name: /reveal the existing value/i }),
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
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
        crewsById={crewsById as never}
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
      <CredentialDetailSheet
        workspaceId="ws1"
        credential={credential}
        open
        onOpenChange={() => {}}
        onRefresh={() => {}}
        onRotate={() => {}}
        crewsById={
          { crew_1: { id: "crew_1", name: "engineering", icon: "rocket", color: "#0f0" } } as never
        }
      />,
    )
    expect(await screen.findByText("engineering")).toBeInTheDocument()
    expect(screen.queryByText("system")).not.toBeInTheDocument()
  })
})
