// P6 additions to /credentials: the left rail (the house explorer pattern)
// and the Readiness column fed by GET /crews/{id}/credential-readiness.
//
// The column is the reason this file exists. "The vault has a valid GitHub
// PAT" and "`gh` is installed in the crew's container" are different facts,
// and before the readiness endpoint the page could only ever report the first
// one — which is how a user ends up with a green vault and `gh: command not
// found`. The tests below pin that the page never claims readiness it has not
// been told about.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { _resetWorkspaceStoreForTests } from "@/hooks/use-workspace"
import CredentialsPage from "../page"

/** The rail now lists the same credential names as the table, so a bare text
 *  query matches twice. Scope to the labelled list region — which is what the
 *  assertion always meant. inList waits for the region first, for states it
 *  has not rendered into yet. */
function list() {
  return within(screen.getByRole("region", { name: /credential list/i }))
}
async function inList(name: string) {
  const region = await screen.findByRole("region", { name: /credential list/i })
  return within(region).findByText(name)
}
/** Category, scope and tag live behind the Filter button now (the /routines
 *  shape), so a facet click has to open the dropdown first. */
async function openFilter() {
  fireEvent.click(await screen.findByRole("button", { name: /filter/i }))
}


const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  capabilities: [] as string[],
  apiFetch: vi.fn(),
}))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
vi.mock("next/link", () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  default: ({ href, children, ...rest }: any) => <a href={href} {...rest}>{children}</a>,
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
vi.mock("@/components/features/credentials/add-secret-sheet", () => ({
  AddSecretSheet: () => <div data-testid="add-secret-sheet" />,
}))
vi.mock("@/components/features/credentials/credential-detail-sheet", () => ({
  CredentialDetailSheet: () => <div data-testid="detail-sheet" />,
}))
vi.mock("@/components/features/credentials/rotation-dialog", () => ({
  RotationDialog: () => <div data-testid="rotation-dialog" />,
}))
vi.mock("@/components/features/credentials/edit-credential-dialog", () => ({
  EditCredentialDialog: () => <div data-testid="edit-dialog" />,
}))
vi.mock("@/components/features/credentials/connect-oauth-dialog", () => ({
  ConnectOAuthDialog: () => <div data-testid="connect-oauth-dialog" />,
}))

function makeCredential(overrides: Record<string, unknown> = {}) {
  return {
    id: "cred_1",
    name: "GH_TOKEN",
    description: null,
    type: "CLI_TOKEN",
    provider: "GITHUB",
    status: "ACTIVE",
    scope: "WORKSPACE",
    crew_id: null,
    crew_ids: [],
    account_label: null,
    account_email: null,
    username: null,
    token_expires_at: null,
    last_checked_at: null,
    last_error: null,
    last_used_at: "2026-07-27T00:00:00Z",
    last_used_ips: [],
    tags: [],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    _count_agent_credentials: 0,
    agent_names: [],
    mcp_used: false,
    ...overrides,
  }
}

function ok(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

interface RouteOpts {
  credentials: unknown[]
  crews?: { id: string; name: string }[]
  readiness?: Record<string, unknown>
}

function routeApi({ credentials, crews = [], readiness = {} }: RouteOpts) {
  h.apiFetch.mockImplementation(async (url: string) => {
    if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws1", name: "Test" }])
    if (url.startsWith("/api/v1/credentials?")) return ok(credentials)
    if (url.startsWith("/api/v1/crews?")) return ok(crews)
    const m = /\/api\/v1\/crews\/([^/]+)\/credential-readiness/.exec(url)
    if (m) return ok(readiness[m[1]] ?? { crew_id: m[1], tools: [], checked: 0, gaps: [] })
    return ok([])
  })
}

function gapFor(credentialId: string, tool: string) {
  return {
    credential_id: credentialId,
    credential_name: "x",
    provider: "GITHUB",
    tool,
    feature: `ghcr.io/devcontainers/features/${tool}-cli:1`,
    feature_id: `${tool}-cli`,
  }
}

beforeEach(() => {
  h.role = "OWNER"
  h.capabilities = []
  h.apiFetch.mockReset()
  _resetWorkspaceStoreForTests()
  try { localStorage.clear() } catch { /* jsdom */ }
})

describe("Readiness column", () => {
  it("names the missing CLI when a crew that can use the credential lacks it", async () => {
    routeApi({
      credentials: [makeCredential()],
      crews: [{ id: "c1", name: "engineering" }],
      readiness: { c1: { crew_id: "c1", tools: [], checked: 1, gaps: [gapFor("cred_1", "gh")] } },
    })
    render(<CredentialsPage />)

    expect(await screen.findByText("needs gh")).toBeInTheDocument()
    expect(screen.queryByText("ready")).not.toBeInTheDocument()
  })

  it("says ready once at least one crew has reported and no gap came back", async () => {
    routeApi({
      credentials: [makeCredential()],
      crews: [{ id: "c1", name: "engineering" }],
      readiness: { c1: { crew_id: "c1", tools: ["gh"], checked: 1, gaps: [] } },
    })
    render(<CredentialsPage />)

    expect(await screen.findByText("ready")).toBeInTheDocument()
  })

  // The important one. No crew answered, so we know nothing — and "nothing
  // reported" must not render as the same green tick as "checked, all good".
  it("shows neither ready nor a gap when no crew has reported at all", async () => {
    routeApi({ credentials: [makeCredential()], crews: [] })
    render(<CredentialsPage />)

    expect(await inList("GH_TOKEN")).toBeInTheDocument()
    expect(screen.queryByText("ready")).not.toBeInTheDocument()
    expect(screen.queryByText(/^needs /)).not.toBeInTheDocument()
    expect(screen.getByTitle(/no crew reported its tool inventory yet/i)).toBeInTheDocument()
  })
})

describe("left rail filtering", () => {
  it("narrows the table to the credentials whose tool is missing", async () => {
    routeApi({
      credentials: [
        makeCredential({ id: "cred_1", name: "GH_TOKEN" }),
        makeCredential({ id: "cred_2", name: "ANTHROPIC_API_KEY", provider: "ANTHROPIC" }),
      ],
      crews: [{ id: "c1", name: "engineering" }],
      readiness: { c1: { crew_id: "c1", tools: [], checked: 2, gaps: [gapFor("cred_1", "gh")] } },
    })
    render(<CredentialsPage />)

    expect(await screen.findByRole("button", { name: /^Missing tool 1$/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /^Missing tool 1$/ }))

    expect(list().getByText("GH_TOKEN")).toBeInTheDocument()
    expect(screen.queryByText("ANTHROPIC_API_KEY")).not.toBeInTheDocument()
  })

  it("narrows by category, and clearing the filters brings the rest back", async () => {
    routeApi({
      credentials: [
        makeCredential({ id: "cred_1", name: "GH_TOKEN", provider: "GITHUB" }),
        makeCredential({ id: "cred_2", name: "ANTHROPIC_API_KEY", provider: "ANTHROPIC" }),
      ],
    })
    render(<CredentialsPage />)

    await openFilter()
    fireEvent.click(await screen.findByRole("button", { name: /^Source control 1$/ }))
    expect(list().getByText("GH_TOKEN")).toBeInTheDocument()
    expect(screen.queryByText("ANTHROPIC_API_KEY")).not.toBeInTheDocument()

    await openFilter()
    fireEvent.click(screen.getByRole("button", { name: /clear filters/i }))
    expect(list().getByText("ANTHROPIC_API_KEY")).toBeInTheDocument()
  })

  it("narrows by crew scope using the crew's name, not its id", async () => {
    routeApi({
      credentials: [
        makeCredential({ id: "cred_1", name: "GH_TOKEN", scope: "CREW", crew_ids: ["c1"] }),
        makeCredential({ id: "cred_2", name: "ANTHROPIC_API_KEY", provider: "ANTHROPIC" }),
      ],
      crews: [{ id: "c1", name: "engineering" }],
    })
    render(<CredentialsPage />)

    await openFilter()
    fireEvent.click(await screen.findByRole("button", { name: /crew · engineering/i }))
    expect(list().getByText("GH_TOKEN")).toBeInTheDocument()
    expect(screen.queryByText("ANTHROPIC_API_KEY")).not.toBeInTheDocument()
  })

  it("searches from the rail", async () => {
    routeApi({
      credentials: [
        makeCredential({ id: "cred_1", name: "GH_TOKEN" }),
        makeCredential({ id: "cred_2", name: "ANTHROPIC_API_KEY", provider: "ANTHROPIC" }),
      ],
    })
    render(<CredentialsPage />)

    fireEvent.change(await screen.findByPlaceholderText(/search a secret or tool/i), {
      target: { value: "anthropic" },
    })
    expect(list().getByText("ANTHROPIC_API_KEY")).toBeInTheDocument()
    expect(screen.queryByText("GH_TOKEN")).not.toBeInTheDocument()
  })

  it("tells the user the filters matched nothing instead of showing an empty table", async () => {
    routeApi({ credentials: [makeCredential()] })
    render(<CredentialsPage />)

    fireEvent.change(await screen.findByPlaceholderText(/search a secret or tool/i), {
      target: { value: "zzzz" },
    })
    expect(screen.getByText(/no credentials match the current filters/i)).toBeInTheDocument()
  })

  // A rail full of zeroes beside "No credentials yet" is a filter surface for
  // a list that does not exist.
  it("is not rendered at all for an empty vault", async () => {
    routeApi({ credentials: [] })
    render(<CredentialsPage />)

    expect(await screen.findByText("No credentials yet")).toBeInTheDocument()
    expect(screen.queryByPlaceholderText(/search a secret or tool/i)).not.toBeInTheDocument()
  })

  it("collapses to a rail that can be reopened", async () => {
    routeApi({ credentials: [makeCredential()] })
    render(<CredentialsPage />)

    fireEvent.click(await screen.findByRole("button", { name: /collapse sidebar/i }))
    expect(screen.queryByPlaceholderText(/search a secret or tool/i)).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: /expand sidebar/i }))
    expect(screen.getByPlaceholderText(/search a secret or tool/i)).toBeInTheDocument()
  })
})

