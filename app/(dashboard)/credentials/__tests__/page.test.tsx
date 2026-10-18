// The Vault page (/credentials, Harbor 2): URL state, the Secrets table and its
// health strip, page states, role-gated actions and bulk actions. The secret
// page itself and the dialogs have their own suites.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { _resetWorkspaceStoreForTests, useWorkspace } from "@/hooks/use-workspace"
import { clearStaleCache } from "@/lib/stale-cache"
import { useAppStore } from "@/lib/store"
import CredentialsPage from "../page"

// ── A router whose search params the page actually reads back ───────────────
const nav = vi.hoisted(() => {
  let search = ""
  const listeners = new Set<() => void>()
  const set = (url: string) => {
    const i = url.indexOf("?")
    search = i === -1 ? "" : url.slice(i + 1)
    listeners.forEach((l) => l())
  }
  return {
    get search() { return search },
    reset(q = "") { search = q },
    subscribe(l: () => void) { listeners.add(l); return () => listeners.delete(l) },
    push: vi.fn((url: string) => set(url)),
    replace: vi.fn((url: string) => set(url)),
  }
})

vi.mock("next/navigation", async () => {
  const React = await import("react")
  return {
    useRouter: () => ({ push: nav.push, replace: nav.replace, prefetch: vi.fn(), back: vi.fn(), forward: vi.fn(), refresh: vi.fn() }),
    useSearchParams: () => {
      const s = React.useSyncExternalStore(nav.subscribe, () => nav.search, () => nav.search)
      return React.useMemo(() => new URLSearchParams(s), [s])
    },
    usePathname: () => "/credentials",
  }
})

const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  capabilities: [] as string[],
  apiFetch: vi.fn(),
  mobile: false,
}))

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("@/hooks/use-paymaster", () => ({ useSubscriptionUsage: () => ({ data: { rows: [] }, loading: false, error: null, notConfigured: false }) }))
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

// Children with their own suites are stubbed; the page shell is what is under test.
vi.mock("@/components/features/credentials/add-secret-sheet", () => ({
  AddSecretSheet: ({ open, initial }: { open: boolean; initial?: { itemType?: string } }) =>
    open ? <div data-testid="add-secret-sheet" data-kind={initial?.itemType ?? "secret"} /> : null,
}))
vi.mock("@/components/features/credentials/credential-detail-sheet", () => ({
  CredentialDetailSheet: ({ credential, tab }: { credential: { name: string }; tab: string | null }) => (
    <div data-testid="detail-sheet" data-tab={tab ?? "overview"}>{credential.name}</div>
  ),
}))
vi.mock("@/components/features/credentials/rotation-dialog", () => ({ RotationDialog: () => <div data-testid="rotation-dialog" /> }))

const DAY = 24 * 3600 * 1000
function makeCredential(overrides: Record<string, unknown> = {}) {
  return {
    id: "cred_1",
    name: "STRIPE_API_KEY",
    description: null,
    type: "API_KEY",
    provider: "CUSTOM_CLI",
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
    last_used_at: new Date(Date.now() - DAY).toISOString(),
    last_used_ips: [],
    tags: [],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    _count_agent_credentials: 0,
    agent_names: [],
    agent_ids: [],
    mcp_used: false,
    security_level: 1,
    sensitivity: "STANDARD",
    ...overrides,
  }
}

function ok(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}
function fail(status: number): Response {
  return { ok: false, status, json: async () => ({}) } as unknown as Response
}

