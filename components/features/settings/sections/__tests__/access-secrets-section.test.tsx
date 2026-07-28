// Settings → Access & Secrets. The card is workspace-wide security policy, so
// the tests are about the gap between "can see it" and "can change it" — the
// reveal switch is the one control in the product whose write tier is a
// literal `role != "OWNER"` rather than the usual role ladder, and an ADMIN
// who is offered it gets a 403.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { AccessSecretsSection } from "../access-secrets-section"
import { isSettingsSectionVisible } from "../../settings-nav"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))

const members = [
  { id: "m1", role: "OWNER", user: { id: "u1", email: "pavel@firma.cz", full_name: "Pavel" } },
  { id: "m2", role: "ADMIN", user: { id: "u2", email: "tom@firma.cz", full_name: null } },
  { id: "m3", role: "ADMIN", user: { id: "u3", email: "eva@firma.cz", full_name: null } },
]

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function route({
  enabled = false,
  caps = {} as Record<string, string[]>,
  policyFails = false,
}: { enabled?: boolean; caps?: Record<string, string[]>; policyFails?: boolean } = {}) {
  h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url)
    if (u.includes("reveal-policy")) {
      if (policyFails) return { ok: false, status: 500, json: async () => ({}) } as unknown as Response
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as { enabled: boolean }
        return ok({ workspace_id: "ws1", enabled: body.enabled })
      }
      return ok({ workspace_id: "ws1", enabled })
    }
    if (u.includes("/members/capabilities")) {
      return ok({
        members: Object.entries(caps).map(([user_id, capabilities]) => ({ user_id, role: "ADMIN", capabilities })),
      })
    }
    return ok([])
  })
}

function renderSection(role: string) {
  render(<AccessSecretsSection workspaceId="ws1" role={role} members={members} />)
}

beforeEach(() => {
  h.apiFetch.mockReset()
  route()
})

describe("who sees the card at all", () => {
  // The nav owns visibility; GET /credentials/reveal-policy is MANAGER+, so
  // MEMBER and VIEWER would get a 403 and an empty pane.
  it.each(["OWNER", "ADMIN", "MANAGER"])("keeps the nav row for %s", (role) => {
    expect(isSettingsSectionVisible("access-secrets", role)).toBe(true)
  })

  it.each(["MEMBER", "VIEWER"])("hides the nav row from %s", (role) => {
    expect(isSettingsSectionVisible("access-secrets", role)).toBe(false)
  })
})

describe("the reveal switch", () => {
  it("reflects the workspace's current setting", async () => {
    route({ enabled: true })
    renderSection("OWNER")
    await waitFor(() =>
      expect(screen.getByLabelText(/enable credential reveal/i)).toBeChecked(),
    )
  })

  it("lets an OWNER turn it on and adopts the value the server echoes", async () => {
    renderSection("OWNER")
    const toggle = await screen.findByLabelText(/enable credential reveal/i)
    expect(toggle).not.toBeChecked()

    fireEvent.click(toggle)
    await waitFor(() => expect(screen.getByLabelText(/enable credential reveal/i)).toBeChecked())

    const put = h.apiFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT")!
    expect(JSON.parse(String((put[1] as RequestInit).body))).toEqual({ enabled: true })
  })

  // SetPolicy compares `role != "OWNER"` literally, so ADMIN is refused.
  it("is read-only for an ADMIN, and says who can move it", async () => {
    renderSection("ADMIN")
    await waitFor(() => expect(screen.getByLabelText(/enable credential reveal/i)).toBeDisabled())
    expect(screen.getByText(/only a workspace owner can move this switch/i)).toBeInTheDocument()
  })

  it("is read-only for a MANAGER, who still needs to know the rule", async () => {
    renderSection("MANAGER")
    await waitFor(() => expect(screen.getByLabelText(/enable credential reveal/i)).toBeDisabled())
  })

  // "We could not read it" and "it is off" are different claims, and only one
  // of them is safe to make.
  it("says the policy could not be read rather than rendering it as off", async () => {
    route({ policyFails: true })
    renderSection("OWNER")
    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't read the reveal policy/i)
  })

  it("surfaces the server's refusal when a write is rejected", async () => {
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (String(url).includes("reveal-policy") && init?.method === "PUT") {
        return {
          ok: false,
          status: 403,
          json: async () => ({ error: "Only a workspace OWNER can change the credential reveal policy." }),
        } as unknown as Response
      }
      if (String(url).includes("reveal-policy")) return ok({ enabled: false })
      return ok({ members: [] })
    })
    renderSection("OWNER")
    fireEvent.click(await screen.findByLabelText(/enable credential reveal/i))
    expect(await screen.findByRole("alert")).toHaveTextContent(/only a workspace owner can change/i)
  })
})

describe("who holds credentials:reveal", () => {
  it("lists the holders, and only the holders", async () => {
    route({ caps: { u1: ["chat", "credentials:reveal"], u2: ["chat"] } })
    renderSection("OWNER")

    expect(await screen.findByText("Pavel")).toBeInTheDocument()
    expect(screen.queryByText("tom@firma.cz")).not.toBeInTheDocument()
  })

  // Reveal being ON with nobody holding the capability is a real and common
  // state, and it is not the same as reveal being off.
  it("says plainly that nobody can reveal when nobody holds it", async () => {
    route({ enabled: true, caps: { u1: ["chat"] } })
    renderSection("OWNER")
    expect(await screen.findByText(/no value can be revealed until someone is granted it/i)).toBeInTheDocument()
  })

  it("warns once the capability is spread wider than the recommendation", async () => {
    route({
      caps: {
        u1: ["credentials:reveal"],
        u2: ["credentials:reveal"],
        u3: ["credentials:reveal"],
      },
    })
    renderSection("OWNER")
    expect(await screen.findByText(/3 people can read secrets in plaintext/i)).toBeInTheDocument()
  })

  it("degrades to an empty list when the capability endpoint refuses", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (String(url).includes("/members/capabilities")) {
        return { ok: false, status: 403, json: async () => ({}) } as unknown as Response
      }
      return ok({ enabled: false })
    })
    renderSection("MANAGER")
    expect(await screen.findByText(/nobody holds/i)).toBeInTheDocument()
  })
})

describe("classification reference", () => {
  it("states that SEALED has no reveal path for anyone", async () => {
    renderSection("OWNER")
    expect(await screen.findByText(/never revealable — rotate instead/i)).toBeInTheDocument()
  })

  it("names the two different tiers for raising and lowering", async () => {
    renderSection("OWNER")
    expect(await screen.findByText(/manager\+ to raise · owner\/admin to lower/i)).toBeInTheDocument()
  })

  // Honesty about the gap rather than a control that saves nowhere.
  it("says per-category defaults are not configurable yet", async () => {
    renderSection("OWNER")
    expect(await screen.findByText(/per-category default classifications are not configurable yet/i)).toBeInTheDocument()
  })
})
