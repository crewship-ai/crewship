import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { TopSpendersTable } from "@/components/features/paymaster/top-spenders-table"
import type { TopSpenderRow } from "@/lib/types/paymaster"

function row(over: Partial<TopSpenderRow> = {}): TopSpenderRow {
  return {
    cost_usd: 1.23,
    call_count: 10,
    total_tokens: 5000,
    ...over,
  } as TopSpenderRow
}

describe("TopSpendersTable", () => {
  it("renders loading state", () => {
    render(<TopSpendersTable rows={[]} loading />)
    expect(screen.getByText(/Loading top spenders/i)).toBeInTheDocument()
  })

  it("renders empty state when not loading", () => {
    render(<TopSpendersTable rows={[]} loading={false} />)
    expect(screen.getByText(/No spenders to show yet/i)).toBeInTheDocument()
  })

  it("renders table headers when rows present", () => {
    render(<TopSpendersTable loading={false} rows={[row({ crew_name: "Crew A" })]} />)
    expect(screen.getByText("#")).toBeInTheDocument()
    expect(screen.getByText("Scope")).toBeInTheDocument()
    expect(screen.getByText("Cost")).toBeInTheDocument()
    expect(screen.getByText("Calls")).toBeInTheDocument()
    expect(screen.getByText("Tokens")).toBeInTheDocument()
  })

  it("renders 1-based row numbers", () => {
    render(
      <TopSpendersTable
        loading={false}
        rows={[row({ crew_name: "A" }), row({ crew_name: "B" }), row({ crew_name: "C" })]}
      />,
    )
    expect(screen.getByText("1")).toBeInTheDocument()
    expect(screen.getByText("2")).toBeInTheDocument()
    expect(screen.getByText("3")).toBeInTheDocument()
  })

  it("formats cost to 4 decimal places with $ prefix", () => {
    render(<TopSpendersTable loading={false} rows={[row({ cost_usd: 1.23456789 })]} />)
    expect(screen.getByText("$1.2346")).toBeInTheDocument()
  })

  it("formats integers with thousand separators", () => {
    render(
      <TopSpendersTable
        loading={false}
        rows={[row({ call_count: 12345, total_tokens: 1234567 })]}
      />,
    )
    // Compare against the exact locale-formatted output produced by the
    // same Intl.NumberFormat the component uses — the previous /12.345/
    // pattern's '.' was a regex wildcard and would have matched even
    // wrong-separator output like "12345" or "12X345".
    const nf = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 })
    expect(screen.getByText(nf.format(12345))).toBeInTheDocument()
    expect(screen.getByText(nf.format(1234567))).toBeInTheDocument()
  })

  it("resolves mission_name preferred over agent_name preferred over crew_name", () => {
    render(
      <TopSpendersTable
        loading={false}
        rows={[
          row({ mission_name: "MissionA", agent_name: "AgentA", crew_name: "CrewA" }),
        ]}
      />,
    )
    expect(screen.getByText("MissionA")).toBeInTheDocument()
    expect(screen.queryByText("AgentA")).not.toBeInTheDocument()
    expect(screen.queryByText("CrewA")).not.toBeInTheDocument()
  })

  it("falls back to truncated id (first 8 chars) when name missing", () => {
    render(
      <TopSpendersTable
        loading={false}
        rows={[row({ agent_id: "agent_abcdefgh_long" })]}
      />,
    )
    expect(screen.getByText("agent_ab")).toBeInTheDocument()
  })

  it("renders the scope hint badge ('mission' / 'agent' / 'crew')", () => {
    render(
      <TopSpendersTable
        loading={false}
        rows={[
          row({ mission_name: "M1" }),
          row({ agent_name: "A1" }),
          row({ crew_name: "C1" }),
        ]}
      />,
    )
    expect(screen.getByText("mission")).toBeInTheDocument()
    expect(screen.getByText("agent")).toBeInTheDocument()
    expect(screen.getByText("crew")).toBeInTheDocument()
  })

  it("falls back to em-dash when no scope info available", () => {
    render(<TopSpendersTable loading={false} rows={[row()]} />)
    expect(screen.getByText("—")).toBeInTheDocument()
  })
})
