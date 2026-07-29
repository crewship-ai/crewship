import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { CheckCircle2, KeyRound, Workflow } from "lucide-react"

import {
  DetailCard, EmptyState, EntityChip, FieldLabel, Pill, StatStrip, StepRow, TickRow,
} from "@/components/ui/detail"

// The kit's job is to stop every screen inventing its own sizes, so the tests
// assert the type ROLE is applied rather than a pixel value — a density change
// in globals.css must not turn these red.

describe("<DetailCard>", () => {
  it("renders the header as a section role and keeps the body", () => {
    render(<DetailCard title="What it does" subtitle="step by step" icon={Workflow}>body</DetailCard>)
    const header = screen.getByText("What it does")
    expect(header).toHaveClass("type-section")
    expect(screen.getByText("step by step")).toHaveClass("type-meta")
    expect(screen.getByText("body")).toBeInTheDocument()
  })

  it("renders headerless when no title or action is given", () => {
    const { container } = render(<DetailCard>body only</DetailCard>)
    expect(container.querySelectorAll(".type-section")).toHaveLength(0)
  })

  it("drops body padding when bare", () => {
    const { container } = render(<DetailCard bare>table</DetailCard>)
    const body = container.querySelector("div > div:last-child") as HTMLElement
    expect(body.className).not.toContain("p-4")
  })

  it("places the action on the right of the header", () => {
    render(<DetailCard title="Runs" action={<button>All</button>}>x</DetailCard>)
    expect(screen.getByRole("button", { name: "All" })).toBeInTheDocument()
  })
})

describe("<StatStrip>", () => {
  const items = [
    { label: "RUNS", value: 77 },
    { label: "PASS", value: "100%", tone: "success" as const },
    { label: "SCHEDULE", value: "*/30 * * * *", mono: true },
  ]

  it("renders one cell per stat with label and value", () => {
    render(<StatStrip items={items} />)
    expect(screen.getByText("RUNS")).toBeInTheDocument()
    expect(screen.getByText("77")).toBeInTheDocument()
    expect(screen.getByText("*/30 * * * *")).toBeInTheDocument()
  })

  it("uses the row role for values and the meta role for captions", () => {
    render(<StatStrip items={items} />)
    expect(screen.getByText("77")).toHaveClass("type-row")
    expect(screen.getByText("RUNS")).toHaveClass("type-meta")
  })

  it("tints a value without touching the others", () => {
    render(<StatStrip items={items} />)
    expect(screen.getByText("100%")).toHaveClass("text-success")
    expect(screen.getByText("77")).toHaveClass("text-foreground")
  })

  it("renders mono values in mono", () => {
    render(<StatStrip items={items} />)
    expect(screen.getByText("*/30 * * * *")).toHaveClass("font-mono")
  })
})

describe("<TickRow>", () => {
  it("shows a success tick", () => {
    render(<TickRow label="Trigger" status="ok" />)
    expect(screen.getByText("✓")).toHaveClass("text-success")
  })

  it("shows a failure mark", () => {
    render(<TickRow label="Fetch" status="failed" />)
    expect(screen.getByText("✕")).toHaveClass("text-destructive")
  })

  it("renders no tick when the step has no status", () => {
    render(<TickRow label="Trigger" />)
    expect(screen.queryByText("✓")).not.toBeInTheDocument()
    expect(screen.queryByText("·")).not.toBeInTheDocument()
  })

  it("keeps detail and meta distinguishable from the label", () => {
    render(<TickRow label="Agent" detail="casey" meta="20.2s" status="ok" />)
    expect(screen.getByText("casey")).toHaveClass("font-mono")
    expect(screen.getByText("20.2s")).toHaveClass("type-meta")
  })
})

describe("<StepRow>", () => {
  it("numbers the step and renders its badge", () => {
    render(<StepRow index={2} title="Transform data" badge={{ label: "script" }} body="@json" />)
    expect(screen.getByText("2")).toBeInTheDocument()
    expect(screen.getByText("script")).toBeInTheDocument()
    expect(screen.getByText("@json")).toHaveClass("font-mono")
  })

  it("swaps the number for an icon when one is given", () => {
    const { container } = render(<StepRow icon={CheckCircle2} title="Runs on trigger" />)
    expect(container.querySelector("svg")).toBeInTheDocument()
  })
})

describe("<EntityChip>", () => {
  it("renders a plain chip when it points nowhere", () => {
    render(<EntityChip icon={KeyRound} label="AI_CLI_TOKEN" note="any" />)
    expect(screen.getByText("AI_CLI_TOKEN")).toBeInTheDocument()
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
    expect(screen.queryByRole("link")).not.toBeInTheDocument()
  })

  it("becomes a button when given a handler", () => {
    const onClick = vi.fn()
    render(<EntityChip label="@casey" onClick={onClick} />)
    fireEvent.click(screen.getByRole("button", { name: /casey/ }))
    expect(onClick).toHaveBeenCalled()
  })

  it("becomes a link when given an href", () => {
    render(<EntityChip label="@casey" href="/crews?agent=casey" />)
    expect(screen.getByRole("link", { name: /casey/ })).toHaveAttribute("href", "/crews?agent=casey")
  })
})

describe("shared roles", () => {
  it("FieldLabel is a section role", () => {
    render(<FieldLabel>Name</FieldLabel>)
    expect(screen.getByText("Name")).toHaveClass("type-section")
  })

  it("Pill carries its tone", () => {
    render(<Pill tone="warn">pending</Pill>)
    expect(screen.getByText("pending")).toHaveClass("text-warn")
  })

  it("EmptyState renders title, description and action", () => {
    render(
      <EmptyState icon={Workflow} title="Nothing yet" description="Description" action={<button>Add</button>} />,
    )
    expect(screen.getByText("Nothing yet")).toBeInTheDocument()
    expect(screen.getByText("Description")).toHaveClass("type-row")
    expect(screen.getByRole("button", { name: "Add" })).toBeInTheDocument()
  })
})
