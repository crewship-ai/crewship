import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"

vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "ADMIN", capabilities: [], abilities: { can: () => true } }) }))
vi.mock("@/lib/api-fetch", async (orig) => ({
  ...(await orig<typeof import("@/lib/api-fetch")>()),
  apiFetch: vi.fn(async (url: string) =>
    String(url).includes("/budget")
      ? ({ ok: true, status: 200, json: async () => ({ crew_id: "c1", window: "month", limit_usd: null, spent_usd: 0, state: "none" }) } as Response)
      : ({ ok: true, status: 200, json: async () => [] } as Response)),
}))
vi.mock("@/components/features/crews/crew-escalations", () => ({ CrewEscalations: () => <div>esc</div> }))
vi.mock("@/components/features/crews/crew-policy-controls", () => ({ CrewPolicyControls: () => <div>policy</div> }))
vi.mock("../crew-members", () => ({ CrewMembers: () => <div>members</div> }))
vi.mock("../crew-yaml", () => ({ CrewCommandLine: () => <div data-testid="cli">cli</div>, crewCommands: () => [] }))

vi.mock("@/components/features/settings/resource-share-dialog", () => ({
  ResourceShareDialog: ({workspaceId,resource}:{workspaceId:string;resource:{kind:string;id:string}}) => <span data-testid="crew-share" data-workspace-id={workspaceId} data-kind={resource.kind} data-resource-id={resource.id} />,
}))

import { CrewSettings } from "../crew-settings"

afterEach(cleanup)
const crew = { id: "c1", name: "Research", slug: "research", description: "", icon: "code", color: "blue" } as never

// v67 ends the Settings tab with the Danger zone. It used to sit in the
// middle: People, Budget and the command line were drawn below the delete
// button.
describe("Settings tab order", () => {
  it("puts the Danger zone after every other card", async () => {
    render(<CrewSettings workspaceId="ws" crew={crew} agents={[]} patch={vi.fn()} onApplyAvatarStyle={vi.fn()} onDelete={vi.fn()} />)
    const danger = await screen.findByRole("heading", { name: "Danger zone" })
    expect(screen.getByTestId("crew-share")).toHaveAttribute("data-workspace-id", "ws")
    expect(screen.getByTestId("crew-share")).toHaveAttribute("data-kind", "crew")
    expect(screen.getByTestId("crew-share")).toHaveAttribute("data-resource-id", "c1")
    const cli = await screen.findByTestId("cli")
    const budget = await screen.findByRole("region", { name: "Budget" })
    const FOLLOWING = Node.DOCUMENT_POSITION_FOLLOWING
    expect(cli.compareDocumentPosition(danger) & FOLLOWING).toBeTruthy()
    expect(budget.compareDocumentPosition(danger) & FOLLOWING).toBeTruthy()
  })
})
