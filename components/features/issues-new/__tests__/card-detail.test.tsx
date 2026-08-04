import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

import { IssueCardDetail } from "../issue-card-detail"
import { ProjectCardDetail } from "../project-card-detail"
import type { Mission, Project, ProjectStats } from "@/lib/types/mission"

// next/link renders an <a> in jsdom without a router; stub it so these stay
// pure render tests.
vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
}))

function issue(over: Partial<Mission> = {}): Mission {
  return {
    id: "cms20ikph011ab4683c02",
    workspace_id: "ws1",
    crew_id: "crew1",
    lead_agent_id: "",
    lead_agent_name: "",
    lead_agent_slug: "",
    trace_id: "",
    title: "Generate a CSV report with random data",
    description: "Create a Python script that generates a CSV file with 50 rows.",
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
    assignee_id: "agent-robin",
    assignee_name: "Robin",
    crew_name: "Engineering",
    ...over,
  }
}

function project(over: Partial<Project> = {}): Project {
  return {
    id: "p1",
    workspace_id: "ws1",
    name: "File Operations",
    slug: "file-operations",
    description: "Everything that reads or writes on disk.",
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

const NO_STATS: ProjectStats = {
  total_issues: 3,
  completed_issues: 0,
  by_status: { BACKLOG: 3 },
  by_assignee: [],
  by_label: [],
  crews: [],
}

describe("IssueCardDetail", () => {
  it("renders the project name in full", () => {
    // The KPI tile it replaces did `name.slice(0, 14)`, so "File Operations"
    // reached the screen as "File Operation" — a project that does not exist.
    render(
      <IssueCardDetail
        issue={issue({ project_id: "p1" })}
        comments={[]}
        activities={[]}
        relations={[]}
        project={project()}
      />,
    )
    expect(screen.getAllByText("File Operations").length).toBeGreaterThan(0)
    expect(screen.queryByText("File Operation")).toBeNull()
  })

  it("titles each rail section exactly once", () => {
    // Every rail card used to be headed twice: a card titled PROPERTIES
    // wrapping a panel that drew its own PROPERTIES header.
    render(
      <IssueCardDetail
        issue={issue()}
        comments={[]}
        activities={[]}
        relations={[]}
        project={null}
      />,
    )
    for (const heading of ["Properties", "Routine", "Project", "Labels", "Metadata"]) {
      expect(screen.getAllByText(heading)).toHaveLength(1)
    }
  })

  it("shows the six figures as one band", () => {
    render(
      <IssueCardDetail
        issue={issue({ estimate: 3, sub_issues_count: 2 })}
        comments={[]}
        activities={[]}
        relations={[]}
        project={null}
      />,
    )
    for (const label of ["Opened", "Updated", "Due", "Estimate", "Sub-issues"]) {
      expect(screen.getByText(label)).toBeInTheDocument()
    }
    // "Comments" is deliberately in two places — the band counts them, the
    // card at the foot holds them. Due and Estimate are not: they used to be
    // in the band AND as rail rows, which is the duplication being removed.
    expect(screen.getAllByText("Comments")).toHaveLength(2)
    expect(screen.getAllByText("Due")).toHaveLength(1)
    expect(screen.getAllByText("Estimate")).toHaveLength(1)
    expect(screen.getByText("3 pts")).toBeInTheDocument()
  })

  it("says what an unbound routine means rather than showing a blank row", () => {
    render(
      <IssueCardDetail
        issue={issue()}
        comments={[]}
        activities={[]}
        relations={[]}
        project={null}
      />,
    )
    expect(screen.getByText(/No routine bound/)).toBeInTheDocument()
  })

  it("names the bound routine when there is one", () => {
    render(
      <IssueCardDetail
        issue={issue({ routine_name: "Nightly export", routine_slug: "nightly-export" })}
        comments={[]}
        activities={[]}
        relations={[]}
        project={null}
      />,
    )
    expect(screen.getAllByText("Nightly export").length).toBeGreaterThan(0)
  })
})

describe("ProjectCardDetail", () => {
  it("lists the project's issues — the question you opened it to ask", () => {
    render(
      <ProjectCardDetail
        project={project()}
        stats={NO_STATS}
        issues={[issue(), issue({ id: "i2", identifier: "ENG-3", title: "Create a directory tree" })]}
      />,
    )
    expect(screen.getByText("ENG-4")).toBeInTheDocument()
    expect(screen.getByText("Create a directory tree")).toBeInTheDocument()
  })

  it("links each row at the canonical issue route", () => {
    render(<ProjectCardDetail project={project()} stats={NO_STATS} issues={[issue()]} />)
    const row = screen.getByText("Generate a CSV report with random data").closest("a")
    expect(row).toHaveAttribute("href", "/issues/ENG-4")
  })

  it("says the project name once, not three times", () => {
    // The old panel stacked a breadcrumb, a header reading "Project" and a
    // title — the name appeared three times above the fold.
    render(<ProjectCardDetail project={project()} stats={NO_STATS} issues={[]} />)
    expect(screen.getAllByText("File Operations")).toHaveLength(1)
  })

  it("reports health in words, not a raw enum", () => {
    render(
      <ProjectCardDetail project={project({ health: "at_risk" })} stats={NO_STATS} issues={[]} />,
    )
    expect(screen.getAllByText("At risk").length).toBeGreaterThan(0)
    expect(screen.queryByText("at_risk")).toBeNull()
  })

  it("tells the reader the project is empty instead of rendering nothing", () => {
    render(<ProjectCardDetail project={project()} stats={null} issues={[]} />)
    expect(screen.getByText(/Nothing is filed under this project yet/)).toBeInTheDocument()
  })
})
