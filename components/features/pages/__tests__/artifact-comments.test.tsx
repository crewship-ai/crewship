import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { ArtifactComments } from "../artifact-comments"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import type { ArtifactThread } from "@/hooks/use-artifact-threads"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
let threads: ArtifactThread[]
let canComment: boolean
let canResolve: boolean
let denied: boolean
let readStatus: number
let failure: { status: number; error: string; code?: string } | null
let loseCreateResponse: boolean
const requests: { method: string; path: string; body: Record<string, unknown> }[] = []
const committed = new Map<string, ArtifactThread>()
const clients: QueryClient[] = []
function thread(revision = 1, id = "thread1"): ArtifactThread {
  return { id, revision, anchor: { schema_version: 1, type: "document" }, status: "open", version: 1,
    created_at: "2026-10-13T12:00:00Z", updated_at: "2026-10-13T12:00:00Z", created_by_user_id: "user1",
    comments: [{ id: "comment1", body: "Please clarify the report.", author_user_id: "user1", author_name: "Alice", created_at: "2026-10-13T12:00:00Z" }] }
}
function mount(revision = 1, onViewRevision = vi.fn(), onDirtyChange = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  clients.push(client)
  const content = (selected: number) => <QueryClientProvider client={client}><ArtifactComments workspaceId="ws1" slug="report" revision={selected} onViewRevision={onViewRevision} onDirtyChange={onDirtyChange} /></QueryClientProvider>
  const result = render(content(revision))
  return { ...result, client, select: (selected: number) => result.rerender(content(selected)), onViewRevision, onDirtyChange }
}

it("distinguishes agent replies from the human who authorized the run", async () => {
  threads = [thread()]
  threads[0].comments.push({ id: "agent-reply", body: "Updated the report.", author_user_id: "user1", author_name: "Alice", author_agent_id: "agent-1", author_run_id: "run-1", created_at: "2026-10-13T12:01:00Z" })
  mount()
  expect(await screen.findByText("Agent reply")).toBeTruthy()
  expect(screen.getByText("On behalf of Alice")).toBeTruthy()
  expect(screen.getByTitle("Agent agent-1 · Run run-1")).toBeTruthy()
})
async function refresh() {
  const callback = vi.mocked(useRealtimeEventSafe).mock.calls.find(call => call[0] === "page.updated")?.[1]
  await act(async () => { (callback as (() => void))() })
}
beforeEach(() => {
  threads = []; canComment = true; canResolve = true; denied = false; readStatus = 403; failure = null; loseCreateResponse = false
  requests.length = 0; committed.clear(); vi.mocked(useRealtimeEventSafe).mockClear()
  vi.mocked(apiFetch).mockImplementation(async (input, options) => {
    const path = new URL(String(input), "https://studio.test").pathname
    if (!options?.method || options.method === "GET") {
      if (denied) return new Response(JSON.stringify({ error: "Forbidden" }), { status: readStatus })
      return new Response(JSON.stringify({ threads, can_comment: canComment, can_resolve: canResolve }))
    }
    const body = JSON.parse(String(options.body)) as Record<string, unknown>
    requests.push({ method: options.method, path, body })
    if (failure) return new Response(JSON.stringify(failure), { status: failure.status })
    const previous = committed.get(String(body.mutation_id))
    if (previous) return new Response(JSON.stringify(previous))
    let changed: ArtifactThread
    if (path.endsWith("/replies")) {
      changed = threads.find(item => path.includes(`/${item.id}/`))!
      changed.comments.push({ id: `reply${changed.comments.length}`, body: String(body.body), author_user_id: "user2", author_name: "Bob", created_at: "2026-10-13T12:01:00Z" })
      changed.version++
    } else if (path.includes('/comments/')) {
      changed = threads.find(item => path.includes(`/${item.id}/`))!
      changed.comments[0].body = String(body.body)
      changed.version++
    } else if (options.method === "PATCH") {
      changed = threads.find(item => path.endsWith(`/${item.id}`))!
      changed.status = body.resolved ? "resolved" : "open"; changed.version++
    } else {
      changed = thread(Number(body.revision), `thread${threads.length + 1}`)
      changed.comments[0].body = String(body.body); threads.push(changed)
    }
    committed.set(String(body.mutation_id), changed)
    if (loseCreateResponse) { loseCreateResponse = false; throw new Error("response lost after commit") }
    return new Response(JSON.stringify(changed), { status: 201 })
  })
})
afterEach(() => { cleanup(); for (const client of clients.splice(0)) client.clear() })