interface Routes {
  credentials?: unknown[]
  /** `?kind=provider_login` — defaults to the rows of `credentials` that carry a login. */
  logins?: unknown[]
  pools?: unknown[]
  poolDetails?: Record<string, unknown>
  bindings?: unknown[]
  preferences?: Record<string, unknown>
  crews?: unknown[]
  agents?: unknown[]
  single?: Response
  dependents?: Record<string, unknown[]>
  /** Answer the single-secret fetch only after a real timer, like a network round trip. */
  singleDelayMs?: number
}
function routeApi({ credentials = [], logins, pools = [], poolDetails = {}, bindings = [], preferences = {}, crews = [], agents = [], single, dependents = {}, singleDelayMs }: Routes = {}) {
  h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws1", name: "Test" }])
    if (url === "/api/v1/me/preferences") return ok(preferences)
    if (url.startsWith("/api/v1/me/preferences/")) return ok({})
    if (init?.method === "DELETE" || init?.method === "PATCH") return ok({})
    if (url === "/api/v1/provider-logins/pools") return ok({ items: pools, next_cursor: null })
    const pool = /^\/api\/v1\/provider-logins\/pools\/([^/?]+)$/.exec(url)
    if (pool) return poolDetails[decodeURIComponent(pool[1])] ? ok(poolDetails[decodeURIComponent(pool[1])]) : fail(404)
    if (url.startsWith("/api/v1/credentials/bindings?")) return ok({ bindings })
    if (url.startsWith("/api/v1/credentials?") && url.includes("kind=provider_login") && logins) return ok(logins)
    if (url.startsWith("/api/v1/credentials?")) return ok(credentials)
    const dep = /\/api\/v1\/credentials\/([^/?]+)\/dependents/.exec(url)
    if (dep) return ok({ routines: dependents[decodeURIComponent(dep[1])] ?? [] })
    if (/^\/api\/v1\/credentials\/[^/?]+\?/.test(url)) {
      if (singleDelayMs) await new Promise((r) => setTimeout(r, singleDelayMs))
      return single ?? fail(404)
    }
    if (url.startsWith("/api/v1/crews?")) return ok(crews)
    if (url.startsWith("/api/v1/agents?")) return ok(agents)
    return ok([])
  })
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(<QueryClientProvider client={client}><CredentialsPage /></QueryClientProvider>)
}

function table() {
  return within(screen.getByRole("table", { name: "Secrets" }))
}
async function findRow(name: string) {
  return within(await screen.findByRole("table", { name: "Secrets" })).findByText(name)
}
function query() {
  return new URLSearchParams(nav.search)
}

beforeEach(() => {
  h.role = "OWNER"
  h.capabilities = []
  h.mobile = false
  h.apiFetch.mockReset()
  nav.reset()
  nav.push.mockClear()
  nav.replace.mockClear()
  clearStaleCache()
  act(() => useAppStore.getState().setSecondaryPanelOpen(false))
  _resetWorkspaceStoreForTests()
  try { localStorage.clear() } catch { /* jsdom */ }
})

describe("page states", () => {
  it("Q01 load error never renders the empty vault, and Retry recovers", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws1", name: "Test" }])
      throw new TypeError("fetch failed")
    })
    renderPage()
    expect(await screen.findByText("Couldn't load the vault")).toBeInTheDocument()
    expect(screen.queryByText("The vault is empty")).not.toBeInTheDocument()
    routeApi({ credentials: [makeCredential()] })
    fireEvent.click(screen.getByRole("button", { name: /retry/i }))
    expect(await findRow("STRIPE_API_KEY")).toBeInTheDocument()
  })

  it("names the HTTP status of a failed load", async () => {
    h.apiFetch.mockImplementation(async (url: string) => (url.startsWith("/api/v1/workspaces") ? ok([{ id: "ws1", name: "Test" }]) : fail(500)))
    renderPage()
    expect(await screen.findByText(/HTTP 500/)).toBeInTheDocument()
  })

  it("shows the empty vault with the CLI that exists", async () => {
    routeApi()
    renderPage()
    expect(await screen.findByText("The vault is empty")).toBeInTheDocument()
    expect(screen.getByText("crewship credential create")).toBeInTheDocument()
    expect(screen.getAllByRole("button", { name: "Add secret" }).length).toBeGreaterThan(0)
    expect(screen.getAllByRole("button", { name: "Add AI provider" }).length).toBeGreaterThan(0)
  })

  it("says the filters matched nothing, without offering to create", async () => {
    nav.reset("q=nothing-like-this")
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    expect(await screen.findByText("Nothing matches these filters")).toBeInTheDocument()
    expect(screen.queryByText("The vault is empty")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }))
    expect(await findRow("STRIPE_API_KEY")).toBeInTheDocument()
    expect(query().get("q")).toBeNull()
  })

  it("a linked secret that is gone shows not found with a way back", async () => {
    nav.reset("id=gone")
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    expect(await screen.findByText("This secret doesn’t exist")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Back to Secrets" }))
    expect(await findRow("STRIPE_API_KEY")).toBeInTheDocument()
  })
  // The lookup used to cancel itself: marking it "loading" re-ran the effect,
  // whose cleanup discarded the response, so a slow answer never landed.
  it("a linked secret that is gone shows not found even when the lookup is slow", async () => {
    nav.reset("id=gone")
    routeApi({ credentials: [makeCredential()], singleDelayMs: 30 })
    renderPage()
    expect(await screen.findByText("This secret doesn’t exist")).toBeInTheDocument()
  })

  it("a linked secret outside the list opens even when the lookup is slow", async () => {
    nav.reset("id=cred_far")
    routeApi({ credentials: [makeCredential()], single: ok(makeCredential({ id: "cred_far", name: "FAR_KEY" })), singleDelayMs: 30 })
    renderPage()
    expect(await screen.findByTestId("detail-sheet")).toHaveTextContent("FAR_KEY")
  })
})

