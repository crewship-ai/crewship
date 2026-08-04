import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react"

import { SettingsNav, isSettingsSectionVisible } from "../settings-nav"

// The Settings sidebar used to list every workspace section to every role.
// A MEMBER was offered Connections (a roleCreate route) and Audit Log — which
// they cannot even READ server-side, so the tab opened an empty pane.
//
// The rule: hide a row only when the role can neither act nor usefully read.
// Everything else stays visible and renders read-only, because "the workspace
// has 3 crews and 12 members" is information a MEMBER is entitled to.

// Privacy is deliberately absent — it is `enabled: false` until peer-card
// extraction stops being a no-op. See the dedicated test below.
const ACCOUNT_ITEMS = ["Profile"]
const WORKSPACE_ITEMS = [
  "General",
  "Crew links",
  "Members",
  "Access & Secrets",
  "Audit Log",
]

// Sections that moved to the page that owns the object they configure. A nav
// row promises a pane; these have none, so the row is gone and only the
// `?tab=` key survives for old links (see the redirect tests at the bottom).
const MOVED_AWAY = ["Notifications", "Notification Prefs", "Crews & Containers"]

function renderNav(role: string | null) {
  return render(<SettingsNav activeTab="profile" onTabChange={vi.fn()} role={role} />)
}

/** Nav rows are SidebarRow → role="button" with the label as aria-label. */
function row(label: string) {
  return screen.queryByRole("button", { name: label })
}

describe("SettingsNav visibility by role", () => {
  beforeEach(() => cleanup())

  it("shows an OWNER every section", () => {
    renderNav("OWNER")
    for (const label of [...ACCOUNT_ITEMS, ...WORKSPACE_ITEMS]) {
      expect(row(label), `OWNER should see ${label}`).toBeTruthy()
    }
  })

  it("hides Audit Log and Access & Secrets from a MEMBER", () => {
    renderNav("MEMBER")
    // Neither is readable below MANAGER server-side, so both panes would
    // render empty — the one case where hiding beats read-only.
    expect(row("Audit Log")).toBeNull()
    expect(row("Access & Secrets")).toBeNull()
  })

  it("keeps General, Crew links and Members visible to a MEMBER", () => {
    renderNav("MEMBER")
    // Read-only, but readable: workspace identity + counts, the link graph
    // (which answers "why can my agent not reach that crew"), the roster.
    for (const label of ["General", "Crew links", "Members", ...ACCOUNT_ITEMS]) {
      expect(row(label), `MEMBER should still see ${label}`).toBeTruthy()
    }
  })

  it("gives a MANAGER Audit Log", () => {
    renderNav("MANAGER")
    expect(row("Crew links")).toBeTruthy()
    expect(row("Audit Log")).toBeTruthy()
  })

  it("offers no row for a section that moved, at any role", () => {
    // Notifications and the preference matrix live on /integrations; crew
    // container limits and network policy live on the crew's own Settings tab,
    // where the rest of that crew's configuration already is. A nav row that
    // only bounces you elsewhere is a promise of a pane that does not exist.
    for (const role of ["OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"]) {
      cleanup()
      renderNav(role)
      for (const label of MOVED_AWAY) {
        expect(row(label), `${role} should not see ${label}`).toBeNull()
      }
    }
  })

  it("no longer offers Auxiliary Models at any role — it lives in Admin now", () => {
    // It reported process-wide config from a workspace-scoped screen, so a
    // new workspace never changed it. Admin → Keeper is where the governance
    // panel that overrides those judges already lives.
    for (const role of ["OWNER", "ADMIN", "MANAGER", "MEMBER"]) {
      cleanup()
      renderNav(role)
      expect(row("Auxiliary Models"), `${role} should not see it`).toBeNull()
    }
  })

  it("treats a VIEWER like a MEMBER for the manager-tier sections", () => {
    renderNav("VIEWER")
    expect(row("Audit Log")).toBeNull()
    expect(row("Access & Secrets")).toBeNull()
    expect(row("Members")).toBeTruthy()
    expect(row("Crew links")).toBeTruthy()
  })

  it("hides gated rows while the role is still unknown", () => {
    // Showing a row and retracting it a beat later is worse than showing it
    // late — matches isAdminTier/isManagerTier's own null handling.
    renderNav(null)
    expect(row("Audit Log")).toBeNull()
    expect(row("Access & Secrets")).toBeNull()
    expect(row("Profile")).toBeTruthy()
  })

  it("never leaks a hidden row through the search filter", () => {
    render(<SettingsNav activeTab="profile" onTabChange={vi.fn()} role="MEMBER" />)
    // Typing "audit" in the command-finder must not resurrect the tab.
    fireEvent.change(screen.getByPlaceholderText("Search settings…"), { target: { value: "audit" } })
    expect(row("Audit Log")).toBeNull()
  })
})

