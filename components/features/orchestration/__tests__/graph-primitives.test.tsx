import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { Position, type EdgeProps, type NodeProps } from "@xyflow/react"
import { PipelineRunNode, type PipelineRunData } from "../pipeline-run-node"
import { AgentCardNode, type AgentCardData } from "../agent-card-node"
import { AnimatedEdge } from "../animated-edge"
import { STATUS_COLORS, GRAPH_CHROME } from "@/lib/colors"

// Nodes are rendered by React Flow with these common, non-interactive geometry props.
function nodeProps(data: Record<string, unknown>): NodeProps {
  return { id: "node", data, type: "custom", dragging: false, zIndex: 1,
    isConnectable: false, selectable: true, deletable: false, draggable: false,
    selected: false, positionAbsoluteX: 0, positionAbsoluteY: 0 }
}
const run: PipelineRunData = { pipelineSlug: "nightly", runId: "run-1", status: "queued" }
const agent: AgentCardData = { name: "Researcher", slug: "researcher", avatarSeed: null,
  avatarStyle: null, role: "Analyst", isLead: false, status: "idle", model: "model-a",
  tokenCount: 0, cost: 0, skills: [], memoryEnabled: false, currentTask: null }
const edge: EdgeProps = { id: "edge", source: "a", target: "b", sourceX: 0, sourceY: 0,
  targetX: 200, targetY: 100, sourcePosition: Position.Right, targetPosition: Position.Left }
afterEach(cleanup)

describe("routine graph cards", () => {
  it.each([ ["running", "Running"], ["completed", "Completed"], ["failed", "Failed"],
    ["dry_run", "Dry run"], ["queued", "Queued"], ["future-state", "Queued"] ])("presents %s with a useful accessible name", (status, label) => {
    const { container } = render(<PipelineRunNode {...nodeProps({ ...run, status })} />)
    expect(screen.getByRole("button", { name: `Routine run nightly (${label})` })).toHaveAttribute("tabindex", "0")
    expect(Boolean(container.querySelector(".animate-pulse"))).toBe(status === "running")
    expect(screen.queryByText(/steps/)).not.toBeInTheDocument()
  })
  it("opens the same run with pointer, Enter and Space but not unrelated keys", () => {
    const onClick = vi.fn()
    render(<PipelineRunNode {...nodeProps({ ...run, pipelineName: "Nightly report", onClick,
      stepIndex: 2, stepCount: 4, tierLabel: "fast", costUsd: 0.123456, authorCrewLabel: "Research" })} />)
    const button = screen.getByRole("button", { name: "Routine run Nightly report (Queued)" })
    fireEvent.click(button)
    fireEvent.keyDown(button, { key: "Enter" })
    fireEvent.keyDown(button, { key: " " })
    fireEvent.keyDown(button, { key: "Escape" })
    expect(onClick.mock.calls).toEqual([["run-1"], ["run-1"], ["run-1"]])
    expect(screen.getByText("2/4 steps")).toBeInTheDocument()
    expect(screen.getByText("$0.1235")).toBeInTheDocument()
    expect(screen.getByText("fast")).toBeInTheDocument()
    expect(screen.getByText("authored by Research")).toBeInTheDocument()
  })
  it("leaves absent optional controls and zero cost unadorned", () => {
    render(<PipelineRunNode {...nodeProps({ ...run, stepIndex: 0, costUsd: 0 })} />)
    const button = screen.getByRole("button")
    fireEvent.click(button)
    fireEvent.keyDown(button, { key: " " })
    expect(button).not.toHaveTextContent("steps")
    expect(button).not.toHaveTextContent("$")
  })
  it.each([-1, undefined])("does not present unknown step count %s as real progress", (stepCount) => {
    render(<PipelineRunNode {...nodeProps({ ...run, stepIndex: -1, stepCount })} />)
    expect(screen.queryByText(/steps/)).not.toBeInTheDocument()
  })
})

describe("agent graph cards", () => {
  it("supports pointer and keyboard navigation to the agent", () => {
    const onAgentClick = vi.fn()
    render(<AgentCardNode {...nodeProps({ ...agent, onAgentClick })} />)
    const button = screen.getByRole("button", { name: "Open agent Researcher" })
    expect(button).toHaveAttribute("tabindex", "0")
    fireEvent.click(button)
    fireEvent.keyDown(button, { key: "Enter" })
    fireEvent.keyDown(button, { key: " " })
    fireEvent.keyDown(button, { key: "ArrowDown" })
    expect(onAgentClick.mock.calls).toEqual([["researcher"], ["researcher"], ["researcher"]])
  })
  it.each([
    ["active", 1_250_000, "1.3M", "bg-success"],
    ["idle", 1200, "1.2k", "bg-muted-foreground"],
    ["blocked", 999, "999", "bg-warn"],
    ["error", 0, "0", "bg-destructive"],
    ["future-state", 1, "1", "bg-muted-foreground"],
  ])("presents %s state and compact token metrics", (status, tokenCount, formatted, color) => {
    const { container } = render(<AgentCardNode {...nodeProps({ ...agent, status, tokenCount })} />)
    expect(screen.getByText(`${formatted} tok`)).toBeInTheDocument()
    expect(screen.getByText("$0.00")).toBeInTheDocument()
    expect(container.querySelector(`.${color}`)).toBeInTheDocument()
    expect(Boolean(container.querySelector(".animate-ping"))).toBe(status === "active")
    expect(screen.queryByText("Lead")).not.toBeInTheDocument()
    fireEvent.click(screen.getByText("Researcher"))
    fireEvent.keyDown(screen.getByRole("button"), { key: "Enter" })
  })
  it("shows lead, memory, current work and bounded skills with overflow", () => {
    const { container, rerender } = render(<AgentCardNode {...nodeProps({ ...agent,
      avatarSeed: "unique", avatarStyle: "bottts", isLead: true, memoryEnabled: true,
      skills: ["search", "review", "code", "deploy"], currentTask: "Check findings", cost: 3.456 })} />)
    expect(screen.getByText("Lead")).toBeInTheDocument()
    expect(container.querySelector(".lucide-brain")).toBeInTheDocument()
    expect(screen.getByText("Check findings")).toBeInTheDocument()
    expect(screen.getByText("$3.46")).toBeInTheDocument()
    expect(screen.getByText("+1 more")).toBeInTheDocument()
    expect(screen.queryByText("deploy")).not.toBeInTheDocument()
    rerender(<AgentCardNode {...nodeProps({ ...agent, skills: ["search"] })} />)
    expect(screen.getByText("search")).toBeInTheDocument()
    expect(screen.queryByText(/more/)).not.toBeInTheDocument()
  })
})