describe("the Secrets table", () => {
  it("lists secrets, not AI provider accounts", async () => {
    routeApi({
      credentials: [
        makeCredential(),
        makeCredential({ id: "seat", name: "Claude Max", type: "PROVIDER_LOGIN", provider: "ANTHROPIC", login: { mode: "subscription", provider: "ANTHROPIC", plan: null, plan_label: null, owner_user_id: null, owner_email: null, expires_at: null, refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null }, quota: null, delivery: { kind: "env", target: "CLAUDE_CODE_OAUTH_TOKEN" }, pays_for: { agents: 0, crews: 0 } } }),
      ],
    })
    renderPage()
    expect(await findRow("STRIPE_API_KEY")).toBeInTheDocument()
    expect(table().queryByText("Claude Max")).not.toBeInTheDocument()
  })

  it("opens a secret's page through the URL", async () => {
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    fireEvent.click(await findRow("STRIPE_API_KEY"))
    expect(nav.push).toHaveBeenCalledWith("/credentials?id=cred_1", { scroll: false })
    expect(await screen.findByTestId("detail-sheet")).toHaveTextContent("STRIPE_API_KEY")
  })

  it("links: ?id= opens the entity page and ?tab= picks its tab", async () => {
    nav.reset("id=cred_1&tab=history")
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    expect(await screen.findByTestId("detail-sheet")).toHaveAttribute("data-tab", "history")
  })

  it("loads 50 rows at a time", async () => {
    routeApi({ credentials: Array.from({ length: 63 }, (_, i) => makeCredential({ id: `c${i}`, name: `KEY_${String(i).padStart(2, "0")}` })) })
    renderPage()
    await screen.findByRole("table", { name: "Secrets" })
    expect(table().getAllByRole("row")).toHaveLength(51)
    expect(screen.getByText("Showing 50 of 63")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /load 50 more/i }))
    expect(table().getAllByRole("row")).toHaveLength(64)
  })
})

describe("health strip", () => {
  it("tiles count rejected/expiring/unused and open the view", async () => {
    routeApi({
      credentials: [
        makeCredential({ id: "a", name: "DEAD", status: "REVOKED" }),
        makeCredential({ id: "b", name: "SOON", token_expires_at: new Date(Date.now() + 5 * DAY).toISOString() }),
        makeCredential({ id: "c", name: "IDLE", last_used_at: null }),
        makeCredential({ id: "d", name: "BUSY" }),
      ],
    })
    renderPage()
    await findRow("BUSY")
    const strip = within(screen.getByRole("group", { name: "Vault health" }))
    expect(strip.getByRole("button", { name: /^1 rejected/i })).toBeInTheDocument()
    expect(strip.getByRole("button", { name: /^1 expire within 14 days/i })).toBeInTheDocument()
    expect(strip.getByRole("button", { name: /^1 not used in 30 days/i })).toBeInTheDocument()
    // Old values and Shared with everyone need data the server does not send yet.
    expect(strip.queryByText(/old values/i)).not.toBeInTheDocument()
    expect(strip.queryByText(/shared with everyone|workspace-wide/i)).not.toBeInTheDocument()

    fireEvent.click(strip.getByRole("button", { name: /^1 rejected/i }))
    expect(query().get("view")).toBe("rejected")
    await waitFor(() => expect(table().getAllByRole("row")).toHaveLength(2))
    expect(table().getByText("DEAD")).toBeInTheDocument()
    expect(screen.queryByRole("group", { name: "Vault health" })).not.toBeInTheDocument()
  })
})

