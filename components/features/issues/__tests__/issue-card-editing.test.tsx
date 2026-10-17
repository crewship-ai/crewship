// The card is now the production issue detail, not a preview of one.
//
// The failure mode this file exists to catch: a prettier screen that quietly
// dropped a picker. A reviewer reading the diff sees a nicer layout and does
// not notice that "milestone" no longer has anywhere to be set. So every
// capability the old two screens had — issue-page-client + issue-sidebar +
// issue-properties-panel — is asserted here by name, and the ones that write
// are asserted to actually write.

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

import { IssueCardDetail } from "../issue-card-detail"
import type { IssueCardEdit } from "../issue-card-editors"
import type { IssueLabel, Milestone, Mission, Project } from "@/lib/types/mission"

vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
}))

// Tiptap drags in ProseMirror and a DOM it cannot have here; the point of
// this file is the wiring, so the editor is stubbed down to a textarea that
// reports its markdown the same way.
vi.mock("@/components/features/issues/tiptap-editor", () => ({
  TiptapEditor: ({
    content,
    onChange,
    onBlur,
  }: {
    content: string
    onChange: (md: string) => void
    onBlur: () => void
  }) => (
    <textarea
      aria-label="Description"
      defaultValue={content}
      onChange={(e) => onChange(e.target.value)}
      onBlur={onBlur}
    />
  ),
}))

const AGENTS = [
  { id: "agent-robin", name: "Robin", slug: "robin" },
  { id: "agent-ada", name: "Ada", slug: "ada" },
]

const LABELS: IssueLabel[] = [
  { id: "l1", name: "backend", color: "#ff0000", label_group: null },
  { id: "l2", name: "urgent-fix", color: "#00ff00", label_group: null },
]

const PROJECTS: Project[] = [
  {
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
  },
]

const MILESTONES: Milestone[] = [
  {
    id: "m1",
    project_id: "p1",
    name: "Beta cut",
    description: null,
    target_date: null,
    status: "active",
    position: 0,
    created_at: "2026-07-26T12:00:00Z",
    updated_at: "2026-07-26T12:00:00Z",
  },
]

function issue(over: Partial<Mission> = {}): Mission {
  return {
    id: "iss1",
    workspace_id: "ws1",
    crew_id: "crew1",
    lead_agent_id: "",
    lead_agent_name: "",
    lead_agent_slug: "",
    trace_id: "",
    title: "Generate a CSV report",
    description: "Create a Python script.",
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
    project_id: "p1",
    ...over,
  }
}

function edit(over: Partial<IssueCardEdit> = {}): IssueCardEdit {
  return {
    agents: AGENTS,
    labels: LABELS,
    projects: PROJECTS,
    routines: [{ id: "r1", name: "Nightly export", slug: "nightly-export" }],
    milestones: MILESTONES,
    patch: vi.fn(async () => true),
    createLabel: vi.fn(async () => {}),
    addRelation: vi.fn(async () => true),
    removeRelation: vi.fn(async () => {}),
    ...over,
  }
}

function renderCard(over: { issue?: Mission; edit?: IssueCardEdit | null } = {}) {
  const e = over.edit === null ? undefined : (over.edit ?? edit())
  render(
    <IssueCardDetail
      issue={over.issue ?? issue()}
      comments={[]}
      activities={[]}
      relations={[]}
      project={PROJECTS[0]}
      edit={e}
    />,
  )
  return e
}

/** Opens a popover by its trigger's accessible name and returns its content. */
function openPicker(name: string | RegExp) {
  fireEvent.click(screen.getByRole("button", { name }))
  return screen.getByRole("dialog")
}

