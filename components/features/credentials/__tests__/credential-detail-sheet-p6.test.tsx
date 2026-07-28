// P6 additions to the credential detail sheet: the Fields tab, slot/assignment
// provenance on Used by, and — the one that matters — the ordering and gating
// of the two ways to get at a value.
//
// §2.6 L8 makes "Rotate and show the new value" the PRIMARY action and reveal
// the secondary one. That is a security decision, not a layout preference: it
// is what keeps the number of reveals low enough that each one is worth
// investigating. A refactor that promotes reveal (or demotes rotate) should
// fail here.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CredentialDetailSheet } from "../credential-detail-sheet"

const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  capabilities: [] as string[],
  apiFetch: vi.fn(),
}))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
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
  name: "GH_TOKEN",
  description: null,
  type: "CLI_TOKEN",
  provider: "GITHUB",
  status: "ACTIVE",
  scope: "CREW",
  crew_ids: ["crew1"],
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
  agent_names: ["sam"],
  _count_agent_credentials: 1,
  mcp_used: false,
}

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

interface Routes {
  revealEnabled?: boolean
  fields?: unknown[]
  bindings?: unknown[]
  agents?: unknown[]
  agentCredentials?: Record<string, unknown[]>
}

function route({ revealEnabled = false, fields = [], bindings = [], agents = [], agentCredentials = {} }: Routes = {}) {
  h.apiFetch.mockImplementation(async (url: string) => {
    const u = String(url)
    if (u.includes("/credentials/reveal-policy")) return ok({ workspace_id: "ws1", enabled: revealEnabled })
    if (u.includes("/fields")) return ok(fields)
    if (u.includes("/credentials/bindings")) return ok({ bindings })
    if (/\/agents\/[^/]+\/credentials/.test(u)) {
      const id = /\/agents\/([^/]+)\/credentials/.exec(u)![1]
      return ok(agentCredentials[id] ?? [])
    }
    if (u.startsWith("/api/v1/agents?")) return ok(agents)
    return ok([])
  })
}

function renderSheet(
  overrides: Record<string, unknown> = {},
  onRotate = vi.fn(),
  onBack = vi.fn(),
) {
  render(
    <CredentialDetailSheet
      workspaceId="ws1"
      credential={{ ...credential, ...overrides }}
      open
      onOpenChange={() => {}}
      onRefresh={() => {}}
      onRotate={onRotate}
      onEdit={() => {}}
      onBack={onBack}
    />,
  )
  return { onRotate, onBack }
}

function openTab(name: RegExp) {
  const trigger = screen.getByRole("tab", { name })
  fireEvent.mouseDown(trigger)
  fireEvent.click(trigger)
}

beforeEach(() => {
  h.role = "OWNER"
  h.capabilities = []
  h.apiFetch.mockReset()
  route()
})

describe("rotate is the primary path, reveal the secondary one (§2.6 L8)", () => {
  it("offers rotation on Overview to a rotate-capable role", () => {
    renderSheet()
    expect(screen.getByRole("button", { name: /rotate and show the new value/i })).toBeInTheDocument()
  })

  it("hands the current credential to the rotation flow", () => {
    const onRotate = vi.fn()
    renderSheet({}, onRotate)
    fireEvent.click(screen.getByRole("button", { name: /rotate and show the new value/i }))
    expect(onRotate).toHaveBeenCalledWith(expect.objectContaining({ id: "cred_1" }))
  })

  it("never shows a masked value as if it were the real one", () => {
    renderSheet()
    // The placeholder is dots, and there is no control that turns it into
    // anything — the only path to a value is the ceremony.
    expect(screen.getByText("••••••••••••••••")).toBeInTheDocument()
  })
})