describe("URL filters from other pages", () => {
  it("links: ?crew= filters by the crew's slug, through its agents and its scope", async () => {
    nav.reset("crew=research")
    routeApi({
      credentials: [
        makeCredential({ id: "a", name: "HELD_BY_SCOUT", agent_ids: ["ag-scout"], agent_names: ["Scout"] }),
        makeCredential({ id: "b", name: "LINKED_TO_CREW", scope: "CREW", crew_ids: ["crew-r"] }),
        makeCredential({ id: "c", name: "OTHER" }),
      ],
      crews: [{ id: "crew-r", name: "Research", slug: "research" }],
      agents: [{ id: "ag-scout", slug: "scout", name: "Scout", crew_id: "crew-r" }],
    })
    renderPage()
    await waitFor(() => expect(screen.getByRole("table", { name: "Secrets" })).toBeInTheDocument())
    await waitFor(() => expect(table().queryByText("OTHER")).not.toBeInTheDocument())
    await waitFor(() => expect(table().getByText("HELD_BY_SCOUT")).toBeInTheDocument())
    expect(table().getByText("LINKED_TO_CREW")).toBeInTheDocument()
    expect(await screen.findByText("Used by crew: Research")).toBeInTheDocument()
  })

  it("links: ?class= filters by classification and the chip removes it", async () => {
    nav.reset("class=SEALED")
    routeApi({ credentials: [makeCredential({ id: "a", name: "SEALED_ONE", sensitivity: "SEALED" }), makeCredential({ id: "b", name: "PLAIN" })] })
    renderPage()
    expect(await findRow("SEALED_ONE")).toBeInTheDocument()
    expect(table().queryByText("PLAIN")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Remove filter Classification: Sealed" }))
    expect(query().get("class")).toBeNull()
    expect(await findRow("PLAIN")).toBeInTheDocument()
  })

  it("links: ?tab=providers opens AI providers for owners", async () => {
    nav.reset("tab=providers")
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    expect(await screen.findByRole("heading", { level: 1, name: "AI providers" })).toBeInTheDocument()
  })

  it("Q02 MEMBER deep link ?tab=providers lands on Secrets with note", async () => {
    h.role = "MEMBER"
    nav.reset("tab=providers")
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    expect(await findRow("STRIPE_API_KEY")).toBeInTheDocument()
    expect(screen.getByRole("note")).toHaveTextContent("AI provider accounts are managed by owners and admins.")
    expect(screen.queryByRole("navigation", { name: "Vault spaces" })).not.toBeInTheDocument()
  })
})

function seat(id: string, name: string, over: Record<string, unknown> = {}) {
  return makeCredential({
    id, name, type: "PROVIDER_LOGIN", provider: (over.provider as string) ?? "ANTHROPIC",
    login: {
      mode: "subscription", provider: (over.provider as string) ?? "ANTHROPIC", plan: null, plan_label: "Max",
      owner_user_id: "u-pavel", owner_email: "pavel@example.test", expires_at: null,
      refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null }, quota: null,
      delivery: { kind: "env", target: "CLAUDE_CODE_OAUTH_TOKEN" }, pays_for: { agents: 1, crews: 0 },
      ...over,
    },
  })
}

