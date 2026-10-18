import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, within } from "@testing-library/react"

const mobile = vi.hoisted(() => ({ value: false }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile.value }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(""),
  usePathname: () => "/crews",
}))
vi.mock("../crews-create", () => ({ CrewsNewMenu: () => <button type="button">New</button> }))

import { CrewPageBar } from "../crews-bar"
import { CrewHeader } from "../crew-header"
import { CrewsPanel } from "../crews-panel"
import type { CrewRecord } from "../crew-canvas-tabs/types"

afterEach(() => { cleanup(); mobile.value = false })

const crew = { id: "c1", name: "Research", slug: "research", description: "Web research and weekly market reports", icon: "search", color: "#0F8C8C", runtime_image: null, devcontainer_config: null } as unknown as CrewRecord

describe("PAR-064 crew bar: Chat with the lead", () => {
  it("offers Chat with <lead> beside Activity and Terminal on desktop when the crew has a lead", () => {
    render(<CrewPageBar crew={crew} lead={{ name: "Scout", slug: "scout" }} onTerminal={vi.fn()} />)
    expect(screen.getByRole("link", { name: /Chat with Scout/ })).toHaveAttribute("href", "/chat/scout")
    expect(screen.getByRole("link", { name: "Activity" })).toBeInTheDocument()
  })
  it("has no Chat button for a crew without a lead", () => {
    render(<CrewPageBar crew={crew} lead={null} onTerminal={vi.fn()} />)
    expect(screen.queryByRole("link", { name: /Chat with/ })).toBeNull()
  })
  it("on a phone only the Chat icon stays", () => {
    mobile.value = true
    render(<CrewPageBar crew={crew} lead={{ name: "Scout", slug: "scout" }} onTerminal={vi.fn()} />)
    expect(screen.getByRole("link", { name: "Chat with Scout" })).toBeInTheDocument()
    expect(screen.queryByRole("link", { name: "Activity" })).toBeNull()
  })
})

describe("PAR-065 crew title block", () => {
  it("the head shows the crew tile, the one-line description and the chip row", () => {
    render(<CrewHeader crew={crew} status={{ state: "working", needs: 0 } as never} lead={{ id: "a", name: "Scout", slug: "scout" } as never} agentCount={3} autonomy="guided" containerState="running" />)
    const head = screen.getByTestId("crew-header")
    const tile = head.querySelector("[data-slot=crew-tile]") as HTMLElement
    expect(tile.style.width).toBe("48px")
    expect(within(head).getByText("Web research and weekly market reports")).toBeInTheDocument()
    expect(within(head).getByText("Working")).toBeInTheDocument()
    expect(within(head).getByText("Lead · Scout")).toBeInTheDocument()
    expect(within(head).getByText("3 agents")).toBeInTheDocument()
    expect(within(head).getByText("Guided")).toBeInTheDocument()
  })
})

describe("PAR-069 crews panel rows", () => {
  const crews = [
    { id: "c-ops", name: "Ops", slug: "ops", icon: "server", color: "#2E8B4E" },
    { id: "c-res", name: "Research", slug: "research", icon: "search", color: "#0F8C8C" },
  ]
  const agents = [
    { id: "a1", name: "Opsy", slug: "opsy", status: "IDLE", agent_role: "LEAD", crew_id: "c-ops" },
    { id: "a4", name: "Scout", slug: "scout", status: "RUNNING", agent_role: "LEAD", crew_id: "c-res" },
  ]
  function renderPanel() {
    window.history.replaceState(null, "", "/crews")
    render(<CrewsPanel crews={crews} agents={agents} openCrewId="c-res" selectedCrewId="c-res" selectedAgentId={null} onCrewSelect={vi.fn()} onAgentSelect={vi.fn()} onNewCrew={vi.fn()} />)
  }
  it("crew rows are 30 px with a square solid crew tile", () => {
    renderPanel()
    const row = screen.getByRole("button", { name: "Ops" })
    expect(row.className).toMatch(/min-h-\[30px\]/)
    const tile = row.querySelector("[data-slot=crew-tile]") as HTMLElement
    expect(tile.style.backgroundColor.toLowerCase()).toBe("#2e8b4e")
    expect(tile.style.width).toBe("20px")
  })
  it("agents nest under the open crew with the LEAD mark and group counts are sans tabular", () => {
    renderPanel()
    const scout = screen.getByRole("button", { name: "Scout" })
    expect(within(scout).getByText("LEAD")).toBeInTheDocument()
    expect(scout.className).toMatch(/min-h-\[30px\]/)
    for (const el of document.querySelectorAll("[data-group] > div span:last-child, [data-testid^=crews-view-] span:last-child")) {
      expect((el as HTMLElement).className).not.toMatch(/font-mono/)
    }
    expect(screen.getByTestId("crews-view-all").querySelector("span:last-child")?.className).toMatch(/tabular-nums/)
  })
})