describe("reveal needs all four layers, not any of them", () => {
  const enabled = { revealEnabled: true }

  it("is offered when the switch, the role and the capability all line up", async () => {
    h.capabilities = ["chat", "credentials:reveal"]
    route(enabled)
    renderSheet()
    expect(await screen.findByRole("button", { name: /reveal the existing value/i })).toBeInTheDocument()
  })

  // The one people get wrong. credentials:reveal is in no bundle — not even
  // "admin" — precisely so that being an OWNER never implies it.
  it("is withheld from an OWNER who does not hold credentials:reveal", async () => {
    route(enabled)
    renderSheet()
    await waitFor(() =>
      expect(h.apiFetch.mock.calls.some(([u]) => String(u).includes("reveal-policy"))).toBe(true),
    )
    expect(screen.queryByRole("button", { name: /reveal the existing value/i })).not.toBeInTheDocument()
  })

  it("is withheld while the workspace switch is off, capability or not", async () => {
    h.capabilities = ["chat", "credentials:reveal"]
    route({ revealEnabled: false })
    renderSheet()
    await waitFor(() =>
      expect(h.apiFetch.mock.calls.some(([u]) => String(u).includes("reveal-policy"))).toBe(true),
    )
    expect(screen.queryByRole("button", { name: /reveal the existing value/i })).not.toBeInTheDocument()
  })

  it("is withheld from a MEMBER below the MANAGER role floor", async () => {
    h.role = "MEMBER"
    h.capabilities = ["chat", "credentials:reveal"]
    route(enabled)
    renderSheet()
    expect(screen.queryByRole("button", { name: /reveal the existing value/i })).not.toBeInTheDocument()
  })

  it("does not even ask for the reveal policy below the role that may read it", async () => {
    h.role = "MEMBER"
    renderSheet()
    for (let i = 0; i < 10; i++) await new Promise((r) => setTimeout(r, 5))
    expect(h.apiFetch.mock.calls.filter(([u]) => String(u).includes("reveal-policy"))).toHaveLength(0)
  })

  it("is withheld for a SEALED credential even with everything else in place", async () => {
    h.capabilities = ["chat", "credentials:reveal"]
    route(enabled)
    renderSheet({ sensitivity: "SEALED" })
    await waitFor(() =>
      expect(h.apiFetch.mock.calls.some(([u]) => String(u).includes("reveal-policy"))).toBe(true),
    )
    expect(screen.queryByRole("button", { name: /reveal the existing value/i })).not.toBeInTheDocument()
  })

  it("treats an unreachable policy endpoint as 'off' rather than 'on'", async () => {
    h.capabilities = ["chat", "credentials:reveal"]
    h.apiFetch.mockRejectedValue(new TypeError("offline"))
    renderSheet()
    for (let i = 0; i < 10; i++) await new Promise((r) => setTimeout(r, 5))
    expect(screen.queryByRole("button", { name: /reveal the existing value/i })).not.toBeInTheDocument()
  })

  it("opens the ceremony, which offers rotation as the way out", async () => {
    h.capabilities = ["chat", "credentials:reveal"]
    route(enabled)
    const onRotate = vi.fn()
    renderSheet({}, onRotate)
    fireEvent.click(await screen.findByRole("button", { name: /reveal the existing value/i }))
    expect(await screen.findByText(/have you considered rotating/i)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /rotate instead/i }))
    expect(onRotate).toHaveBeenCalled()
  })
})

describe("Fields tab", () => {
  it("lists a secret part by key and marks it secret — never a value, not even a masked one", async () => {
    route({
      fields: [
        { key: "passphrase", is_secret: true, ordinal: 0, value: null, created_at: "", updated_at: "" },
        { key: "region", is_secret: false, ordinal: 1, value: "eu-central-1", created_at: "", updated_at: "" },
      ],
    })
    renderSheet()
    openTab(/fields/i)

    expect(await screen.findByText("passphrase")).toBeInTheDocument()
    expect(screen.getByText("secret")).toBeInTheDocument()
    // The non-secret half IS shown — that is the entire point of storing it
    // in the clear.
    expect(screen.getByText("eu-central-1")).toBeInTheDocument()
    expect(screen.queryByText(/•/)).not.toBeInTheDocument()
  })

  it("says the credential is a single value when it has no extra parts", async () => {
    renderSheet()
    openTab(/fields/i)
    expect(await screen.findByText(/single value — no extra fields/i)).toBeInTheDocument()
  })

  it("degrades to the empty state when the fields request fails", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (String(url).includes("/fields")) throw new TypeError("offline")
      return ok([])
    })
    renderSheet()
    openTab(/fields/i)
    expect(await screen.findByText(/single value — no extra fields/i)).toBeInTheDocument()
  })
})

describe("Used by — slots and grant provenance", () => {
  it("shows which env var each scope will fill", async () => {
    route({
      bindings: [
        {
          id: "b1", credential_id: "cred_1", credential_name: "GH_TOKEN",
          scope: "CREW", crew_id: "crew1", agent_id: null, slot: "GH_TOKEN", created_at: "",
        },
      ],
    })
    renderSheet()
    openTab(/used by/i)

    expect(await screen.findByText("CREW")).toBeInTheDocument()
    expect(screen.getByText("GH_TOKEN", { selector: "span.font-mono" })).toBeInTheDocument()
  })

  // grant_source decides where the revoke lives — an explicit grant has its
  // own DELETE, a crew-derived one has no row at all. Showing them alike is
  // how you end up with a revoke button that silently does nothing.
  it("labels a crew-inherited grant differently from an explicit one", async () => {
    route({
      agents: [
        { id: "a1", name: "sam", crew_id: "crew1" },
        { id: "a2", name: "quinn", crew_id: "crew1" },
      ],
      agentCredentials: {
        a1: [{ credential_id: "cred_1", env_var_name: "GH_TOKEN", grant_source: "explicit", expired: false }],
        a2: [{ credential_id: "cred_1", env_var_name: "GH_TOKEN", grant_source: "crew", expired: false }],
      },
    })
    renderSheet()
    openTab(/used by/i)

    // Wait on the provenance badge, not on the name: the name is already on
    // screen from the fallback list, and asserting on it would pass before the
    // real answer arrived.
    expect(await screen.findByText("crew grant")).toBeInTheDocument()
    expect(screen.getByText("explicit")).toBeInTheDocument()
    expect(screen.getByText("sam")).toBeInTheDocument()
    expect(screen.getByText("quinn")).toBeInTheDocument()
  })

  it("flags a lapsed lease, which the container has already stopped honouring", async () => {
    route({
      agents: [{ id: "a1", name: "sam", crew_id: "crew1" }],
      agentCredentials: {
        a1: [{
          credential_id: "cred_1", env_var_name: "GH_TOKEN", grant_source: "explicit",
          expires_at: "2026-01-01T00:00:00Z", expired: true,
        }],
      },
    })
    renderSheet()
    openTab(/used by/i)
    expect(await screen.findByText("lease expired")).toBeInTheDocument()
  })

  it("falls back to the names on the payload when provenance cannot be resolved", async () => {
    route({ agents: [] })
    renderSheet()
    openTab(/used by/i)
    expect(await screen.findByText("sam")).toBeInTheDocument()
    expect(screen.queryByText("explicit")).not.toBeInTheDocument()
  })
})

