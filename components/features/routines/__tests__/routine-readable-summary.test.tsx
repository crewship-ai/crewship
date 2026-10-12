import { cleanup, render, screen, within } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"
import { RoutineReadableSummary } from "../routine-readable-summary"

afterEach(cleanup)

it.each([undefined, null, {}, { steps: "unfinished draft" }])("renders an incomplete definition without inventing steps", (definition) => {
  render(<RoutineReadableSummary definition={definition} />)
  expect(screen.getByText("Runs manually / on demand")).toBeInTheDocument()
  expect(screen.getByText("No steps declared.")).toBeInTheDocument()
  expect(screen.queryByRole("list")).not.toBeInTheDocument()
  expect(screen.queryByText("Needs")).not.toBeInTheDocument()
})

it("renders ordered, readable steps and deduplicated integration requirements from the actual DSL", () => {
  const { container } = render(<RoutineReadableSummary className="preview-summary" definition={{
    trigger: { type: "webhook" },
    steps: [
      { type: "agent_run", agent_slug: "alex", prompt: "Review this report" },
      { type: "transform", transform: { expression: "input.total" } },
      { type: "code", code: { runtime: "python" } },
      null,
    ],
    integrations_required: ["slack", " github ", "slack"],
  }} />)
  expect(container.firstElementChild).toHaveClass("preview-summary")
  expect(screen.getByText("When its webhook is called")).toBeInTheDocument()
  const rows = screen.getAllByRole("listitem")
  expect(rows).toHaveLength(4)
  expect(within(rows[0]).getByText("Ask alex")).toBeInTheDocument()
  expect(within(rows[0]).getByText("Review this report")).toBeInTheDocument()
  expect(within(rows[1]).getByText("Transform data")).toBeInTheDocument()
  expect(within(rows[1]).getByText("input.total")).toBeInTheDocument()
  expect(within(rows[2]).getByText("Run python code")).toBeInTheDocument()
  expect(within(rows[2]).getByText("code · python")).toBeInTheDocument()
  expect(within(rows[3]).getByText("Step")).toBeInTheDocument()
  expect(screen.getAllByTitle("slack")).toHaveLength(1)
  expect(screen.getByTitle("slack")).toHaveTextContent("Slack")
  expect(screen.getByTitle("github")).toHaveTextContent("GitHub")
})
