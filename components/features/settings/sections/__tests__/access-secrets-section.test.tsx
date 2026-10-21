// Settings → Access & Secrets. The card is workspace-wide security policy, so
// the tests are about the gap between "can see it" and "can change it" — the
// reveal switch is the one control in the product whose write tier is a
// literal `role != "OWNER"` rather than the usual role ladder, and an ADMIN
// who is offered it gets a 403.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { AccessSecretsSection } from "../access-secrets-section"
import { isSettingsSectionVisible } from "../../settings-nav"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), toastError: vi.fn() }))
vi.mock("sonner", () => ({ toast: { error: h.toastError, success: vi.fn() } }))
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
  h.toastError.mockReset()
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
    // The rule is on the lock beside the label (its tooltip), not repeated as
    // the row's hint: v67 explains the switch itself there (PAR-118).
    expect(screen.getByTitle(/only a workspace owner can move this switch/i)).toHaveTextContent("Owner")
    expect(screen.queryByText(/only a workspace owner can move this switch/i)).toBeNull()
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
    // A switch error is a toast (docs/ux/README.md › Saving); the switch stays where it was.
    await waitFor(() =>
      expect(h.toastError).toHaveBeenCalledWith(
        "Couldn’t change Value reveal",
        expect.objectContaining({ description: "Only a workspace OWNER can change the credential reveal policy." }),
      ),
    )
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(screen.getByLabelText(/enable credential reveal/i)).not.toBeChecked()
  })
})

describe("who holds credentials:reveal", () => {
  it("lists the holders, and only the holders", async () => {
    route({ caps: { u1: ["chat", "credentials:reveal"], u2: ["chat"] } })
    renderSection("OWNER")

    // The count and the avatars only (PAR-119): who they are is Members' job.
    expect(await screen.findByText("1 person")).toBeInTheDocument()
    expect(screen.queryByText(/tom@firma.cz/)).not.toBeInTheDocument()
    expect(document.querySelectorAll("[data-slot=reveal-holders] > *")).toHaveLength(1)
  })

  // Reveal being ON with nobody holding the capability is a real and common
  // state, and it is not the same as reveal being off.
  it("says plainly that nobody can reveal when nobody holds it", async () => {
    route({ enabled: true, caps: { u1: ["chat"] } })
    renderSection("OWNER")
    expect(await screen.findByText(/no value can be revealed until someone is granted it/i)).toBeInTheDocument()
  })

  // The amber footer moved to Members' "Needs attention" (PAR-119); this card
  // keeps the rule in its hint and the count.
  it("keeps the recommendation in the hint once the capability is spread wider", async () => {
    route({
      caps: {
        u1: ["credentials:reveal"],
        u2: ["credentials:reveal"],
        u3: ["credentials:reveal"],
      },
    })
    renderSection("OWNER")
    expect(await screen.findByText("3 people")).toBeInTheDocument()
    expect(screen.getByText(/More than 2 shows a warning/)).toBeInTheDocument()
    expect(screen.queryByText(/can read secrets in plaintext/i)).toBeNull()
  })

  // A failed read is not an empty answer: "Nobody holds …" is a claim about
  // the workspace, and it is only made from a list that actually loaded.
  it("says the grants could not be loaded when the read fails", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (String(url).includes("/members/capabilities")) {
        return { ok: false, status: 500, json: async () => ({}) } as unknown as Response
      }
      return ok({ enabled: false })
    })
    renderSection("OWNER")
    expect(await screen.findByText(/could not be loaded/i)).toBeInTheDocument()
    expect(screen.queryByText(/nobody holds/i)).toBeNull()
  })
})

describe("classification reference", () => {
  it("states that SEALED values have no reveal path for anyone", async () => {
    renderSection("OWNER")
    expect(await screen.findByText(/sealed values are never revealed or exported/i)).toBeInTheDocument()
  })

  // No control that saves nowhere: the card points to where a class is set.
  it("points to where a credential's class is set", async () => {
    renderSection("OWNER")
    expect(await screen.findByText(/change it on each secret in vault/i)).toBeInTheDocument()
  })

  it("says only an owner can move the reveal switch, beside its label", async () => {
    renderSection("ADMIN")
    const lock = await screen.findByTitle(/only a workspace owner can move this switch/i)
    expect(lock).toHaveTextContent("Owner")
  })
})

