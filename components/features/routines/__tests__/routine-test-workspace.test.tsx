import { useState } from "react"
import { fireEvent, render, screen } from "@testing-library/react"
import { expect, it, vi } from "vitest"
vi.mock("../routine-fixture-test", () => ({
  RoutineFixtureTest: ({ selectedStepId }: { selectedStepId: string }) => {
    const [text, setText] = useState("")
    return (
      <input
        aria-label={`Sample for ${selectedStepId}`}
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
    )
  },
}))
import { RoutineTestWorkspace } from "../routine-test-workspace"
const props = {
  workspaceId: "ws",
  definition: {},
  busy: false,
  result: null,
  onValidate: vi.fn(),
  onOpenCode: vi.fn(),
  parseError: null,
  stepId: "review",
}

it("tests the selected step without nested navigation or starting live work", () => {
  render(<RoutineTestWorkspace {...props} />)
  expect(screen.getByRole("textbox", { name: "Sample for review" })).toBeVisible()
  expect(screen.queryByRole("tablist")).not.toBeInTheDocument()
  expect(screen.queryByText("Compare versions")).not.toBeInTheDocument()
  expect(props.onValidate).not.toHaveBeenCalled()
  expect(screen.getByText(/does not run agents, scripts or HTTP calls/)).toBeVisible()
})

it("keeps samples while the routine test result updates", () => {
  const view = render(<RoutineTestWorkspace {...props} />)
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "saved sample" } })
  fireEvent.click(screen.getByRole("button", { name: "Test routine" }))
  expect(props.onValidate).toHaveBeenCalledTimes(1)
  view.rerender(
    <RoutineTestWorkspace {...props} result={{ passed: true, details: "References exist" }} />,
  )
  expect(screen.getByRole("textbox")).toHaveValue("saved sample")
  expect(screen.getByRole("status")).toHaveTextContent("Test passed")
})

it("prevents validation while busy or without a parsed definition", () => {
  const onValidate = vi.fn()
  const view = render(<RoutineTestWorkspace {...props} busy onValidate={onValidate} />)
  fireEvent.click(screen.getByRole("button", { name: "Testing…" }))
  expect(onValidate).not.toHaveBeenCalled()
  view.rerender(<RoutineTestWorkspace {...props} definition={null} onValidate={onValidate} />)
  expect(screen.getByRole("button", { name: "Test routine" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Test routine" }))
  expect(onValidate).not.toHaveBeenCalled()
})

it("explains a failed validation and opens code to repair a parse error", () => {
  const onOpenCode = vi.fn()
  const view = render(<RoutineTestWorkspace {...props} result={{ passed: false, details: "Missing credential: deploy" }} parseError="Invalid YAML" onOpenCode={onOpenCode} />)
  expect(screen.getByRole("status")).toHaveTextContent("Test needs attention")
  expect(screen.getByRole("status")).toHaveTextContent("Missing credential: deploy")
  fireEvent.click(screen.getByRole("button", { name: "Invalid YAML · Open Code" }))
  expect(onOpenCode).toHaveBeenCalledTimes(1)
  view.rerender(<RoutineTestWorkspace {...props} parseError="Invalid YAML" onOpenCode={undefined} />)
  expect(screen.queryByRole("button", { name: /Open Code/ })).not.toBeInTheDocument()
})
