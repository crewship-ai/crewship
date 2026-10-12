import type { ComponentProps } from "react"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { RoutineDefinitionCanvas } from "../routine-definition-canvas"
import type { TraceCanvas } from "@/components/features/activity/trace-canvas"
import { buildTraceGraph } from "@/lib/trace/build-trace-graph"

// Exercise the real definition-to-status graph projection at the canvas
// boundary; React Flow's layout and viewport have their own interaction tests.
vi.mock("@/components/features/activity/trace-canvas", () => ({
  TraceCanvas: (props: ComponentProps<typeof TraceCanvas>) => {
    const { nodes } = buildTraceGraph(props.run!, props.dsl, props)
    return <section aria-label="Definition graph" data-selected={props.selectedStepId} data-focus={props.focusStepId}>
      {nodes.filter(node => node.type === "traceStep").map(node => <button key={node.id} onClick={() => props.onStepSelect(node.id)}>{node.id}: {String(node.data.status)}</button>)}
      <button onClick={() => props.onStepSelect(null)}>Clear step</button>
      <output>{props.run?.id}</output>
    </section>
  },
}))
afterEach(cleanup)
const definition = { steps: [{ id: "a", type: "transform", expression: "input" }, { id: "b", type: "waitpoint", depends_on: ["a"] }] }
it("renders a definition without fabricating completed/running steps or approval controls", () => {
  const onStepSelect = vi.fn()
  render(<RoutineDefinitionCanvas slug="nightly" name="Nightly" definition={definition} onStepSelect={onStepSelect} selectedStepId="a" focusStepId="b" />)
  expect(screen.getByText("Definition · not a run")).toBeInTheDocument()
  expect(screen.getByText("definition:nightly")).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "a: pending" })).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "b: pending" })).toBeInTheDocument()
  expect(screen.getByRole("region", { name: "Definition graph" })).toHaveAttribute("data-selected", "a")
  expect(screen.getByRole("region", { name: "Definition graph" })).toHaveAttribute("data-focus", "b")
  fireEvent.click(screen.getByRole("button", { name: "b: pending" }))
  fireEvent.click(screen.getByRole("button", { name: "Clear step" }))
  expect(onStepSelect.mock.calls).toEqual([["b"], [null]])
})
it.each([null, undefined, {}, { steps: "invalid" }, "invalid" as never])("handles incomplete definitions without inventing steps: %s", definition => {
  render(<RoutineDefinitionCanvas slug="draft" name="Draft" definition={definition} />)
  expect(screen.getAllByRole("button")).toHaveLength(1)
  fireEvent.click(screen.getByRole("button", { name: "Clear step" }))
})
it("switches definition identity and replaces the selected routine's steps", () => {
  const { rerender } = render(<RoutineDefinitionCanvas slug="first" name="First" definition={definition} />)
  rerender(<RoutineDefinitionCanvas slug="next" name="Next" definition={{ steps: [{ id: "c", type: "http" }] }} className="custom-height" />)
  expect(screen.queryByRole("button", { name: "a: pending" })).not.toBeInTheDocument()
  expect(screen.getByRole("button", { name: "c: pending" })).toBeInTheDocument()
  expect(screen.getByText("definition:next")).toBeInTheDocument()
})
