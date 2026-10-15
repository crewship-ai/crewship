import React from "react"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { IssueDetailSurface } from "../issue-detail-surface"
import type { Mission } from "@/lib/types/mission"
import type { IssueCardEdit } from "../issue-card-editors"
const { fetch } = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {} }))
vi.mock("@/hooks/use-pipelines", () => ({ usePipelines: () => ({ pipelines: [] }) }))
vi.mock("@/hooks/use-automations", () => ({ useAutomations: () => ({ automations: [] }) }))
vi.mock("@/hooks/use-pipeline-run-records", () => ({ usePipelineRunRecords: () => ({ records: [] }) }))
vi.mock("../issue-card-detail", () => ({ IssueCardDetail: ({ issue, edit, commentDraft, onCommentDraft }: { issue: Mission; edit: IssueCardEdit; commentDraft: string; onCommentDraft: (v: string) => void }) => <><input aria-label="Title" value={issue.title} onChange={e => edit.onTitleDraft?.(e.target.value)} /><textarea aria-label="Comment" value={commentDraft} onChange={e => onCommentDraft(e.target.value)} /></> }))
afterEach(() => { cleanup(); fetch.mockReset() })
const actions = ["view", "edit_fields", "comment"] as const
const write = { patch: vi.fn(async () => true), comment: vi.fn(async () => true) }
function setup() {
  fetch.mockImplementation(async (path: string) => new Response(JSON.stringify(path.startsWith("/api/v1/issues/") ? { id: "issue", identifier: "ENG-1", title: "Server title", revision: 1, crew_id: "crew", allowed_actions: actions, labels: [] } : [])))
  return render(<IssueDetailSurface workspaceId="w" identifier="ENG-1" actions={actions} write={write} authorizationKey={1} />)
}
it("clears only the successful operation's draft", async () => {
  const view = setup()
  await screen.findByDisplayValue("Server title")
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Field draft" } })
  fireEvent.change(screen.getByLabelText("Comment"), { target: { value: "Comment draft" } })
  view.rerender(<IssueDetailSurface workspaceId="w" identifier="ENG-1" actions={actions} write={{ ...write, saved: "PATCH" }} authorizationKey={1} />)
  await waitFor(() => expect(screen.getByLabelText("Title")).toHaveValue("Server title"))
  expect(screen.getByLabelText("Comment")).toHaveValue("Comment draft")
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Another field draft" } })
  view.rerender(<IssueDetailSurface workspaceId="w" identifier="ENG-1" actions={actions} write={{ ...write, saved: "POST" }} authorizationKey={1} />)
  await waitFor(() => expect(screen.getByLabelText("Comment")).toHaveValue(""))
  expect(screen.getByLabelText("Title")).toHaveValue("Another field draft")
})
it("keeps the focused editor mounted while access is rechecked", async () => {
  const view = setup()
  const title = await screen.findByDisplayValue("Server title")
  title.focus()
  fireEvent.change(title, { target: { value: "Composing draft" } })
  view.rerender(<IssueDetailSurface workspaceId="w" identifier="ENG-1" actions={actions} write={write} authorizationKey={1} rechecking />)
  expect(screen.getByLabelText("Title")).toBe(title)
  expect(title).toHaveFocus()
  expect(title).toHaveValue("Composing draft")
  view.rerender(<IssueDetailSurface workspaceId="w" identifier="ENG-1" actions={actions} write={write} authorizationKey={2} />)
  await waitFor(() => expect(fetch.mock.calls.filter(([path]) => String(path).startsWith("/api/v1/issues/")).length).toBeGreaterThan(2))
  expect(screen.getByLabelText("Title")).toBe(title)
  expect(title).toHaveFocus()
  expect(title).toHaveValue("Composing draft")
})
