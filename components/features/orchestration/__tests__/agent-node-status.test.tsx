import { cleanup, render as renderUI, screen } from "@testing-library/react"
import type { ReactElement } from "react"
import { afterEach, expect, it } from "vitest"
import { ReactFlowProvider, type NodeProps } from "@xyflow/react"
import { AgentNode } from "../agent-node"

afterEach(cleanup)
function render(element: ReactElement) {
  return renderUI(element, { wrapper: ReactFlowProvider })
}
const data = { label: "Write report", status: "PENDING", agentName: "Report Writer", agentSlug: "writer", avatarSeed: null, avatarStyle: null, iteration: null, maxIterations: null, tokenCount: null, estimatedCost: null, durationMs: null }
function props(overrides: Record<string, unknown> = {}): NodeProps {
  return { id: "node", data: { ...data, ...overrides }, type: "custom", dragging: false, zIndex: 1, isConnectable: false, selectable: true, deletable: false, draggable: false, selected: false, positionAbsoluteX: 0, positionAbsoluteY: 0 }
}

it.each([["COMPLETED", "Completed"], ["IN_PROGRESS", "Running"], ["FAILED", "Failed"], ["BLOCKED", "Blocked"], ["PENDING", "Pending"], ["REVIEW", "Review"], ["SKIPPED", "Skipped"], ["AWAITING_APPROVAL", "Awaiting Approval"], ["new-status", "Pending"]])("labels %s and only shows live activity while running", (status, label) => {
  render(<AgentNode {...props({ status, activitySnippet: "Reading sources" })} />)
  expect(screen.getByText(label)).toBeInTheDocument()
  expect(screen.getByText("@writer")).toBeInTheDocument()
  expect(screen.queryByText("Reading sources") !== null).toBe(status === "IN_PROGRESS")
})

it.each([[999, "999 tok"], [1500, "1.5k tok"], [1250000, "1.3M tok"]])("formats %s tokens alongside bounded iteration and actual cost", (tokenCount, tokenLabel) => {
  render(<AgentNode {...props({ tokenCount, estimatedCost: 0.125, durationMs: 1500, iteration: 2, maxIterations: 3, avatarSeed: "custom", avatarStyle: "bottts" })} />)
  expect(screen.getByText(tokenLabel)).toBeInTheDocument()
  expect(screen.getByText("$0.1250")).toBeInTheDocument()
  expect(screen.getByText("2/3")).toBeInTheDocument()
  expect(screen.getByText("1.5s")).toBeInTheDocument()
})

it("defaults the iteration and handles an unassigned named agent", () => {
  render(<AgentNode {...props({ agentSlug: null, agentName: "_ Unassigned", maxIterations: 4, tokenCount: 0, estimatedCost: 0, durationMs: 0 })} />)
  expect(screen.getByText("unassigned")).toBeInTheDocument()
  expect(screen.getByText("1/4")).toBeInTheDocument()
  expect(screen.queryByText(/ tok$/)).not.toBeInTheDocument()
  expect(screen.queryByText(/^\$/)).not.toBeInTheDocument()
})

it("renders anonymous nodes without inventing an avatar or iteration counter", () => {
  const { container } = render(<AgentNode {...props({ agentSlug: null, agentName: "", maxIterations: 1, status: "IN_PROGRESS", activitySnippet: "" })} />)
  expect(screen.getByText("Write report")).toBeInTheDocument()
  expect(screen.getByText("unassigned")).toBeInTheDocument()
  expect(container.querySelector("img")).toBeNull()
  expect(screen.queryByText("1/1")).not.toBeInTheDocument()
})