describe("needs-attention banner", () => {
  it("hands off to the rail's attention filter rather than a second tab strip", async () => {
    routeApi({ credentials: [makeCredential({ status: "REVOKED" })] })
    render(<CredentialsPage />)

    fireEvent.click(await screen.findByText("Review →"))
    expect(screen.getByRole("button", { name: /^Needs attention 1$/ })).toHaveAttribute(
      "aria-pressed",
      "true",
    )
  })
})

describe("KPI strip", () => {
  it("reports how many crews were actually asked, so a zero is never mistaken for 'all clear'", async () => {
    routeApi({ credentials: [makeCredential()], crews: [] })
    render(<CredentialsPage />)

    const tile = (await screen.findByText("Tools missing")).closest("div")!.parentElement!
    expect(within(tile).getByText(/no crew reported/i)).toBeInTheDocument()
  })
})

// Master-detail, not a modal: selecting a credential replaces the table.
//
// This file stubs CredentialDetailSheet, so the breadcrumb and its Back button
// belong to the component test — asserting them here would only ever exercise
// the stub. What IS the page's responsibility, and only testable here, is the
// swap: the list must be GONE rather than covered, and the rail must drive it.
describe("master-detail", () => {
  it("replaces the list with the detail rather than covering it", async () => {
    routeApi({
      credentials: [
        makeCredential({ id: "cred_1", name: "GH_TOKEN" }),
        makeCredential({ id: "cred_2", name: "ANTHROPIC_API_KEY", provider: "ANTHROPIC" }),
      ],
    })
    render(<CredentialsPage />)

    const rail = await screen.findByRole("complementary")
    fireEvent.click(within(rail).getByRole("button", { name: /GH_TOKEN/ }))

    await waitFor(() =>
      expect(screen.queryByRole("region", { name: /credential list/i })).not.toBeInTheDocument(),
    )
    // A modal would have left the table queryable behind a scrim, and would
    // have announced itself as a dialog. Neither is true here.
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })

  // No "clicking the selected row closes it" test: re-selecting a row does
  // not toggle, and it should not — Back is the way out, and a rail row that
  // sometimes navigates and sometimes dismisses is a coin flip. The Back
  // button itself is covered in the component test, where the real component
  // renders instead of this file's stub.
})