describe("AI providers space", () => {
  const SEATS = [
    seat("s1", "Claude Max · Pavel"),
    seat("s2", "Gemini · Tomas", { provider: "GOOGLE", owner_email: "tomas@example.test", owner_user_id: "u-tomas", refresh: { supported: true, status: "needs_relogin", last_at: null, next_at: null, error: "invalid_grant" } }),
    seat("s3", "OpenRouter", { provider: "OPENROUTER", mode: "api_key", pays_for: { agents: 0, crews: 0 } }),
  ]

  it("shows the health strip, the cards and the account groups in the panel", async () => {
    nav.reset("space=providers")
    routeApi({ credentials: [makeCredential(), ...SEATS], pools: [{ id: "p1", name: "Claude seats", provider: "ANTHROPIC", mode: "subscription", revision: 1, allow_cross_owner: false, member_count: 1 }], poolDetails: { p1: { id: "p1", name: "Claude seats", provider: "ANTHROPIC", mode: "subscription", revision: 1, allow_cross_owner: false, member_count: 1, members: [{ credential_id: "s1", priority: 0 }] } } })
    renderPage()
    expect(await screen.findByRole("heading", { level: 1, name: "AI providers" })).toBeInTheDocument()
    const health = screen.getByRole("group", { name: "AI providers health" })
    expect(within(health).getByRole("button", { name: /^1 need a new sign-in/i })).toBeInTheDocument()
    expect(within(health).getByRole("button", { name: /^1 pay for nothing/i })).toBeInTheDocument()
    // Three accounts: cards, the group named on its member.
    expect(document.querySelector("[data-slot=provider-accounts]")).toHaveAttribute("data-layout", "cards")
    expect(await screen.findByText("· Claude seats")).toBeInTheDocument()
    const panel = document.querySelector('[data-slot="secondary-panel"]') as HTMLElement
    fireEvent.click(within(panel).getByText("Claude seats"))
    expect(query().get("pool")).toBe("p1")
    expect(await screen.findByRole("heading", { level: 1, name: "Claude seats" })).toBeInTheDocument()
  })

  it("views and the health tiles live in the URL", async () => {
    nav.reset("space=providers")
    routeApi({ credentials: SEATS })
    renderPage()
    fireEvent.click(await screen.findByRole("button", { name: /^1 need a new sign-in/i }))
    expect(query().get("view")).toBe("attention")
    expect(await screen.findByRole("heading", { level: 1, name: "Needs attention" })).toBeInTheDocument()
    expect(screen.getByText("Gemini · Tomas")).toBeInTheDocument()
    expect(screen.queryByText("Claude Max · Pavel")).not.toBeInTheDocument()
  })

  it("one provider chosen is that provider's page", async () => {
    nav.reset("space=providers&provider=GOOGLE")
    routeApi({ credentials: SEATS, bindings: [{ id: "b1", credential_id: "s2", credential_name: "Gemini · Tomas", scope: "WORKSPACE", crew_id: null, agent_id: null, slot: "GEMINI_API_KEY" }] })
    renderPage()
    expect(await screen.findByRole("heading", { level: 1, name: "Gemini / Google" })).toBeInTheDocument()
    expect(screen.getByText("Gemini CLI, OpenCode")).toBeInTheDocument()
    expect(await screen.findByText("Gemini · Tomas", { selector: "dd" })).toBeInTheDocument()
  })

  it("keys 1 and 2 switch the spaces, never while typing", async () => {
    routeApi({ credentials: [makeCredential(), ...SEATS] })
    renderPage()
    await findRow("STRIPE_API_KEY")
    fireEvent.keyDown(window, { key: "2" })
    expect(query().get("space")).toBe("providers")
    expect(await screen.findByRole("heading", { level: 1, name: "AI providers" })).toBeInTheDocument()
    const search = screen.getByRole("textbox", { name: "Search AI provider accounts" })
    fireEvent.keyDown(search, { key: "1" })
    expect(query().get("space")).toBe("providers")
    fireEvent.keyDown(window, { key: "1" })
    expect(query().get("space")).toBeNull()
  })

  it("a MEMBER's key 2 does nothing", async () => {
    h.role = "MEMBER"
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    await findRow("STRIPE_API_KEY")
    fireEvent.keyDown(window, { key: "2" })
    expect(query().get("space")).toBeNull()
  })

  it("remembers the last space and a bare /credentials opens it", async () => {
    routeApi({ credentials: [makeCredential(), ...SEATS], preferences: { "vault.lastSpace": "providers" } })
    renderPage()
    await waitFor(() => expect(query().get("space")).toBe("providers"))
    expect(nav.replace).toHaveBeenCalled()
    // Switching back writes the new space.
    fireEvent.keyDown(window, { key: "1" })
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/me/preferences/vault.lastSpace", expect.objectContaining({ method: "PUT", body: JSON.stringify("secrets") })), { timeout: 2000 })
  })

  it("an address that names a space wins over the memory", async () => {
    nav.reset("type=API_KEY")
    routeApi({ credentials: [makeCredential()], preferences: { "vault.lastSpace": "providers" } })
    renderPage()
    await findRow("STRIPE_API_KEY")
    expect(query().get("space")).toBeNull()
  })

  it("Add AI provider opens its own dialog", async () => {
    nav.reset("space=providers")
    routeApi({ credentials: SEATS })
    renderPage()
    await screen.findByRole("heading", { level: 1, name: "AI providers" })
    fireEvent.click(screen.getAllByRole("button", { name: "Add AI provider" })[0])
    expect(await screen.findByRole("dialog", { name: /Add AI provider/ })).toBeInTheDocument()
    expect(screen.queryByTestId("add-secret-sheet")).not.toBeInTheDocument()
  })
})

