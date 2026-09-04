import { describe, it, expect, vi } from "vitest"
import { render, screen, within, fireEvent } from "@testing-library/react"

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

  // A10 (invariant I5 — "delegating to an agent never changes the human
  // owner"): owner and delegate must render as two separate things, and an
  // agent delegate must never appear in the owner's slot.
  it("renders owner and delegate as two separate people, not one merged assignee", () => {
    render(
      <IssueCardDetail
        issue={issue({
          owner: { id: "user-nadia", name: "Nadia" },
          delegate: { id: "agent-robin", name: "Robin" },
        })}
        comments={[]}
        activities={[]}
        relations={[]}
        project={null}
      />,
    )
    expect(screen.getByText("Owner")).toBeInTheDocument()
    expect(screen.getByText("Delegate")).toBeInTheDocument()
    expect(screen.getAllByText("Nadia").length).toBeGreaterThan(0)
    expect(screen.getAllByText("Robin").length).toBeGreaterThan(0)
    // The legacy single-"Assignee" row must not also render once the typed
    // fields are present — that would be the same defect under a new name.
    expect(screen.queryByText("Assignee")).toBeNull()
  })

  it("falls back to the legacy assignee row when neither owner nor delegate is set", () => {
    // A row this client fetched before the A10 backfill reached it —
    // covered separately from the typed-field case above so a regression
    // in either path fails its own test.
    render(
      <IssueCardDetail
        issue={issue({ owner: undefined, delegate: undefined })}
        comments={[]}
        activities={[]}
        relations={[]}
        project={null}
      />,
    )
    expect(screen.getByText("Assignee")).toBeInTheDocument()
    expect(screen.getAllByText("Robin").length).toBeGreaterThan(0)
    expect(screen.queryByText("Owner")).toBeNull()
    expect(screen.queryByText("Delegate")).toBeNull()
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

  it("lists every run with its status as a word, and links the run when it has one", () => {
    render(
      <IssueCardDetail
        issue={issue({ status: "COMPLETED" })}
        comments={[]}
        activities={[]}
        relations={[]}
        runs={[
          { id: "asg_1", run_id: "run_1", status: "COMPLETED", duration_ms: 23700, agent_name: "Robin", agent_slug: "robin", task: "Build the page" },
          { id: "asg_2", status: "FAILED", duration_ms: 900, error_message: "exit 1", agent_name: "Sam" },
        ]}
        project={null}
      />,
    )
    // Both runs, not runs[0] alone; the raw enum never reaches the screen.
    const rows = screen.getAllByTestId("issue-run-row")
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText("Done")).toBeInTheDocument()
    expect(within(rows[1]).getByText("Failed")).toBeInTheDocument()
    expect(screen.queryByText("COMPLETED")).toBeNull()
    expect(screen.getByText("exit 1")).toBeInTheDocument()
    // The run that reached the journal opens; the one that did not says so.
    expect(screen.getByRole("link", { name: /open run/i })).toHaveAttribute("href", "/activity?run=run_1")
    expect(screen.getByText("no run")).toBeInTheDocument()
    // The Related card names the crew, the journal and Activity as links.
    expect(screen.getByRole("link", { name: /trace ENG-4/i })).toHaveAttribute("href", "/journal?mission_id=ENG-4")
    expect(screen.getByRole("link", { name: /all runs$/i }).getAttribute("href")).toMatch(/^\/activity\?mission=/)
  })

  it("keeps the rail in view instead of stranding it beside a long description", () => {
    // A description longer than the rail used to end the rail halfway down
    // and leave a black column beside it for the rest of the scroll. The
    // reverse case (a rail longer than the body) was already fixed by moving
    // the short cards to the full-width foot row.
    render(
      <IssueCardDetail
        issue={issue({ description: "para\n\n".repeat(400) })}
        comments={[]}
        activities={[]}
        relations={[]}
        project={null}
      />,
    )
    const rail = screen.getByTestId("issue-detail-rail")
    expect(rail.className).toMatch(/\bxl:sticky\b/)
    // Grid children stretch by default, and a stretched item can never stick.
    expect(rail.className).toMatch(/\bxl:self-start\b/)
    // A rail taller than the viewport would pin its head and hide its tail;
    // capping it and letting it scroll keeps every card reachable.
    expect(rail.className).toMatch(/\bxl:overflow-y-auto\b/)
    // Below xl the columns stack, and a sticky column there fights the page
    // scroll — every sticky utility must carry the breakpoint.
    expect(rail.className).not.toMatch(/(^|\s)sticky\b/)
  })

  it("renders the run activity the host passes in", () => {
    render(
      <IssueCardDetail
        issue={issue()}
        comments={[]}
        activities={[]}
        relations={[]}
        project={null}
        runActivity={<div data-testid="run-activity-slot" />}
      />,
    )
    expect(screen.getByTestId("run-activity-slot")).toBeInTheDocument()
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
