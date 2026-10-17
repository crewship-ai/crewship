// The project page (Harbor 2): a hero that says what the project is, then
// tabs — Overview, Tree, Issues, Files, Settings. Updates is not a tab: the
// backend has no project updates, and a tab that cannot post is a promise
// the page does not keep.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, within, waitFor, cleanup } from "@testing-library/react"

const h = vi.hoisted(() => ({ api: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.api(...a) }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("next/link", () => ({
  default: ({ children, href, ...rest }: { children: React.ReactNode; href: string }) => <a href={href} {...rest}>{children}</a>,
}))
vi.mock("@/lib/toast", () => ({ toast: { error: vi.fn(), success: vi.fn() } }))
vi.mock("../project-milestones-card", () => ({ ProjectMilestonesCard: () => <div data-testid="milestones-card" /> }))
vi.mock("@/components/features/settings/resource-share-dialog", () => ({
  ResourceShareDialog: () => <button type="button">Share</button>,
}))

import { ProjectDetailSurface } from "../project-detail-surface"
import type { Mission, Project } from "@/lib/types/mission"
import type { ProjectTab } from "@/hooks/use-project-detail"

const PROJECT: Project = {
  id: "p1", workspace_id: "ws1", name: "Launch v2", slug: "launch-v2", description: "Ship the new pricing and onboarding.",
  icon: "rocket", color: "blue", status: "in_progress", priority: "high", health: "at_risk",
  lead_type: "agent", lead_id: "agent-robin", lead_name: "Robin", start_date: "2026-10-01", target_date: "2026-10-31",
  created_at: "2026-07-26T12:00:00Z", updated_at: "2026-08-01T12:00:00Z", issue_count: 3, done_count: 1, progress: 33,
}

function issue(n: number, over: Partial<Mission> = {}): Mission {
  return {
    id: `i${n}`, identifier: `ENG-${n}`, title: `Issue ${n}`, status: "TODO", priority: "none",
    project_id: "p1", crew_id: "c1", created_at: "2026-09-03T10:00:00Z", updated_at: "2026-09-03T10:00:00Z",
    ...over,
  } as unknown as Mission
}

const ISSUES = [
  issue(1, { status: "DONE", milestone_id: "m1" }),
  issue(2, { milestone_id: "m1" }),
  issue(3),
  issue(9, { project_id: "other" }),
]

const FILES = {
  files: [
    { version_id: "v5", file_id: "f1", project_id: "p1", name: "plan.md", revision: 5, size_bytes: 2048, sha256: "x", created_at: "2026-10-16T08:00:00Z" },
  ],
}

beforeEach(() => {
  h.api.mockReset()
  h.api.mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url)
    if (init?.method === "DELETE") return new Response(null, { status: 204 })
    if (u.includes("/files")) return new Response(JSON.stringify(FILES), { status: 200 })
    if (u.includes("/milestones")) return new Response(JSON.stringify([{ id: "m1", project_id: "p1", name: "Beta", description: null, target_date: null, status: "active", position: 0, created_at: "", updated_at: "" }]), { status: 200 })
    if (u.includes("/stats")) return new Response(JSON.stringify({ total_issues: 3, completed_issues: 1, by_status: { DONE: 1, TODO: 2 }, by_assignee: [], by_label: [], crews: ["Research"] }), { status: 200 })
    if (u.includes("/agents")) return new Response("[]", { status: 200 })
    return new Response("{}", { status: 200 })
  })
})
afterEach(() => cleanup())

function renderPage(over: Partial<React.ComponentProps<typeof ProjectDetailSurface>> & { tab?: ProjectTab } = {}) {
  const onTabChange = vi.fn()
  const onOpenIssue = vi.fn()
  const utils = render(
    <ProjectDetailSurface
      workspaceId="ws1"
      project={PROJECT}
      issues={ISSUES}
      tab={over.tab ?? "overview"}
      onTabChange={onTabChange}
      onOpenIssue={onOpenIssue}
      {...over}
    />,
  )
  return { ...utils, onTabChange, onOpenIssue }
}

