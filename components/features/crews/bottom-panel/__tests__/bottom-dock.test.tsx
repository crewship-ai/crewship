import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

const h = vi.hoisted(() => ({ role: "ADMIN" as string }))
vi.mock("@/hooks/use-user-preference", () => ({ useUserPreference: () => [320, vi.fn(), { loading: false }] }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: h.role, abilities: { can: () => true } }) }))
vi.mock("next/dynamic", () => ({
  default: () => (p: { agentSlug?: string; crewSlug: string }) => <p data-testid="shell">{p.agentSlug ? `agent:${p.agentSlug}` : `crew:${p.crewSlug}`}</p>,
}))
vi.mock("../files-tab", () => ({ FilesTab: () => <p>files</p> }))
vi.mock("../exec-tab", () => ({ ExecTab: () => <p>log</p> }))

import { BottomPanel } from "../index"

const crew = { kind: "crew" as const, crewId: "c1", crewSlug: "research" }
const agent = { kind: "agent" as const, agentId: "a1", agentSlug: "scout", agentName: "Scout", crewId: "c1", crewSlug: "research" }

beforeEach(() => { h.role = "ADMIN" })
afterEach(cleanup)

describe("the Crews dock", () => {
  it("dock offers only Terminal and Files", () => {
    render(<BottomPanel workspaceId="ws" context={crew} />)
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual(["Terminal", "Files"])
  })

  it("an agent adds its run log, labelled Logs", () => {
    render(<BottomPanel workspaceId="ws" context={agent} tabs={["terminal", "files", "exec"]} />)
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual(["Terminal", "Files", "Logs"])
  })

  it("the Terminal button opens the dock on the crew's shell", () => {
    const { rerender } = render(<BottomPanel workspaceId="ws" context={crew} request={null} />)
    expect(screen.queryByTestId("shell")).toBeNull()
    rerender(<BottomPanel workspaceId="ws" context={crew} request={{ tab: "terminal", at: 1 }} />)
    expect(screen.getByTestId("shell")).toHaveTextContent("crew:research")
  })

  it("an agent's shell opens in its folder", () => {
    render(<BottomPanel workspaceId="ws" context={agent} initialOpen initialTab="terminal" />)
    expect(screen.getByTestId("shell")).toHaveTextContent("agent:scout")
  })

  it("viewer sees a locked terminal", () => {
    h.role = "VIEWER"
    render(<BottomPanel workspaceId="ws" context={crew} initialOpen initialTab="terminal" />)
    expect(screen.queryByTestId("shell")).toBeNull()
    expect(screen.getByText(/Viewers cannot open a shell/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("tab", { name: "Files" }))
    expect(screen.getByText("files")).toBeInTheDocument()
  })
})