describe("classification control", () => {
  it("says the current classification is unknown when the API does not report one", () => {
    renderSheet()
    const trigger = screen.getByRole("tab", { name: /settings/i })
    fireEvent.mouseDown(trigger)
    fireEvent.click(trigger)
    expect(screen.getByText(/not reported by the credentials api/i)).toBeInTheDocument()
  })

  it("writes the chosen classification and adopts the value the server echoes", async () => {
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (String(url).includes("/sensitivity")) {
        return ok({ credential_id: "cred_1", sensitivity: "RESTRICTED", previous: "STANDARD" })
      }
      if (String(url).includes("reveal-policy")) return ok({ enabled: false })
      void init
      return ok([])
    })
    renderSheet()
    const trigger = screen.getByRole("tab", { name: /settings/i })
    fireEvent.mouseDown(trigger)
    fireEvent.click(trigger)

    fireEvent.click(screen.getByRole("button", { name: "RESTRICTED" }))
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "RESTRICTED" })).toHaveAttribute("aria-pressed", "true"),
    )
    // …and the header badge now carries it.
    expect(screen.getAllByText("RESTRICTED").length).toBeGreaterThan(1)
  })

  // SetSensitivity re-checks with "manage" on the lowering branch only.
  it("blocks a MANAGER from lowering a classification, which the server would refuse", async () => {
    h.role = "MANAGER"
    renderSheet({ sensitivity: "SEALED" })
    const trigger = screen.getByRole("tab", { name: /settings/i })
    fireEvent.mouseDown(trigger)
    fireEvent.click(trigger)
    expect(screen.getByRole("button", { name: "STANDARD" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "RESTRICTED" })).toBeDisabled()
  })

  it("surfaces the server's refusal rather than silently keeping the old value", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (String(url).includes("/sensitivity")) {
        return { ok: false, status: 403, json: async () => ({ error: "Forbidden" }) } as unknown as Response
      }
      return ok([])
    })
    renderSheet()
    const trigger = screen.getByRole("tab", { name: /settings/i })
    fireEvent.mouseDown(trigger)
    fireEvent.click(trigger)
    fireEvent.click(screen.getByRole("button", { name: "SEALED" }))
    expect(await screen.findByText("Forbidden")).toBeInTheDocument()
  })

  it("is not offered at all to a role that cannot write it", () => {
    h.role = "VIEWER"
    renderSheet()
    const trigger = screen.getByRole("tab", { name: /settings/i })
    fireEvent.mouseDown(trigger)
    fireEvent.click(trigger)
    expect(screen.queryByRole("button", { name: "SEALED" })).not.toBeInTheDocument()
  })
})

// The detail view is master-detail INLINE, the way /integrations does it:
// clicking a row in the rail swaps the main pane for that credential and
// offers a way back. Not a modal — a modal keeps the list visible behind a
// scrim and asks the reader to dismiss something before they can look at the
// next credential, which is the wrong rhythm for a page whose whole job is
// comparing and moving between secrets.
//
// Add-a-credential stays a centred dialog. That is not an inconsistency: a
// create flow is a task you finish or abandon, an inspect flow is somewhere
// you navigate. /integrations makes the same split.
describe("container", () => {
  it("renders inline rather than inside a modal", () => {
    h.role = "OWNER"
    renderSheet()
    // No dialog at all: a modal here would trap the reader behind a scrim.
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    // Present twice on purpose: once as the breadcrumb's nav context, once as
    // the identity header — the same shape /integrations uses.
    expect(screen.getAllByText("GH_TOKEN").length).toBeGreaterThan(0)
  })

  it("offers a way back to the list", () => {
    h.role = "OWNER"
    const onBack = vi.fn()
    renderSheet({}, undefined, onBack)
    fireEvent.click(screen.getByRole("button", { name: /back to credentials/i }))
    expect(onBack).toHaveBeenCalled()
  })
})