describe("IssueCardDetail — every editor survives the promotion", () => {
  it("offers each property the old sidebar could set", () => {
    renderCard()
    for (const name of [
      /change status/i,
      /change priority/i,
      /change assignee/i,
      /change due date/i,
      /change estimate/i,
      /change milestone/i,
      /change project/i,
      /change routine/i,
      /add label/i,
      /add relation/i,
      /edit title/i,
    ]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument()
    }
    // Description is a real editor, not a rendered blob.
    expect(screen.getByLabelText("Description")).toBeInTheDocument()
  })

  it("stays read-only when the host supplies no editors", () => {
    renderCard({ edit: null })
    expect(screen.queryByRole("button", { name: /change status/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /add label/i })).toBeNull()
    expect(screen.queryByLabelText("Description")).toBeNull()
  })
})

describe("IssueCardDetail — the editors write", () => {
  it("patches the status the reader picks", () => {
    const e = renderCard()!
    const menu = openPicker(/change status/i)
    fireEvent.click(within(menu).getByRole("button", { name: /in progress/i }))
    expect(e.patch).toHaveBeenCalledWith({ status: "IN_PROGRESS" })
  })

  it("patches the priority", () => {
    const e = renderCard()!
    const menu = openPicker(/change priority/i)
    fireEvent.click(within(menu).getByRole("button", { name: /^urgent$/i }))
    expect(e.patch).toHaveBeenCalledWith({ priority: "urgent" })
  })

  it("assigns an agent, and unassigns again", () => {
    const e = renderCard()!
    fireEvent.click(within(openPicker(/change assignee/i)).getByRole("button", { name: /Ada/ }))
    expect(e.patch).toHaveBeenCalledWith({ assignee_type: "agent", assignee_id: "agent-ada" })

    // "" and not null: PATCH reads every field as an optional pointer, so a
    // JSON null is indistinguishable from an omitted field. The old sidebar
    // sent null here and unassigning silently did nothing.
    fireEvent.click(within(openPicker(/change assignee/i)).getByRole("button", { name: /unassigned/i }))
    expect(e.patch).toHaveBeenCalledWith({ assignee_type: "", assignee_id: "" })
  })

  it("filters the assignee list by the search box", () => {
    renderCard()
    const menu = openPicker(/change assignee/i)
    fireEvent.change(within(menu).getByRole("searchbox"), { target: { value: "ada" } })
    expect(within(menu).queryByRole("button", { name: /Robin/ })).toBeNull()
    expect(within(menu).getByRole("button", { name: /Ada/ })).toBeInTheDocument()
  })

  it("sets and clears the due date", () => {
    const e = renderCard({ issue: issue({ due_date: "2026-09-01" }) })!
    const menu = openPicker(/change due date/i)
    fireEvent.change(within(menu).getByLabelText(/due date/i), { target: { value: "2026-10-02" } })
    expect(e.patch).toHaveBeenCalledWith({ due_date: "2026-10-02" })
    fireEvent.click(within(menu).getByRole("button", { name: /clear due date/i }))
    expect(e.patch).toHaveBeenCalledWith({ due_date: "" })
  })

  it("sets and clears the estimate", () => {
    const e = renderCard({ issue: issue({ estimate: 5 }) })!
    fireEvent.click(within(openPicker(/change estimate/i)).getByRole("button", { name: /^8 points$/i }))
    expect(e.patch).toHaveBeenCalledWith({ estimate: 8 })
    // Picking closes the menu — clearing is a second trip, as it is for a user.
    fireEvent.click(within(openPicker(/change estimate/i)).getByRole("button", { name: /clear estimate/i }))
    expect(e.patch).toHaveBeenCalledWith({ estimate: null })
  })

  it("sets a milestone from the issue's project", () => {
    const e = renderCard()!
    fireEvent.click(within(openPicker(/change milestone/i)).getByRole("button", { name: /beta cut/i }))
    expect(e.patch).toHaveBeenCalledWith({ milestone_id: "m1" })
  })

  it("moves the issue to another project, and out of one", () => {
    const e = renderCard()!
    fireEvent.click(within(openPicker(/change project/i)).getByRole("button", { name: /no project/i }))
    expect(e.patch).toHaveBeenCalledWith({ project_id: "" })
    fireEvent.click(within(openPicker(/change project/i)).getByRole("button", { name: /file operations/i }))
    expect(e.patch).toHaveBeenCalledWith({ project_id: "p1" })
  })

  it("binds and unbinds a routine", () => {
    const e = renderCard()!
    fireEvent.click(within(openPicker(/change routine/i)).getByRole("button", { name: /nightly export/i }))
    expect(e.patch).toHaveBeenCalledWith({ routine_id: "r1" })
    fireEvent.click(within(openPicker(/change routine/i)).getByRole("button", { name: /no routine/i }))
    expect(e.patch).toHaveBeenCalledWith({ routine_id: "" })
  })

  it("toggles a label on and off — through the field the API actually reads", () => {
    // `labels`, not `label_ids`. The shipped sidebar sent label_ids, which
    // PATCH /crews/{id}/issues/{ident} has no field for, so the request fell
    // through to "No fields to update" and 400ed. Asserting the wire name is
    // the point of this test: the gesture looked fine and never worked.
    const withLabel = issue({ labels: [LABELS[0]] })
    const e = renderCard({ issue: withLabel })!
    const menu = openPicker(/add label/i)
    fireEvent.click(within(menu).getByRole("button", { name: /^backend$/i }))
    expect(e.patch).toHaveBeenCalledWith({ labels: [] })
    fireEvent.click(within(menu).getByRole("button", { name: /^urgent-fix$/i }))
    expect(e.patch).toHaveBeenCalledWith({ labels: ["l1", "l2"] })
  })

  it("creates a label that does not exist yet", () => {
    const e = renderCard()!
    const menu = openPicker(/add label/i)
    fireEvent.change(within(menu).getByRole("searchbox"), { target: { value: "flaky" } })
    fireEvent.click(within(menu).getByRole("button", { name: /create .flaky./i }))
    expect(e.createLabel).toHaveBeenCalledWith("flaky")
  })

  it("adds a link to another issue and removes an existing one", () => {
    const e = edit()
    render(
      <IssueCardDetail
        issue={issue()}
        comments={[]}
        activities={[]}
        relations={[
          {
            id: "rel1",
            source_id: "iss1",
            target_id: "iss2",
            relation_type: "blocks",
            target_identifier: "ENG-9",
            target_title: "Ship it",
            created_at: "2026-08-01T12:00:00Z",
          },
        ]}
        project={PROJECTS[0]}
        edit={e}
      />,
    )
    const menu = openPicker(/add relation/i)
    fireEvent.change(within(menu).getByLabelText(/^issue$/i), { target: { value: "ENG-9" } })
    fireEvent.click(within(menu).getByRole("button", { name: /^add relation$/i }))
    expect(e.addRelation).toHaveBeenCalledWith("ENG-9", "relates_to")

    fireEvent.click(screen.getByRole("button", { name: /remove link to ENG-9/i }))
    expect(e.removeRelation).toHaveBeenCalledWith("rel1")
  })

  it("Add relation is a popover at the Relations field: a type choice, an issue picker, Cancel and Add relation", async () => {
    const searchIssues = vi.fn(async () => [{ identifier: "ENG-12", title: "Replace the Linear token", status: "IN_PROGRESS" as const }])
    const e = edit({ searchIssues })
    renderCard({ edit: e })
    const trigger = screen.getByRole("button", { name: "Add relation" })
    // A labelled button in the section header, not a bare plus.
    expect(trigger).toHaveTextContent("Add")
    fireEvent.click(trigger)
    const pop = screen.getByRole("dialog")
    expect(within(pop).getByRole("heading", { name: "Add relation" })).toBeInTheDocument()
    const kinds = within(pop).getByRole("radiogroup", { name: "Relation" })
    expect(within(kinds).getAllByRole("radio").map((r) => r.textContent)).toEqual(["Blocks", "Blocked by", "Related", "Duplicate of"])
    fireEvent.click(within(kinds).getByRole("radio", { name: "Blocked by" }))
    expect(within(kinds).getByRole("radio", { name: "Blocked by" })).toHaveAttribute("aria-checked", "true")

    fireEvent.change(within(pop).getByLabelText(/^issue$/i), { target: { value: "linear" } })
    const option = await within(pop).findByRole("option", { name: /ENG-12/ })
    expect(searchIssues).toHaveBeenCalledWith("linear")
    fireEvent.click(option)
    expect(within(pop).getByLabelText(/^issue$/i)).toHaveValue("ENG-12")
    fireEvent.click(within(pop).getByRole("button", { name: "Add relation" }))
    expect(e.addRelation).toHaveBeenCalledWith("ENG-12", "blocked_by")
  })

  it("Owner is a picker of workspace people when the host supplies them, and stages owner_user_id", () => {
    const e = edit({ people: [{ id: "u1", name: "Pavel Srba" }, { id: "u2", name: "Jana Nováková" }] })
    renderCard({ edit: e })
    const menu = openPicker("Change owner")
    fireEvent.change(within(menu).getByRole("searchbox"), { target: { value: "jana" } })
    expect(within(menu).queryByRole("button", { name: /Pavel Srba/ })).toBeNull()
    fireEvent.click(within(menu).getByRole("button", { name: /Jana Nováková/ }))
    expect(e.patch).toHaveBeenCalledWith({ owner_user_id: "u2", owner_name: "Jana Nováková" })
  })

  it("Owner stays text when the host offers no people (an embed, or no assign right)", () => {
    renderCard({ edit: edit() })
    expect(screen.queryByRole("button", { name: "Change owner" })).toBeNull()
  })

  it("Cancel closes Add relation without adding anything", () => {
    const e = edit()
    renderCard({ edit: e })
    fireEvent.click(screen.getByRole("button", { name: "Add relation" }))
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }))
    expect(screen.queryByRole("dialog")).toBeNull()
    expect(e.addRelation).not.toHaveBeenCalled()
  })

  it("saves an edited title on Enter and abandons it on Escape", () => {
    const e = renderCard()!
    fireEvent.click(screen.getByRole("button", { name: /edit title/i }))
    const box = screen.getByRole("textbox", { name: /issue title/i })
    fireEvent.change(box, { target: { value: "Renamed" } })
    fireEvent.keyDown(box, { key: "Enter" })
    expect(e.patch).toHaveBeenCalledWith({ title: "Renamed" })

    fireEvent.click(screen.getByRole("button", { name: /edit title/i }))
    const again = screen.getByRole("textbox", { name: /issue title/i })
    fireEvent.change(again, { target: { value: "Nope" } })
    fireEvent.keyDown(again, { key: "Escape" })
    expect(e.patch).toHaveBeenCalledTimes(1)
  })

  it("patches the description on blur, and only when it changed", () => {
    const e = renderCard()!
    const box = screen.getByLabelText("Description")
    fireEvent.blur(box)
    expect(e.patch).not.toHaveBeenCalled()
    fireEvent.change(box, { target: { value: "A different brief." } })
    fireEvent.blur(box)
    expect(e.patch).toHaveBeenCalledWith({ description: "A different brief." })
  })

  it("lists sub-issues the host resolved, at the canonical route", () => {
    render(
      <IssueCardDetail
        issue={issue({ sub_issues_count: 1 })}
        comments={[]}
        activities={[]}
        relations={[]}
        project={PROJECTS[0]}
        subIssues={[issue({ id: "iss2", identifier: "ENG-9", title: "A child" })]}
        edit={edit()}
      />,
    )
    expect(screen.getByRole("link", { name: /A child/ })).toHaveAttribute("href", "/issues/ENG-9")
  })
})

it("gates each issue property with the viewer's concrete actions", () => {
  renderCard({ edit: edit({ allowedActions: ["view", "assign", "change_status"] }) })
  expect(screen.getByRole("button", { name: "Change status" })).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Change assignee" })).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Edit title" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Change priority" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Add relation" })).toBeNull()
  expect(screen.queryByLabelText("Description")).toBeNull()
})
