import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, within, cleanup } from "@testing-library/react"

import { apiFetch } from "@/lib/api-fetch"

// useRouter/usePathname are globally mocked in vitest.setup.ts.
const h = vi.hoisted(() => ({ role: "OWNER" as string | null }))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", role: h.role }),
}))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { CommandPalette } from "../command-palette"

// Per-endpoint fixtures. The palette fans out over several lists, so keying
// on the URL is the only way to give each group its own shape.
const FIXTURES: Record<string, unknown[]> = {}

beforeEach(() => {
  h.role = "OWNER"
  for (const k of Object.keys(FIXTURES)) delete FIXTURES[k]
  vi.mocked(apiFetch).mockImplementation(async (url: string) => {
    const key = Object.keys(FIXTURES).find((k) => url.includes(k))
    return { ok: true, json: async () => (key ? FIXTURES[key] : []) } as unknown as Response
  })
})

afterEach(cleanup)

function openPalette() {
  return render(<CommandPalette open={true} onOpenChange={vi.fn()} />)
}

async function group(name: RegExp) {
  return screen.findByRole("group", { name })
}

describe("CommandPalette — feature coverage", () => {
  it.each(["Inbox", "Approvals", "Activity", "Routines", "Integrations"])(
    "exposes the %s feature in navigation",
    (label) => {
      openPalette()
      expect(screen.getByText(label)).toBeInTheDocument()
    },
  )

  it("drops Marketplace, which has no page and 404s", () => {
    openPalette()
    expect(screen.queryByText("Marketplace")).not.toBeInTheDocument()
  })

  // /crews is a SINGLE page with a canvas: crews and agents are selected
  // through ?crew= and ?agent=, and /crews/<anything> is not a route at all.
  // The palette named /crews/agents, /crews/agents/<id>, /crews/new,
  // /crews/agents/new AND /crews/<crewId>, so its first two rows, every agent
  // and every crew landed on a page carrying nothing but the sidebar.
  it("never links under /crews, which has no sub-routes", async () => {
    // Every entity kind has to be on screen or this walks the static rows
    // alone and passes while an entity row points somewhere dead — which is
    // exactly how the agent and crew rows survived.
    FIXTURES["/agents"] = [{ id: "a1", name: "Casey", slug: "casey", role_title: null, status: "idle", avatar_seed: null, avatar_style: null, crew: null }]
    FIXTURES["/crews"] = [{ id: "c1", name: "Ops", slug: "ops", color: "blue", icon: "server", _count: { agents: 1, members: 1 } }]
    FIXTURES["/projects"] = [{ id: "p1", name: "Harborlight", slug: "h", color: "blue", icon: "rocket", status: "active", issue_count: 1 }]
    FIXTURES["/issues"] = [{ id: "i1", identifier: "ENG-1", title: "T", status: "todo", priority: "high", assignee_name: null, crew_name: null, crew_slug: null }]
    FIXTURES["/pipelines"] = [{ id: "r1", slug: "s", name: "R", description: "", status: "active" }]
    FIXTURES["/members"] = [{ id: "m1", role: "ADMIN", user: { id: "u1", email: "a@x.io", full_name: "A", avatar_url: null } }]
    FIXTURES["/integrations"] = [{ id: "n1", name: "n", display_name: "N", transport: "stdio", icon: null, enabled: true }]
    FIXTURES["/credentials"] = [{ id: "cr1", name: "K", provider: "GITHUB", type: "API_KEY" }]
    FIXTURES["/skills"] = [{ id: "s1", name: "S", slug: "s", display_name: null, category: "AUTOMATION" }]
    openPalette()
    // CommandDialog renders through a Radix portal, so the rows live in
    // document.body, not in render()'s container. Querying the container
    // found nothing and passed vacuously.
    await group(/navigation/i)
    const dead = [...document.body.querySelectorAll("[data-href]")]
      .map((el) => el.getAttribute("data-href") ?? "")
      .filter((h) => /^\/crews\//.test(h))
    expect(dead).toEqual([])
  })
})

describe("CommandPalette — RBAC", () => {
  // The settings nav owns the per-section rule (isSettingsSectionVisible) and
  // states it plainly: "Role gate first, so search can never surface a hidden
  // row." The palette kept its own hardcoded list and honoured neither the
  // role gate nor the enabled flag.
  it("offers a MANAGER-tier settings section to an owner", async () => {
    openPalette()
    const g = await group(/settings/i)
    expect(within(g).getByText("Audit Log")).toBeInTheDocument()
    expect(within(g).getByText("Access & Secrets")).toBeInTheDocument()
  })

  it("hides MANAGER-tier settings sections from a member", async () => {
    h.role = "MEMBER"
    openPalette()
    const g = await group(/settings/i)
    expect(within(g).queryByText("Audit Log")).not.toBeInTheDocument()
    expect(within(g).queryByText("Access & Secrets")).not.toBeInTheDocument()
    // Profile is the one section every role owns.
    expect(within(g).getByText("Profile")).toBeInTheDocument()
  })

  it("drops the Privacy section, which is disabled for every role", () => {
    openPalette()
    expect(screen.queryByText(/Privacy/)).not.toBeInTheDocument()
  })

  it("offers the create actions to a manager", async () => {
    h.role = "MANAGER"
    openPalette()
    const g = await group(/quick actions/i)
    expect(within(g).getByText("Create new agent")).toBeInTheDocument()
    expect(within(g).getByText("Create new crew")).toBeInTheDocument()
  })

  it("hides the create actions from a member, who cannot create either", () => {
    // CASL: can("create", "Crew"|"Agent") starts at MANAGER.
    h.role = "MEMBER"
    openPalette()
    expect(screen.queryByText("Create new agent")).not.toBeInTheDocument()
    expect(screen.queryByText("Create new crew")).not.toBeInTheDocument()
  })

  it("hides Admin from a member", () => {
    h.role = "MEMBER"
    openPalette()
    expect(screen.queryByText("Admin")).not.toBeInTheDocument()
  })
})

describe("CommandPalette — entity coverage", () => {
  it("finds routines, the one large entity that was unsearchable", async () => {
    FIXTURES["/pipelines"] = [
      { id: "pln_1", slug: "classify-ticket", name: "Classify support ticket", description: "…", status: "active" },
    ]
    openPalette()
    const g = await group(/routines/i)
    expect(within(g).getByText("Classify support ticket")).toBeInTheDocument()
  })

  it("finds people in the workspace", async () => {
    FIXTURES["/members"] = [
      { id: "m1", role: "ADMIN", user: { id: "u1", email: "alex@x.dev", full_name: "Alex Rivers", avatar_url: null } },
    ]
    openPalette()
    const g = await group(/people/i)
    expect(within(g).getByText("Alex Rivers")).toBeInTheDocument()
  })

  it("selects a crew on the canvas instead of a route that does not exist", async () => {
    FIXTURES["/crews"] = [
      { id: "c1", name: "Ops", slug: "ops", color: "blue", icon: "server", _count: { agents: 2, members: 1 } },
    ]
    openPalette()
    const g = await group(/^crews$/i)
    expect(within(g).getByRole("option")).toHaveAttribute("data-href", "/crews?crew=ops")
  })

  it("finds integrations", async () => {
    FIXTURES["/integrations"] = [
      { id: "i1", name: "github-server", display_name: "GitHub", transport: "stdio", icon: "github", crew_name: "Ops", enabled: true },
    ]
    openPalette()
    const g = await group(/integrations/i)
    expect(within(g).getByText("GitHub")).toBeInTheDocument()
  })
})

describe("CommandPalette — real icons", () => {
  it("draws a project with its own icon and colour, not a generic folder", async () => {
    FIXTURES["/projects"] = [
      { id: "p1", name: "Harborlight", slug: "harborlight", color: "purple", icon: "rocket", status: "active", issue_count: 3 },
    ]
    openPalette()
    await group(/projects/i)
    // CrewIcon renders the named glyph; the generic FolderKanban must be gone.
    expect(document.body.querySelector('[data-project-icon="rocket"]')).toBeInTheDocument()
  })

  it("draws a credential with its provider's brand mark, not a generic key", async () => {
    FIXTURES["/credentials"] = [
      { id: "c1", name: "Prod GitHub token", provider: "GITHUB", type: "API_KEY" },
    ]
    openPalette()
    await group(/credentials/i)
    expect(document.body.querySelector('[data-credential-brand="GITHUB"]')).toBeInTheDocument()
  })
})

describe("CommandPalette — honesty about what was fetched", () => {
  it("does not cap issues at a page size that makes older ones unfindable", () => {
    openPalette()
    const issueCall = vi.mocked(apiFetch).mock.calls.map(String).find((u) => u.includes("/issues"))
    expect(issueCall).toBeDefined()
    const limit = Number(/limit=(\d+)/.exec(issueCall!)?.[1] ?? 0)
    // 50 silently hid every older issue behind a "No results found" that was
    // not true. Whatever the cap is, it has to clear a real workspace.
    expect(limit).toBeGreaterThanOrEqual(200)
  })
})

describe("CommandPalette — recent", () => {
  // vitest.setup.ts replaces localStorage with a stub whose getItem always
  // returns null, so the store has to be driven through the mock rather than
  // written to. Reset it per test — a leaked value would silently give the
  // next test a Recent section it never asked for.
  beforeEach(() => {
    vi.mocked(localStorage.getItem).mockReturnValue(null)
    vi.mocked(localStorage.setItem).mockClear()
  })

  it("offers what was opened last, newest first, above everything else", async () => {
    vi.mocked(localStorage.getItem).mockReturnValue(
      JSON.stringify([
        { href: "/routines?slug=classify-ticket", label: "Classify support ticket", group: "Routines" },
        { href: "/issues/ENG-2", label: "Rewrite the README", group: "Issues" },
      ]),
    )
    openPalette()
    const g = await group(/recent/i)
    const rows = within(g).getAllByRole("option").map((el) => el.textContent)
    expect(rows[0]).toContain("Classify support ticket")
    expect(rows[1]).toContain("Rewrite the README")
  })

  it("stays out of the way when nothing has been opened yet", () => {
    openPalette()
    expect(screen.queryByRole("group", { name: /recent/i })).not.toBeInTheDocument()
  })

  it("survives a corrupted store rather than blanking the palette", async () => {
    vi.mocked(localStorage.getItem).mockReturnValue("{not json")
    openPalette()
    // The palette still renders; the unreadable history is simply dropped.
    expect(await group(/navigation/i)).toBeInTheDocument()
    expect(screen.queryByRole("group", { name: /recent/i })).not.toBeInTheDocument()
  })

  it("drops entries that are not shaped like a destination", async () => {
    // Another tab, an older build or a curious user can all have written
    // something else under this key.
    vi.mocked(localStorage.getItem).mockReturnValue(
      JSON.stringify([{ nope: true }, { href: "/issues/ENG-1", label: "Real one", group: "Issues" }]),
    )
    openPalette()
    const g = await group(/recent/i)
    expect(within(g).getAllByRole("option")).toHaveLength(1)
    expect(within(g).getByText("Real one")).toBeInTheDocument()
  })
})

describe("CommandPalette — people deep-link", () => {
  it("names the person in the link, not just the roster", async () => {
    FIXTURES["/members"] = [
      { id: "m1", role: "ADMIN", user: { id: "u-fredy", email: "f@x.dev", full_name: "Fredy", avatar_url: null } },
    ]
    openPalette()
    const g = await group(/people/i)
    // Landing on /settings?tab=members alone left the caller to find the row
    // they had just searched for, by eye.
    expect(within(g).getByRole("option")).toHaveAttribute(
      "data-href",
      "/settings?tab=members&member=u-fredy",
    )
  })
})

describe("CommandPalette — ranking", () => {
  it("puts the thing actually called Ops above an issue that merely mentions it", async () => {
    FIXTURES["/crews"] = [
      { id: "c1", name: "Ops", slug: "ops", color: "blue", icon: "server", _count: { agents: 2, members: 1 } },
    ]
    FIXTURES["/issues"] = [
      { id: "i1", identifier: "OPS-6", title: "Map container resource limits", status: "todo", priority: "high", assignee_name: null, crew_name: null, crew_slug: null },
    ]
    openPalette()
    await group(/^crews$/i)

    // Leading every value with its kind ("crew Ops") pushed the real name six
    // characters in, so a name match could never score as a prefix and the
    // group that happened to render first won.
    const { paletteFilter } = await import("@/lib/palette-filter")
    const crew = paletteFilter("Ops ops crew", "ops")
    const issue = paletteFilter("OPS-6 Map container resource limits issue", "ops")
    expect(crew).toBeGreaterThanOrEqual(issue)
    expect(paletteFilter("Ops ops crew", "crew")).toBeGreaterThan(0)
  })
})

describe("CommandPalette — every row opens the thing, not its index", () => {
  it("names the credential, the project, the routine and the person in the link", async () => {
    FIXTURES["/credentials"] = [{ id: "cr1", name: "Prod GitHub token", provider: "GITHUB", type: "API_KEY" }]
    FIXTURES["/projects"] = [{ id: "p1", name: "Harborlight", slug: "h", color: "blue", icon: "rocket", status: "active", issue_count: 1 }]
    FIXTURES["/pipelines"] = [{ id: "r1", slug: "classify-ticket", name: "Classify", description: "", status: "active" }]
    FIXTURES["/members"] = [{ id: "m1", role: "ADMIN", user: { id: "u1", email: "a@x.io", full_name: "A", avatar_url: null } }]
    openPalette()
    await group(/credentials/i)

    const hrefOf = (label: string) =>
      [...document.body.querySelectorAll("[data-href]")]
        .find((el) => el.textContent?.includes(label))
        ?.getAttribute("data-href")

    // A row that lands on the index leaves the caller to repeat, by eye, the
    // search they just did.
    expect(hrefOf("Prod GitHub token")).toBe("/credentials?id=cr1")
    expect(hrefOf("Harborlight")).toBe("/issues?project=p1")
    expect(hrefOf("Classify")).toBe("/routines?slug=classify-ticket")
    expect(hrefOf("A")).toBe("/settings?tab=members&member=u1")
  })
})
