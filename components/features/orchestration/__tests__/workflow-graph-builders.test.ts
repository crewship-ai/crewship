import { describe, it, expect, vi } from "vitest"
import type { Mission, MissionTask } from "@/lib/types/mission"
import type { CrewSummary, AgentSummary, CrewConnection } from "@/lib/types/orchestration"
import { buildGraphData, buildFlatGraphData, buildPipelineNodes, buildIssueRoutineEdges, parseDependsOn, pickEdgeColor, type BuildInput } from "../workflow-graph-builders"
import { EDGE_COLOR_PALETTE, STATUS_COLORS } from "@/lib/colors"

function makeTask(overrides: Partial<MissionTask>): MissionTask {
  return {
    id: "t1",
    mission_id: "m1",
    assigned_agent_id: null,
    agent_name: null,
    agent_slug: null,
    title: "task",
    description: null,
    status: "PENDING",
    task_order: 1,
    depends_on: "",
    iteration: null,
    max_iterations: null,
    result_summary: null,
    output_path: null,
    error_message: null,
    assignment_id: null,
    token_count: null,
    estimated_cost: null,
    started_at: null,
    completed_at: null,
    duration_ms: null,
    created_at: "2026-04-30T00:00:00Z",
    updated_at: "2026-04-30T00:00:00Z",
    complexity: null,
    token_budget: null,
    tokens_used: null,
    tool_calls_count: null,
    tool_calls_budget: null,
    confidence: null,
    approval_required: false,
    approval_status: null,
    approved_by: null,
    approved_at: null,
    needs_review: false,
    handoff_context: null,
    evaluation_status: null,
    evaluation_notes: null,
    retry_count: null,
    priority: null,
    labels: null,
    ...overrides,
  }
}

function makeMission(overrides: Partial<Mission>): Mission {
  return {
    id: "m1",
    workspace_id: "w1",
    crew_id: "c1",
    lead_agent_id: "a1",
    lead_agent_name: "Lead",
    lead_agent_slug: "lead",
    trace_id: "trace-1",
    title: "M",
    description: "desc",
    status: "TODO",
    plan: null,
    workflow_template: null,
    total_token_count: null,
    total_estimated_cost: null,
    created_at: "2026-04-30T00:00:00Z",
    updated_at: "2026-04-30T00:00:00Z",
    completed_at: null,
    task_stats: null,
    tasks: [],
    total_token_budget: null,
    complexity: null,
    pattern: null,
    ...overrides,
  }
}

function crew(id: string): CrewSummary { return { id, name: id, slug: id, color: null, icon: null } }
function agent(id: string, crewId: string | null): AgentSummary {
  return { id, slug: id, name: id, crew_id: crewId, avatar_seed: id, avatar_style: "bottts", role_title: null, agent_role: null, crew: null }
}
function connection(from: string, to: string, id = "conn"): CrewConnection {
  return { id, from_crew_id: from, to_crew_id: to, from_crew_name: from, from_crew_slug: from, to_crew_name: to, to_crew_slug: to, direction: "bidirectional", status: "active", created_at: "2026-01-01" }
}
function graph(overrides: Partial<BuildInput> = {}) {
  return buildGraphData({ missions: [], crews: [], agents: [], connections: [], collapsedCrews: new Set(), onToggleCollapse: vi.fn(), ...overrides })
}
const pipeline = { id: "p", name: "Pipeline", slug: "pipeline", invocation_count: 3 }