// The bulk capabilities read is ADMIN+. A MANAGER got a 403, which the card
// read as "no grants" and printed "Nobody holds credentials:reveal" — a false
// statement about the workspace, on the page meant to answer exactly that.
describe("who may reveal, when the grants cannot be read", () => {
  it("says the role cannot see the grants instead of claiming nobody has them", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      const u = String(url)
      if (u.includes("reveal-policy")) return ok({ workspace_id: "ws1", enabled: true })
      if (u.includes("/members/capabilities")) return { ok: false, status: 403, json: async () => ({}) } as unknown as Response
      return ok([])
    })
    renderSection("MANAGER")
    expect(await screen.findByText(/your role cannot see who holds/i)).toBeInTheDocument()
    expect(screen.queryByText(/nobody holds/i)).toBeNull()
  })
})

// Who holds reveal is a question about people: it is changed in Members, and
// this card says how many and links there instead of keeping a second
// editable list of the same rows. The class and placement summaries link to
// the credentials.
describe("acting on who may reveal", () => {
  function routeWith(extra: (url: string, init?: RequestInit) => Response | undefined) {
    const base = h.apiFetch.getMockImplementation()!
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => extra(String(url), init) ?? base(url, init))
  }

  it("counts the holders and sends grants and revocations to Members", async () => {
    route({ caps: { u1: ["credentials:reveal"], u2: ["credentials:reveal"] } })
    renderSection("OWNER")
    expect(await screen.findByText("2 people")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /Manage in Members/ })).toHaveAttribute("href", "/settings/members?flag=reveal")
    expect(screen.queryByRole("button", { name: /^Revoke reveal/ })).toBeNull()
    expect(screen.queryByLabelText("Person to grant reveal to")).toBeNull()
    expect(h.apiFetch.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === "PATCH")).toBe(false)
  })

  // #3487 SET-05: the Vault reads ?class= (lib/credentials/vault-url-state.ts,
  // PR #3402), so each count opens the Vault filtered to that class.
  it("classification counts open the Vault filtered by that class", async () => {
    route()
    routeWith((url) => {
      if (url.startsWith("/api/v1/credentials?")) return ok([
        { id: "c1", name: "GITHUB_TOKEN", scope: "WORKSPACE", sensitivity: "STANDARD" },
        { id: "c2", name: "PROD_DB", scope: "CREW", crew_ids: ["cr1"], sensitivity: "SEALED" },
      ])
      return undefined
    })
    renderSection("OWNER")
    const classification = await screen.findByRole("region", { name: "Classification" })
    await waitFor(() => expect(within(classification).getByRole("link", { name: /1\s*Standard/ })).toHaveAttribute("href", "/credentials?class=STANDARD"))
    expect(within(classification).getByRole("link", { name: /0\s*Restricted/ })).toHaveAttribute("href", "/credentials?class=RESTRICTED")
    expect(within(classification).getByRole("link", { name: /1\s*Sealed/ })).toHaveAttribute("href", "/credentials?class=SEALED")
  })

  it("classification counts link to the Vault", async () => {
    route()
    routeWith((url) => {
      if (url.startsWith("/api/v1/credentials?")) return ok([
        { id: "c1", name: "GITHUB_TOKEN", scope: "WORKSPACE", sensitivity: "STANDARD" },
        { id: "c2", name: "PROD_DB", scope: "CREW", crew_ids: ["cr1"], sensitivity: "SEALED" },
        { id: "c3", name: "STRIPE_KEY", scope: "CREW", crew_ids: ["cr1"], sensitivity: "RESTRICTED" },
        { id: "c4", name: "OLD_KEY", scope: "CREW", crew_ids: [], sensitivity: "STANDARD" },
      ])
      return undefined
    })
    renderSection("OWNER")
    const classification = await screen.findByRole("region", { name: "Classification" })
    await waitFor(() => expect(within(classification).getByRole("link", { name: /2\s*Standard/ })).toHaveAttribute("href", "/credentials?class=STANDARD"))
    expect(within(classification).getByRole("link", { name: /1\s*Restricted/ })).toBeInTheDocument()
    expect(within(classification).getByRole("link", { name: /1\s*Sealed/ })).toBeInTheDocument()

    const where = screen.getByRole("region", { name: "Where credentials live" })
    expect(within(where).getByRole("link", { name: /1\s*Workspace-wide/ })).toBeInTheDocument()
    expect(within(where).getByRole("link", { name: /2\s*Per crew/ })).toBeInTheDocument()
    expect(within(where).getByRole("link", { name: /1\s*Crew-scoped, no crew/ })).toBeInTheDocument()
  })
})
