import { describe, it, expect } from "vitest"
import { derivePhases, groupTasksByStatusBucket } from "../mission-modes"
import type { Mission, MissionTask, MissionTaskStatus } from "@/lib/types/mission"

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

describe("derivePhases", () => {
  it("specify is always done; plan/tasks/implement default to active+pending without plan or tasks", () => {
    const phases = derivePhases(makeMission({}))
    expect(phases.map((p) => p.status)).toEqual(["done", "active", "pending", "pending"])
  })

  it("plan blob marks plan done and promotes tasks to active when no tasks exist yet", () => {
    const phases = derivePhases(makeMission({ plan: "the plan" }))
    expect(phases[1].status).toBe("done")
    expect(phases[2].status).toBe("active")
  })

  it("IN_PROGRESS status without an explicit plan still promotes plan past active", () => {
    const phases = derivePhases(makeMission({ status: "IN_PROGRESS" }))
    expect(phases[1].status).toBe("done")
  })

  it("a single running task pins tasks to active and implement to pending", () => {
    const phases = derivePhases(
      makeMission({
        plan: "p",
        status: "IN_PROGRESS",
        tasks: [makeTask({ status: "IN_PROGRESS" })],
      }),
    )
    expect(phases[2].status).toBe("active")
    expect(phases[3].status).toBe("pending")
  })

  it("all tasks terminal flips tasks to done and implement to active until mission completes", () => {
    const phases = derivePhases(
      makeMission({
        plan: "p",
        status: "REVIEW",
        tasks: [
          makeTask({ id: "a", status: "COMPLETED" }),
          makeTask({ id: "b", status: "SKIPPED" }),
        ],
      }),
    )
    expect(phases[2].status).toBe("done")
    expect(phases[3].status).toBe("active")
  })

  it("all tasks FAILED keeps Implement pending (failure should not look like 'ready to implement')", () => {
    // When every task fails before the mission status flips to FAILED,
    // we still mark Tasks as 'done' (resolved — work is no longer in
    // flight) but Implement must stay pending. Otherwise Spec Mode
    // would render 'implementation can start', which is the wrong
    // signal — the operator needs to see the failures, not advance.
    const phases = derivePhases(
      makeMission({
        plan: "p",
        status: "IN_PROGRESS",
        tasks: [
          makeTask({ id: "a", status: "FAILED" as MissionTaskStatus }),
          makeTask({ id: "b", status: "FAILED" as MissionTaskStatus }),
        ],
      }),
    )
    expect(phases[2].status).toBe("done")
    expect(phases[3].status).toBe("pending")
  })

  it("mission terminal flips implement to done", () => {
    const phases = derivePhases(
      makeMission({
        plan: "p",
        status: "COMPLETED",
        tasks: [makeTask({ status: "COMPLETED" })],
      }),
    )
    expect(phases.map((p) => p.status)).toEqual(["done", "done", "done", "done"])
  })

  it("never returns all-pending — at least one phase is active when nothing else qualifies", () => {
    // Synthetic edge case: empty mission with status that maps neither
    // plan-done nor terminal. The promote-to-active fallback ensures
    // the bar always shows a cursor.
    const phases = derivePhases(makeMission({ status: "BACKLOG" }))
    expect(phases.some((p) => p.status === "active")).toBe(true)
  })
})

describe("groupTasksByStatusBucket", () => {
  it("partitions tasks by visible bucket", () => {
    const tasks: MissionTask[] = [
      makeTask({ id: "1", status: "IN_PROGRESS" }),
      makeTask({ id: "2", status: "COMPLETED" }),
      makeTask({ id: "3", status: "AWAITING_APPROVAL" }),
      makeTask({ id: "4", status: "BLOCKED" }),
      makeTask({ id: "5", status: "PENDING", depends_on: "1" }),
    ]
    const b = groupTasksByStatusBucket(tasks)
    expect(b.running.map((t) => t.id)).toEqual(["1"])
    expect(b.done.map((t) => t.id)).toEqual(["2"])
    expect(b.waiting.map((t) => t.id)).toEqual(["3"])
    // BLOCKED and PENDING with deps both fall into the blocked bucket so
    // the wireframe's 🔒 counter reflects everything that can't run yet.
    expect(b.blocked.map((t) => t.id).sort()).toEqual(["4", "5"])
  })

  it("treats FAILED as terminal (done) so the count of 'still moving' tasks stays accurate", () => {
    const b = groupTasksByStatusBucket([
      makeTask({ id: "x", status: "FAILED" as MissionTaskStatus }),
    ])
    expect(b.done.map((t) => t.id)).toEqual(["x"])
  })
})
