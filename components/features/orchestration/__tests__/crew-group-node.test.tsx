import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { ReactFlowProvider, type NodeProps } from "@xyflow/react"
import { CrewGroupNode, type CrewGroupData } from "../crew-group-node"

afterEach(cleanup)
const crew: CrewGroupData = { label: "Research", slug: "research", crewId: "crew-id", color: null, icon: null, agentCount: 3, collapsed: false, taskCount: 4, activeCount: 0, completedCount: 0, failedCount: 0 }
function props(patch: Partial<CrewGroupData> = {}): NodeProps {
  return { id: "group", data: { ...crew, ...patch }, type: "custom", dragging: false, zIndex: 1, isConnectable: false, selectable: true, deletable: false, draggable: false, selected: false, positionAbsoluteX: 0, positionAbsoluteY: 0 }
}

it("names the collapse control, exposes its state and supports pointer and keyboard without selecting the parent", () => {
  const toggle = vi.fn(), parent = vi.fn()
  const { rerender } = render(<div onClick={parent}><CrewGroupNode {...props({ onToggleCollapse: toggle })} /></div>, { wrapper: ReactFlowProvider })
  const control = screen.getByRole("button", { name: "Collapse Research crew" })
  expect(control).toHaveAttribute("aria-expanded", "true")
  fireEvent.click(control)
  fireEvent.keyDown(control, { key: "Enter" })
  fireEvent.keyDown(control, { key: " " })
  fireEvent.keyDown(control, { key: "Escape" })
  expect(toggle.mock.calls).toEqual([["crew-id"], ["crew-id"], ["crew-id"]])
  expect(parent).not.toHaveBeenCalled()
  rerender(<div onClick={parent}><CrewGroupNode {...props({ collapsed: true, onToggleCollapse: toggle })} /></div>)
  expect(screen.getByRole("button", { name: "Expand Research crew" })).toHaveAttribute("aria-expanded", "false")
  expect(screen.getByText("4 tasks")).toBeInTheDocument()
})

it("shows collapsed status counts without flattening active and failed work into a total", () => {
  const { rerender } = render(<CrewGroupNode {...props({ collapsed: true, color: "blue", icon: "rocket", activeCount: 1, completedCount: 2, failedCount: 1 })} />, { wrapper: ReactFlowProvider })
  expect(screen.getByText("1 running")).toBeInTheDocument()
  expect(screen.getByText("2 done")).toBeInTheDocument()
  expect(screen.getByText("1 failed")).toBeInTheDocument()
  expect(screen.queryByText("4 tasks")).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Expand Research crew" }))
  fireEvent.keyDown(screen.getByRole("button", { name: "Expand Research crew" }), { key: "Enter" })
  rerender(<CrewGroupNode {...props({ collapsed: true, color: "unrecognised", taskCount: 0 })} />)
  expect(screen.queryByText(/running|done|failed|tasks/)).not.toBeInTheDocument()
  rerender(<CrewGroupNode {...props({ activeCount: 1 })} />)
  expect(screen.queryByText("1 running")).not.toBeInTheDocument()
})
