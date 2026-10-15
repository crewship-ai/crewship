import React from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import type { IssueAction } from "@/lib/chat-entity-cards"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"
import { IssueDetailSurface } from "../issue-detail-surface"

vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {} }))
vi.mock("@/hooks/use-pipelines", () => ({ usePipelines: () => ({ pipelines: [] }) }))
vi.mock("@/components/features/activity/run-activity-timeline", () => ({ RunActivityTimeline: () => null, RUN_WORK_ENTRY_TYPES: ["exec"] }))
vi.mock("@/components/features/issues/tiptap-editor", () => ({ TiptapEditor: () => null }))
vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))
const actions: IssueAction[] = ["view", "comment", "edit_fields", "change_status"]
let revision = 1
let status = 200
function mockServer(allowed = actions) {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => new Response(JSON.stringify(url.includes("/api/v1/issues/ENG-1?") ? {
    id: "issue", identifier: "ENG-1", title: "Server title", status: "TODO", priority: "none", crew_id: "crew", workspace_id: "ws", revision,
    aggregate_revision: revision, allowed_actions: allowed, labels: [], created_at: "2026-10-13T09:00:00Z", updated_at: "2026-10-13T09:00:00Z",
  } : []), { status: url.includes("/api/v1/issues/ENG-1?") ? status : 200 })))
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); revision = 1; status = 200 })
it("retains local fields through authorization refresh and sends the original edit revision", async () => {
  mockServer()
  const patch = vi.fn(async () => false), comment = vi.fn(async () => false)
  const write = { patch, comment }
  const view = (key: number, rechecking = false) => <PageSaveProvider><IssueDetailSurface workspaceId="ws" identifier="ENG-1" actions={actions} authorizationKey={key} rechecking={rechecking} write={write} /><PageSaveBar /></PageSaveProvider>
  const { rerender } = render(view(1))
  await screen.findByRole("button", { name: "Edit title" })
  fireEvent.click(screen.getByRole("button", { name: "Edit title" }))
  fireEvent.change(screen.getByRole("textbox", { name: "Issue title" }), { target: { value: "Local draft" } })
  await screen.findByRole("button", { name: "Save changes" })
  expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull()
  revision = 2
  rerender(view(2, true))
  expect(screen.queryByText("Server title")).toBeNull()
  expect(screen.queryByDisplayValue("Local draft")).toBeNull()
  rerender(view(2))
  await screen.findByRole("heading", { name: "Local draft" })
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "Save changes" })))
  expect(patch).toHaveBeenCalledWith(expect.any(String), { title: "Local draft" }, 1)
})
it("intersects card actions with freshly authorized detail actions", async () => {
  mockServer(["view", "comment", "change_status"])
  render(<IssueDetailSurface workspaceId="ws" identifier="ENG-1" actions={actions} authorizationKey={1} />)
  await screen.findByRole("button", { name: "Change status" })
  expect(screen.queryByRole("button", { name: "Edit title" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Change priority" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Hand off" })).toBeNull()
})
it("removes authorized detail data when access is revoked", async () => {
  mockServer()
  const unauthorized = vi.fn()
  const { rerender } = render(<IssueDetailSurface workspaceId="ws" identifier="ENG-1" actions={actions} authorizationKey={1} onUnauthorized={unauthorized} />)
  await screen.findByRole("heading", { name: "Server title" })
  status = 404
  rerender(<IssueDetailSurface workspaceId="ws" identifier="ENG-1" actions={actions} authorizationKey={2} onUnauthorized={unauthorized} />)
  await waitFor(() => expect(unauthorized).toHaveBeenCalled())
  await screen.findByText("Unavailable")
  expect(screen.queryByText("Server title")).toBeNull()
  expect(screen.queryByText("ENG-1")).toBeNull()
})
