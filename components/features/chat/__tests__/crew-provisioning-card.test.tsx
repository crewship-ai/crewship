import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"
import type { ProvisioningCrewState } from "@/hooks/use-provisioning-status"

let detail: ProvisioningCrewState[] = []
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-1" }) }))
vi.mock("@/hooks/use-provisioning-status", () => ({
  useProvisioningStatus: () => ({ detail, acknowledge: vi.fn() }),
}))
vi.mock("@/components/layout/app-toolbar-provisioning", () => ({
  ProvisioningChecklist: () => <div data-testid="checklist" />,
  ProvisioningEventSteps: () => <div data-testid="event-steps" />,
  ProvisioningFailure: () => <div data-testid="failure" />,
  ProvisioningBuildLog: () => <div data-testid="build-log" />,
  RecentBuildSummary: () => <div data-testid="recent" />,
  formatProvisionAgo: () => "34s ago",
}))

import { CrewProvisioningCard } from "../crew-provisioning-card"

const crew = (over: Partial<ProvisioningCrewState>): ProvisioningCrewState => ({
  id: "crew-1", slug: "operations", name: "Operations", status: "running", featureIds: [], ...over,
})

// The server keeps a message sent while the workspace is built and runs it
// itself when the build is done (chatbridge.HandleChatMessage →
// AttachPendingMessage, api.ProvisioningHandler.resumePending), so the card
// never asks the person to send it again.
describe("crew provisioning card", () => {
  beforeEach(() => { cleanup(); detail = [] })

  it("before the first progress event it says the workspace is being prepared, calmly", () => {
    const { container } = render(<CrewProvisioningCard crewId="crew-1" crewSlug="operations" />)
    expect(screen.getByText("Preparing the workspace…")).toBeTruthy()
    expect(container.querySelector("[data-testid='working-mark'][data-active='true']")).not.toBeNull()
    expect(container.querySelector(".animate-spin")).toBeNull()
  })

  it("while the build runs it says the same, with the calm graphic and no spinner", () => {
    detail = [crew({ status: "running", activeFeature: "python" })]
    const { container } = render(<CrewProvisioningCard crewId="crew-1" />)
    expect(screen.getByText("Preparing the workspace…")).toBeTruthy()
    expect(container.querySelector("[data-testid='working-mark'][data-active='true']")).not.toBeNull()
    expect(container.querySelector(".animate-spin")).toBeNull()
  })

  it.each([
    ["just completed", crew({ status: "completed" })],
    ["completed with a lingering summary", crew({ status: "completed", recent: { outcome: "completed", at: Date.now(), stepCount: 3, features: [] } })],
  ])("when the build %s it never asks to re-send: the message goes by itself", (_name, state) => {
    detail = [state]
    const { container } = render(<CrewProvisioningCard crewId="crew-1" />)
    expect(container.textContent).not.toMatch(/re-?send/i)
    expect(container.textContent).toContain("Workspace ready")
  })

  it("a failed build still says so", () => {
    detail = [crew({ status: "failed" })]
    render(<CrewProvisioningCard crewId="crew-1" />)
    expect(screen.getByText("Build failed for Operations")).toBeTruthy()
  })
})
