import { beforeEach, describe, expect, it, vi } from "vitest"
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { UnifiedChatProvider } from "../unified-chat"
import { LegacyConversationRedirect } from "../legacy-conversation-redirect"
import { CreateConversation } from "../workspace-conversations"
import { ChatClient } from "@/app/(dashboard)/chat/chat-client"
import { useArtifactStore } from "@/stores/artifact-store"

const fixtures = vi.hoisted(() => {
  const agent = { id: "agent", name: "Ava", slug: "ava", status: "IDLE" }
  return { workspaceId: "ws", userId: "alice", agent, tree: { agents: [agent], roster: [agent], threadsLoaded: true, threadErrors: {}, threadsByAgent: { agent: [{ id: "legacy", title: "Ava history", started_at: "2026-09-01T12:00:00Z", message_count: 1 }] }, retryThreads: vi.fn(), retryRoster: vi.fn(), loadAllFor: vi.fn(), totalsByAgent: {}, kindCounts: null }, api: vi.fn() }
})
let params = new URLSearchParams()
vi.mock("next/navigation", () => ({ useSearchParams: () => params }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: fixtures.workspaceId, loading: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: fixtures.userId } } }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fixtures.api }))
vi.mock("@/lib/telemetry", () => ({ emitChatEvent: vi.fn() }))
vi.mock("@/components/features/chat/chat-tree-data", async (original) => ({ ...await original<typeof import("@/components/features/chat/chat-tree-data")>(), useChatCompactLayout: () => false, useChatTreeData: () => fixtures.tree }))
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))
vi.mock("@/components/features/chat/chat-panel", () => ({ ChatPanel: ({ agentSlug, sessionId }: { agentSlug: string; sessionId: string }) => <div data-testid="agent-panel">{agentSlug}:{sessionId}</div> }))
const room = { id: "room", title: "Alice · Bob", kind: "group", is_direct: true, access_scope: "participants", created_by: "alice", last_sequence: 0, unread_count: 0 }
function wrap(children: React.ReactNode) {
  return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>
}
beforeEach(() => {
  fixtures.workspaceId = "ws"
  fixtures.userId = "alice"
  fixtures.tree.threadErrors = {}
  fixtures.tree.threadsByAgent.agent = [{ id: "legacy", title: "Ava history", started_at: "2026-09-01T12:00:00Z", message_count: 1 }]
  sessionStorage.clear()
  useArtifactStore.getState().closeAll()
  window.history.replaceState(null, "", "/chat/ava?conversation=room&workspace_id=ws")
  params = new URLSearchParams(window.location.search)
  Element.prototype.scrollIntoView = vi.fn()
  fixtures.api.mockReset()
  fixtures.api.mockImplementation(async (url: string, options?: RequestInit) => {
    if (options?.method === "PUT" || url.includes("/read")) return new Response(null, { status: 204 })
    if (url.includes("/messages")) return Response.json({ messages: [], has_more: false })
    if (url.includes("/conversations/room?")) return Response.json(room)
    if (url.includes("/members")) return Response.json([])
    return Response.json({ conversations: [room], next_offset: null })
  })
})
describe("unified Chat", () => {
  it("starts an agent session from the shared Chat sub-bar", async () => {
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    await screen.findByRole("button", { name: "Open Ava chat" })
    expect(screen.getByRole("heading", { name: "Chat" })).toBeInTheDocument()
    fireEvent.keyDown(screen.getByRole("button", { name: "New chat" }), { key: "ArrowDown" })
    fireEvent.click(await screen.findByRole("menuitem", { name: /Chat with an agent/ }))
    expect(screen.getByRole("region", { name: "Choose an agent" })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Ava" }))
    expect(await screen.findByText("Ava · Draft")).toBeInTheDocument()
  })

  it("folds the chat list for focused artifact reading and restores its prior fold", async () => {
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    await screen.findByRole("button", { name: "Open Ava chat" })
    act(() => { useArtifactStore.getState().openFile({ id: "agent:report.pdf", agentId: "agent", path: "report.pdf", title: "report.pdf" }); useArtifactStore.getState().setFocus(true) })
    expect(screen.getByRole("button", { name: "Expand sidebar" })).toBeInTheDocument()
    act(() => useArtifactStore.getState().setFocus(false))
    expect(screen.getByRole("button", { name: "Collapse sidebar" })).toBeInTheDocument()
  })

  it("keeps Activity visible and applies sidebar filters to agent rows", async () => {
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    await screen.findByRole("button", { name: "Open Ava chat" })
    const activity = screen.getByRole("region", { name: "Agent activity" })
    fireEvent.click(within(activity).getByRole("button", { name: /^All/ }))
    expect(within(activity).getByRole("button", { name: /^All/ })).toHaveAttribute("aria-pressed", "true")
    fireEvent.click(screen.getByRole("button", { name: "Filter" }))
    fireEvent.click(screen.getByRole("button", { name: "Unread agent sessions" }))
    expect(screen.queryByRole("button", { name: "Open Ava chat" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Clear all" }))
    expect(screen.getByRole("button", { name: "Open Ava chat" })).toBeInTheDocument()
  })

  it("keeps shared rooms visible across agent activity scopes and sizes rows alike", async () => {
    const teamRoom = { ...room, id: "team-room", title: "Design updates", kind: "channel", is_direct: false }
    fixtures.api.mockImplementation(async (url: string, options?: RequestInit) => {
      if (options?.method === "PUT" || url.includes("/read")) return new Response(null, { status: 204 })
      if (url.includes("/messages")) return Response.json({ messages: [], has_more: false })
      if (url.includes("/conversations/room?")) return Response.json(room)
      return Response.json({ conversations: [room, teamRoom], next_offset: null })
    })
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    const person = await screen.findByRole("button", { name: /Alice · Bob Direct message/ })
    const agent = screen.getByRole("button", { name: "Open Ava chat" })
    expect(person.className).toContain("h-9")
    expect(person.className).toContain("text-xs")
    expect(agent.className).toContain("min-h-9")
    expect(agent.className).toContain("text-xs")
    expect(screen.getByRole("region", { name: "Team spaces" })).toBeInTheDocument()

    const activity = screen.getByRole("region", { name: "Agent activity" })
    fireEvent.click(within(activity).getByRole("button", { name: /^All/ }))
    const spaces = screen.getByRole("region", { name: "Team spaces" })
    const space = within(spaces).getByRole("button", { name: /Design updates Workspace channel/ })
    expect(space.className).toContain("h-9")
    expect(space.className).toContain("text-xs")
    fireEvent.click(within(activity).getByRole("button", { name: /^Routines/ }))
    expect(screen.getByRole("region", { name: "People" })).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "Team spaces" })).toBeInTheDocument()
    fireEvent.click(within(activity).getByRole("button", { name: /^All/ }))
    fireEvent.click(screen.getByRole("button", { name: "Filter" }))
    fireEvent.click(screen.getByRole("button", { name: "Ava" }))
    expect(screen.queryByRole("region", { name: "People" })).not.toBeInTheDocument()
    expect(screen.queryByRole("region", { name: "Team spaces" })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Open Ava chat" })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Ava" }))
    expect(screen.getByRole("button", { name: "Ava" })).toHaveAttribute("aria-pressed", "false")
    expect(screen.getByRole("region", { name: "Team spaces" })).toBeInTheDocument()
  })

  it("searches channels from Direct and lets the section filter toggle off", async () => {
    const teamRoom = { ...room, id: "team-room", title: "Design updates", kind: "channel", is_direct: false }
    fixtures.api.mockImplementation(async (url: string, options?: RequestInit) => {
      if (options?.method === "PUT" || url.includes("/read")) return new Response(null, { status: 204 })
      if (url.includes("/messages")) return Response.json({ messages: [], has_more: false })
      if (url.includes("/conversations/room?")) return Response.json(room)
      return Response.json({ conversations: [room, teamRoom], next_offset: null })
    })
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    await screen.findByRole("button", { name: /Design updates Workspace channel/ })
    fireEvent.change(screen.getByRole("textbox", { name: "Search conversations" }), { target: { value: "Design" } })
    expect(within(screen.getByRole("region", { name: "Team spaces" })).getByRole("button", { name: /Design updates/ })).toBeInTheDocument()
    expect(screen.queryByRole("region", { name: "People" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Clear search" }))
    fireEvent.click(screen.getByRole("button", { name: "Filter" }))
    fireEvent.click(screen.getByRole("button", { name: "Team spaces" }))
    expect(screen.queryByRole("region", { name: "Agents" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Team spaces" }))
    expect(screen.getByRole("region", { name: "Agents" })).toBeInTheDocument()
  })

  it("uses All to clear filters and lets a selected agent row collapse without leaving its chat", async () => {
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    await screen.findByRole("button", { name: "Open Ava chat" })
    fireEvent.click(screen.getByRole("button", { name: "Open Ava chat" }))
    expect(await screen.findByTestId("agent-panel")).toHaveTextContent("ava:legacy")
    expect(screen.getByRole("button", { name: "Hide Ava sessions" })).toHaveAttribute("aria-expanded", "true")
    expect(screen.getByRole("button", { name: "Open Ava chat" }).parentElement?.className).toContain("bg-primary/10")
    fireEvent.click(screen.getByRole("button", { name: "Open Ava chat" }))
    expect(screen.getByRole("button", { name: "Show Ava sessions" })).toHaveAttribute("aria-expanded", "false")
    expect(screen.getByRole("button", { name: "Open Ava chat" }).parentElement?.className).not.toContain("bg-primary/10")
    expect(screen.getByTestId("agent-panel")).toHaveTextContent("ava:legacy")

    fireEvent.click(screen.getByRole("button", { name: "Filter" }))
    fireEvent.click(screen.getByRole("button", { name: "Agent sessions" }))
    expect(screen.queryByRole("region", { name: "Team spaces" })).not.toBeInTheDocument()
    fireEvent.click(within(screen.getByRole("region", { name: "Agent activity" })).getByRole("button", { name: /^All/ }))
    expect(screen.getByRole("region", { name: "People" })).toBeInTheDocument()
  })

  it("reveals a linked team space without changing the agent activity scope", async () => {
    const teamRoom = { ...room, id: "team-room", title: "Design updates", kind: "channel", is_direct: false }
    window.history.replaceState(null, "", "/chat?conversation=team-room&workspace_id=ws")
    params = new URLSearchParams(window.location.search)
    fixtures.api.mockImplementation(async (url: string) => {
      if (url.includes("/conversations/team-room?")) return Response.json(teamRoom)
      if (url.includes("/conversations/team-room/agents")) return Response.json({ agents: [] })
      if (url.includes("/conversations/team-room/agent-jobs")) return Response.json({ jobs: [] })
      if (url.includes("/messages")) return Response.json({ messages: [], has_more: false })
      return Response.json({ conversations: [room, teamRoom], next_offset: null })
    })
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    const activity = screen.getByRole("region", { name: "Agent activity" })
    expect(await screen.findByRole("region", { name: "Team spaces" })).toBeInTheDocument()
    expect(within(activity).getByRole("button", { name: /^Direct/ })).toHaveAttribute("aria-pressed", "true")
    fireEvent.click(within(activity).getByRole("button", { name: /^Direct/ }))
    expect(screen.getByRole("region", { name: "Team spaces" })).toBeInTheDocument()
    expect(within(activity).getByRole("button", { name: /^All/ })).toHaveAttribute("aria-pressed", "true")
  })

  it("shows people and agents in one sidebar and gives a human deep link precedence without creating an agent session", async () => {
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    await screen.findByRole("textbox", { name: "Message Alice · Bob" })
    expect(screen.queryByTestId("agent-panel")).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Show Ava sessions" })).toHaveAttribute("aria-expanded", "false")
    expect(screen.getByRole("button", { name: "Open Ava chat" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "New chat" })).toBeInTheDocument()
    expect(screen.queryByRole("link", { name: /People.*channels/ })).not.toBeInTheDocument()
    expect(fixtures.api.mock.calls.some(([, options]) => options?.method === "POST")).toBe(false)
  })
  it("switches human→agent→human in place with distinct canonical URLs and restores the human draft", async () => {
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    const composer = await screen.findByRole("textbox", { name: "Message Alice · Bob" })
    fireEvent.change(composer, { target: { value: "Draft for Bob" } })
    fireEvent.click(screen.getByRole("button", { name: "Open Ava chat" }))
    await screen.findByTestId("agent-panel")
    expect(window.location.pathname).toBe("/chat/ava")
    expect(new URLSearchParams(window.location.search).has("conversation")).toBe(false)
    fireEvent.click(screen.getByRole("button", { name: /Alice · Bob Direct message/ }))
    expect(window.location.pathname).toBe("/chat")
    expect(new URLSearchParams(window.location.search).get("workspace_id")).toBe("ws")
    expect(await screen.findByRole("textbox", { name: "Message Alice · Bob" })).toHaveValue("Draft for Bob")
    expect(screen.getByRole("button", { name: "Open Ava chat" })).not.toHaveAttribute("aria-current")
    expect(screen.getByRole("button", { name: "Ava history" })).not.toHaveAttribute("aria-current")
    window.history.back()
    await screen.findByTestId("agent-panel")
    window.history.forward()
    expect(await screen.findByRole("textbox", { name: "Message Alice · Bob" })).toHaveValue("Draft for Bob")
  })
  it("collapses independent sections without navigating or hiding other conversation types", async () => {
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    await screen.findByRole("button", { name: "Open Ava chat" })
    const before = window.location.href
    const agents = screen.getByRole("region", { name: "Agents" })
    fireEvent.click(within(agents).getByRole("button", { name: /^Agents/ }))
    expect(screen.queryByRole("button", { name: "Open Ava chat" })).not.toBeInTheDocument()
    expect(await screen.findByRole("button", { name: /Alice · Bob Direct message/ })).toBeInTheDocument()
    expect(window.location.href).toBe(before)
    expect(screen.queryByLabelText("Conversation types")).not.toBeInTheDocument()
  })
  it("keeps an agent once in the roster while exposing session history and an explicit new draft", async () => {
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    await screen.findByRole("button", { name: "Open Ava chat" })
    expect(screen.queryByText("Ava history")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Show Ava sessions" }))
    fireEvent.click(screen.getByRole("button", { name: "Ava history" }))
    expect(await screen.findByTestId("agent-panel")).toHaveTextContent("ava:legacy")
    fireEvent.click(screen.getByRole("button", { name: "New session with Ava" }))
    await screen.findByText("Ava · Draft")
    expect(screen.getAllByRole("button", { name: "Open Ava chat" })).toHaveLength(1)
    expect(screen.getByTestId("agent-panel")).not.toHaveTextContent("ava:legacy")
    expect(new URLSearchParams(window.location.search).get("session")).not.toBe("legacy")
    expect(fixtures.api.mock.calls.some(([, options]) => options?.method === "POST")).toBe(false)
    expect(screen.getByRole("button", { name: "Ava history" })).toBeInTheDocument()
    fireEvent.click(await screen.findByRole("button", { name: /Alice · Bob Direct message/ }))
    await screen.findByRole("textbox", { name: "Message Alice · Bob" })
    expect(screen.queryByText("Ava · Draft")).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Open Ava chat" })).not.toHaveAttribute("aria-current")
  })
  it("never mistakes unavailable history for a request to create a new session", async () => {
    fixtures.tree.threadErrors = { agent: "503" }
    fixtures.tree.threadsByAgent.agent = []
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    await screen.findByRole("textbox", { name: "Message Alice · Bob" })
    fireEvent.click(screen.getByRole("button", { name: "Open Ava chat" }))
    expect(screen.queryByTestId("agent-panel")).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Hide Ava sessions" })).toHaveAttribute("aria-expanded", "true")
    expect(screen.getByRole("alert")).toHaveTextContent("Sessions unavailable")
    fireEvent.click(screen.getByRole("button", { name: "New session with Ava" }))
    await screen.findByText("Ava · Draft")
    expect(fixtures.api.mock.calls.some(([, options]) => options?.method === "POST")).toBe(false)
  })
  it("restores collapsed sections and expanded agents only for the same workspace and user", async () => {
    const view = render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    await screen.findByRole("textbox", { name: "Message Alice · Bob" })
    fireEvent.click(screen.getByRole("button", { name: "Show Ava sessions" }))
    fireEvent.click(within(screen.getByRole("region", { name: "Agents" })).getByRole("button", { name: /^Agents/ }))
    view.unmount()
    const restored = render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    const section = within(screen.getByRole("region", { name: "Agents" })).getByRole("button", { name: /^Agents/ })
    expect(section).toHaveAttribute("aria-expanded", "false")
    fireEvent.click(section)
    expect(screen.getByRole("button", { name: "Hide Ava sessions" })).toHaveAttribute("aria-expanded", "true")
    restored.unmount()
    fixtures.workspaceId = "another-workspace"
    fixtures.userId = "bob"
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    expect(within(screen.getByRole("region", { name: "Agents" })).getByRole("button", { name: /^Agents/ })).toHaveAttribute("aria-expanded", "true")
    expect(screen.getByRole("button", { name: "Show Ava sessions" })).toHaveAttribute("aria-expanded", "false")
  })
  it("reveals an explicitly selected agent session even when its saved section was collapsed", async () => {
    const key = JSON.stringify(["chat-sidebar", "ws", "alice"])
    sessionStorage.setItem(`${key}:sections`, JSON.stringify({ agents: true }))
    sessionStorage.setItem(`${key}:agents`, JSON.stringify({ agent: false }))
    window.history.replaceState(null, "", "/chat/ava?session=legacy")
    params = new URLSearchParams(window.location.search)
    render(wrap(<UnifiedChatProvider><ChatClient /></UnifiedChatProvider>))
    await screen.findByTestId("agent-panel")
    expect(screen.getByRole("button", { name: "Ava history" })).toHaveAttribute("aria-current", "page")
    expect(screen.getByRole("button", { name: "Hide Ava sessions" })).toHaveAttribute("aria-expanded", "true")
  })
  it("does not navigate or submit twice when a group creation is dismissed in flight", async () => {
    const onCreated = vi.fn()
    let finish!: (response: Response) => void
    fixtures.api.mockImplementation(async (_url: string, options?: RequestInit) => options?.method === "POST" ? new Promise<Response>((resolve) => { finish = resolve }) : Response.json([]))
    const view = render(wrap(<CreateConversation workspaceId="ws" userId="alice" initialKind="channel" onCreated={onCreated} />))
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "Engineering" } })
    const form = screen.getByRole("button", { name: "Create conversation" }).closest("form")!
    fireEvent.submit(form)
    fireEvent.submit(form)
    expect(fixtures.api.mock.calls.filter(([, options]) => options?.method === "POST")).toHaveLength(1)
    view.unmount()
    finish(Response.json(room))
    await waitFor(() => expect(onCreated).not.toHaveBeenCalled())
  })
  it("redirects legacy links into Chat while preserving conversation and workspace scope", () => {
    window.history.replaceState(null, "", "/conversations?conversation=room&workspace_id=ws#message")
    const replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
    render(<LegacyConversationRedirect />)
    expect(replace).toHaveBeenCalledWith("/chat?conversation=room&workspace_id=ws#message")
    replace.mockRestore()
  })

})