describe("workflow graph data", () => {
  it.each([null, undefined, "", "broken", "{}", '"text"'])("ignores invalid dependencies %s", raw => {
    expect(parseDependsOn(raw)).toEqual([])
  })
  it("retains only string dependency identities and uses stable palette colors", () => {
    expect(parseDependsOn('["a",null,7,"b"]')).toEqual(["a", "b"])
    expect(pickEdgeColor("a", "b")).toBe(pickEdgeColor("a", "b"))
    expect(EDGE_COLOR_PALETTE).toContain(pickEdgeColor("long-source", "long-target"))
  })
  it("lays out assigned tasks in their crews, with parents before children", () => {
    const tasks = [
      makeTask({ id: "a", status: "COMPLETED", agent_slug: "one", agent_name: "One" }),
      makeTask({ id: "b", status: "IN_PROGRESS", agent_slug: "one", depends_on: '["a"]' }),
      makeTask({ id: "c", status: "FAILED", agent_slug: "two", depends_on: '["a","b"]' }),
      makeTask({ id: "d", status: "PENDING", depends_on: '["a","missing"]' }),
    ]
    const onToggleCollapse = vi.fn()
    const second = { ...agent("two", null), crew: { name: "c2", slug: "c2", color: null } }
    const built = graph({ missions: [makeMission({ status: "IN_PROGRESS", tasks })], crews: [{ ...crew("c1"), _count: { agents: 1 } }, crew("c2")], agents: [agent("one", "c1"), second, agent("unassigned", null)], connections: [connection("c1", "c2"), connection("c1", "absent", "ignored")], onToggleCollapse })
    expect(built.nodes.slice(0, 2).map(n => n.id)).toEqual(["crew-c1", "crew-c2"])
    expect(built.nodes.find(n => n.id === "crew-c1")?.data).toMatchObject({ taskCount: 3, activeCount: 1, completedCount: 1, failedCount: 0, agentCount: 1, onToggleCollapse })
    expect(built.nodes.find(n => n.id === "crew-c2")?.data).toMatchObject({ taskCount: 1, failedCount: 1 })
    expect(built.nodes.find(n => n.id === "a")?.data).toMatchObject({ agentName: "One", avatarSeed: "one", avatarStyle: "bottts" })
    expect(built.nodes.find(n => n.id === "d")?.data.agentName).toBe("Unassigned")
    expect(built.edges.find(e => e.id === "e-a-b")?.data).toMatchObject({ active: true, color: STATUS_COLORS.IN_PROGRESS })
    expect(built.edges.find(e => e.id === "e-x-a-c")?.data).toMatchObject({ active: false, color: STATUS_COLORS.REVIEW })
    expect(built.edges.some(e => e.id === "perm-conn")).toBe(true)
    expect(built.edges.some(e => e.id === "perm-ignored")).toBe(false)
    for (const node of built.nodes) {
      expect(Number.isFinite(node.position.x)).toBe(true)
      expect(Number.isFinite(node.position.y)).toBe(true)
    }
    for (const edge of built.edges) {
      expect(built.nodes.some(n => n.id === edge.source)).toBe(true)
      expect(built.nodes.some(n => n.id === edge.target)).toBe(true)
    }
  })
  it("hides collapsed children and their dependency edges but retains permissions", () => {
    const missions = [makeMission({ status: "REVIEW", tasks: [makeTask({ id: "a" }), makeTask({ id: "b", agent_slug: "two", depends_on: '["a"]' })] })]
    const built = graph({ missions, crews: [crew("c1"), crew("c2")], agents: [agent("two", "c2")], collapsedCrews: new Set(["c1"]), connections: [connection("c1", "c2")] })
    expect(built.nodes.find(n => n.id === "crew-c1")).toMatchObject({ data: { collapsed: true }, style: { width: 300, height: 50 } })
    expect(built.nodes.some(n => n.id === "a")).toBe(false)
    expect(built.edges.map(e => e.id)).toEqual(["perm-conn"])
  })
  it("ignores inactive missions when active work exists and limits fallback to three recent missions", () => {
    const missions = [1, 2, 3, 4].map(i => makeMission({ id: String(i), status: "DONE", crew_id: `c${i}`, updated_at: `2026-01-0${i}`, tasks: [makeTask({ id: `t${i}` })] }))
    const crews = [1, 2, 3, 4].map(i => crew(`c${i}`))
    expect(graph({ missions, crews }).nodes.filter(n => n.type === "agent").map(n => n.id).sort()).toEqual(["t2", "t3", "t4"])
    missions[0].status = "PLANNING"
    expect(graph({ missions, crews }).nodes.filter(n => n.type === "agent").map(n => n.id)).toEqual(["t1"])
    expect(graph().nodes).toEqual([])
  })
  it("orders connected crews without a task dependency", () => {
    const missions = [makeMission({ tasks: [makeTask({ id: "a" })] }), makeMission({ id: "m2", crew_id: "c2", tasks: [makeTask({ id: "b" })] })]
    const built = graph({ missions, crews: [crew("c1"), crew("c2")], connections: [connection("c2", "c1"), connection("c2", "c1", "second")] })
    expect(built.nodes.find(n => n.id === "crew-c2")!.position.x).toBeLessThan(built.nodes.find(n => n.id === "crew-c1")!.position.x)
  })
})

describe("flat workflow graph", () => {
  it("places roots before dependent tasks and reports token totals", () => {
    const mission = makeMission({ status: "IN_PROGRESS", tasks: [makeTask({ id: "b", depends_on: '["a"]', status: "IN_PROGRESS", task_order: 2, token_count: 1000 }), makeTask({ id: "a", task_order: 1, agent_name: "Worker" }), makeTask({ id: "c", task_order: 3, depends_on: '["a"]', token_count: 500 })] })
    const built = buildFlatGraphData([mission])
    expect(built.nodes[0].data.label).toBe("M · 1.5k tok")
    expect(built.nodes.find(n => n.id === "b")!.position.x).toBeGreaterThan(built.nodes.find(n => n.id === "a")!.position.x)
    expect(built.edges.map(e => [e.source, e.target])).toEqual([["mission-m1", "a"], ["a", "b"], ["a", "c"]])
    expect(built.edges.find(e => e.target === "b")?.data?.active).toBe(true)
    expect(built.edges.find(e => e.target === "c")?.data?.active).toBe(false)
  })
  it.each(["PLANNING", "IN_PROGRESS"] as const)("labels %s missions awaiting tasks", status => {
    expect(buildFlatGraphData([makeMission({ status })]).nodes[0].data.label).toBe("M — Lead is planning tasks...")
  })
  it("selects recent inactive missions without mutating their order", () => {
    const missions = [1, 2, 3, 4].map(i => makeMission({ id: String(i), status: "DONE", updated_at: `2026-01-0${i}` }))
    expect(buildFlatGraphData(missions).nodes.map(n => n.id)).toEqual(["mission-4", "mission-3", "mission-2"])
    expect(missions.map(m => m.id)).toEqual(["1", "2", "3", "4"])
  })
  it("terminates layout for cyclic dependencies", () => {
    const built = buildFlatGraphData([makeMission({ tasks: [makeTask({ id: "a", depends_on: '["b"]' }), makeTask({ id: "b", depends_on: '["a"]' })] })])
    expect(built.nodes).toHaveLength(3)
    expect(built.nodes.every(n => Number.isFinite(n.position.x))).toBe(true)
  })
  it("does not emit edges for nonexistent dependency nodes", () => {
    const built = buildFlatGraphData([makeMission({ tasks: [makeTask({ depends_on: '["missing"]' })] })])
    expect(built.edges.every(e => built.nodes.some(n => n.id === e.source))).toBe(true)
  })
})