describe("isSettingsSectionVisible", () => {
  it("agrees with the rendered nav", () => {
    expect(isSettingsSectionVisible("audit", "MEMBER")).toBe(false)
    expect(isSettingsSectionVisible("audit", "MANAGER")).toBe(true)
    // The link graph reads at any tier; only its controls are MANAGER+.
    expect(isSettingsSectionVisible("connections", "MEMBER")).toBe(true)
    expect(isSettingsSectionVisible("general", "MEMBER")).toBe(true)
    expect(isSettingsSectionVisible("members", "VIEWER")).toBe(true)
  })

  it("leaves ungated keys alone", () => {
    // Unknown keys are the URL parser's problem, not the gate's.
    expect(isSettingsSectionVisible("profile", null)).toBe(true)
    expect(isSettingsSectionVisible("does-not-exist", "MEMBER")).toBe(true)
  })

  it("lets a moved section's key through so the redirect can run", () => {
    // The gate must not swallow `?tab=crews` into the Profile fallback — the
    // layout has to see the key to know where the section went.
    for (const key of ["notifications", "notification-prefs", "crews"]) {
      expect(isSettingsSectionVisible(key, "MEMBER"), key).toBe(true)
    }
  })

  // Peer-card extraction runs NoopExtractor in production, so the Privacy
  // pane can only ever be empty. `enabled: false` outranks role entirely —
  // an OWNER must not see it either, or the "nothing yet, check back"
  // reading of the empty state simply moves up the org chart.
  it("hides a section whose backing feature is not built, at every role", () => {
    for (const role of ["OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER", null]) {
      expect(isSettingsSectionVisible("privacy", role), `privacy for ${role}`).toBe(false)
    }
  })
})

/* ------------------------------------------------------- deep-link fallback */

const h = vi.hoisted(() => ({ role: "MEMBER" as string | null, replace: vi.fn() }))

// The global setup mock hands out a fresh router per call, which cannot be
// asserted on. This one keeps the same `replace` so a redirect is observable.
vi.mock("next/navigation", () => ({
  useRouter: () => ({
    replace: h.replace,
    push: vi.fn(),
    prefetch: vi.fn(),
    back: vi.fn(),
    forward: vi.fn(),
    refresh: vi.fn(),
  }),
  // Reads the real location, so the history.replaceState calls below still
  // set the URL under test — and so this models the browser, where
  // useSearchParams reflects the current url rather than an empty set.
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => "/settings",
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "w1", role: h.role, loading: false }),
}))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    session: { user: { id: "u1", name: "Ada", email: "ada@example.com" } },
    signOut: vi.fn().mockResolvedValue(undefined),
    refresh: vi.fn(),
  }),
}))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => false }, hasCapability: () => false }),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (url: string) => ({
    ok: true,
    status: 200,
    json: async () =>
      url.includes("/members")
        ? []
        : { id: "w1", name: "Acme", slug: "acme", preferred_language: null, _count: { crews: 1, agents: 2, members: 3 } },
  })),
}))
// Sections are stubbed: this test is about which pane the router picks, not
// about what the panes fetch.
vi.mock("../sections/profile-section", () => ({
  ProfileSection: () => <div data-testid="profile-section" />,
}))
vi.mock("../sections/crew-audit-section", () => ({
  CrewAuditSection: () => <div data-testid="audit-section" />,
}))

describe("SettingsLayout deep-link into a hidden section", () => {
  beforeEach(() => cleanup())

  it("falls back to Profile when a MEMBER opens ?tab=audit", async () => {
    h.role = "MEMBER"
    window.history.replaceState(null, "", "/settings?tab=audit")
    const { SettingsLayout } = await import("../settings-layout")
    render(<SettingsLayout />)

    // Not a blank pane, and not the audit pane either.
    await waitFor(() => expect(screen.getByTestId("profile-section")).toBeTruthy())
    expect(screen.queryByTestId("audit-section")).toBeNull()
    // The sub-bar names the section you actually landed on.
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Settings / Profile")
  })

  it("still honours ?tab=audit for a role that can read it", async () => {
    h.role = "OWNER"
    window.history.replaceState(null, "", "/settings?tab=audit")
    const { SettingsLayout } = await import("../settings-layout")
    render(<SettingsLayout />)

    await waitFor(() => expect(screen.getByTestId("audit-section")).toBeTruthy())
  })
})

/* ------------------------------------------------------- moved-section links */

describe("SettingsLayout deep-link into a section that moved", () => {
  beforeEach(() => {
    cleanup()
    h.replace.mockClear()
    h.role = "OWNER"
  })

  const cases: Array<[string, string]> = [
    ["notifications", "/integrations?tab=notifications&section=connections"],
    ["notification-prefs", "/integrations?tab=notifications&section=preferences"],
    ["crews", "/crews"],
  ]

  for (const [tab, href] of cases) {
    it(`sends ?tab=${tab} to ${href}`, async () => {
      window.history.replaceState(null, "", `/settings?tab=${tab}`)
      const { SettingsLayout } = await import("../settings-layout")
      render(<SettingsLayout />)

      // The bookmark keeps resolving; it just lands where the object lives now.
      await waitFor(() => expect(h.replace).toHaveBeenCalledWith(href))
      // …and never renders the Profile pane behind the jump.
      expect(screen.queryByTestId("profile-section")).toBeNull()
    })
  }
})
