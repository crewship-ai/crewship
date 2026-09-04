import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// A failed fetch is not an empty state (README §6, S6). The surface used to
// fold every sub-resource failure into `[]`, so a 500 on comments rendered
// "Nobody has said anything about this issue yet" — a lie the reader could
// not detect and had no way to retry. And an unknown identifier was one
// grey sentence with nowhere to go.

vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {} }))
vi.mock("@/hooks/use-pipelines", () => ({
  usePipelines: () => ({ pipelines: [], loading: false }),
}))
vi.mock("@/hooks/use-automations", () => ({
  useAutomations: () => ({ automations: [], loading: false }),
}))
vi.mock("@/components/features/issues/tiptap-editor", () => ({
  TiptapEditor: () => <div data-testid="tiptap" />,
}))
vi.mock("@/components/features/activity/run-activity-timeline", () => ({
  RunActivityTimeline: () => null,
  RUN_WORK_ENTRY_TYPES: ["exec"],
}))
vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a>,
}))

import { IssueDetailSurface } from "../issue-detail-surface"

const issueBody = {
  id: "id-ENG-1",
  identifier: "ENG-1",
  title: "Coordinate the launch page",
  description: "",
  status: "IN_PROGRESS",
  crew_id: "crew-1",
  created_at: "2026-08-01T12:00:00Z",
  updated_at: "2026-08-01T12:00:00Z",
  labels: [],
}

function jsonResponse(status: number, body: unknown) {
  return Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
    headers: new Headers(),
  } as unknown as Response)
}

let commentsStatus = 500
let commentsCalls = 0

beforeEach(() => {
  commentsStatus = 500
  commentsCalls = 0
  global.fetch = vi.fn((input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes("/api/v1/issues/ENG-1?")) return jsonResponse(200, issueBody)
    if (url.includes("/api/v1/issues/ENG-404?")) return jsonResponse(404, { detail: "Issue not found" })
    if (url.includes("/comments?")) {
      commentsCalls += 1
      return jsonResponse(commentsStatus, commentsStatus === 200 ? [] : { detail: "boom" })
    }
    if (url.includes("/subtasks?")) return jsonResponse(200, { subtasks: [] })
    return jsonResponse(200, [])
  }) as unknown as typeof fetch
})

describe("IssueDetailSurface — failures are not empties", () => {
  it("says which sub-resource failed and retries it, instead of showing an empty comments card", async () => {
    render(<IssueDetailSurface workspaceId="ws-1" identifier="ENG-1" editable={false} />)
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent(/could not load comments/i)
    expect(screen.queryByText(/nobody has said anything/i)).toBeNull()

    commentsStatus = 200
    fireEvent.click(screen.getByRole("button", { name: /try again/i }))
    await waitFor(() => expect(commentsCalls).toBe(2))
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull())
  })

  it("gives an unknown identifier a way back", async () => {
    render(<IssueDetailSurface workspaceId="ws-1" identifier="ENG-404" editable={false} />)
    expect(await screen.findByText(/no issue is called ENG-404/i)).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /back to issues/i })).toHaveAttribute("href", "/issues")
  })
})
