import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// v67 parity for the Settings sections: Appearance (PAR-097, 098, 100),
// General (PAR-115, 116, 117) and Access & Secrets (PAR-118, 119). jsdom has
// no layout, so sizes are pinned through the classes that produce them;
// e2e/settings-h2.spec.ts measures them in a browser.

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
const hooks = vi.hoisted(() => ({ theme: {} as unknown, hex: null as string | null | undefined, themeName: "dark" }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspacePagesTheme: () => hooks.theme, refreshWorkspaceSettings: vi.fn() }))
vi.mock("next-themes", () => ({ useTheme: () => ({ theme: hooks.themeName, setTheme: vi.fn() }) }))
vi.mock("@/components/layout/workspace-accent", () => ({ useWorkspaceAccentHex: () => hooks.hex }))
vi.mock("@/hooks/use-appearance-sync", () => ({ useAppearanceSyncStatus: () => "synced", retryAppearanceSync: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }))
vi.mock("@/components/features/settings/sections/privileged-credentials-card", () => ({ PrivilegedCredentialsCard: () => null }))

import { AppearanceSection } from "../appearance-section"
import { GeneralSection } from "../general-section"
import { AccessSecretsSection } from "../access-secrets-section"
import { ACCENTS } from "@/lib/theme/accents"

