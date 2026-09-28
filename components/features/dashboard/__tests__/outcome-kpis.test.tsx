import { describe, it, expect, afterEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { OutcomeKpis } from "../dashboard-overview"

afterEach(cleanup)

const EMPTY = { completed: 0, successPct: null, successOk: 0, successTotal: 0, p95Ms: 0 }

// Four tiles of "0 / — / — / $0.00" took a full row on dev3 and said one
// thing: nothing ran. Say that thing once, and keep the tiles for when there
// is something to measure.
describe("OutcomeKpis", () => {
  it("collapses to one line when the window has no agent runs", () => {
    render(<OutcomeKpis data={EMPTY} window="24h" spendUsd={null} />)
    expect(screen.getByText(/No agent runs in the last 24h/)).toBeTruthy()
    expect(screen.queryByText("P95 duration")).toBeNull()
  })

  it("keeps the four tiles once anything ran", () => {
    render(<OutcomeKpis data={{ ...EMPTY, completed: 3, successOk: 3, successTotal: 3, successPct: 100 }} window="24h" spendUsd={null} />)
    expect(screen.getByText("P95 duration")).toBeTruthy()
    expect(screen.queryByText(/No agent runs/)).toBeNull()
  })

  it("keeps the tiles when runs finished but none succeeded", () => {
    render(<OutcomeKpis data={{ ...EMPTY, successTotal: 2, successPct: 0 }} window="7d" spendUsd={null} />)
    expect(screen.getByText("Success")).toBeTruthy()
  })
})