describe("artifact revision comment threads", () => {
  it("posts the captured viewport anchor unchanged and keeps its original revision", async () => {
    const anchor = { schema_version: 1 as const, type: "point" as const, coordinate_space: "viewport" as const, x: 120, y: 90, viewport_width: 800, viewport_height: 500 }
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }); clients.push(client)
    const consumed = vi.fn()
    render(<QueryClientProvider client={client}><ArtifactComments workspaceId="ws1" slug="report" revision={2} selectedAnchor={{ revision: 1, anchor }} onAnchorConsumed={consumed} onViewRevision={vi.fn()} onDirtyChange={vi.fn()} /></QueryClientProvider>)
    fireEvent.change(await screen.findByLabelText("New thread"), { target: { value: "Clarify this point." } })
    fireEvent.click(screen.getByRole("button", { name: "Post comment" }))
    await waitFor(() => expect(consumed).toHaveBeenCalledOnce())
    expect(requests[0].body).toMatchObject({ revision: 1, anchor })
  })

  it("posts a document anchor to the revision where drafting began, even after preview changes", async () => {
    const view = mount(1)
    fireEvent.change(await screen.findByLabelText("New thread"), { target: { value: "Keep this with the first revision." } })
    view.select(2)
    expect(screen.getByText("This draft stays anchored to revision 1. Switching the preview does not move it.")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Post comment" }))
    await screen.findByText("Keep this with the first revision.")
    expect(requests[0]).toMatchObject({ method: "POST", path: "/api/v1/pages/report/artifact/threads", body: { revision: 1, anchor: { schema_version: 1, type: "document" }, body: "Keep this with the first revision." } })
    expect(requests[0].body).not.toHaveProperty("author_user_id")
    expect(requests[0].body.mutation_id).toEqual(expect.any(String))
    expect(screen.getByRole("button", { name: "View original revision 1" })).toBeTruthy()
  })
  it("keeps a submission identity after a lost response and does not duplicate the committed comment", async () => {
    mount(); loseCreateResponse = true
    fireEvent.change(await screen.findByLabelText("New thread"), { target: { value: "Retry safely." } })
    fireEvent.click(screen.getByRole("button", { name: "Post comment" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("response lost")
    expect(screen.getByLabelText("New thread")).toHaveValue("Retry safely.")
    fireEvent.click(screen.getByRole("button", { name: "Post comment" }))
    await screen.findByText("Retry safely.")
    expect(requests).toHaveLength(2)
    expect(requests[0].body.mutation_id).toBe(requests[1].body.mutation_id)
    expect(threads).toHaveLength(1)
  })
  it("retries the original reply safely after a lost response and a newer server snapshot", async () => {
    threads = [thread()]; mount(); loseCreateResponse = true
    fireEvent.change(await screen.findByLabelText("Reply to thread on revision 1"), { target: { value: "Exactly one reply." } })
    fireEvent.click(screen.getByRole("button", { name: "Reply" }))
    await screen.findByRole("alert")
    await refresh()
    fireEvent.click(await screen.findByRole("button", { name: "Retry reply" }))
    await waitFor(() => expect(screen.getByLabelText("Reply to thread on revision 1")).toHaveValue(""))
    expect(requests[0].body.mutation_id).toBe(requests[1].body.mutation_id)
    expect(requests[1].body.base_version).toBe(1)
    expect(threads[0].comments).toHaveLength(2)
  })
  it("keeps a failed create identity when another thread mutation succeeds first", async () => {
    threads = [thread()]; mount(); loseCreateResponse = true
    fireEvent.change(await screen.findByLabelText("New thread"), { target: { value: "One new thread." } })
    fireEvent.click(screen.getByRole("button", { name: "Post comment" }))
    await screen.findByRole("alert")
    fireEvent.change(screen.getByLabelText("Reply to thread on revision 1"), { target: { value: "Another operation." } })
    fireEvent.click(screen.getByRole("button", { name: "Reply" }))
    await screen.findByText("Another operation.")
    fireEvent.click(screen.getByRole("button", { name: "Post comment" }))
    await waitFor(() => expect(screen.getByLabelText("New thread")).toHaveValue(""))
    expect(requests[0].body.mutation_id).toBe(requests[2].body.mutation_id)
    expect(threads).toHaveLength(2)
  })
  it("preserves uncertain reply identity and original base across refresh and another thread write", async () => {
    threads = [thread(1), thread(2, "thread2")]; mount(); loseCreateResponse = true
    fireEvent.change(await screen.findByLabelText("Reply to thread on revision 1"), { target: { value: "Only one committed reply." } })
    fireEvent.click(within(screen.getByRole("article", { name: "Thread on revision 1" })).getByRole("button", { name: "Reply" }))
    await screen.findByRole("alert")
    fireEvent.click(screen.getByRole("button", { name: "Refresh comments" }))
    await screen.findByRole("button", { name: "Retry reply" })
    fireEvent.click(within(screen.getByRole("article", { name: "Thread on revision 2" })).getByRole("button", { name: "Resolve thread" }))
    await screen.findByText("Resolved")
    fireEvent.click(screen.getByRole("button", { name: "Retry reply" }))
    await waitFor(() => expect(screen.getByLabelText("Reply to thread on revision 1")).toHaveValue(""))
    expect(requests[0].body.mutation_id).toBe(requests[2].body.mutation_id)
    expect(requests[2].body.base_version).toBe(1)
    expect(threads[0].comments).toHaveLength(2)
  })
  it("shows historical threads on their original revision and preserves reply drafts across selection", async () => {
    threads = [thread(1), thread(2, "thread2")]
    const view = mount(1)
    const cards = await screen.findAllByRole("article")
    fireEvent.change(within(cards[0]).getByLabelText("Reply to thread on revision 1"), { target: { value: "Unsaved reply" } })
    view.select(2)
    expect(screen.getByLabelText("Reply to thread on revision 1")).toHaveValue("Unsaved reply")
    fireEvent.click(screen.getByRole("button", { name: "View original revision 1" }))
    expect(view.onViewRevision).toHaveBeenCalledWith(1)
    expect(requests).toHaveLength(0)
  })
  it("replies and resolves or reopens through thread version fences", async () => {
    threads = [thread()]; mount()
    fireEvent.change(await screen.findByLabelText("Reply to thread on revision 1"), { target: { value: "Here is the explanation." } })
    fireEvent.click(screen.getByRole("button", { name: "Reply" }))
    await screen.findByText("Here is the explanation.")
    expect(requests[0]).toMatchObject({ path: "/api/v1/pages/report/artifact/threads/thread1/replies", body: { base_version: 1, body: "Here is the explanation." } })
    fireEvent.click(screen.getByRole("button", { name: "Resolve thread" }))
    await screen.findByText("Resolved")
    expect(requests[1]).toMatchObject({ method: "PATCH", body: { base_version: 2, resolved: true } })
    fireEvent.click(screen.getByRole("button", { name: "Reopen thread" }))
    await screen.findByText("Open")
    expect(requests[2].body).toMatchObject({ base_version: 3, resolved: false })
  })
  it("keeps stale reply text and requires an explicit reviewed version before retry", async () => {
    threads = [thread()]; mount()
    fireEvent.change(await screen.findByLabelText("Reply to thread on revision 1"), { target: { value: "My private draft" } })
    threads[0].version = 2
    failure = { status: 409, code: "stale_thread", error: "thread changed" }
    fireEvent.click(screen.getByRole("button", { name: "Reply" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("This thread changed")
    expect(requests[0].body.base_version).toBe(1)
    fireEvent.click(screen.getByRole("button", { name: "Refresh comments" }))
    await screen.findByRole("button", { name: "Use latest thread version" })
    expect(screen.getByLabelText("Reply to thread on revision 1")).toHaveValue("My private draft")
    expect(screen.getByRole("button", { name: "Reply" })).toBeDisabled()
    failure = null
    fireEvent.click(screen.getByRole("button", { name: "Use latest thread version" }))
    fireEvent.click(screen.getByRole("button", { name: "Reply" }))
    await screen.findByText("My private draft")
    expect(requests[1].body.base_version).toBe(2)
  })
  it("preserves reply text, its original version and the dirty guard across a transient read failure", async () => {
    threads = [thread()]; const view = mount()
    fireEvent.change(await screen.findByLabelText("Reply to thread on revision 1"), { target: { value: "Unsent reply survives a read outage." } })
    await waitFor(() => expect(view.onDirtyChange).toHaveBeenLastCalledWith(true))
    denied = true; readStatus = 500; await refresh()
    expect(await screen.findByRole("alert")).toHaveTextContent("drafts are preserved")
    expect(screen.getByLabelText("Reply to thread on revision 1")).toHaveValue("Unsent reply survives a read outage.")
    expect(view.onDirtyChange).toHaveBeenLastCalledWith(true)
    denied = false
    fireEvent.click(screen.getByRole("button", { name: "Refresh comments" }))
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull())
    fireEvent.click(screen.getByRole("button", { name: "Reply" }))
    await screen.findByText("Unsent reply survives a read outage.")
    expect(requests[0].body.base_version).toBe(1)
  })
  it("renders comment source as escaped text and omits writes for readers", async () => {
    threads = [thread()]; threads[0].comments[0].body = '<script>run()</script><img src="https://sink.test">'
    canComment = false; canResolve = false; mount()
    await screen.findByText(threads[0].comments[0].body)
    expect(document.querySelector("script,img")).toBeNull()
    expect(screen.queryByLabelText("New thread")).toBeNull()
    expect(screen.queryByRole("button", { name: "Resolve thread" })).toBeNull()
    expect(requests).toHaveLength(0)
  })
  it.each([403, 404])("clears private drafts and cached content on denied revalidation (%s)", async status => {
    readStatus = status
    threads = [thread()]; const view = mount()
    fireEvent.change(await screen.findByLabelText("New thread"), { target: { value: "Private unsaved comment" } })
    await waitFor(() => expect(view.onDirtyChange).toHaveBeenLastCalledWith(true))
    denied = true; await refresh()
    expect(await screen.findByRole("alert")).toHaveTextContent("access to comments changed")
    expect(screen.queryByText("Please clarify the report.")).toBeNull()
    expect(view.client.getQueryData(["artifact-threads", "ws1", "report"])).toBeUndefined()
    await waitFor(() => expect(view.onDirtyChange).toHaveBeenLastCalledWith(false))
    denied = false; canComment = false
    fireEvent.click(screen.getByRole("button", { name: "Retry comments" }))
    await screen.findByText(/You can read these threads/)
    expect(screen.queryByDisplayValue("Private unsaved comment")).toBeNull()
  })
  it("validates byte length without posting oversized multibyte text", async () => {
    mount()
    fireEvent.change(await screen.findByLabelText("New thread"), { target: { value: "ě".repeat(3000) } })
    fireEvent.click(screen.getByRole("button", { name: "Post comment" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("fit within 4 KiB")
    expect(requests).toHaveLength(0)
  })
})

it("retries a committed edit with the same mutation and captured thread version", async () => {
  threads = [thread()]
  threads[0].comments[0].can_edit = true
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Edit comment" }))
  fireEvent.change(screen.getByLabelText("Edit comment text"), { target: { value: "More precise" } })
  loseCreateResponse = true
  fireEvent.click(screen.getByRole("button", { name: "Save comment" }))
  fireEvent.click(await screen.findByRole("button", { name: "Retry unchanged comment update" }))
  await waitFor(() => expect(requests).toHaveLength(2))
  expect(requests[0]).toEqual(requests[1])
  expect(requests[0]).toMatchObject({ method: "PATCH", path: "/api/v1/pages/report/artifact/threads/thread1/comments/comment1", body: { body: "More precise", base_version: 1 } })
  await waitFor(() => expect(screen.queryByLabelText("Edit comment text")).toBeNull())
  expect(threads[0].version).toBe(2)
})
