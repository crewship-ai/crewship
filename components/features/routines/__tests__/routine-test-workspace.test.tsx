import { useState } from "react"
import { fireEvent, render, screen } from "@testing-library/react"
import { expect, it, vi } from "vitest"
vi.mock("../routine-fixture-test", () => ({
  RoutineFixtureTest: () => {
    const [text, setText] = useState("")
    return (
      <input aria-label="Fixture data" value={text} onChange={(e) => setText(e.target.value)} />
    )
  },
}))
vi.mock("../routine-comparison", () => ({
  RoutineComparison: ({ onBusyChange }: { onBusyChange: (busy: boolean) => void }) => (
    <button onClick={() => onBusyChange(true)}>Begin comparison</button>
  ),
}))
import { RoutineTestWorkspace } from "../routine-test-workspace"
const props = {
  published: true,
  workspaceId: "ws",
  slug: "demo",
  definition: {},
  busy: false,
  result: null,
  onValidate: vi.fn(),
  onPublish: vi.fn(),
  onOpenCode: vi.fn(),
  parseError: null,
}
const select = (name: string) =>
  fireEvent.mouseDown(screen.getByRole("tab", { name }), { button: 0, ctrlKey: false })

it("preserves fixture work when changing test methods and never starts work just by switching", () => {
  render(<RoutineTestWorkspace {...props} />)
  select("Test a step")
  fireEvent.change(screen.getByRole("textbox", { name: "Fixture data" }), {
    target: { value: "saved sample" },
  })
  select("Check definition")
  expect(screen.queryByRole("textbox", { name: "Fixture data" })).not.toBeInTheDocument()
  select("Test a step")
  expect(screen.getByRole("textbox", { name: "Fixture data" })).toHaveValue("saved sample")
  expect(props.onValidate).not.toHaveBeenCalled()
})

it("keeps live comparison status visible when inspecting another method", () => {
  render(<RoutineTestWorkspace {...props} />)
  select("Compare versions")
  fireEvent.click(screen.getByRole("button", { name: "Begin comparison" }))
  select("Check definition")
  expect(screen.getByRole("status")).toHaveTextContent("Live comparison is running")
  fireEvent.click(screen.getByRole("button", { name: "View progress" }))
  expect(screen.getByRole("tab", { name: "Compare versions" })).toHaveAttribute(
    "aria-selected",
    "true",
  )
})

it("explains why a new draft cannot run a version comparison", () => {
  render(<RoutineTestWorkspace {...props} published={false} />)
  select("Compare versions")
  expect(screen.getByText("Publish a version to compare real runs")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Begin comparison" })).not.toBeInTheDocument()
})
