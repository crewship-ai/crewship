import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

// Harbor 2 M3 (#3350): the crew/agent tree is the "crews" SecondaryPanel —
// collapse is the panel's, and on a phone the tree is the panel sheet.

const mocks = vi.hoisted(() => ({ mobile: false }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mocks.mobile }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => new Response("{}")) }))
vi.mock("@/hooks/use-provisioning-status", () => ({ useProvisioningStatus: () => ({ detail: [] }) }))
vi.mock("@/hooks/use-credential-readiness", () => ({ useCredentialReadiness: () => ({ gapsByCredential: new Map() }) }))
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: ({ seed }: { seed: string }) => <span>{seed}</span> }))
vi.mock("next/link", () => ({
  default: ({ children, href, ...rest }: { children: React.ReactNode; href: string }) => <a href={href} {...rest}>{children}</a>,
}))
vi.mock("@/components/features/crews/crews-subbar", () => ({ CrewsSubbar: () => <div>Crews subbar</div> }))
vi.mock("@/components/features/crews/agent-canvas", () => ({ AgentCanvas: () => null }))
vi.mock("@/components/features/crews/crew-canvas", () => ({ CrewCanvas: () => null }))
vi.mock("@/components/features/crews/crew-catalog", () => ({ CrewCatalog: () => <p>Crew catalog</p> }))
vi.mock("@/components/features/crews/bottom-panel", () => ({ BottomPanel: () => null }))

import { useAppStore } from "@/lib/store"
import { CrewsLayout } from "../crews-layout"

const crews = [{ id: "ops", name: "Ops", slug: "ops", description: null, color: null, icon: null, created_at: "2026-01-01", _count: { agents: 1, members: 0 } }]
const agents = [{
  id: "morgan", name: "morgan", slug: "morgan", status: "IDLE", description: null, role_title: "Engineer",
  agent_role: "AGENT", llm_provider: "x", llm_model: "y", cli_adapter: "z", crew_id: "ops",
}]
const renderLayout = () => render(<CrewsLayout crews={crews} agents={agents} missions={[]} workspaceId="ws" loaded onRefresh={() => {}} />)

beforeEach(() => {
  mocks.mobile = false
  window.history.replaceState(null, "", "/crews")
  try { window.localStorage.removeItem("crewship.pref.panel.crews.collapsed") } catch { /* no storage */ }
  act(() => useAppStore.getState().setSecondaryPanelOpen(false))
})
afterEach(cleanup)

describe("CrewsLayout panel", () => {
  it("renders the crew/agent tree inside the crews SecondaryPanel with one collapse button", () => {
    renderLayout()
    const panel = document.querySelector<HTMLElement>("[data-slot=secondary-panel]")!
    expect(panel).toHaveAttribute("data-panel-id", "crews")
    expect(within(panel).getByRole("textbox", { name: "Search crews, agents…" })).toBeInTheDocument()
    expect(within(panel).getAllByRole("button", { name: "Hide Crews and agents" })).toHaveLength(1)
    expect(screen.queryByRole("button", { name: "Collapse sidebar" })).not.toBeInTheDocument()
  })

  it("collapses to the panel rail and expands again", () => {
    renderLayout()
    const panel = document.querySelector<HTMLElement>("[data-slot=secondary-panel]")!
    fireEvent.click(within(panel).getByRole("button", { name: "Hide Crews and agents" }))
    expect(panel).toHaveAttribute("data-collapsed", "true")
    expect(screen.queryByRole("textbox", { name: "Search crews, agents…" })).not.toBeInTheDocument()
    expect(screen.getByText("Crew catalog")).toBeInTheDocument()
    fireEvent.click(within(panel).getByRole("button", { name: "Show Crews and agents" }))
    expect(panel).toHaveAttribute("data-collapsed", "false")
  })

  it("on a phone puts the tree in the panel sheet and closes it when a crew is picked", () => {
    mocks.mobile = true
    renderLayout()
    expect(document.querySelector("[data-slot=secondary-panel]")).toBeNull()
    expect(screen.queryByRole("textbox", { name: "Search crews, agents…" })).not.toBeInTheDocument()
    act(() => useAppStore.getState().setSecondaryPanelOpen(true))
    const sheet = screen.getByRole("dialog", { name: "Crews and agents" })
    expect(within(sheet).getByRole("textbox", { name: "Search crews, agents…" })).toBeInTheDocument()
    fireEvent.click(within(sheet).getByRole("button", { name: "Ops" }))
    expect(useAppStore.getState().secondaryPanelOpen).toBe(false)
    expect(new URL(window.location.href).searchParams.get("crew")).toBe("ops")
  })
})
