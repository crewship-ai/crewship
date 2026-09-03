import { describe, it, expect } from "vitest"
import {
  crewPill,
  explorerCountLine,
  foldRows,
  groupExplorerCrews,
  type ExplorerAgent,
  type ExplorerCrew,
} from "@/components/features/crews/explorer-groups"

const crew = (id: string, name: string, agents?: number): ExplorerCrew => ({
  id, name, slug: id, color: null, icon: null, _count: agents == null ? undefined : { agents },
})
const agent = (id: string, crew_id: string | null, status = "IDLE", extra: Partial<ExplorerAgent> = {}): ExplorerAgent => ({
  id, name: id.toUpperCase(), slug: id, status, role_title: null, agent_role: "AGENT", crew_id, ...extra,
})

describe("crewPill", () => {
  it("ranks an error above everything, then a failed build, a rebuild, a gap, waiting, running", () => {
    expect(crewPill([agent("a", "c", "ERROR"), agent("b", "c", "RUNNING")], "failed", 3)).toEqual({ tone: "danger", label: "1 error" })
    expect(crewPill([agent("b", "c", "RUNNING")], "failed", 3)).toEqual({ tone: "danger", label: "Build failed" })
    expect(crewPill([agent("b", "c", "RUNNING")], "needs_provision", 3)).toEqual({ tone: "warn", label: "Rebuild" })
    expect(crewPill([agent("b", "c", "RUNNING")], "idle", 2)).toEqual({ tone: "warn", label: "2 gaps" })
    expect(crewPill([agent("b", "c", "PENDING_REVIEW")], "idle", 0)).toEqual({ tone: "warn", label: "Waiting" })
    expect(crewPill([agent("b", "c", "RUNNING"), agent("d", "c", "RUNNING")], "idle", 0)).toEqual({ tone: "blue", label: "2 running" })
  })
  it("says nothing about an idle crew — idle is the normal state and needs no ink", () => {
    expect(crewPill([agent("a", "c")], "idle", 0)).toBeNull()
    expect(crewPill([], undefined, 0)).toBeNull()
  })
  it("ignores an expired hire's status", () => {
    expect(crewPill([agent("a", "c", "ERROR", { expired_at: "2026-01-01T00:00:00Z" })], "idle", 0)).toBeNull()
  })
})

describe("groupExplorerCrews", () => {
  const crews = [crew("ops", "Ops"), crew("eng", "Engineering"), crew("qa", "Quality"), crew("empty", "Empty")]
  const agents = [
    agent("morgan", "ops", "ERROR"), agent("riley", "ops"),
    agent("alex", "eng", "PENDING_REVIEW"), agent("sam", "eng"), agent("robin", "eng", "RUNNING"),
    agent("jordan", "qa", "RUNNING"), agent("casey", "qa"),
    agent("drifter", null),
  ]

  it("puts attention first, then running, then idle, each with a pill", () => {
    const { groups } = groupExplorerCrews({ crews, agents })
    expect(groups.map((g) => g.key)).toEqual(["attention", "running", "idle"])
    expect(groups[0].rows.map((r) => r.crew.id)).toEqual(["eng", "ops"])
    expect(groups[0].rows.map((r) => r.pill?.label)).toEqual(["Waiting", "1 error"])
    expect(groups[1].rows.map((r) => r.crew.id)).toEqual(["qa"])
    expect(groups[2].rows.map((r) => r.crew.id)).toEqual(["empty"])
    expect(groups[2].rows[0].pill).toBeNull()
  })

  it("lifts a crew into attention on provisioning state and credential gaps alone", () => {
    const { groups } = groupExplorerCrews({
      crews, agents,
      provisioningByCrew: new Map([["empty", "needs_provision"]]),
      gapsByCrew: new Map([["qa", 1]]),
    })
    expect(groups[0].rows.map((r) => r.crew.id)).toEqual(["eng", "ops", "qa", "empty"])
    expect(groups[0].rows.find((r) => r.crew.id === "qa")?.pill?.label).toBe("1 gap")
    expect(groups[0].rows.find((r) => r.crew.id === "empty")?.pill?.label).toBe("Rebuild")
  })

  it("keeps the real roster size on a crew row while a search hides its agents", () => {
    const { groups, matchedCrews, matchedAgents } = groupExplorerCrews({ crews, agents, search: "ops" })
    const rows = groups.flatMap((g) => g.rows)
    expect(rows.map((r) => r.crew.id)).toEqual(["ops"])
    expect(rows[0].agentCount).toBe(2)
    expect(rows[0].agents).toEqual([])
    expect(matchedCrews).toBe(1)
    expect(matchedAgents).toBe(0)
  })

  it("matches agents by name, slug and role and brings their crew along", () => {
    const withRole = agents.map((a) => (a.id === "casey" ? { ...a, role_title: "Test & Review Engineer" } : a))
    const { groups, matchedCrews, matchedAgents, unassigned } = groupExplorerCrews({ crews, agents: withRole, search: "review" })
    const rows = groups.flatMap((g) => g.rows)
    expect(rows.map((r) => r.crew.id)).toEqual(["qa"])
    expect(rows[0].agents.map((a) => a.id)).toEqual(["casey"])
    expect(matchedCrews).toBe(1)
    expect(matchedAgents).toBe(1)
    expect(unassigned).toEqual([])
  })

  it("returns no groups and the unassigned matches when nothing else matches", () => {
    const r = groupExplorerCrews({ crews, agents, search: "drift" })
    expect(r.groups).toEqual([])
    expect(r.unassigned.map((a) => a.id)).toEqual(["drifter"])
    expect(r.matchedCrews).toBe(0)
    expect(r.matchedAgents).toBe(1)
  })

  it("uses the crew's own count when the agent list is a page that missed its agents", () => {
    const { groups } = groupExplorerCrews({ crews: [crew("far", "Far", 3)], agents: [] })
    expect(groups[0].rows[0].agentCount).toBe(3)
  })
})

describe("foldRows", () => {
  it("folds after six and reports how many are hidden", () => {
    const rows = Array.from({ length: 100 }, (_, i) => i)
    expect(foldRows(rows, false)).toEqual({ visible: rows.slice(0, 6), hidden: 94 })
    expect(foldRows(rows, true).hidden).toBe(0)
    expect(foldRows(rows.slice(0, 6), false).hidden).toBe(0)
  })
})

describe("explorerCountLine", () => {
  it("reports the server's totals, not the page", () => {
    expect(explorerCountLine({ search: "", crewsTotal: 103, agentsTotal: 308, matchedCrews: 100, matchedAgents: 100 })).toBe("103 crews · 308 agents")
  })
  it("reports matches while searching, singular when one", () => {
    expect(explorerCountLine({ search: "ops", crewsTotal: 103, agentsTotal: 308, matchedCrews: 1, matchedAgents: 0 })).toBe("1 crew · 0 agents match")
  })
  it("falls back to what is loaded on a server that sends no total", () => {
    expect(explorerCountLine({ search: "", crewsTotal: null, agentsTotal: null, matchedCrews: 3, matchedAgents: 7 })).toBe("3 crews · 7 agents")
  })
})
