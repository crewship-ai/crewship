// PAR-047 / PAR-048 / PAR-051: the Routines overview cards, KPI tiles and the
// header shortcut follow v67 — sentence-case sans titles on a card header with
// an icon tile, sans tabular numerals, a "C" chip on New routine.

import * as React from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, within } from "@testing-library/react"

import { RoutinesOverviewBoard } from "../routines-overview-board"
import { RoutinesDashboard } from "../routines-dashboard"

vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))
vi.mock("@/components/features/dashboard/run-volume-chart", () => ({ RunVolumeChart: () => <div data-testid="run-volume" /> }))

afterEach(cleanup)

const now = new Date("2026-10-17T08:00:00Z")
const board: React.ComponentProps<typeof RoutinesOverviewBoard> = {
  routines: [{ slug: "weekly", name: "Weekly report", status: "active" }] as never[],
  activeRuns: [],
  waitpoints: [],
  schedules: [],
  pendingStarts: [],
  kpis: { total: 58, successPct: 93 },
  coveredSince: null,
  spend: { month: "2026-10", total_spent_usd: 32.7, total_budget_usd: 80, routines: [] },
  role: "MANAGER",
  now,
  onDecide: vi.fn(async () => true),
  onStop: vi.fn(async () => true),
  onOpenRun: vi.fn(),
  onSelect: vi.fn(),
}

describe("Routines overview cards (v67)", () => {
  it("titles every board card in sentence case sans with an icon tile, not an uppercase mono eyebrow", () => {
    render(<RoutinesOverviewBoard {...board} />)
    for (const name of ["Needs you", "Running now", "Next 24 hours"]) {
      const card = screen.getByRole("region", { name })
      expect(screen.getAllByRole("region", { name })).toHaveLength(1) // one card, no wrapper repeating the name
      const heading = within(card).getByRole("heading", { name: new RegExp(`^${name}`) })
      expect(heading.className).not.toMatch(/eyebrow|uppercase|font-mono/)
      expect(card.querySelector('[data-slot="concept-tile"]')).not.toBeNull()
    }
  })

  it("opens the Calendar destination from Next 24 hours, not a tab", () => {
    render(<RoutinesOverviewBoard {...board} />)
    expect(within(screen.getByRole("region", { name: "Next 24 hours" })).getByRole("link", { name: /^Calendar$/ })).toHaveAttribute("href", "/calendar")
  })

  it("draws KPI numerals in sans with tabular figures", () => {
    render(<RoutinesOverviewBoard {...board} />)
    const tiles = within(screen.getByRole("list", { name: "Routine figures" })).getAllByRole("listitem")
    for (const tile of tiles) {
      const value = tile.querySelector("span:nth-child(2)") as HTMLElement
      expect(value.className).toContain("tabular-nums")
      expect(value.className).not.toContain("font-mono")
      expect(value.className).toContain("font-bold")
    }
  })

  it("titles the dashboard cards in sentence case too", () => {
    render(<RoutinesDashboard routines={[]} runs={[]} runsLoading={false} coveredSince={null} pendingStarts={[]} schedules={[]} onSelect={vi.fn()} />)
    const root = screen.getByTestId("routines-dashboard")
    expect(root.querySelector(".eyebrow")).toBeNull()
    expect(root.querySelector('[class*="uppercase"]')).toBeNull()
    expect(within(root).getByRole("heading", { name: "Latest results" })).toBeInTheDocument()
    expect(root.querySelector('[data-testid="run-summary-scope"]')?.className).not.toContain("font-mono")
  })
})
