// INT-13 (MCP servers): On/Off per tool and "Choose tools…" for a crew MCP
// server, through GET/PATCH /crews/{id}/integrations/{id}/tools. Composio apps
// have no such route (#3448), so their Tools pane stays read-only.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

import { apiFetch } from "@/lib/api-fetch"

const m = vi.hoisted(() => ({ fetchCrewTools: vi.fn() }))
vi.mock("../crew-tools", async (orig) => ({ ...(await orig<typeof import("../crew-tools")>()), fetchCrewTools: m.fetchCrewTools }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))

import { CrewToolsView } from "../views/crew-tools-view"

const server = { id: "s1", crew_id: "c1", crew_name: "Research", crew_slug: "research", name: "github", display_name: "GitHub", transport: "stdio", endpoint: null, command: "gh-mcp", enabled: true, agent_binding_count: 2, auth_status: "connected" }
const binding = (name: string, enabled: boolean) => ({ id: `b-${name}`, tool_name: name, description: `${name} tool`, enabled, created_at: "", updated_at: "" })
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status })

beforeEach(() => {
  m.fetchCrewTools.mockResolvedValue([server])
  vi.mocked(apiFetch).mockImplementation(async (url: string | URL | Request, init?: RequestInit) => {
    const u = String(url)
    if (init?.method === "PATCH") return json({})
    if (u.includes("/crews/c1/integrations/s1/tools")) return json([binding("list_issues", true), binding("create_issue", false)])
    return json([])
  })
})
afterEach(() => { cleanup(); vi.clearAllMocks() })

const show = (canManage = true) => render(<CrewToolsView workspaceId="ws" search="" initialServerId={null} canManage={canManage} />)

describe("Crew MCP tool switches", () => {
  it("lists the server's tools with their state after Choose tools…", async () => {
    show()
    fireEvent.click(await screen.findByRole("button", { name: "Choose tools…" }))
    const list = await screen.findByTestId("crew-tool-switches")
    expect(within(list).getByRole("switch", { name: "list_issues" })).toBeChecked()
    expect(within(list).getByRole("switch", { name: "create_issue" })).not.toBeChecked()
    expect(list).toHaveTextContent("1 of 2 on")
    expect(vi.mocked(apiFetch)).toHaveBeenCalledWith(expect.stringContaining("/api/v1/crews/c1/integrations/s1/tools"))
  })

  it("PATCHes the tool and keeps the new state", async () => {
    show()
    fireEvent.click(await screen.findByRole("button", { name: "Choose tools…" }))
    fireEvent.click(await screen.findByRole("switch", { name: "create_issue" }))
    await waitFor(() =>
      expect(vi.mocked(apiFetch)).toHaveBeenCalledWith(
        expect.stringContaining("/api/v1/crews/c1/integrations/s1/tools/create_issue"),
        expect.objectContaining({ method: "PATCH", body: JSON.stringify({ enabled: true }) }),
      ),
    )
    expect(screen.getByRole("switch", { name: "create_issue" })).toBeChecked()
  })

  it("puts the switch back when the server refuses", async () => {
    vi.mocked(apiFetch).mockImplementation(async (url: string | URL | Request, init?: RequestInit) =>
      init?.method === "PATCH" ? json({ error: "no" }, 403) : String(url).includes("/tools") ? json([binding("list_issues", true)]) : json([]),
    )
    show()
    fireEvent.click(await screen.findByRole("button", { name: "Choose tools…" }))
    const sw = await screen.findByRole("switch", { name: "list_issues" })
    fireEvent.click(sw)
    await waitFor(() => expect(screen.getByRole("switch", { name: "list_issues" })).toBeChecked())
  })

  it("is read-only without permission to manage", async () => {
    show(false)
    fireEvent.click(await screen.findByRole("button", { name: "Choose tools…" }))
    expect(await screen.findByRole("switch", { name: "list_issues" })).toBeDisabled()
  })
})
