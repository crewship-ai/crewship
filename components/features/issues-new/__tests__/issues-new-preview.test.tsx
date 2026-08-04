import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

import { IssuesNewPreview } from "../issues-new-preview"

const useWorkspace = vi.fn()
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => useWorkspace() }))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
}))

/** Every URL apiFetch has been called with, in order. */
function urls(): string[] {
  return apiFetch.mock.calls.map((c) => String(c[0]))
}

function ok(body: unknown) {
  return Promise.resolve({ ok: true, json: () => Promise.resolve(body) })
}

function issueRow(over: Record<string, unknown> = {}) {
  return {
    id: "i1",
    workspace_id: "ws-a",
    crew_id: "crew1",
    identifier: "ENG-1",
    title: "Alpha issue",
    status: "BACKLOG",
    priority: "medium",
    created_at: "2026-08-01T12:00:00Z",
    updated_at: "2026-08-02T12:00:00Z",
    completed_at: null,
    tasks: [],
    ...over,
  }
}

/** Routes every URL the preview asks for to a per-workspace fixture. */
function routeFor(ws: string) {
  return (url: string) => {
    if (url.includes("/api/v1/issues?")) {
      return ok([
        issueRow({
          id: `${ws}-i`,
          title: `${ws} issue`,
          workspace_id: ws,
          crew_id: `${ws}-crew`,
          identifier: `${ws.toUpperCase()}-1`,
        }),
      ])
    }
    if (url.includes("/api/v1/projects?")) {
      return ok([
        {
          id: `${ws}-p`,
          workspace_id: ws,
          name: `${ws} project`,
          slug: `${ws}-project`,
          description: null,
          icon: "folder",
          color: "blue",
          status: "in_progress",
          priority: "none",
          health: "on_track",
          lead_type: null,
          lead_id: null,
          start_date: null,
          target_date: null,
          created_at: "2026-07-01T12:00:00Z",
          updated_at: "2026-07-02T12:00:00Z",
          issue_count: 1,
          done_count: 0,
          progress: 0,
        },
      ])
    }
    if (url.includes("/stats")) return ok(null)
    if (url.match(/\/api\/v1\/issues\/[^?]+\?/)) {
      return ok(
        issueRow({
          id: `${ws}-i`,
          title: `${ws} issue`,
          workspace_id: ws,
          crew_id: `${ws}-crew`,
          identifier: `${ws.toUpperCase()}-1`,
        }),
      )
    }
    return ok([])
  }
}

describe("IssuesNewPreview", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    useWorkspace.mockReset()
  })

  it("renders the first issue of the workspace it was given", async () => {
    useWorkspace.mockReturnValue({ workspaceId: "ws-a", loading: false })
    apiFetch.mockImplementation(routeFor("ws-a"))

    render(<IssuesNewPreview />)
    await waitFor(() => expect(screen.getByText("ws-a issue")).toBeInTheDocument())
  })

  it("never asks the new workspace about the old workspace's issue", async () => {
    // The bug this replaces: selection was kept with `prev ?? first`, so
    // after a switch the detail effect still held ws-a's crew and identifier
    // and sent them scoped to ws-b — a cross-workspace read.
    useWorkspace.mockReturnValue({ workspaceId: "ws-a", loading: false })
    apiFetch.mockImplementation(routeFor("ws-a"))

    const { rerender } = render(<IssuesNewPreview />)
    await waitFor(() =>
      expect(urls().some((u) => u.includes("WS-A-1") && u.includes("ws-a"))).toBe(true),
    )

    useWorkspace.mockReturnValue({ workspaceId: "ws-b", loading: false })
    apiFetch.mockImplementation(routeFor("ws-b"))
    rerender(<IssuesNewPreview />)

    await waitFor(() => expect(urls().some((u) => u.includes("WS-B-1"))).toBe(true))
    const crossed = urls().filter(
      (u) => u.includes("workspace_id=ws-b") && (u.includes("WS-A-1") || u.includes("ws-a-crew")),
    )
    expect(crossed).toEqual([])
  })

  it("selects the new workspace's first entity rather than holding the old id", async () => {
    useWorkspace.mockReturnValue({ workspaceId: "ws-a", loading: false })
    apiFetch.mockImplementation(routeFor("ws-a"))

    const { rerender } = render(<IssuesNewPreview />)
    await waitFor(() => expect(screen.getByText("ws-a issue")).toBeInTheDocument())

    useWorkspace.mockReturnValue({ workspaceId: "ws-b", loading: false })
    apiFetch.mockImplementation(routeFor("ws-b"))
    rerender(<IssuesNewPreview />)

    await waitFor(() => expect(screen.getByText("ws-b issue")).toBeInTheDocument())
    expect(screen.queryByText("ws-a issue")).toBeNull()
  })

  it("stops loading instead of spinning forever when there is no workspace", async () => {
    useWorkspace.mockReturnValue({ workspaceId: "", loading: false })
    apiFetch.mockImplementation(routeFor("ws-a"))

    render(<IssuesNewPreview />)
    await waitFor(() =>
      expect(screen.getByText(/This workspace has no issue to preview/)).toBeInTheDocument(),
    )
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
