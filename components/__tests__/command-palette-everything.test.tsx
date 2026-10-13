import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, within } from "@testing-library/react"

import { apiFetch } from "@/lib/api-fetch"

// ⌘K finds every place in the app (#3045): cards inside Settings, tabs and
// sections inside pages, button-only actions, and the workspace lists the
// palette never fetched — missions, schedules, automations, crew tools.

const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  admin: true,
  setWorkspaceId: (() => {}) as (id: string) => void,
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({
    workspaceId: "ws-test",
    role: h.role,
    workspaces: [
      { id: "ws-test", name: "Harbor", slug: "harbor" },
      { id: "ws-2", name: "Northstar Studio", slug: "northstar" },
    ],
    setWorkspaceId: h.setWorkspaceId,
  }),
}))
vi.mock("@/hooks/use-auth", () => ({
  useIsInstanceAdmin: () => h.admin,
  useSessionSafe: () => ({ data: { user: { id: "u-test" } }, status: "authenticated" }),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(), broadcastSessionExpired: vi.fn() }))

import { CommandPalette } from "../command-palette"

// Longest key first: "/integrations/crews" must win over "/integrations".
const FIXTURES: Record<string, unknown> = {}

beforeEach(() => {
  h.role = "OWNER"
  h.admin = true
  h.setWorkspaceId = vi.fn()
  for (const k of Object.keys(FIXTURES)) delete FIXTURES[k]
  vi.mocked(apiFetch).mockImplementation(async (input: RequestInfo | URL) => {
    const url = String(input)
    const key = Object.keys(FIXTURES).sort((a, b) => b.length - a.length).find((k) => url.includes(k))
    return { ok: true, status: 200, json: async () => (key ? FIXTURES[key] : []) } as unknown as Response
  })
})

afterEach(cleanup)

function openPalette() {
  return render(<CommandPalette open={true} onOpenChange={vi.fn()} />)
}

function type(text: string) {
  fireEvent.change(screen.getByPlaceholderText(/search issues/i), { target: { value: text } })
}

const hrefOf = (name: string | RegExp) =>
  screen.getByRole("option", { name }).getAttribute("data-href")

describe("CommandPalette — every place in the app", () => {
  it("keeps the cards out of an empty palette and finds them as you type", async () => {
    openPalette()
    await screen.findByRole("group", { name: /navigation/i })
    expect(screen.queryByRole("group", { name: /settings & sections/i })).not.toBeInTheDocument()

    type("danger")
    expect(hrefOf(/Danger zone/)).toBe("/settings?tab=general&card=danger-zone")
    // Where it lives is on the row.
    expect(within(screen.getByRole("option", { name: /Danger zone/ })).getByText("Settings › General")).toBeInTheDocument()
  })

  it("finds a card by its words in any order", async () => {
    openPalette()
    type("avatars store")
    expect(hrefOf(/Store missing avatars/)).toBe("/settings?tab=general&card=usage")
  })

  it("finds a section by what is in it, not just its name", async () => {
    openPalette()
    type("slack")
    expect(hrefOf(/Notification connections/)).toBe("/integrations?tab=notifications&section=connections")
  })

  it("offers a member only what a member can open", async () => {
    h.role = "MEMBER"
    h.admin = false
    openPalette()
    type("danger")
    expect(screen.queryByRole("option", { name: /Danger zone/ })).not.toBeInTheDocument()
    type("backups")
    expect(screen.queryByRole("option", { name: /Backup/ })).not.toBeInTheDocument()
    type("token")
    expect(hrefOf(/Create CLI token/)).toBe("/settings?tab=profile&card=sessions-and-access")
  })

  it("finds missions, schedules, automations and crew tools, each linked to itself", async () => {
    FIXTURES["/api/v1/missions"] = [{ id: "m-1", title: "Quarter close", status: "IN_PROGRESS", lead_agent_name: "Jordan" }]
    FIXTURES["/pipeline-schedules"] = [{ id: "sc-1", name: "Nightly invoices", target_pipeline_slug: "invoice-sweep", cron_expr: "0 2 * * *" }]
    FIXTURES["/automations"] = { automations: [{ id: "au-1", name: "On new lead", enabled: true, event_type: "lead.created", action: { routine_slug: "lead-intake" } }], count: 1 }
    FIXTURES["/integrations/crews"] = [{ id: "ct-1", name: "github", display_name: "GitHub", transport: "stdio", icon: null, crew_name: "Operations" }]
    openPalette()
    // The lists arrive after the palette opens.
    await screen.findByRole("group", { name: /crew tools/i })

    type("quarter")
    expect(hrefOf(/Quarter close/)).toBe("/missions/m-1/timeline")
    type("nightly")
    expect(hrefOf(/Nightly invoices/)).toBe("/routines?slug=invoice-sweep&view=plan#schedule-sc-1")
    type("new lead")
    expect(hrefOf(/On new lead/)).toBe("/routines?slug=lead-intake&view=plan")
    type("github")
    expect(hrefOf(/GitHub/)).toBe("/integrations?tab=tools&section=crew-tools&server=ct-1")
  })

  it("ignores list bodies it cannot read instead of breaking the palette", async () => {
    FIXTURES["/api/v1/missions"] = { error: "nope" }
    FIXTURES["/automations"] = [{ unexpected: true }]
    openPalette()
    await screen.findByRole("group", { name: /navigation/i })
    expect(screen.queryByRole("group", { name: /missions/i })).not.toBeInTheDocument()
  })

  it("switches to another workspace", async () => {
    openPalette()
    type("northstar")
    fireEvent.click(screen.getByRole("option", { name: /Northstar Studio/ }))
    expect(h.setWorkspaceId).toHaveBeenCalledWith("ws-2")
  })
})
