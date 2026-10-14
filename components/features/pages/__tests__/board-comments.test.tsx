import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { BoardComments } from "../board-comments"
import { apiFetch } from "@/lib/api-fetch"
import type { BoardThread } from "@/hooks/use-board-threads"
import type { PageBoard } from "@/hooks/use-page-board"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
let threads: BoardThread[]
let denied: boolean
let loseResponse: boolean
let canComment: boolean
let canResolve: boolean
const requests: { path: string; body: Record<string, unknown> }[] = []
const receipts = new Map<string, BoardThread>()
const clients: QueryClient[] = []
const board: PageBoard = { format: "board/v1", seq: 3, can_write: true, placements: [], records: [{ element_id: "shape", element_rev: 2, deleted: false, element: { id: "shape", type: "rectangle", x: 20, y: 30, width: 100, height: 80 } }] }
function thread(): BoardThread {
  return { id: "thread1", anchor: { schema_version: 1, type: "board-element", board_seq: 3, element_id: "shape", element_rev: 2, snapshot: { x: 20, y: 30, width: 100, height: 80, angle: 0, element_type: "rectangle" } }, status: "open", version: 1,
    created_at: "2026-10-13T12:00:00Z", updated_at: "2026-10-13T12:00:00Z", created_by_user_id: "user1",
    comments: [{ id: "c1", body: "Discuss this drawing.", author_user_id: "user1", author_name: "Alice", created_at: "2026-10-13T12:00:00Z" }] }
}
function mount(initial = board, selectedElementId: string | undefined = "shape") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }); clients.push(client)
  const dirty = vi.fn()
  const content = (value: PageBoard, selection?: string) => <QueryClientProvider client={client}><BoardComments workspaceId="ws" slug="design" board={value} selectedElementId={selection} onDirtyChange={dirty} /></QueryClientProvider>
  const view = render(content(initial, selectedElementId))
  return { ...view, client, dirty, change: (value: PageBoard, selection?: string) => view.rerender(content(value, selection)), refresh: async () => { await act(async () => { await client.invalidateQueries({ queryKey: ["board-threads"] }) }) } }
}
beforeEach(() => {
  threads = []; denied = false; loseResponse = false; canComment = true; canResolve = true; requests.length = 0; receipts.clear()
  vi.mocked(apiFetch).mockImplementation(async (url, options) => {
    if (!options?.method) return new Response(JSON.stringify(denied ? { error: "Forbidden" } : { threads, can_comment: canComment, can_resolve: canResolve }), { status: denied ? 403 : 200 })
    const path = new URL(String(url), "https://studio.test").pathname
    const body = JSON.parse(String(options.body)); requests.push({ path, body })
    if (receipts.has(body.mutation_id)) return new Response(JSON.stringify(receipts.get(body.mutation_id)))
    let changed = thread()
    if (path.endsWith("/comments")) {
      changed = threads[0]; changed.comments.push({ id: "c2", body: body.body, author_user_id: "user1", created_at: changed.created_at }); changed.version++
    } else if (path.endsWith("/resolve")) { changed = threads[0]; changed.status = body.resolved ? "resolved" : "open"; changed.version++ }
    else { changed.anchor = body.anchor; changed.comments[0].body = body.body; threads.push(changed) }
    receipts.set(body.mutation_id, structuredClone(changed))
    if (loseResponse) { loseResponse = false; throw new Error("Response lost after commit") }
    return new Response(JSON.stringify(changed), { status: 201 })
  })
})
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()) })

it("preserves the saved element and sequence where the draft began across selection and remote changes", async () => {
  const view = mount()
  fireEvent.change(await screen.findByLabelText("New board thread"), { target: { value: "Keep this anchored." } })
  view.change({ ...board, seq: 4 }, undefined)
  expect(screen.getByText(/The board changed while you were writing/)).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Post comment" }))
  await screen.findByText("Keep this anchored.")
  expect(requests[0]).toMatchObject({ path: "/api/v1/pages/design/board/threads", body: { anchor: { schema_version: 1, type: "board-element", board_seq: 3, element_id: "shape", element_rev: 2 } } })
  expect(requests[0].body).not.toHaveProperty("author_user_id")
})
it("retries a lost create response with exactly the same identity and original anchor", async () => {
  const view = mount(); loseResponse = true
  fireEvent.change(await screen.findByLabelText("New board thread"), { target: { value: "Exactly once." } })
  fireEvent.click(screen.getByRole("button", { name: "Post comment" })); await screen.findByRole("alert")
  view.change({ ...board, seq: 5 }, undefined)
  expect(screen.getByLabelText("New board thread")).toBeDisabled()
  expect(screen.getByRole("button", { name: "Use current selection and board version" })).toBeDisabled()
  await view.refresh()
  expect(screen.getByLabelText("New board thread")).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Post comment" }))
  await waitFor(() => expect(screen.getByLabelText("New board thread")).toHaveValue(""))
  expect(requests).toHaveLength(2); expect(requests[0].body).toEqual(requests[1].body); expect(threads).toHaveLength(1)
})
it("retries an uncertain reply against its original thread version after refresh", async () => {
  threads = [thread()]; const view = mount(); loseResponse = true
  fireEvent.change(await screen.findByLabelText("Reply to board thread thread1"), { target: { value: "One reply." } })
  fireEvent.click(screen.getByRole("button", { name: "Reply" })); await screen.findByRole("alert")
  await view.refresh(); fireEvent.click(await screen.findByRole("button", { name: "Retry reply" }))
  await waitFor(() => expect(screen.getByLabelText("Reply to board thread thread1")).toHaveValue(""))
  expect(requests[0].body).toEqual(requests[1].body); expect(requests[1].body.base_version).toBe(1); expect(threads[0].comments).toHaveLength(2)
})
it("retains original geometry after element deletion rather than attaching to another element", async () => {
  threads = [thread()]; const view = mount()
  await screen.findByText("Discuss this drawing.")
  view.change({ ...board, seq: 4, records: [] })
  expect(screen.getByText(/Original element deleted/)).toBeTruthy()
  expect(screen.getByText(/original position \(20, 30\)/)).toBeTruthy()
})
it("hides comments and drafts immediately when live access is revoked", async () => {
  threads = [thread()]; const view = mount()
  fireEvent.change(await screen.findByLabelText("New board thread"), { target: { value: "Private draft" } })
  expect(view.dirty).toHaveBeenLastCalledWith(true)
  denied = true; await view.refresh()
  await screen.findByText(/Comments and drafts are hidden/)
  expect(screen.queryByText("Discuss this drawing.")).toBeNull(); expect(screen.queryByDisplayValue("Private draft")).toBeNull()
  await waitFor(() => expect(view.dirty).toHaveBeenLastCalledWith(false))
})
it("allows comment-only writers to reply without offering resolve or agent actions", async () => {
  threads = [thread()]; canResolve = false; mount()
  await screen.findByLabelText("Reply to board thread thread1")
  expect(screen.queryByRole("button", { name: "Resolve thread" })).toBeNull()
  expect(screen.queryByRole("button", { name: /agent/i })).toBeNull()
})
it("uses an explicit POST resolution with a version precondition", async () => {
  threads = [thread()]; mount()
  fireEvent.click(await screen.findByRole("button", { name: "Resolve thread" }))
  await screen.findByRole("button", { name: "Reopen thread" })
  expect(requests[0]).toMatchObject({ path: "/api/v1/pages/design/board/threads/thread1/resolve", body: { resolved: true, base_version: 1 } })
})
