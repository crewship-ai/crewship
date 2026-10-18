import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, within } from "@testing-library/react"

import type { CrewSummary } from "@/app/(dashboard)/dashboard-types"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"

import { DashCard, DashSubHead } from "../dash-card"
import { DashboardUpNext } from "../dashboard-overview"
import type { FleetHealthRow } from "../dashboard-overview"
import { FleetBoard, type FleetCard } from "../fleet-board"

// Harbor 2 parity with the v67 wireframe (PAR-011, 014, 018, 152, 153).
// jsdom has no layout, so sizes are asserted through the classes that set them.
vi.mock("@/components/ui/crew-icon", () => ({ CrewIcon: () => null }))
afterEach(cleanup)

const NOW = new Date("2026-10-18T09:00:00Z")
const schedule = (over: Partial<PipelineSchedule> = {}): PipelineSchedule =>
  ({ id: "s1", name: "Daily standup digest", enabled: true, next_run_at: "2026-10-18T09:30:00Z", target_pipeline_slug: "standup", ...over }) as PipelineSchedule

const services = { total: 0, running: 0, degraded: 0, checked: false }
const card = (i: number): FleetCard => ({
  row: { crew: { id: `c${i}`, name: `Crew ${i}`, slug: `crew-${i}`, color: "blue", icon: "code" } as CrewSummary, status: "Ready", detail: "", tone: "success", agents: 1, runningAgents: 0, services } as FleetHealthRow,
  agents: [], runSeries: [], runsTotal: 0,
})

describe("PAR-011 Up next", () => {
  it("draws the Calendar glyph in the header tile, the card links to Calendar", () => {
    render(<DashboardUpNext schedules={[schedule()]} now={NOW} />)
    const tile = document.querySelector('[data-slot="concept-tile"]')!
    expect(tile.querySelector("svg")?.getAttribute("class")).toContain("lucide-calendar")
    expect(screen.getByRole("link", { name: /Calendar/ })).toHaveAttribute("href", "/calendar")
  })

  it("styles the empty-state action as a link, underlined at rest", () => {
    render(<DashboardUpNext schedules={[]} now={NOW} />)
    const link = screen.getByRole("link", { name: "Add a trigger" })
    expect(link.className).toMatch(/(^|\s)underline(\s|$)/)
  })
})

describe("PAR-014 Your crews", () => {
  it("lists at most six crews and has no '+N more' footer row (the header link is the way on)", () => {
    render(<FleetBoard cards={Array.from({ length: 8 }, (_, i) => card(i))} />)
    expect(screen.getAllByTestId("dashboard-fleet-card")).toHaveLength(6)
    expect(screen.queryByText(/more in Crews/)).toBeNull()
    expect(screen.getByRole("link", { name: /^Crews/ })).toHaveAttribute("href", "/crews")
  })
})

describe("PAR-018 card header", () => {
  it("keeps the 44 px tap target of the header link without growing the header", () => {
    render(<DashCard title="Results" concept="issues" link={{ label: "All issues", href: "/issues" }}><div /></DashCard>)
    const link = screen.getByRole("link", { name: /All issues/ })
    // A coarse pointer used to turn the 28 px header into 64 px: the target
    // hangs out of the header through a negative margin instead.
    expect(link.className).toContain("coarse:min-h-[44px]")
    expect(link.className).toContain("coarse:-my-2")
  })
})

describe("PAR-152/153 sans, sentence case, tabular numerals", () => {
  it("shows a card's meta fact and a sub-heading count in sans tabular figures", () => {
    render(<DashCard title="Run volume" meta="12 runs"><DashSubHead title="Running now" count={2} /></DashCard>)
    const meta = document.querySelector('[data-slot="dashboard-card-hint"]')!
    expect(meta.className).not.toContain("font-mono")
    expect(meta.className).toContain("tabular-nums")
    const count = within(screen.getByText("Running now").closest("div")!).getByText("2")
    expect(count.className).not.toContain("font-mono")
    expect(count.className).toContain("tabular-nums")
  })
})
