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
const ACCOUNT_ITEMS = ["Profile", "Notification Prefs"]
const WORKSPACE_ITEMS = [
  "General",
  "Crews & Containers",
  "Auxiliary Models",
  "Connections",
  "Notifications",
  "Members",
  "Audit Log",
]

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

  it("hides Connections and Audit Log from a MEMBER", () => {
    renderNav("MEMBER")
    // Connections is a roleCreate (MANAGER+) route; the audit log is not
    // readable at all below MANAGER, so its pane would render empty.
    expect(row("Connections")).toBeNull()
    expect(row("Audit Log")).toBeNull()
    expect(row("Auxiliary Models")).toBeNull()
  })

  it("keeps General, Crews, Members and Notifications visible to a MEMBER", () => {
    renderNav("MEMBER")
    // Read-only, but readable: workspace identity + counts, the crew roster,
    // the member roster. Notifications is role-OR-capability on the server,
    // so the role alone must never hide it.
    for (const label of ["General", "Crews & Containers", "Notifications", "Members", ...ACCOUNT_ITEMS]) {
      expect(row(label), `MEMBER should still see ${label}`).toBeTruthy()
    }
  })

  it("gives a MANAGER Connections and Audit Log but not Auxiliary Models", () => {
    renderNav("MANAGER")
    expect(row("Connections")).toBeTruthy()
    expect(row("Audit Log")).toBeTruthy()
    // Aux-model status is an ADMIN+ endpoint (#868).
    expect(row("Auxiliary Models")).toBeNull()
  })

  it("treats a VIEWER like a MEMBER for the manager-tier sections", () => {
    renderNav("VIEWER")
    expect(row("Connections")).toBeNull()
    expect(row("Audit Log")).toBeNull()
    expect(row("Members")).toBeTruthy()
  })

  it("hides gated rows while the role is still unknown", () => {
    // Showing a row and retracting it a beat later is worse than showing it
    // late — matches isAdminTier/isManagerTier's own null handling.
    renderNav(null)
    expect(row("Connections")).toBeNull()
    expect(row("Audit Log")).toBeNull()
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
    expect(isSettingsSectionVisible("connections", "MEMBER")).toBe(false)
    expect(isSettingsSectionVisible("aux-models", "MANAGER")).toBe(false)
    expect(isSettingsSectionVisible("aux-models", "ADMIN")).toBe(true)
    expect(isSettingsSectionVisible("general", "MEMBER")).toBe(true)
    expect(isSettingsSectionVisible("members", "VIEWER")).toBe(true)
    expect(isSettingsSectionVisible("notifications", "MEMBER")).toBe(true)
  })

  it("leaves ungated keys alone", () => {
    // Unknown keys are the URL parser's problem, not the gate's.
    expect(isSettingsSectionVisible("profile", null)).toBe(true)
    expect(isSettingsSectionVisible("does-not-exist", "MEMBER")).toBe(true)
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

const h = vi.hoisted(() => ({ role: "MEMBER" as string | null }))

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