describe("roles", () => {
  it("OWNER can tick rows, change the tier and delete", async () => {
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    await findRow("STRIPE_API_KEY")
    fireEvent.click(screen.getByRole("checkbox", { name: "Select STRIPE_API_KEY" }))
    const bar = screen.getByRole("toolbar", { name: "Change the selected secrets" })
    expect(within(bar).getByText("1 selected")).toBeInTheDocument()
    expect(within(bar).getByRole("button", { name: /change tier/i })).toBeInTheDocument()
    expect(within(bar).getByRole("button", { name: /delete/i })).toBeInTheDocument()
  })

  it("MANAGER can change the tier but not delete (DELETE is OWNER/ADMIN)", async () => {
    h.role = "MANAGER"
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    await findRow("STRIPE_API_KEY")
    fireEvent.click(screen.getByRole("checkbox", { name: "Select STRIPE_API_KEY" }))
    const bar = screen.getByRole("toolbar", { name: "Change the selected secrets" })
    expect(within(bar).getByRole("button", { name: /change tier/i })).toBeInTheDocument()
    expect(within(bar).queryByRole("button", { name: /delete/i })).not.toBeInTheDocument()
  })

  it("MEMBER without credential.create sees the list without actions", async () => {
    h.role = "MEMBER"
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    await findRow("STRIPE_API_KEY")
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Add secret" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Add AI provider" })).not.toBeInTheDocument()
  })

  it("MEMBER with credential.create can add secrets but not AI providers", async () => {
    h.role = "MEMBER"
    h.capabilities = ["credential.create"]
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    await findRow("STRIPE_API_KEY")
    fireEvent.click(screen.getByRole("button", { name: "Add secret" }))
    expect(screen.getByTestId("add-secret-sheet")).toHaveAttribute("data-kind", "secret")
    expect(screen.queryByRole("button", { name: "Add AI provider" })).not.toBeInTheDocument()
  })
})

describe("bulk delete from the page", () => {
  it("counts only the visible selection and keeps failures selected", async () => {
    routeApi({ credentials: [makeCredential({ id: "a", name: "ALPHA" }), makeCredential({ id: "b", name: "BETA" })] })
    renderPage()
    await findRow("ALPHA")
    fireEvent.click(screen.getByRole("checkbox", { name: "Select ALPHA" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "Select BETA" }))
    // Narrow the list: BETA is still ticked but no longer on screen.
    fireEvent.change(screen.getByRole("textbox", { name: "Search secrets" }), { target: { value: "ALPHA" } })
    await waitFor(() => expect(table().queryByText("BETA")).not.toBeInTheDocument())
    // The filter change drops the selection — nothing hidden can be deleted.
    expect(screen.queryByRole("toolbar", { name: "Change the selected secrets" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("checkbox", { name: "Select ALPHA" }))
    fireEvent.click(within(screen.getByRole("toolbar", { name: "Change the selected secrets" })).getByRole("button", { name: /delete/i }))
    const dialog = await screen.findByRole("alertdialog")
    expect(within(dialog).getByText("Delete ALPHA?")).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }))
    await waitFor(() => expect(h.apiFetch.mock.calls.filter(([, o]) => (o as RequestInit | undefined)?.method === "DELETE")).toHaveLength(1))
    expect(String(h.apiFetch.mock.calls.find(([, o]) => (o as RequestInit | undefined)?.method === "DELETE")![0])).toContain("/credentials/a?")
  })
})