describe("Project page — hero and tabs", () => {
  it("says the name once, with the dates, status, health in words and the lead", () => {
    renderPage()
    expect(screen.getByRole("heading", { level: 1, name: "Launch v2" })).toBeInTheDocument()
    expect(screen.getAllByText("Launch v2")).toHaveLength(1)
    expect(screen.getByText(/^Project ·/)).toBeInTheDocument()
    expect(screen.getAllByText("At risk").length).toBeGreaterThan(0)
    expect(screen.queryByText("at_risk")).toBeNull()
    expect(screen.getAllByText("Robin").length).toBeGreaterThan(0)
  })

  it("hidden tabs (Updates) are absent, Files lists project files", async () => {
    renderPage({ tab: "files" })
    const tabs = within(screen.getByRole("tablist", { name: "Project" })).getAllByRole("tab")
    expect(tabs.map((t) => t.textContent?.replace(/\d+$/, "").trim())).toEqual(["Overview", "Tree", "Issues", "Files", "Settings"])
    expect(screen.queryByRole("tab", { name: /Updates/ })).toBeNull()
    await waitFor(() => expect(screen.getByText("plan.md")).toBeInTheDocument())
    expect(h.api).toHaveBeenCalledWith(expect.stringContaining("/api/v1/workspaces/ws1/projects/p1/files"))
    expect(screen.getByText("r5")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Download plan.md" })).toHaveAttribute(
      "href",
      "/api/v1/workspaces/ws1/projects/p1/files/v5/download",
    )
  })

  it("the Issues tab counts this project's issues only", () => {
    renderPage()
    expect(screen.getByRole("tab", { name: /Issues/ })).toHaveTextContent("3")
  })

  it("a tab click asks the host to switch (the host keeps it in ?tab=)", () => {
    const { onTabChange } = renderPage()
    fireEvent.click(screen.getByRole("tab", { name: /Settings/ }))
    expect(onTabChange).toHaveBeenCalledWith("settings")
  })

  it("Overview shows done of total and the issues by status", async () => {
    renderPage()
    await waitFor(() => expect(screen.getByText("1 of 3")).toBeInTheDocument())
    expect(screen.getByTestId("milestones-card")).toBeInTheDocument()
  })
})

describe("Project page — Issues and Tree", () => {
  it("Issues lists the project's rows and a row opens the issue", () => {
    const { onOpenIssue } = renderPage({ tab: "issues" })
    expect(screen.getByText("Issue 1")).toBeInTheDocument()
    expect(screen.queryByText("Issue 9")).toBeNull()
    fireEvent.click(screen.getByText("Issue 2"))
    expect(onOpenIssue).toHaveBeenCalledWith(expect.objectContaining({ id: "i2" }))
  })

  it("an empty project says so and offers New issue", () => {
    const onNewIssue = vi.fn()
    renderPage({ tab: "issues", issues: [], onNewIssue })
    expect(screen.getByText("No issues in this project yet")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "New issue" }))
    expect(onNewIssue).toHaveBeenCalled()
  })

  it("Tree groups issues under their milestone, the rest under No milestone", async () => {
    renderPage({ tab: "tree" })
    const tree = await screen.findByRole("tree", { name: "Launch v2 outline" })
    await waitFor(() => expect(within(tree).getByText("Beta")).toBeInTheDocument())
    const beta = within(tree).getByRole("treeitem", { name: /Beta/ })
    expect(within(beta).getByText("Issue 1")).toBeInTheDocument()
    expect(within(beta).getByText("1/2")).toBeInTheDocument()
    const none = within(tree).getByRole("treeitem", { name: /No milestone/ })
    expect(within(none).getByText("Issue 3")).toBeInTheDocument()
  })
})

describe("Project page — Settings", () => {
  it("holds the properties, Share under Access, and no Archive (no backend)", () => {
    renderPage({ tab: "settings" })
    for (const name of [/change project status/i, /change project priority/i, /change health/i, /change lead/i, /change dates/i]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument()
    }
    expect(screen.getByRole("button", { name: "Share" })).toBeInTheDocument()
    expect(screen.queryByText(/Archive/)).toBeNull()
  })

  it("hides Delete from someone who cannot delete projects", () => {
    renderPage({ tab: "settings", canDelete: false })
    expect(screen.queryByRole("button", { name: /Delete/ })).toBeNull()
  })

  it("Delete asks first, then sends DELETE and tells the host", async () => {
    const onDeleted = vi.fn()
    renderPage({ tab: "settings", canDelete: true, onDeleted })
    fireEvent.click(screen.getByRole("button", { name: "Delete…" }))
    const dialog = screen.getByRole("alertdialog")
    expect(dialog).toHaveTextContent("Issues keep working without a project.")
    expect(h.api.mock.calls.some((c) => (c[1] as RequestInit | undefined)?.method === "DELETE")).toBe(false)
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete project" }))
    await waitFor(() => expect(onDeleted).toHaveBeenCalled())
    const del = h.api.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === "DELETE")
    expect(String(del![0])).toBe("/api/v1/projects/p1?workspace_id=ws1")
  })
})
