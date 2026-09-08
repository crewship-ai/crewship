import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { WorkspaceConversations } from "../workspace-conversations"
const reading = vi.hoisted(() => vi.fn())
vi.mock("@/lib/notification-sound-coordinator", () => ({ setSoundReadingConversation: reading }))
const fetcher = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher }))
vi.mock("@/hooks/use-workspace", () => ({ useCurrentWorkspaceId: () => "ws", useWorkspace: () => ({ workspaceId: "ws", loading: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "alice" } } }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams("conversation=room") }))
const room = { id: "room", title: "Engineering", kind: "group", access_scope: "participants", created_by: "alice", last_sequence: 0, unread_count: 0 }
function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return { ...render(<QueryClientProvider client={client}><WorkspaceConversations /></QueryClientProvider>), client }
}
beforeEach(() => {
  sessionStorage.clear()
  fetcher.mockReset()
  reading.mockReset()
  Element.prototype.scrollIntoView = vi.fn()
  fetcher.mockImplementation(async (url: string, options?: RequestInit) => {
    if (url.includes("/messages") && options?.method === "POST") return new Response("{}", { status: 201 })
    if (url.includes("/messages")) return Response.json({ messages: [], has_more: false })
    if (url.includes("/read")) return new Response(null, { status: 204 })
    if (url.includes("/conversations/room?")) return Response.json(room)
    return Response.json({ conversations: [room], next_offset: null })
  })
})
describe("workspace conversations transport", () => {
  it("does not mark visible background windows read and retries when the window gains focus", async () => {
    const focused = vi.spyOn(document, "hasFocus").mockReturnValue(false)
    const normal = fetcher.getMockImplementation()!
    fetcher.mockImplementation(async (url: string, options?: RequestInit) => url.includes("/messages") ? Response.json({ messages: [{ id: "focus-message", client_id: "focus-message", sequence: 4, author_user_id: "bob", author_name: "Bob", content: "Keep unread until focus", created_at: "2026-09-08T10:00:00Z" }], has_more: false }) : normal(url, options))
    const view = setup()
    await screen.findByText("Keep unread until focus")
    expect(fetcher.mock.calls.some(([url]) => url.includes("/read"))).toBe(false)
    focused.mockReturnValue(true)
    fireEvent(window, new Event("focus"))
    await waitFor(() => expect(fetcher.mock.calls.some(([url, options]) => url.includes("/read") && JSON.parse(options.body).last_read_sequence === 4)).toBe(true))
    view.unmount()
    focused.mockRestore()
  })
  it("registers reading by account/workspace, unregisters while scrolling up and on unmount", async () => {
    const view = setup()
    const empty = await screen.findByText("Start the conversation with a message.")
    const scroller = empty.closest(".overflow-y-auto")!
    expect(reading).toHaveBeenLastCalledWith(JSON.stringify(["alice", "ws"]), "room", true)
    Object.defineProperties(scroller, { scrollHeight: { configurable: true, value: 1000 }, clientHeight: { configurable: true, value: 200 }, scrollTop: { configurable: true, value: 0 } })
    fireEvent.scroll(scroller)
    expect(reading).toHaveBeenLastCalledWith(JSON.stringify(["alice", "ws"]), "room", false)
    Object.defineProperty(scroller, "scrollTop", { configurable: true, value: 800 })
    fireEvent.scroll(scroller)
    expect(reading).toHaveBeenLastCalledWith(JSON.stringify(["alice", "ws"]), "room", true)
    view.unmount()
    expect(reading).toHaveBeenLastCalledWith(JSON.stringify(["alice", "ws"]), "room", false)
  })

  it("retries an ambiguous failed send with the original client ID and content", async () => {
    const posts: { client_id: string; content: string }[] = []
    const normal = fetcher.getMockImplementation()!
    fetcher.mockImplementation(async (url: string, options?: RequestInit) => {
      if (url.includes("/messages") && options?.method === "POST") {
        posts.push(JSON.parse(options.body as string))
        return posts.length === 1 ? new Response("", { status: 503 }) : Response.json({ id: "m" })
      }
      return normal(url, options)
    })
    setup()
    const composer = await screen.findByRole("textbox", { name: "Message Engineering" })
    fireEvent.change(composer, { target: { value: "Ship it" } })
    fireEvent.click(screen.getByRole("button", { name: "Send" }))
    await screen.findByRole("button", { name: "Retry" })
    expect(composer).toBeDisabled()
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    await waitFor(() => expect(posts).toHaveLength(2))
    expect(posts[1]).toEqual(posts[0])
    expect(posts[0].client_id).toBeTruthy()
    await waitFor(() => expect(composer).toHaveValue(""))
  })
  it("does not display cached message content after a history access failure", async () => {
    const normal = fetcher.getMockImplementation()!
    let denied = false
    fetcher.mockImplementation(async (url: string, options?: RequestInit) => {
      if (url.includes("/messages")) return denied ? new Response("", { status: 404 }) : Response.json({ messages: [{ id: "secret", sequence: 1, content: "Private transcript", author_name: "Bob", author_user_id: "bob", created_at: "2026-09-06T12:00:00Z" }], has_more: false })
      return normal(url, options)
    })
    const { client } = setup()
    await screen.findByText("Private transcript")
    denied = true
    await client.invalidateQueries({ queryKey: ["workspace-conversations", "ws", "alice", "room", "messages"] })
    await screen.findByText("This conversation is unavailable or you no longer have access.")
    expect(screen.getByRole("textbox", { name: "Message Engineering" })).toBeDisabled()
    expect(screen.queryByText("Private transcript")).not.toBeInTheDocument()

  })
  it("loads older history with the oldest sequence and keeps messages ordered", async () => {
    const normal = fetcher.getMockImplementation()!
    const message = (sequence: number) => ({ id: `m${sequence}`, client_id: `c${sequence}`, sequence, author_user_id: "bob", author_name: "Bob", content: `Message ${sequence}`, created_at: "2026-09-06T12:00:00Z" })
    fetcher.mockImplementation(async (url: string, options?: RequestInit) => {
      if (url.includes("/messages")) return Response.json(url.includes("before_sequence=101") ? { messages: [message(100)], has_more: false } : { messages: [message(101)], has_more: true })
      return normal(url, options)
    })
    setup()
    fireEvent.click(await screen.findByRole("button", { name: "Load older messages" }))
    await screen.findByText("Message 100")
    expect(screen.getAllByRole("article").map((el) => el.textContent)).toEqual([expect.stringContaining("Message 100"), expect.stringContaining("Message 101")])
  })
  it("sends structured targets only for explicitly selected joined channel agents", async () => {
    const normal = fetcher.getMockImplementation()!
    fetcher.mockImplementation(async (url: string, options?: RequestInit) => {
      if (url.includes("/room/agents?")) return Response.json({ agents: [{ agent_id: "agent-1", name: "Mařena", slug: "marena" }] })
      if (url.includes("/agent-jobs")) return Response.json({ jobs: [] })
      if (url.includes("/conversations/room?")) return Response.json({ ...room, kind: "channel", access_scope: "workspace" })
      if (url.includes("/conversations?")) return Response.json({ conversations: [{ ...room, kind: "channel", access_scope: "workspace" }], next_offset: null })
      return normal(url, options)
    })
    setup()
    fireEvent.click(await screen.findByRole("checkbox", { name: "@marena" }))
    fireEvent.change(screen.getByRole("textbox", { name: "Message Engineering" }), { target: { value: "Review this" } })
    fireEvent.click(screen.getByRole("button", { name: "Send" }))
    await waitFor(() => expect(fetcher.mock.calls.some(([url, options]) => url.includes("/messages") && options?.method === "POST")).toBe(true))
    const [, options] = fetcher.mock.calls.find(([url, options]) => url.includes("/messages") && options?.method === "POST")!
    expect(JSON.parse(options.body)).toMatchObject({ content: "Review this", mentioned_agent_ids: ["agent-1"] })
  })
  it("does not request agent execution surfaces for a private group", async () => {
    setup()
    await screen.findByRole("textbox", { name: "Message Engineering" })
    expect(fetcher.mock.calls.some(([url]) => url.includes("/agent-jobs") || url.includes("/room/agents?"))).toBe(false)
  })

  it("picks a joined agent with Enter without sending, and removing the mention removes its target", async () => {
    const normal = fetcher.getMockImplementation()!
    fetcher.mockImplementation(async (url: string, options?: RequestInit) => {
      if (url.includes("/room/agents?")) return Response.json({ agents: [{ agent_id: "agent-1", name: "Mařena", slug: "marena" }] })
      if (url.includes("/agent-jobs")) return Response.json({ jobs: [] })
      if (url.includes("/conversations/room?")) return Response.json({ ...room, kind: "channel", access_scope: "workspace" })
      if (url.includes("/conversations?")) return Response.json({ conversations: [{ ...room, kind: "channel", access_scope: "workspace" }], next_offset: null })
      return normal(url, options)
    })
    setup()
    const target = await screen.findByRole("checkbox", { name: "@marena" })
    const composer = screen.getByRole("textbox", { name: "Message Engineering" }) as HTMLTextAreaElement
    fireEvent.input(composer, { target: { value: "@ma", selectionStart: 3, selectionEnd: 3 } })
    await screen.findByRole("listbox", { name: "Mention suggestions" })
    fireEvent.keyDown(composer, { key: "Enter" })
    await waitFor(() => expect(composer).toHaveValue("@marena "))
    expect(target).toBeChecked()
    expect(fetcher.mock.calls.some(([url, options]) => url.includes("/messages") && options?.method === "POST")).toBe(false)
    fireEvent.input(composer, { target: { value: "Hello people" } })
    expect(target).not.toBeChecked()
    fireEvent.click(screen.getByRole("button", { name: "Send" }))
    await waitFor(() => expect(fetcher.mock.calls.some(([url, options]) => url.includes("/messages") && options?.method === "POST")).toBe(true))
    const [, options] = fetcher.mock.calls.find(([url, options]) => url.includes("/messages") && options?.method === "POST")!
    expect(JSON.parse(options.body)).not.toHaveProperty("mentioned_agent_ids")
  })

  it("mutes personal inbox notifications without disabling messaging", async () => {
    const normal = fetcher.getMockImplementation()!
    let muted = false
    fetcher.mockImplementation(async (url: string, options?: RequestInit) => {
      if (url.includes("/mute")) { muted = JSON.parse(options!.body as string).muted; return new Response(null, { status: 204 }) }
      if (url.includes("/conversations/room?")) return Response.json({ ...room, muted })
      return normal(url, options)
    })
    setup()
    fireEvent.click(await screen.findByRole("button", { name: "Mute" }))
    await screen.findByRole("button", { name: "Unmute" })
    expect(screen.getByRole("textbox", { name: "Message Engineering" })).not.toBeDisabled()
    expect(screen.getByText("Inbox notifications are muted for you. Messages remain visible.")).toBeInTheDocument()
  })

  it("opens a reused direct message outside the listed page and hides participant management", async () => {
    const normal = fetcher.getMockImplementation()!
    const dm = { ...room, id: "old-direct", title: "Alice · Bob", is_direct: true }
    fetcher.mockImplementation(async (url: string, options?: RequestInit) => {
      if (url.includes("/workspaces/ws/members")) return Response.json([{ user: { id: "alice", full_name: "Alice", email: "alice@example.test" } }, { user: { id: "bob", full_name: "Bob", email: "bob@example.test" } }])
      if (url.includes("/conversations/direct")) return Response.json(dm)
      if (url.includes("/conversations/old-direct?")) return Response.json(dm)
      if (url.includes("/participants")) return Response.json({ participants: [{ user_id: "alice", name: "Alice", role: "owner" }, { user_id: "bob", name: "Bob", role: "member" }] })
      return normal(url, options)
    })
    setup()
    fireEvent.click(screen.getByRole("button", { name: "New direct message" }))
    fireEvent.click(await screen.findByRole("button", { name: /Bob/ }))
    await screen.findByRole("textbox", { name: "Message Alice · Bob" })
    expect(new URLSearchParams(window.location.search).get("conversation")).toBe("old-direct")
    expect(screen.getByText("Direct message · Only the two participants have access")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "People" }))
    await screen.findByText("Direct message participants")
    await screen.findByText("Alice")
    expect(screen.queryByText("Manage people")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument()
    expect(fetcher.mock.calls.some(([url]) => url.includes("old-direct/agents") || url.includes("old-direct/agent-jobs"))).toBe(false)
  })

})
