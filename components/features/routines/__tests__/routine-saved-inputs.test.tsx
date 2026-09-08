import { describe, expect, it } from "vitest"
import { fireEvent, render, screen, within } from "@testing-library/react"
import { RoutineSavedInputs } from "../routine-saved-inputs"

describe("saved run inputs", () => {
  it("uses historical labels and actual values without substituting defaults", () => {
    render(<RoutineSavedInputs values={{ text: "Original\nsecond line", count: 0, approved: false, empty: "", nil: null, extra_key: "9007199254740993" }} definition={{ inputs: [{ name: "text", label: "Source text", description: "Text to summarize", default: "NEW DEFAULT" }, { name: "missing", default: "MUST NOT APPEAR" }] }} />)
    expect(within(screen.getByRole("region", { name: "Source text" })).getByText(/Original/)).toHaveTextContent("Original second line")
    expect(screen.getByText("0")).toBeInTheDocument()
    expect(screen.getByText("No", { exact: true })).toBeInTheDocument()
    expect(screen.getByText("Empty text")).toBeInTheDocument()
    expect(screen.getByText("No value (null)")).toBeInTheDocument()
    expect(screen.getByText("9007199254740993")).toBeInTheDocument()
    expect(screen.getByText("Extra key")).toBeInTheDocument()
    expect(screen.queryByText("NEW DEFAULT")).not.toBeInTheDocument()
    expect(screen.queryByText("MUST NOT APPEAR")).not.toBeInTheDocument()
    expect(within(screen.getByRole("region", { name: "Missing" })).getAllByText("Not supplied")).toHaveLength(2)
  })
  it("renders lists and nested fields without interpreting text as HTML", () => {
    const { container } = render(<RoutineSavedInputs values={{ targets: ["first", "second"], options: { dry_run: true }, text: '<img src=x onerror="alert(1)">' }} definition={null} />)
    expect(screen.getByText("Item 1")).toBeInTheDocument()
    expect(screen.getByText("Dry run")).toBeInTheDocument()
    expect(screen.getByText("Yes", { exact: true })).toBeInTheDocument()
    expect(container.querySelector("img")).toBeNull()
    expect(container.querySelector("pre")).toBeNull()
    fireEvent.click(screen.getByText("Technical details · saved JSON"))
  })
  it("does not describe unavailable inputs as an empty submission", () => {
    render(<RoutineSavedInputs values={undefined} definition={null} />)
    expect(screen.getByText("Saved inputs are unavailable for this run.")).toBeInTheDocument()
    expect(screen.queryByText("This run has no saved inputs.")).not.toBeInTheDocument()
  })
  it("keeps an explicitly empty submission distinct", () => {
    render(<RoutineSavedInputs values={{}} definition={null} />)
    expect(screen.getByText("This run has no saved inputs.")).toBeInTheDocument()
  })
})
