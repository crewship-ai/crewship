import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

import { IssueCardDetail } from "../issue-card-detail"
import { ProjectCardDetail } from "../project-card-detail"
import type { IssueActivity, IssueComment, Mission, Project, ProjectStats } from "@/lib/types/mission"
import type { MentionAgent } from "@/lib/mentions"

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

  it("says nothing has run rather than showing a card of dashes", () => {
    render(
      <IssueCardDetail
        issue={issue()}
        comments={[]}
        activities={[]}
        relations={[]}
        project={null}
      />,
    )
    expect(screen.getByText(/Not started yet/)).toBeInTheDocument()
  })

  it("tints the last run by how it ended", () => {
    const { container, rerender } = render(
      <IssueCardDetail
        issue={issue({ status: "COMPLETED" })}
        comments={[]}
        activities={[]}
        relations={[]}
        runs={[{ id: "run_1", status: "COMPLETED", duration_ms: 23700, agent_name: "Robin" }]}
        project={null}
      />,
    )
    expect(screen.getByText("Last run · completed")).toBeInTheDocument()
    expect(container.querySelector(".from-success\\/\\[0\\.06\\]")).not.toBeNull()

    rerender(
      <IssueCardDetail
        issue={issue({ status: "FAILED" })}
        comments={[]}
        activities={[]}
        relations={[]}
        runs={[{ id: "run_2", status: "FAILED", duration_ms: 900, error_message: "exit 1" }]}
        project={null}
      />,
    )
    // The wash is the whole point: it says how this ended before a word of
    // it is read, so a failed run must not keep the success gradient.
    expect(container.querySelector(".from-destructive\\/\\[0\\.06\\]")).not.toBeNull()
    expect(container.querySelector(".from-success\\/\\[0\\.06\\]")).toBeNull()
    expect(screen.getByText("exit 1")).toBeInTheDocument()
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

describe("IssueCardDetail — mentions", () => {
  const ROBIN: MentionAgent = { id: "a_robin", name: "Robin", slug: "robin" }

  function comment(over: Partial<IssueComment> = {}): IssueComment {
    return {
      id: "c1",
      mission_id: "m1",
      author_type: "user",
      author_id: "u1",
      author_name: "Pavel",
      body: "over to you [@robin](crewship:agent/a_robin)",
      created_at: "2026-08-04T09:00:00Z",
      updated_at: "2026-08-04T09:00:00Z",
      ...over,
    }
  }

  function activity(over: Partial<IssueActivity> = {}): IssueActivity {
    return {
      id: "act1",
      mission_id: "m1",
      actor_type: "user",
      actor_id: "u1",
      actor_name: "Pavel",
      action: "mentioned",
      details: "a_robin",
      created_at: "2026-08-04T09:00:00Z",
      ...over,
    }
  }

  it("renders a mention in a comment as a chip", () => {
    render(
      <IssueCardDetail
        issue={issue()}
        comments={[comment()]}
        activities={[]}
        relations={[]}
        project={null}
        agents={[ROBIN]}
      />,
    )
    expect(screen.getByTestId("mention-chip")).toHaveTextContent("@Robin")
  })

  it("reads a mention chip from the roster even when the body says otherwise", () => {
    render(
      <IssueCardDetail
        issue={issue()}
        comments={[comment({ body: "[@release-manager](crewship:agent/a_robin) ship it" })]}
        activities={[]}
        relations={[]}
        project={null}
        agents={[ROBIN]}
      />,
    )
    expect(screen.getByTestId("mention-chip")).toHaveTextContent("@Robin")
    expect(screen.queryByText(/release-manager/)).toBeNull()
  })

  it("says who mentioned whom in the history", () => {
    render(
      <IssueCardDetail
        issue={issue()}
        comments={[]}
        activities={[activity()]}
        relations={[]}
        project={null}
        agents={[ROBIN]}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "history" }))
    expect(screen.getByText("mentioned")).toBeInTheDocument()
    expect(screen.getByTestId("mention-chip")).toHaveTextContent("@Robin")
  })

  it("reads the mention target out of whichever shape the backend sends", () => {
    render(
      <IssueCardDetail
        issue={issue()}
        comments={[]}
        activities={[
          activity({ id: "a1", details: '{"agent_id":"a_robin"}' }),
          activity({ id: "a2", details: "[@robin](crewship:agent/a_robin)" }),
        ]}
        relations={[]}
        project={null}
        agents={[ROBIN]}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "history" }))
    expect(screen.getAllByTestId("mention-chip")).toHaveLength(2)
  })

  it("does not lose a mention activity it cannot resolve", () => {
    render(
      <IssueCardDetail
        issue={issue()}
        comments={[]}
        activities={[activity({ details: "a_someone_else" })]}
        relations={[]}
        project={null}
        agents={[ROBIN]}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "history" }))
    expect(screen.queryByTestId("mention-chip")).toBeNull()
    expect(screen.getByText(/mentioned/)).toBeInTheDocument()
    expect(screen.getByText(/a_someone_else/)).toBeInTheDocument()
  })

  it("still renders activity kinds it has never seen", () => {
    render(
      <IssueCardDetail
        issue={issue()}
        comments={[]}
        activities={[activity({ action: "some_future_kind", details: "whatever" })]}
        relations={[]}
        project={null}
        agents={[ROBIN]}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "history" }))
    expect(screen.getByText(/some future kind/)).toBeInTheDocument()
  })

  it("offers a composer only when the host can actually post", () => {
    const { rerender } = render(
      <IssueCardDetail
        issue={issue()}
        comments={[]}
        activities={[]}
        relations={[]}
        project={null}
        agents={[ROBIN]}
      />,
    )
    expect(screen.queryByRole("combobox")).toBeNull()

    rerender(
      <IssueCardDetail
        issue={issue()}
        comments={[]}
        activities={[]}
        relations={[]}
        project={null}
        agents={[ROBIN]}
        onSubmitComment={vi.fn(async () => true)}
      />,
    )
    expect(screen.getByRole("combobox")).toBeInTheDocument()
  })

  it("hands the composer's body straight to the host", async () => {
    const onSubmitComment = vi.fn(async () => true)
    render(
      <IssueCardDetail
        issue={issue()}
        comments={[]}
        activities={[]}
        relations={[]}
        project={null}
        agents={[ROBIN]}
        onSubmitComment={onSubmitComment}
      />,
    )
    const box = screen.getByRole("combobox") as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "[@robin](crewship:agent/a_robin) please" } })
    fireEvent.keyDown(box, { key: "Enter", metaKey: true })
    expect(onSubmitComment).toHaveBeenCalledWith("[@robin](crewship:agent/a_robin) please")
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

  it("keeps the short cards out of the rail so it cannot outgrow the main column", () => {
    // The void in the first screenshot was structural: a rail taller than the
    // main column leaves dead space beside it that a two-column grid has
    // nothing to fill. Teams / Labels / Metadata span the full width instead.
    const { container } = render(
      <ProjectCardDetail project={project()} stats={NO_STATS} issues={[issue()]} />,
    )
    const body = container.querySelector(".xl\\:grid-cols-3.2xl\\:grid-cols-4")
    expect(body).not.toBeNull()
    for (const heading of ["Teams", "Labels", "Metadata"]) {
      expect(body!.contains(screen.getByText(heading))).toBe(false)
    }
    // Progress and Properties are what the rail keeps.
    expect(body!.contains(screen.getByText("Properties"))).toBe(true)
  })

  it("tints the progress card by health", () => {
    const { container, rerender } = render(
      <ProjectCardDetail project={project({ health: "on_track" })} stats={NO_STATS} issues={[]} />,
    )
    expect(container.querySelector(".from-success\\/\\[0\\.06\\]")).not.toBeNull()

    rerender(
      <ProjectCardDetail project={project({ health: "off_track" })} stats={NO_STATS} issues={[]} />,
    )
    expect(container.querySelector(".from-destructive\\/\\[0\\.06\\]")).not.toBeNull()
    expect(container.querySelector(".from-success\\/\\[0\\.06\\]")).toBeNull()
  })

  it("tells the reader the project is empty instead of rendering nothing", () => {
    render(<ProjectCardDetail project={project()} stats={null} issues={[]} />)
    expect(screen.getByText(/Nothing is filed under this project yet/)).toBeInTheDocument()
  })
})
