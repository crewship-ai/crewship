import { describe, it, expect } from "vitest"

import { crewChipStatus, deriveFleetBoard, prioritiseFleet, type FleetCard } from "../fleet-board"
import { foldRunVolumeSeries, RUN_VOLUME_OTHER_KEY, crewColor, CREW_PALETTE } from "@/app/(dashboard)/dashboard-helpers"
import { sparklinePoints } from "@/components/ui/sparkline"
import { type FleetHealthRow } from "../dashboard-overview"
import type { AgentSummary, CrewSummary } from "@/app/(dashboard)/dashboard-types"

const crew = (id: string, extra: Partial<CrewSummary> = {}): CrewSummary => ({ id, name: id, slug: id, color: "blue", icon: "code", ...extra })
const agent = (id: string, crewId: string, status: string): AgentSummary => ({
  id, name: id, slug: id, role_title: null, agent_role: "worker", status, crew: null, crew_id: crewId,
  _count: { skills: 0, credentials: 0, chats: 0 },
})
describe("the fleet board is one card per crew", () => {
  it("attaches the crew's agents and run series", () => {
    const rows: FleetHealthRow[] = [
      { crew: crew("c1"), status: "Running", detail: "1 active agent", tone: "blue", agents: 2, runningAgents: 1, services: { total: 0, running: 0, degraded: 0, checked: false } },
      { crew: crew("c2"), status: "Ready", detail: "No active run", tone: "success", agents: 1, runningAgents: 0, services: { total: 0, running: 0, degraded: 0, checked: false } },
    ]
    const cards = deriveFleetBoard({
      rows,
      agents: [agent("a", "c1", "RUNNING"), agent("b", "c1", "IDLE"), agent("c", "c2", "IDLE")],
      buckets: [{ ts: "t1", c1: 2, c2: 0 }, { ts: "t2", c1: 3, c2: 1 }],
    })
    expect(cards[0].agents.map((a) => a.id)).toEqual(["a", "b"])
    expect(cards[0].runSeries).toEqual([2, 3])
    expect(cards[0].runsTotal).toBe(5)
    expect(cards[1].runsTotal).toBe(1)
  })
})

describe("small visual helpers", () => {
  it("draws a flat baseline for an empty series and scales a real one into the box", () => {
    expect(sparklinePoints([], 100, 30)).toEqual([[2, 28], [98, 28]])
    const pts = sparklinePoints([0, 10, 5], 100, 30)
    expect(pts).toHaveLength(3)
    expect(pts[0][1]).toBeCloseTo(28) // min at the bottom
    expect(pts[1][1]).toBeCloseTo(2) // max at the top
    expect(pts[2][0]).toBeCloseTo(98)
  })

})

