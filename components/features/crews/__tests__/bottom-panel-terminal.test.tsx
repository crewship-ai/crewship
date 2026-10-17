import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { BottomPanelTerminal } from "../bottom-panel-terminal"
import { useTerminal, type TerminalStatus } from "@/hooks/use-terminal"

vi.mock("@/hooks/use-terminal", () => ({ useTerminal: vi.fn() }))
const terminal = vi.mocked(useTerminal)
const disconnect = vi.fn()
const props = { crewId: "crew-id", crewSlug: "research", agentSlug: "writer", agentName: "Writer" }
beforeEach(() => { terminal.mockReset(); disconnect.mockReset() })
afterEach(cleanup)

it.each(["connecting", "connected", "disconnected", "error"] as TerminalStatus[])("presents %s and only offers reconnect when offline", status => {
  terminal.mockReturnValue({ status, disconnect })
  const { unmount } = render(<BottomPanelTerminal {...props} />)
  expect(screen.getByText("Writer")).toBeInTheDocument()
  expect(screen.getByText("· /crew/agents/writer")).toBeInTheDocument()
  expect(terminal).toHaveBeenLastCalledWith(expect.objectContaining({ crewId: "crew-id", crewSlug: "research", agentSlug: "writer", mode: "shell", enabled: true, key: 0 }))
  const offline = status === "disconnected" || status === "error"
  expect(screen.queryByRole("button", { name: "Reconnect" }) !== null).toBe(offline)
  if (offline) {
    fireEvent.click(screen.getByRole("button", { name: "Reconnect" }))
    expect(terminal).toHaveBeenLastCalledWith(expect.objectContaining({ key: 1 }))
    fireEvent.click(screen.getByRole("button", { name: "Reconnect" }))
    expect(terminal).toHaveBeenLastCalledWith(expect.objectContaining({ key: 2 }))
  }
  expect(disconnect).not.toHaveBeenCalled()
  unmount()
  expect(disconnect).toHaveBeenCalledOnce()
})

it("crew context opens a shell without agent_slug, in /crew/shared", () => {
  terminal.mockReturnValue({ status: "connected", disconnect })
  render(<BottomPanelTerminal crewId="crew-id" crewSlug="research" agentName="research" />)
  expect(screen.getByText("· /crew/shared")).toBeInTheDocument()
  expect(terminal).toHaveBeenLastCalledWith(expect.objectContaining({ crewId: "crew-id", crewSlug: "research", agentSlug: undefined, mode: "shell" }))
})
