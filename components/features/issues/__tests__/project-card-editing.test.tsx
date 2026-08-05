// The project card replaces a 360px rail rendered at full width. Everything
// that rail could set has to survive the move: name, icon, colour, status,
// priority, health and lead. The dates row is the one addition — it read
// "Set dates" and did nothing, because it was a PropertyRow with no popover
// behind it, while PATCH /projects has taken start_date and target_date all
// along.

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

import { ProjectCardDetail } from "../project-card-detail"
import type { ProjectCardEdit } from "../project-card-editors"
import type { Project, ProjectStats } from "@/lib/types/mission"

vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
}))

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

const STATS: ProjectStats = {
  total_issues: 3,
  completed_issues: 0,
  by_status: { BACKLOG: 3 },
  by_assignee: [],
  by_label: [],
  crews: [],
}

function edit(over: Partial<ProjectCardEdit> = {}): ProjectCardEdit {
  return {
    agents: [{ id: "agent-robin", name: "Robin", slug: "robin" }],
    patch: vi.fn(async () => true),
    ...over,
  }
}

function renderCard(over: { project?: Project; edit?: ProjectCardEdit | null } = {}) {
  const e = over.edit === null ? undefined : (over.edit ?? edit())
  render(
    <ProjectCardDetail
      project={over.project ?? project()}
      stats={STATS}
      issues={[]}
      edit={e}
    />,
  )
  return e
}

function openPicker(name: RegExp) {
  fireEvent.click(screen.getByRole("button", { name }))
  return screen.getByRole("dialog")
}

describe("ProjectCardDetail — the rail's editors survive the move", () => {
  it("offers everything the rail could set", () => {
    renderCard()
    for (const name of [
      /edit project name/i,
      /change project status/i,
      /change project priority/i,
      /change health/i,
      /change lead/i,
      /change dates/i,
    ]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument()
    }
  })

  it("stays read-only without editors", () => {
    renderCard({ edit: null })
    expect(screen.queryByRole("button", { name: /change health/i })).toBeNull()
  })

  it("patches status, priority and health", () => {
    const e = renderCard()!
    fireEvent.click(within(openPicker(/change project status/i)).getByRole("button", { name: /^completed$/i }))
    expect(e.patch).toHaveBeenCalledWith({ status: "completed" })

    fireEvent.click(within(openPicker(/change project priority/i)).getByRole("button", { name: /^urgent$/i }))
    expect(e.patch).toHaveBeenCalledWith({ priority: "urgent" })

    fireEvent.click(within(openPicker(/change health/i)).getByRole("button", { name: /off track/i }))
    expect(e.patch).toHaveBeenCalledWith({ health: "off_track" })
  })

  it("sets and clears the lead", () => {
    const e = renderCard()!
    fireEvent.click(within(openPicker(/change lead/i)).getByRole("button", { name: /Robin/ }))
    expect(e.patch).toHaveBeenCalledWith({ lead_type: "agent", lead_id: "agent-robin" })

    fireEvent.click(within(openPicker(/change lead/i)).getByRole("button", { name: /no lead/i }))
    expect(e.patch).toHaveBeenCalledWith({ lead_type: "", lead_id: "" })
  })

  it("renames the project on Enter", () => {
    const e = renderCard()!
    fireEvent.click(screen.getByRole("button", { name: /edit project name/i }))
    const box = screen.getByRole("textbox", { name: /project name/i })
    fireEvent.change(box, { target: { value: "Disk I/O" } })
    fireEvent.keyDown(box, { key: "Enter" })
    expect(e.patch).toHaveBeenCalledWith({ name: "Disk I/O" })
  })

  it("writes the dates the old rail only displayed", () => {
    const e = renderCard()!
    const menu = openPicker(/change dates/i)
    fireEvent.change(within(menu).getByLabelText(/start date/i), { target: { value: "2026-09-01" } })
    expect(e.patch).toHaveBeenCalledWith({ start_date: "2026-09-01" })
    fireEvent.change(within(menu).getByLabelText(/target date/i), { target: { value: "2026-10-01" } })
    expect(e.patch).toHaveBeenCalledWith({ target_date: "2026-10-01" })
  })
})
