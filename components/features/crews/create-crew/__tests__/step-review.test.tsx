import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { StepReview } from "../step-review"
import { INITIAL_STATE, type WizardState } from "../types"

const filledState: WizardState = {
  ...INITIAL_STATE,
  name: "Engineering",
  slug: "engineering",
  description: "Backend services",
  icon: "code",
  color: "blue",
  memoryMB: 2048,
  cpus: 1,
  ttlHours: 4,
  networkMode: "free",
}

describe("<StepReview>", () => {
  const baseSummary = { count: 0, source: "empty" }

  it("renders identity row with name, slug, icon, color", () => {
    render(<StepReview state={filledState} lineupSummary={baseSummary} />)
    expect(screen.getByText("Engineering")).toBeInTheDocument()
    expect(screen.getByText("@engineering")).toBeInTheDocument()
    expect(screen.getByText("icon: code")).toBeInTheDocument()
    expect(screen.getByText("color: blue")).toBeInTheDocument()
  })

  it("renders description row when set", () => {
    render(<StepReview state={filledState} lineupSummary={baseSummary} />)
    expect(screen.getByText("Backend services")).toBeInTheDocument()
  })

  it("hides description row when blank", () => {
    render(<StepReview state={{ ...filledState, description: "" }} lineupSummary={baseSummary} />)
    // The "Description" row label shouldn't appear at all when empty.
    expect(screen.queryByText("Description")).toBeNull()
  })

  it("renders 'Empty crew' for zero-agent lineup", () => {
    render(<StepReview state={filledState} lineupSummary={baseSummary} />)
    expect(screen.getByText(/Empty crew — agents added later/)).toBeInTheDocument()
  })

  it("renders agent roster preview when count > 0", () => {
    render(<StepReview
      state={filledState}
      lineupSummary={{
        count: 3,
        source: "Software Development",
        agents: [
          { name: "Tech Lead", agent_role: "LEAD" },
          { name: "Backend", agent_role: "AGENT" },
          { name: "QA", agent_role: "AGENT" },
        ],
      }}
    />)
    expect(screen.getByText("3 agents")).toBeInTheDocument()
    expect(screen.getByText("Software Development")).toBeInTheDocument()
  })

  it("renders container resources as pills", () => {
    render(<StepReview
      state={{ ...filledState, memoryMB: 4096, cpus: 2, ttlHours: 24 }}
      lineupSummary={baseSummary}
    />)
    expect(screen.getByText("4 GB")).toBeInTheDocument()
    expect(screen.getByText("2 CPU")).toBeInTheDocument()
    expect(screen.getByText("TTL: 24 h")).toBeInTheDocument()
  })

  it("renders Memory pretty-print for sub-GB values", () => {
    render(<StepReview state={{ ...filledState, memoryMB: 512 }} lineupSummary={baseSummary} />)
    expect(screen.getByText("512 MB")).toBeInTheDocument()
  })

  it("renders 'never' when ttlHours is null", () => {
    render(<StepReview state={{ ...filledState, ttlHours: null }} lineupSummary={baseSummary} />)
    expect(screen.getByText("TTL: never")).toBeInTheDocument()
  })

  it("renders Network 'Free' with green dot", () => {
    render(<StepReview state={{ ...filledState, networkMode: "free" }} lineupSummary={baseSummary} />)
    // text is "free" lowercase, CSS capitalize handles display
    expect(screen.getByText("free")).toBeInTheDocument()
  })

  it("renders Network 'Restricted' with allowed-domains pills", () => {
    render(<StepReview
      state={{
        ...filledState,
        networkMode: "restricted",
        allowedDomains: ["github.com", "*.npmjs.org"],
      }}
      lineupSummary={baseSummary}
    />)
    expect(screen.getByText("restricted")).toBeInTheDocument()
    expect(screen.getByText("github.com")).toBeInTheDocument()
    expect(screen.getByText("*.npmjs.org")).toBeInTheDocument()
  })

  it("truncates allowed-domains pills past 6 with a +N more pill", () => {
    const domains = Array.from({ length: 9 }, (_, i) => `host${i}.example.com`)
    render(<StepReview
      state={{ ...filledState, networkMode: "restricted", allowedDomains: domains }}
      lineupSummary={baseSummary}
    />)
    expect(screen.getByText("+3 more")).toBeInTheDocument()
  })

  it("clicking 'edit' on a row calls onEdit with the right step number", () => {
    const onEdit = vi.fn()
    render(<StepReview state={filledState} lineupSummary={baseSummary} onEdit={onEdit} />)

    // Identity row → step 1
    const editButtons = screen.getAllByText("edit")
    expect(editButtons.length).toBeGreaterThanOrEqual(3)
    fireEvent.click(editButtons[0])
    expect(onEdit).toHaveBeenCalledWith(1)
  })

  it("After-create hint mentions container name and includes agent count when seeded", () => {
    render(<StepReview
      state={filledState}
      lineupSummary={{ count: 4, source: "Software Development" }}
    />)
    expect(screen.getByText(/crewship-team-engineering/)).toBeInTheDocument()
    expect(screen.getByText(/4 agents auto-assigned/)).toBeInTheDocument()
  })
})