describe("animated graph edges", () => {
  it("draws idle and active paths, respects custom colors and dims without animation", () => {
    const { container, rerender } = render(<svg><AnimatedEdge {...edge} /></svg>)
    expect(container.querySelector(".edge-dash-idle")).toHaveAttribute("stroke-width", "1.8")
    expect(container.querySelector("stop")).toHaveAttribute("stop-color", STATUS_COLORS.IN_PROGRESS)
    expect(container.querySelector("circle")).not.toBeInTheDocument()
    rerender(<svg><AnimatedEdge {...edge} data={{ active: true, color: "#123456" }} /></svg>)
    expect(container.querySelector(".edge-dash-active")).toHaveAttribute("stroke-width", "2.5")
    expect(container.querySelectorAll("circle")).toHaveLength(2)
    expect(container.querySelector("stop")).toHaveAttribute("stop-color", "#123456")
    expect(container.querySelector("style")).toHaveTextContent("prefers-reduced-motion")
    rerender(<svg><AnimatedEdge {...edge} data={{ active: true, dimmed: true }} /></svg>)
    expect(container.querySelectorAll("path")).toHaveLength(1)
    expect(container.querySelector("path")).toHaveAttribute("stroke", GRAPH_CHROME.dimmedEdge)
    expect(container.querySelector("circle")).not.toBeInTheDocument()
  })
  it("updates a marker without requiring a position or color change", () => {
    const { container, rerender } = render(<svg><AnimatedEdge {...edge} markerEnd="url(#one)" /></svg>)
    rerender(<svg><AnimatedEdge {...edge} markerEnd="url(#two)" /></svg>)
    expect(container.querySelector(".edge-dash-idle")).toHaveAttribute("marker-end", "url(#two)")
  })
  it.each(["sourcePosition", "targetPosition"] as const)("updates the curve when %s changes without moving a node", (position) => {
    const { container, rerender } = render(<svg><AnimatedEdge {...edge} /></svg>)
    const original = container.querySelector("path")!.getAttribute("d")
    rerender(<svg><AnimatedEdge {...edge} {...{ [position]: Position.Bottom }} /></svg>)
    expect(container.querySelector("path")!.getAttribute("d")).not.toBe(original)
  })
  it.each(["sourceX", "sourceY", "targetX", "targetY"] as const)("tracks movement along %s", (coordinate) => {
    const { container, rerender } = render(<svg><AnimatedEdge {...edge} data={{}} /></svg>)
    const original = container.querySelector("path")!.getAttribute("d")
    rerender(<svg><AnimatedEdge {...edge} data={{}} {...{ [coordinate]: edge[coordinate] + 40 }} /></svg>)
    expect(container.querySelector("path")!.getAttribute("d")).not.toBe(original)
    const changed = container.querySelector("path")!.getAttribute("d")
    rerender(<svg><AnimatedEdge {...edge} data={{}} {...{ [coordinate]: edge[coordinate] + 40 }} /></svg>)
    expect(container.querySelector("path")!.getAttribute("d")).toBe(changed)
  })
  it("updates activity and dimming independently of geometry and color", () => {
    const { container, rerender } = render(<svg><AnimatedEdge {...edge} data={{ color: "#123456", active: false, dimmed: false }} /></svg>)
    expect(container.querySelector("circle")).not.toBeInTheDocument()
    rerender(<svg><AnimatedEdge {...edge} data={{ color: "#123456", active: true, dimmed: false }} /></svg>)
    expect(container.querySelectorAll("circle")).toHaveLength(2)
    rerender(<svg><AnimatedEdge {...edge} data={{ color: "#123456", active: true, dimmed: true }} /></svg>)
    expect(container.querySelector("path")).toHaveAttribute("stroke", GRAPH_CHROME.dimmedEdge)
    expect(container.querySelector("circle")).not.toBeInTheDocument()
  })
  it("gives simultaneous edges independent gradients and glow references", () => {
    const { container } = render(<svg><AnimatedEdge {...edge} data={{ active: true }} /><AnimatedEdge {...edge} id="other" data={{ active: true }} /></svg>)
    const ids = Array.from(container.querySelectorAll("[id]"), (element) => element.id)
    expect(new Set(ids).size).toBe(ids.length)
    for (const path of container.querySelectorAll(".edge-dash-active")) {
      expect(ids).toContain(path.getAttribute("stroke")!.slice(5, -1))
    }
  })
})
