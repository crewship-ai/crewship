import type { ReactNode } from "react"
import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { MarkerType, Position, type EdgeProps } from "@xyflow/react"
import { PermissionEdge, getPermissionMarkers } from "../permission-edge"
import { DIRECTION_COLORS, GRAPH_CHROME } from "@/lib/colors"
// Keep path calculation real; place the label in an SVG foreignObject instead
// of React Flow's graph-wide portal so its text is observable in this unit.
vi.mock("@xyflow/react", async (original) => ({ ...await original<typeof import("@xyflow/react")>(),
  EdgeLabelRenderer: ({ children }: { children: ReactNode }) => <foreignObject>{children}</foreignObject> }))
const edge: EdgeProps = { id: "permission", source: "a", target: "b", sourceX: 0, sourceY: 0,
  targetX: 200, targetY: 100, sourcePosition: Position.Right, targetPosition: Position.Left }
afterEach(cleanup)
it.each(["bidirectional", "unidirectional"] as const)("draws the %s permission with matching markers and readable direction", (direction) => {
  const markers = getPermissionMarkers(direction)
  expect(markers.markerEnd).toEqual({ type: MarkerType.ArrowClosed, color: DIRECTION_COLORS[direction], width: 16, height: 16 })
  expect(markers.markerStart).toEqual(direction === "bidirectional" ? markers.markerEnd : undefined)
  const { container } = render(<svg><PermissionEdge {...edge} data={{ direction, status: "active" }} markerEnd="url(#end)" markerStart="url(#start)" /></svg>)
  expect(screen.getByText(direction === "bidirectional" ? "both" : "one-way")).toBeInTheDocument()
  expect(screen.getByText(direction === "bidirectional" ? "↔" : "→")).toBeInTheDocument()
  const path = container.querySelector("#permission")!
  expect(path).toHaveAttribute("stroke", DIRECTION_COLORS[direction])
  expect(path).toHaveAttribute("marker-end", "url(#end)")
  expect(path).toHaveAttribute("marker-start", "url(#start)")
  expect(path.getAttribute("d")).toContain("200,100")
})
it("uses one-way defaults without metadata and removes interactive decoration when dimmed", () => {
  const { container, rerender } = render(<svg><PermissionEdge {...edge} /></svg>)
  expect(screen.getByText("one-way")).toBeInTheDocument()
  rerender(<svg><PermissionEdge {...edge} data={{ dimmed: true, direction: "bidirectional" }} /></svg>)
  expect(screen.queryByText("both")).not.toBeInTheDocument()
  expect(container.querySelectorAll("path")).toHaveLength(1)
  expect(container.querySelector("path")).toHaveAttribute("stroke", GRAPH_CHROME.dimmedEdge)
  expect(container.querySelector("animate")).not.toBeInTheDocument()
})
