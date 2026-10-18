import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, within } from "@testing-library/react"

vi.mock("@/components/features/crews/crew-activity-feed", () => ({ CrewActivityFeed: () => <p>recent activity</p> }))

const res = vi.hoisted(() => ({ data: {} as Record<string, unknown> }))
vi.mock("../memory-workspace", () => ({
  useWorkspaceResource: (url: string) => {
    const key = Object.keys(res.data).find((k) => url.includes(k))
    return key ? { key: url, data: res.data[key] } : { key: url, error: "nope" }
  },
}))

import { CrewOverview } from "@/components/features/crews/crew-overview"
import type { CrewRecord } from "@/components/features/crews/crew-canvas-tabs/types"

afterEach(() => { cleanup(); res.data = {} })

const crew = {
  id: "c1", name: "Research", slug: "research", issue_prefix: "CRW", network_mode: "restricted", allowed_domains: [], container_memory_mb: 4096,
  runtime_image: null, mise_config: null, devcontainer_config: null, created_at: "", updated_at: "",
} as unknown as CrewRecord

function renderOverview() {
  render(<CrewOverview workspaceId="ws" crew={crew} agents={[]} needs={[]} onBuild={vi.fn()} onInstall={vi.fn()} containers={null} gapCount={0} integrations={[]} onSelectAgent={vi.fn()} onTab={vi.fn()} onNewAgent={vi.fn()} />)
}

// PAR-067: v67's Works on card is chips of the crew's routines and issues.
describe("Works on", () => {
  it("shows routines and issues as chips that open them", () => {
    res.data = {
      "/pipelines": [{ id: "p1", slug: "weekly", name: "Weekly competitor scan", status: "ACTIVE" }],
      "/api/v1/issues": [{ id: "i1", identifier: "CRW-151", title: "Compare pricing", status: "IN_PROGRESS" }],
    }
    renderOverview()
    const card = screen.getByTestId("crew-works-on")
    expect(within(card).getByRole("link", { name: /Weekly competitor scan/ })).toHaveAttribute("href", expect.stringContaining("/routines"))
    expect(within(card).getByRole("link", { name: /CRW-151/ })).toHaveAttribute("href", expect.stringContaining("/issues"))
    expect(within(card).getByText("Routines")).toBeInTheDocument()
    expect(within(card).getByText("Issues")).toBeInTheDocument()
    // The old two-row form with an Open button each is gone.
    expect(within(card).queryByText("Recurring work this crew runs")).toBeNull()
  })
  it("with nothing it is one compact empty row", () => {
    res.data = { "/pipelines": [], "/api/v1/issues": [] }
    renderOverview()
    const card = screen.getByTestId("crew-works-on")
    expect(within(card).getByText("Nothing assigned to this crew yet.")).toBeInTheDocument()
    expect(within(card).queryAllByRole("link")).toHaveLength(0)
  })
})