describe("routine registry graph", () => {
  it("handles empty registries", () => {
    expect(buildPipelineNodes([])).toEqual([])
    expect(buildIssueRoutineEdges([], [pipeline], new Set())).toEqual([])
    expect(buildIssueRoutineEdges([makeMission({})], [], new Set())).toEqual([])
  })
  it("maps status, authors and configurable row positions", () => {
    const nodes = buildPipelineNodes([
      { ...pipeline, last_invocation_status: "completed", author_crew_id: "c1" },
      { ...pipeline, id: "p2", last_invocation_status: "FAILED", author_crew_id: "missing" },
      { ...pipeline, id: "p3" },
    ], { baselineY: 0, xStart: 10, pitch: 400, crewNameById: new Map([["c1", "Crew One"]]) })
    expect(nodes.map(n => n.position)).toEqual([{ x: 10, y: 0 }, { x: 410, y: 0 }, { x: 810, y: 0 }])
    expect(nodes.map(n => n.data.status)).toEqual(["completed", "failed", "queued"])
    expect(nodes.map(n => n.data.authorCrewLabel)).toEqual(["Crew One", "missing", undefined])
    expect(buildPipelineNodes([pipeline])[0]).toMatchObject({ position: { x: 0, y: 800 }, data: { stepCount: 3, stepIndex: 3 }, draggable: false })
  })
  it("binds the visible mission or first visible task to its routine", () => {
    const missions = [makeMission({ id: "flat", routine_id: "p" }), makeMission({ id: "grouped", routine_id: "p", tasks: [makeTask({ id: "hidden" }), makeTask({ id: "shown" })] })]
    const edges = buildIssueRoutineEdges(missions, [pipeline], new Set(["mission-flat", "shown", "pipeline:p"]))
    expect(edges.map(e => [e.source, e.target])).toEqual([["mission-flat", "pipeline:p"], ["shown", "pipeline:p"]])
    expect(edges.every(e => e.animated === false && e.style?.strokeDasharray)).toBe(true)
  })
  it("omits bindings without a visible source, target or known routine", () => {
    const missions = [makeMission({}), makeMission({ routine_id: "missing" }), makeMission({ routine_id: "p" }), makeMission({ routine_id: "p", tasks: [makeTask({})] })]
    expect(buildIssueRoutineEdges(missions, [pipeline], new Set(["pipeline:p"]))).toEqual([])
    expect(buildIssueRoutineEdges(missions, [pipeline], new Set(["mission-m1"]))).toEqual([])
  })
})

describe("partial workflow responses", () => {
  it("tolerates missing task arrays in legacy mission responses", () => {
    const mission = makeMission({ status: "PLANNING" })
    Reflect.deleteProperty(mission, "tasks")
    expect(graph({ missions: [mission], crews: [crew("c1")] }).nodes).toEqual([])
    expect(buildFlatGraphData([mission]).nodes).toHaveLength(1)
  })
  it("skips tasks without a resolvable crew and does not invent permission endpoints", () => {
    const missions = [
      makeMission({ id: "missing", crew_id: "missing", tasks: [makeTask({ id: "orphan" })] }),
      makeMission({ id: "empty", crew_id: "", tasks: [makeTask({ id: "no-crew" })] }),
      makeMission({ tasks: [makeTask({ id: "known", agent_slug: "lost" })] }),
    ]
    const missingCrewAgent = { ...agent("lost", null), crew: { name: "missing", slug: "absent", color: null } }
    const built = graph({ missions, crews: [{ ...crew("c1"), name: "" }], agents: [{ ...agent("no-slug", null), slug: "" }, missingCrewAgent], connections: [connection("missing", "c1")] })
    expect(built.nodes.map(n => n.id)).toEqual(["crew-c1", "known"])
    expect(built.edges.every(e => built.nodes.some(n => n.id === e.source) && built.nodes.some(n => n.id === e.target))).toBe(true)
  })
})
