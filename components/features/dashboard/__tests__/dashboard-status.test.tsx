import { afterEach, describe, expect, it } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"

import { SystemSignals, type FleetHealthRow } from "../dashboard-overview"
import type { AgentSummary, CrewSummary } from "@/app/(dashboard)/dashboard-types"

afterEach(cleanup)

const crew: CrewSummary = { id: "crew-1", name: "Docs", slug: "docs", color: "blue", icon: null }
const fleet: FleetHealthRow[] = [{
  crew, status: "Ready", detail: "No active run", tone: "success", agents: 1, runningAgents: 0,
  services: { total: 0, running: 0, degraded: 0, checked: false },
}]
const agent = { id: "agent-1", name: "Writer", status: "IDLE" } as AgentSummary

describe("dashboard status", () => {
  it("shows actionable workspace state without claiming unchecked services are healthy", () => {
    render(<SystemSignals capacity={{ enabled: true, held: [] }} heldCrews={[]} fleet={fleet} agents={[agent]} schedules={[]} schedulesLoading={false} schedulesError={null} />)
    expect(screen.getByText("New run capacity")).toBeTruthy()
    expect(screen.getByText("1 not checked")).toBeTruthy()
    expect(screen.queryByText("No failures")).toBeNull()
    expect(screen.getByRole("link", { name: /Crew health/ }).getAttribute("href")).toBe("/crews")
    expect(screen.getByRole("link", { name: /Scheduled routines/ }).getAttribute("href")).toBe("/routines?tab=calendar")
    expect(screen.queryByText(/tool gap/i)).toBeNull()
  })

  it("keeps missing tools out of the crew failure count", () => {
    const toolGap = { ...fleet[0], status: "Needs tool", tone: "warn" as const, services: { ...fleet[0].services, checked: true } }
    render(<SystemSignals capacity={{ enabled: true, held: [] }} heldCrews={[]} fleet={[toolGap]} agents={[agent]} schedules={[]} schedulesLoading={false} schedulesError={null} />)
    expect(screen.getByText("No failures")).toBeTruthy()
    expect(screen.getByText("No agent or service problems detected")).toBeTruthy()
  })

  it("surfaces unavailable agents instead of treating configured as healthy", () => {
    render(<SystemSignals capacity={{ enabled: true, held: [] }} heldCrews={[]} fleet={[]} agents={[{ ...agent, status: "UNAVAILABLE" }]} schedules={[]} schedulesLoading={false} schedulesError={null} />)
    expect(screen.getByText("1 unavailable")).toBeTruthy()
    expect(screen.queryByText("1 configured")).toBeNull()
  })
})
