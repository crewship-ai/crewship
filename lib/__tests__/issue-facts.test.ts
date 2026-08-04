import { describe, it, expect } from "vitest"

import {
  issueFacts,
  projectFacts,
  issueStatusTone,
  issuePriorityTone,
  projectHealthTone,
} from "@/lib/issue-facts"
import type { Mission, Project, ProjectStats } from "@/lib/types/mission"

// A fixed "now" so the relative-time facts are assertable. Every date in
// this file is expressed against it.
const NOW = new Date("2026-08-04T12:00:00Z")

function issue(over: Partial<Mission> = {}): Mission {
  return {
    id: "m1",
    workspace_id: "ws1",
    crew_id: "c1",
    lead_agent_id: "",
    lead_agent_name: "",
    lead_agent_slug: "",
    trace_id: "",
    title: "Generate a CSV report with random data",
    description: null,
    status: "BACKLOG",
    plan: null,
    workflow_template: null,
    total_token_count: null,
    total_estimated_cost: null,
    created_at: "2026-08-01T12:00:00Z",
    updated_at: "2026-08-04T10:00:00Z",
    completed_at: null,
    task_stats: null,
    tasks: [],
    total_token_budget: null,
    complexity: null,
    pattern: null,
    identifier: "ENG-4",
    priority: "medium",
    ...over,
  }
}

function project(over: Partial<Project> = {}): Project {
  return {
    id: "p1",
    workspace_id: "ws1",
    name: "File Operations",
    slug: "file-operations",
    description: null,
    icon: "folder",
    color: "blue",
    status: "in_progress",
    priority: "high",
    health: "on_track",
    lead_type: null,
    lead_id: null,
    start_date: null,
    target_date: null,
    created_at: "2026-07-26T12:00:00Z",
    updated_at: "2026-08-01T12:00:00Z",
    issue_count: 3,
    done_count: 0,
    progress: 0,
    ...over,
  }
}

describe("issueFacts", () => {
  it("returns the six operator facts in a stable order", () => {
    const facts = issueFacts(issue(), { comments: 0, now: NOW })
    expect(facts.map((f) => f.label)).toEqual([
      "Opened",
      "Updated",
      "Due",
      "Estimate",
      "Sub-issues",
      "Comments",
    ])
  })

  it("marks a past due date on an open issue as destructive", () => {
    const facts = issueFacts(issue({ due_date: "2026-08-02T12:00:00Z" }), {
      comments: 0,
      now: NOW,
    })
    const due = facts.find((f) => f.label === "Due")!
    expect(due.tone).toBe("destructive")
  })

  it("does not call a past due date overdue once the issue is done", () => {
    const facts = issueFacts(
      issue({
        due_date: "2026-08-02T12:00:00Z",
        status: "COMPLETED",
        completed_at: "2026-08-03T12:00:00Z",
      }),
      { comments: 0, now: NOW },
    )
    // A completed issue reports when it closed, not a due date it can no
    // longer miss.
    expect(facts.map((f) => f.label)).toContain("Closed")
    expect(facts.map((f) => f.label)).not.toContain("Due")
    expect(facts.find((f) => f.label === "Closed")!.tone).toBe("success")
  })

  it("shows an em dash rather than a zero for an absent estimate", () => {
    const facts = issueFacts(issue({ estimate: null }), { comments: 0, now: NOW })
    expect(facts.find((f) => f.label === "Estimate")!.value).toBe("—")
  })

  it("renders a set estimate in points", () => {
    const facts = issueFacts(issue({ estimate: 3 }), { comments: 0, now: NOW })
    expect(facts.find((f) => f.label === "Estimate")!.value).toBe("3 pts")
  })

  it("counts sub-issues and comments as plain numbers", () => {
    const facts = issueFacts(issue({ sub_issues_count: 2 }), { comments: 5, now: NOW })
    expect(facts.find((f) => f.label === "Sub-issues")!.value).toBe("2")
    expect(facts.find((f) => f.label === "Comments")!.value).toBe("5")
  })
})

describe("projectFacts", () => {
  it("returns the six project facts in a stable order", () => {
    const facts = projectFacts(project(), null)
    expect(facts.map((f) => f.label)).toEqual([
      "Scope",
      "Completed",
      "In progress",
      "Progress",
      "Started",
      "Target",
    ])
  })

  it("prefers the stats endpoint over the denormalised counters", () => {
    const stats: ProjectStats = {
      total_issues: 7,
      completed_issues: 4,
      by_status: { IN_PROGRESS: 2 },
      by_assignee: [],
      by_label: [],
      crews: [],
    }
    const facts = projectFacts(project({ issue_count: 3, done_count: 0 }), stats)
    expect(facts.find((f) => f.label === "Scope")!.value).toBe("7")
    expect(facts.find((f) => f.label === "Completed")!.value).toBe("4")
    expect(facts.find((f) => f.label === "In progress")!.value).toBe("2")
  })

  it("counts issues under review as in progress", () => {
    const stats: ProjectStats = {
      total_issues: 4,
      completed_issues: 0,
      by_status: { IN_PROGRESS: 1, REVIEW: 2 },
      by_assignee: [],
      by_label: [],
      crews: [],
    }
    expect(projectFacts(project(), stats).find((f) => f.label === "In progress")!.value).toBe("3")
  })

  it("marks a missed target date on an unfinished project as destructive", () => {
    const facts = projectFacts(
      project({ target_date: "2026-08-01T12:00:00Z" }),
      null,
      { now: NOW },
    )
    expect(facts.find((f) => f.label === "Target")!.tone).toBe("destructive")
  })

  it("leaves the target alone once the project is completed", () => {
    const facts = projectFacts(
      project({ target_date: "2026-08-01T12:00:00Z", status: "completed" }),
      null,
      { now: NOW },
    )
    expect(facts.find((f) => f.label === "Target")!.tone).toBeUndefined()
  })
})

describe("tones", () => {
  it("maps issue status onto the shared detail palette", () => {
    expect(issueStatusTone("IN_PROGRESS")).toBe("blue")
    expect(issueStatusTone("REVIEW")).toBe("purple")
    expect(issueStatusTone("COMPLETED")).toBe("success")
    expect(issueStatusTone("DONE")).toBe("success")
    expect(issueStatusTone("FAILED")).toBe("destructive")
    expect(issueStatusTone("BACKLOG")).toBe("default")
  })

  it("keeps low priority out of the alarm colours", () => {
    expect(issuePriorityTone("urgent")).toBe("destructive")
    expect(issuePriorityTone("high")).toBe("warn")
    expect(issuePriorityTone("low")).toBe("default")
    expect(issuePriorityTone("none")).toBe("default")
  })

  it("maps project health onto the shared detail palette", () => {
    expect(projectHealthTone("on_track")).toBe("success")
    expect(projectHealthTone("at_risk")).toBe("warn")
    expect(projectHealthTone("off_track")).toBe("destructive")
  })
})
