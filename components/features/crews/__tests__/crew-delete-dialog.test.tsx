import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, waitFor } from "@testing-library/react"

import { CrewDeleteDialog } from "@/components/features/crews/crew-delete-dialog"
import type { AgentSummary } from "@/components/features/crews/crew-canvas-tabs/types"

afterEach(() => { cleanup(); vi.restoreAllMocks() })

const crew = { id: "c1", name: "Research", slug: "research" }
const agents = [{ id: "a1" }, { id: "a2" }, { id: "a3" }] as unknown as AgentSummary[]

function serve(routines: unknown, ok = true) {
  vi.spyOn(global, "fetch").mockImplementation(async (input) => {
    const u = String(input)
    if (u.includes("/pipelines")) return ok ? new Response(JSON.stringify(routines)) : new Response("{}", { status: 500 })
    return new Response("{}", { status: 404 })
  })
}
const view = (containers: Array<{ name: string; image: string; status: string; kind?: string }> | null, list = agents) =>
  render(<CrewDeleteDialog open onOpenChange={() => {}} workspaceId="ws1" crew={crew} agents={list} containers={containers} onConfirm={async () => {}} />)

describe("Delete crew dialog (ODCH-CR-06)", () => {
  it("names what the crew holds: agents, routines and the container", async () => {
    serve([{ id: "r1" }, { id: "r2" }])
    view([{ name: "research", image: "x", status: "running", kind: "crew" }])
    const line = await screen.findByTestId("confirm-summary")
    await waitFor(() => expect(line.textContent).toBe("3 agents · 2 routines · container running"))
  })

  it("keeps the counts it knows when routines cannot be read", async () => {
    serve({}, false)
    view([{ name: "research", image: "x", status: "stopped", kind: "crew" }])
    const line = await screen.findByTestId("confirm-summary")
    await waitFor(() => expect(line.textContent).toBe("3 agents · container stopped"))
  })

  it("singular counts read as singular", async () => {
    serve([{ id: "r1" }])
    view(null, [agents[0]])
    const line = await screen.findByTestId("confirm-summary")
    await waitFor(() => expect(line.textContent).toBe("1 agent · 1 routine"))
  })
})