beforeEach(() => {
  cleanup()
  api.mockReset()
  api.mockImplementation(async () => ({ ok: true, status: 200, json: async () => [] }))
  hooks.theme = {}
  hooks.hex = null
  hooks.themeName = "dark"
  vi.stubGlobal("localStorage", { getItem: () => null, setItem: () => undefined, removeItem: () => undefined, clear: () => undefined })
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe("PAR-097 accent colour", () => {
  it("is one row of small round swatches, named only by tooltip", () => {
    render(<AppearanceSection />)
    const group = screen.getByRole("radiogroup", { name: "Accent colour" })
    for (const { label } of ACCENTS) {
      const swatch = within(group).getByRole("radio", { name: label })
      expect(swatch).toHaveAttribute("title", label)
      expect(swatch.className.split(" ")).toContain("size-[1.4808rem]")
      expect(swatch.className.split(" ")).toContain("rounded-full")
      // No visible name under the swatch.
      expect(within(group).queryByText(label)).toBeNull()
    }
    expect(group.className.split(" ")).not.toContain("gap-y-4")
  })

  it("puts the team colour in a plain row with Use, not a dashed box", () => {
    hooks.hex = "#E11D48"
    render(<AppearanceSection />)
    const card = screen.getByRole("region", { name: "Accent" })
    expect(card.querySelector("[class*='border-dashed']")).toBeNull()
    const row = screen.getByTestId("workspace-accent-hint")
    expect(row.getAttribute("data-slot")).toBe("settings-row")
    expect(row).toHaveTextContent("The workspace brand colour from General › Pages appearance")
    expect(within(row).getByRole("radio", { name: "Team colour" })).toHaveTextContent("Use")
  })
})

describe("PAR-098 theme previews", () => {
  it("are v67's wireframe cards: a sidebar, a top bar and cards, split for System", () => {
    render(<AppearanceSection />)
    const group = screen.getByRole("radiogroup", { name: "Theme" })
    for (const variant of ["dark", "light", "system"]) {
      const preview = group.querySelector(`[data-slot=theme-preview][data-variant=${variant}]`)!
      expect(preview, variant).not.toBeNull()
      expect(preview.querySelectorAll("[data-slot=theme-preview-sidebar] > b")).toHaveLength(3)
      expect(preview.querySelector("[data-slot=theme-preview-topbar]")).not.toBeNull()
      expect(preview.querySelector("[data-slot=theme-preview-cards]")).not.toBeNull()
    }
    // System is one preview split down the middle, not two app screenshots.
    expect(group.querySelectorAll("[data-slot=theme-preview]")).toHaveLength(3)
  })
})

describe("PAR-100 segmented controls", () => {
  it("are 28 px high with 13 px labels", () => {
    render(<AppearanceSection />)
    const text = screen.getByRole("group", { name: "Text size" })
    for (const button of Array.from(text.querySelectorAll("button"))) {
      expect(button.className.split(" ")).toContain("h-[var(--control-sm)]")
      expect(button.className.split(" ")).toContain("text-control")
      expect(button.className.split(" ")).not.toContain("text-xs")
    }
  })
})

function general(overrides: Partial<React.ComponentProps<typeof GeneralSection>> = {}) {
  return render(
    <GeneralSection workspaceId="ws1" orgName="Acme" orgSlug="acme" preferredLanguage="Czech" agentCount={3} crewCount={2}
      memberCount={4} role="OWNER" onUpdated={vi.fn()} onDelete={vi.fn()} {...overrides} />,
  )
}

describe("PAR-115 inline fields", () => {
  it("right-align their value; the slug is sans and the language is text only", () => {
    general()
    for (const label of ["Workspace name", "Slug"]) {
      expect(screen.getByLabelText(label).className.split(" ")).toContain("text-right")
    }
    expect(screen.getByLabelText("Slug").className.split(" ")).not.toContain("font-mono")
    const language = screen.getByRole("button", { name: /Czech/ })
    expect(language.textContent?.trim()).toBe("Czech")
    expect(language.className.split(" ")).toContain("justify-end")
    expect(language.querySelector("svg.lucide-chevron-down")).not.toBeNull()
    expect(language.querySelector("svg.lucide-chevrons-up-down")).toBeNull()
  })
})

describe("PAR-117 agent avatars copy", () => {
  it("says Fill in missing avatars", () => {
    general()
    expect(screen.getByRole("button", { name: "Fill in missing avatars" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Store missing avatars/ })).toBeNull()
  })
})

describe("PAR-116 Pages appearance", () => {
  it("is five plain tiles, with no mono caption, no hex fields and no sample block", () => {
    general()
    const card = screen.getByRole("region", { name: "Pages appearance" })
    expect(within(card).getAllByRole("radio")).toHaveLength(5)
    for (const caption of ["Theme", "Colours", "Live sample"]) {
      expect(within(card).queryByText(caption)).toBeNull()
    }
    expect(card.querySelector(".uppercase")).toBeNull()
    expect(within(card).queryByLabelText("Accent")).toBeNull()
    expect(within(card).queryByTestId("pages-mini-preview")).toBeNull()
    // The six colours are shown, read-only.
    for (const name of ["Accent", "Background", "Cards", "Borders", "Text", "Secondary text"]) {
      expect(within(card).getByText(name)).toBeInTheDocument()
    }
  })

  it("keeps the editor under Custom…", () => {
    general()
    const card = screen.getByRole("region", { name: "Pages appearance" })
    fireEvent.click(within(card).getByRole("radio", { name: "Custom" }))
    expect(within(card).getByLabelText("Accent")).toBeInTheDocument()
    expect(within(card).getAllByLabelText(/picker$/)).toHaveLength(6)
  })
})

function secrets(role: string, caps: Record<string, string[]> = {}, members?: { id: string; role: string; user: { id: string; email: string; full_name: string | null } }[]) {
  api.mockImplementation(async (url: string) => {
    const u = String(url)
    const json = u.includes("reveal-policy") ? { enabled: false } : u.includes("/members/capabilities")
      ? { members: Object.entries(caps).map(([user_id, capabilities]) => ({ user_id, role: "ADMIN", capabilities })) } : []
    return { ok: true, status: 200, json: async () => json }
  })
  const list = members ?? Array.from({ length: 11 }, (_, i) => ({ id: `m${i}`, role: "MEMBER", user: { id: `u${i}`, email: `p${i}@firma.cz`, full_name: `Person ${i}` } }))
  return render(<AccessSecretsSection workspaceId="ws1" role={role} members={list} />)
}

describe("PAR-118 reveal copy", () => {
  it("explains the switch the same way to an Admin, who cannot move it", async () => {
    secrets("ADMIN")
    const hint = await screen.findByText(/Off for new workspaces\. Each reveal still needs a fresh sign-in and a reason, and is written to the audit log first\./)
    expect(hint).toBeInTheDocument()
    expect(screen.queryByText(/the API refuses everyone else/)).toBeNull()
    // The owner-only rule stays on the lock's tooltip and on the switch.
    expect(document.querySelector("[data-slot=settings-lock]")).toHaveAttribute("title", expect.stringContaining("Only a workspace OWNER"))
    expect(screen.getByRole("switch")).toBeDisabled()
  })
})

describe("PAR-119 who may reveal", () => {
  it("shows the count and the avatars, not a list of people and permissions", async () => {
    const members = Array.from({ length: 11 }, (_, i) => ({ id: `m${i}`, role: "MEMBER", user: { id: `u${i}`, email: `p${i}@firma.cz`, full_name: `Person ${i}` } }))
    const caps = Object.fromEntries(members.map((m) => [m.user.id, ["credentials:reveal"]]))
    secrets("OWNER", caps, members)
    expect(await screen.findByText("11 people")).toBeInTheDocument()
    const card = screen.getByRole("region", { name: "Who may reveal" })
    expect(card).not.toHaveTextContent("Person 0")
    expect(card).not.toHaveTextContent("credentials:reveal, ")
    expect(card).not.toHaveTextContent("can read secrets in plaintext")
    expect(within(card).getByText(/Owners and admins by default\. More than 2 shows a warning\./)).toBeInTheDocument()
    expect(within(card).getByRole("link", { name: /Manage in Members/ })).toHaveAttribute("href", "/settings/members?flag=reveal")
    await waitFor(() => expect(card.querySelectorAll("[data-slot=reveal-holders] > *").length).toBeGreaterThan(0))
  })
})