describe("a fleet of a hundred crews stays readable", () => {
  const card = (name: string, tone: FleetHealthRow["tone"], runs: number): FleetCard => ({
    row: { crew: crew(name), status: tone, detail: "", tone, agents: 1, runningAgents: 0, services: { total: 0, running: 0, degraded: 0, checked: false } },
    agents: [], runSeries: [runs], runsTotal: runs,
  })

  it("puts what needs a person first, then the busiest, then the rest by name", () => {
    const ordered = prioritiseFleet([
      card("zeta-idle", "success", 0), card("alpha-idle", "success", 0), card("busy", "blue", 9),
      card("busier", "blue", 40), card("gap", "warn", 0), card("broken", "danger", 0), card("empty", "muted", 0),
    ]).map((c) => c.row.crew.name)
    expect(ordered).toEqual(["broken", "gap", "busier", "busy", "empty", "alpha-idle", "zeta-idle"])
  })

  it("ranks a staffed crew above an empty shell with the same status", () => {
    const staffed = { ...card("staffed", "warn", 0), agents: [agent("x", "staffed", "IDLE")] }
    const ordered = prioritiseFleet([card("aaa-empty", "warn", 0), staffed]).map((c) => c.row.crew.name)
    expect(ordered).toEqual(["staffed", "aaa-empty"])
  })

  it("folds the run-volume chart to the busiest crews plus one Other series", () => {
    const series = Array.from({ length: 30 }, (_, i) => ({ key: `c${i}`, label: `Crew ${i}`, color: "#000" }))
    const bucket: Record<string, string | number> = { ts: "t" }
    series.forEach((s, i) => { bucket[s.key] = i })
    const folded = foldRunVolumeSeries([bucket as { ts: string; [k: string]: string | number }], series, 8)
    expect(folded.series).toHaveLength(9)
    expect(folded.series.slice(0, 8).map((s) => s.key)).toEqual(["c29", "c28", "c27", "c26", "c25", "c24", "c23", "c22"])
    expect(folded.series[8].key).toBe(RUN_VOLUME_OTHER_KEY)
    expect(folded.folded).toBe(22)
    // Other carries exactly the runs of the folded crews: 0+1+...+21.
    expect(folded.buckets[0][RUN_VOLUME_OTHER_KEY]).toBe(231)
    expect(folded.buckets[0]).not.toHaveProperty("c0")
  })

  it("leaves a small fleet's chart untouched", () => {
    const series = [{ key: "a", label: "A", color: "#000" }]
    const out = foldRunVolumeSeries([{ ts: "t", a: 3 }], series)
    expect(out.series).toBe(series)
    expect(out.folded).toBe(0)
  })
})

describe("a crew chip says its state in a few words", () => {
  const row = (over: Partial<FleetHealthRow>): FleetHealthRow => ({
    crew: crew("c"), status: "Ready", detail: "", tone: "success", agents: 2, runningAgents: 0,
    services: { total: 0, running: 0, degraded: 0, checked: false }, ...over,
  })
  it("names working agents, tool gaps, errors, idle and empty crews", () => {
    expect(crewChipStatus(row({ status: "Running", tone: "blue", runningAgents: 2 }))).toEqual({ text: "2 working", tone: "ok" })
    expect(crewChipStatus(row({ status: "Needs tool", tone: "warn" }))).toEqual({ text: "Needs tool", tone: "warn" })
    expect(crewChipStatus(row({ status: "Agent error", tone: "danger" }))).toEqual({ text: "Agent error", tone: "bad" })
    expect(crewChipStatus(row({}))).toEqual({ text: "Idle", tone: "mut" })
    expect(crewChipStatus(row({ status: "Empty", tone: "muted", agents: 0 }))).toEqual({ text: "No agents", tone: "mut" })
  })
})

describe("crewColor", () => {
  it("resolves palette ids and raw hex values", () => {
    expect(crewColor("blue")).toBe(CREW_PALETTE.blue)
    expect(crewColor("sky")).toBe("rgb(14, 165, 233)")
    expect(crewColor("1E7BFE")).toBe("#1E7BFE")
    expect(crewColor("#ff8800")).toBe("#ff8800")
  })

  it("greys out an unknown colour when no index is offered", () => {
    expect(crewColor(null)).toBe("rgb(148, 163, 184)")
    expect(crewColor("not-a-color")).toBe("rgb(148, 163, 184)")
  })

  it("does not treat inherited object keys as palette colours", () => {
    expect(crewColor("constructor", 1)).toBe(Object.values(CREW_PALETTE)[1])
    expect(crewColor("__proto__", 2)).toBe(Object.values(CREW_PALETTE)[2])
  })

  it("rotates the palette by index for colourless crews (#2187)", () => {
    // A workspace of default-coloured crews gets distinguishable segments
    // instead of N identical greys.
    const first = crewColor(null, 0)
    const second = crewColor(null, 1)
    expect(first).toBe(Object.values(CREW_PALETTE)[0])
    expect(second).toBe(Object.values(CREW_PALETTE)[1])
    expect(first).not.toBe(second)
    // Wraps around the palette rather than running off the end.
    expect(crewColor(null, Object.keys(CREW_PALETTE).length)).toBe(first)
    // A real colour always wins over the rotation.
    expect(crewColor("blue", 3)).toBe(CREW_PALETTE.blue)
  })
})