describe("workspace switch", () => {
  function renderWithSwitcher() {
    function Harness() {
      const { setWorkspaceId } = useWorkspace()
      return (
        <>
          <button onClick={() => setWorkspaceId("ws-b")}>switch to B</button>
          <CredentialsPage />
        </>
      )
    }
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
    return render(<QueryClientProvider client={client}><Harness /></QueryClientProvider>)
  }

  it("ignores a slow response from the previous workspace (#1156)", async () => {
    let resolveA: (v: Response) => void = () => {}
    const pendingA = new Promise<Response>((resolve) => { resolveA = resolve })
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws-a", name: "A" }, { id: "ws-b", name: "B" }])
      if (url.startsWith("/api/v1/credentials?") && url.includes("workspace_id=ws-a")) return pendingA
      if (url.startsWith("/api/v1/credentials?") && url.includes("workspace_id=ws-b")) return ok([makeCredential({ id: "cb", name: "BETA_KEY" })])
      return ok([])
    })
    renderWithSwitcher()
    await waitFor(() => expect(h.apiFetch.mock.calls.some((c) => String(c[0]).includes("workspace_id=ws-a"))).toBe(true))
    fireEvent.click(screen.getByText("switch to B"))
    expect(await findRow("BETA_KEY")).toBeInTheDocument()
    resolveA(ok([makeCredential({ id: "ca", name: "ALPHA_KEY" })]))
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByText("ALPHA_KEY")).not.toBeInTheDocument()
  })

  it("clears the selection when the workspace changes", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws-a", name: "A" }, { id: "ws-b", name: "B" }])
      if (url.startsWith("/api/v1/credentials?") && url.includes("workspace_id=ws-a")) return ok([makeCredential({ id: "ca", name: "ALPHA_KEY" })])
      if (url.startsWith("/api/v1/credentials?") && url.includes("workspace_id=ws-b")) return ok([makeCredential({ id: "cb", name: "BETA_KEY" })])
      return ok([])
    })
    renderWithSwitcher()
    await findRow("ALPHA_KEY")
    fireEvent.click(screen.getByRole("checkbox", { name: "Select ALPHA_KEY" }))
    expect(screen.getByText("1 selected")).toBeInTheDocument()
    fireEvent.click(screen.getByText("switch to B"))
    expect(await findRow("BETA_KEY")).toBeInTheDocument()
    expect(screen.queryByText(/selected/)).not.toBeInTheDocument()
  })
})

describe("readiness", () => {
  it("offers Missing tool only once a crew has reported a gap, and it narrows the table", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws1", name: "Test" }])
      if (url.startsWith("/api/v1/credentials?")) return ok([makeCredential({ id: "gh", name: "GH_TOKEN" }), makeCredential({ id: "x", name: "OTHER" })])
      if (url.startsWith("/api/v1/crews?")) return ok([{ id: "crew1", name: "engineering" }])
      if (url.includes("/credential-readiness")) return ok({ gaps: [{ credential_id: "gh", tool: "gh" }] })
      return ok([])
    })
    renderPage()
    const missing = await screen.findByText("Missing tool")
    fireEvent.click(missing)
    expect(query().get("view")).toBe("missing-tool")
    await waitFor(() => expect(table().queryByText("OTHER")).not.toBeInTheDocument())
    expect(table().getByText("GH_TOKEN")).toBeInTheDocument()
  })
})

describe("the panel is the shared SecondaryPanel", () => {
  it("renders inside the vault panel with spaces and views", async () => {
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    await findRow("STRIPE_API_KEY")
    const panel = document.querySelector('[data-slot="secondary-panel"]') as HTMLElement
    expect(panel).toHaveAttribute("data-panel-id", "vault")
    expect(within(panel).getByRole("navigation", { name: "Vault spaces" })).toBeInTheDocument()
    expect(within(panel).getByText("All secrets")).toBeInTheDocument()
    // Folders and external vaults have no backend yet.
    expect(within(panel).queryByText(/folders|external vaults/i)).not.toBeInTheDocument()
  })

  it("on a phone the panel is the sheet, and a space switch sits above the content", async () => {
    h.mobile = true
    routeApi({ credentials: [makeCredential()] })
    renderPage()
    await waitFor(() => expect(useAppStore.getState().secondaryPanel).toEqual({ id: "vault", label: "Vault" }))
    expect(document.querySelector('[data-slot="secondary-panel"]')).toBeNull()
    fireEvent.click(await screen.findByRole("radio", { name: "AI providers" }))
    expect(query().get("space")).toBe("providers")
  })
})
