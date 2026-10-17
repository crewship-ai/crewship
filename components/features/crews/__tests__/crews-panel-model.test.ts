import { describe, it, expect } from "vitest"

import {
  buildPanel,
  crewStatus,
  firstCrewSlug,
  orderAgents,
  panelStateFromParams,
  panelStateToParams,
  tempHireLabel,
  type PanelAgent,
} from "@/components/features/crews/crews-panel-model"

const crew = (id: string, name: string) => ({ id, name, slug: id, icon: null, color: null })
const agent = (p: Partial<PanelAgent> & { id: string; crew_id: string | null }): PanelAgent => ({
  name: p.id, slug: p.id, status: "IDLE", agent_role: "AGENT", ...p,
})

describe("panel URL state", () => {
  it("reads ?status= and ?group=, defaults when absent", () => {
    expect(panelStateFromParams(new URLSearchParams(""))).toEqual({ view: "all", group: "status" })
    expect(panelStateFromParams(new URLSearchParams("status=needs&group=name"))).toEqual({ view: "needs", group: "name" })
  })

  it("old links still load: expired → Temporary hires, group=crew → status, none → A–Z, unknown → default", () => {
    expect(panelStateFromParams(new URLSearchParams("status=expired"))).toEqual({ view: "temp", group: "status" })
    expect(panelStateFromParams(new URLSearchParams("group=crew&details=off"))).toEqual({ view: "all", group: "status" })
    expect(panelStateFromParams(new URLSearchParams("group=none"))).toEqual({ view: "all", group: "name" })
    expect(panelStateFromParams(new URLSearchParams("status=banana"))).toEqual({ view: "all", group: "status" })
  })

  it("writes only what differs from the default and drops the old facets", () => {
    const p = panelStateToParams({ view: "needs", group: "status" })
    expect(p.status).toBe("needs")
    expect(p.group).toBeNull()
    for (const k of ["role", "model", "runtime", "hire", "activity", "health", "sort", "details"]) expect(p[k]).toBeNull()
  })
})

describe("crewStatus", () => {
  it("anything waiting for a person wins over work in progress", () => {
    expect(crewStatus([agent({ id: "a", crew_id: "c", status: "RUNNING" }), agent({ id: "b", crew_id: "c", status: "ERROR" })])).toEqual({ state: "needs", needs: 1 })
  })
  it("counts a due rebuild and credential gaps as needs", () => {
    expect(crewStatus([], "needs_provision", 2)).toEqual({ state: "needs", needs: 3 })
  })
  it("a build in progress or a running agent is Working; otherwise Idle", () => {
    expect(crewStatus([], "running").state).toBe("working")
    expect(crewStatus([agent({ id: "a", crew_id: "c", status: "RUNNING" })]).state).toBe("working")
    expect(crewStatus([agent({ id: "a", crew_id: "c" })]).state).toBe("idle")
  })
  it("an expired hire does not count", () => {
    expect(crewStatus([agent({ id: "a", crew_id: "c", status: "ERROR", expired_at: "2026-10-01T00:00:00Z" })]).state).toBe("idle")
  })
})

describe("buildPanel", () => {
  const crews = [crew("ops", "Ops"), crew("research", "Research"), crew("finance", "Finance")]
  const agents = [
    agent({ id: "opsy", crew_id: "ops", status: "ERROR" }),
    agent({ id: "scout", crew_id: "research", status: "RUNNING", agent_role: "LEAD" }),
    agent({ id: "nightwatch", crew_id: "finance", ephemeral: true, expired_at: "2026-10-10T00:00:00Z" }),
    agent({ id: "concierge", crew_id: null }),
  ]

  it("groups crews Needs you → Working → Idle and counts every view", () => {
    const m = buildPanel({ crews, agents, state: { view: "all", group: "status" } })
    expect(m.sections.map((s) => [s.label, s.rows.map((r) => r.crew.slug)])).toEqual([
      ["Needs you", ["ops"]],
      ["Working", ["research"]],
      ["Idle", ["finance"]],
    ])
    expect(m.counts).toEqual({ all: 3, needs: 1, working: 1, idle: 1, temp: 1 })
    expect(m.unassigned.map((a) => a.id)).toEqual(["concierge"])
    expect(firstCrewSlug(m)).toBe("ops")
  })

  it("A–Z is one list without a heading", () => {
    const m = buildPanel({ crews, agents, state: { view: "all", group: "name" } })
    expect(m.sections).toHaveLength(1)
    expect(m.sections[0].label).toBe("")
    expect(m.sections[0].rows.map((r) => r.crew.name)).toEqual(["Finance", "Ops", "Research"])
  })

  it("Temporary hires includes crews whose hire has expired", () => {
    const m = buildPanel({ crews, agents, state: { view: "temp", group: "status" } })
    expect(m.sections.flatMap((s) => s.rows.map((r) => r.crew.slug))).toEqual(["finance"])
    expect(m.unassigned).toEqual([])
  })

  it("search matches crew names and agent names", () => {
    const m = buildPanel({ crews, agents, state: { view: "all", group: "status" }, search: "scout" })
    expect(m.shown).toBe(1)
    expect(m.sections[0].rows[0].crew.slug).toBe("research")
  })
})

describe("agents in the panel", () => {
  it("lead first, then permanent A–Z, then temporary hires", () => {
    const list = orderAgents([
      agent({ id: "zed", crew_id: "c", ephemeral: true }),
      agent({ id: "bob", crew_id: "c" }),
      agent({ id: "lee", crew_id: "c", agent_role: "LEAD" }),
      agent({ id: "amy", crew_id: "c" }),
    ])
    expect(list.map((a) => a.id)).toEqual(["lee", "amy", "bob", "zed"])
  })

  it("a temporary hire shows time left, 'hire' while pending and 'expired' after", () => {
    const now = Date.parse("2026-10-17T10:00:00Z")
    expect(tempHireLabel(agent({ id: "a", crew_id: "c", ephemeral: true, expires_at: "2026-10-17T10:38:00Z" }), now)).toEqual({ label: "38 min", tone: "warn" })
    expect(tempHireLabel(agent({ id: "a", crew_id: "c", ephemeral: true, expires_at: "2026-10-17T12:30:00Z" }), now)?.label).toBe("2 h 30 min")
    expect(tempHireLabel(agent({ id: "a", crew_id: "c", ephemeral: true, status: "PENDING_REVIEW" }), now)?.label).toBe("hire")
    expect(tempHireLabel(agent({ id: "a", crew_id: "c", ephemeral: true, expired_at: "2026-10-16T22:00:00Z" }), now)).toEqual({ label: "expired", tone: "muted" })
    expect(tempHireLabel(agent({ id: "a", crew_id: "c" }), now)).toBeNull()
  })
})
