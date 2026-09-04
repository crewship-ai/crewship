import { describe, it, expect } from "vitest"

import { deriveBridge } from "../bridge-strip"
import { deriveFleetBoard, prioritiseFleet, FLEET_CARD_LIMIT, type FleetCard } from "../fleet-board"
import { foldRunVolumeSeries, RUN_VOLUME_OTHER_KEY } from "@/app/(dashboard)/dashboard-helpers"
import { issueBoardCounts } from "../work-snapshot"
import { sparklinePoints } from "../sparkline"
import { tickerTone } from "../activity-ticker"
import { scheduleDotClass, type FleetHealthRow } from "../dashboard-overview"
import type { AgentSummary, CrewSummary } from "@/app/(dashboard)/dashboard-types"
import type { Mission } from "@/lib/types/mission"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"

const crew = (id: string, extra: Partial<CrewSummary> = {}): CrewSummary => ({ id, name: id, slug: id, color: "blue", icon: "code", ...extra })
const agent = (id: string, crewId: string, status: string): AgentSummary => ({
  id, name: id, slug: id, role_title: null, agent_role: "worker", status, crew: null, crew_id: crewId,
  _count: { skills: 0, credentials: 0, chats: 0 },
})
const kpis = { completed: 35, successPct: 94, successOk: 35, successTotal: 37, p95Ms: 220_000 }

describe("the bridge says what the ship is doing", () => {
  it("counts working, idle and errored agents and sums metered spend", () => {
    const b = deriveBridge({
      agents: [agent("a", "c1", "RUNNING"), agent("b", "c1", "IDLE"), agent("c", "c2", "ERROR")],
      crews: [crew("c1"), crew("c2")],
      spendRows: [{ cost_usd: 1.5 }, { cost_usd: 0.25 }],
      kpis,
      attentionCount: 2,
      attentionKnown: true,
      schedules: [],
    })
    expect(b.workingAgents).toBe(1)
    expect(b.idleAgents).toBe(1)
    expect(b.errorAgents).toBe(1)
    expect(b.spendUsd).toBeCloseTo(1.75)
    expect(b.failed).toBe(2)
    expect(b.nextSchedule).toBeNull()
  })

  it("reports spend as unmetered (null) rather than $0 when the ledger has no rows", () => {
    const b = deriveBridge({ agents: [], crews: [], spendRows: [], kpis, attentionCount: 0, attentionKnown: true, schedules: [] })
    expect(b.spendUsd).toBeNull()
    const c = deriveBridge({ agents: [], crews: [], spendRows: null, kpis, attentionCount: 0, attentionKnown: true, schedules: [] })
    expect(c.spendUsd).toBeNull()
  })

  it("picks the soonest enabled future schedule as the next run", () => {
    const now = Date.parse("2026-09-02T10:00:00Z")
    const sched = (id: string, at: string, enabled = true): PipelineSchedule =>
      ({ id, workspace_id: "ws", name: id, target_pipeline_id: id, target_pipeline_slug: id, cron_expr: "", timezone: "UTC", inputs: {}, enabled, next_run_at: at }) as PipelineSchedule
    const b = deriveBridge({
      agents: [], crews: [], spendRows: null, kpis, attentionCount: 0, attentionKnown: true, now,
      schedules: [sched("later", "2026-09-02T12:00:00Z"), sched("soon", "2026-09-02T10:30:00Z"), sched("disabled", "2026-09-02T10:05:00Z", false), sched("past", "2026-09-02T09:00:00Z")],
    })
    expect(b.nextSchedule?.id).toBe("soon")
  })
})

describe("the fleet board is one card per crew", () => {
  it("attaches the crew's agents, spend and run series", () => {
    const rows: FleetHealthRow[] = [
      { crew: crew("c1"), status: "Running", detail: "1 active agent", tone: "blue", agents: 2, runningAgents: 1, services: { total: 0, running: 0, degraded: 0, checked: false } },
      { crew: crew("c2"), status: "Ready", detail: "No active run", tone: "success", agents: 1, runningAgents: 0, services: { total: 0, running: 0, degraded: 0, checked: false } },
    ]
    const cards = deriveFleetBoard({
      rows,
      agents: [agent("a", "c1", "RUNNING"), agent("b", "c1", "IDLE"), agent("c", "c2", "IDLE")],
      spendByCrew: new Map([["c1", 2.31]]),
      buckets: [{ ts: "t1", c1: 2, c2: 0 }, { ts: "t2", c1: 3, c2: 1 }],
    })
    expect(cards[0].agents.map((a) => a.id)).toEqual(["a", "b"])
    expect(cards[0].spendUsd).toBe(2.31)
    expect(cards[0].runSeries).toEqual([2, 3])
    expect(cards[0].runsTotal).toBe(5)
    // No ledger row for c2 → not metered, not "$0.00".
    expect(cards[1].spendUsd).toBeNull()
    expect(cards[1].runsTotal).toBe(1)
  })
})

describe("the issues board counts columns, not statuses", () => {
  it("folds planning states into backlog and both completed spellings into done; cancelled counts nowhere", () => {
    const m = (status: Mission["status"]): Mission => ({ status } as Mission)
    const counts = issueBoardCounts([m("BACKLOG"), m("TODO"), m("PLANNING"), m("IN_PROGRESS"), m("REVIEW"), m("COMPLETED"), m("DONE"), m("CANCELLED"), m("FAILED")])
    expect(counts).toEqual({ backlog: 3, inProgress: 1, review: 1, done: 2, open: 5 })
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

  it("colours a journal line by what happened, not by its type name alone", () => {
    expect(tickerTone({ entry_type: "run.completed", severity: "info" })).toBe("success")
    expect(tickerTone({ entry_type: "run.failed", severity: "info" })).toBe("danger")
    expect(tickerTone({ entry_type: "memory.updated", severity: "error" })).toBe("danger")
    expect(tickerTone({ entry_type: "agent.status_change", severity: "info" })).toBe("blue")
    expect(tickerTone({ entry_type: "memory.updated", severity: "info" })).toBe("muted")
  })

  it("colours a schedule's last-run dot", () => {
    expect(scheduleDotClass(undefined)).toMatch(/muted/)
    expect(scheduleDotClass("completed")).toMatch(/success/)
    expect(scheduleDotClass("failed")).toMatch(/destructive/)
    expect(scheduleDotClass("running")).toMatch(/primary/)
  })
})

describe("a fleet of a hundred crews stays readable", () => {
  const card = (name: string, tone: FleetHealthRow["tone"], runs: number): FleetCard => ({
    row: { crew: crew(name), status: tone, detail: "", tone, agents: 1, runningAgents: 0, services: { total: 0, running: 0, degraded: 0, checked: false } },
    agents: [], spendUsd: null, runSeries: [runs], runsTotal: runs,
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

  it("keeps the card limit small enough to read", () => {
    expect(FLEET_CARD_LIMIT).toBeLessThanOrEqual(6)
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
