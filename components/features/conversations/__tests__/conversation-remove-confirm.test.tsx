import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"

const fetcher = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher }))
vi.mock("@/hooks/use-workspace", () => ({ useCurrentWorkspaceId: () => "ws", useWorkspace: () => ({ workspaceId: "ws", loading: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "alice" } } }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams() }))

import { ConversationAgents } from "../conversation-agents"
import { ConversationMembers } from "../workspace-conversations"
import type { WorkspaceConversation } from "@/hooks/use-workspace-conversations"

// #3342 — removing an agent from a channel or a person from a conversation
// asks in the page; Cancel leaves the member where it was.

function wrap(node: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>)
}
const deletes = () => fetcher.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === "DELETE")

async function answer(title: string, label: string) {
  const dialog = await screen.findByRole("alertdialog", { name: title })
  fireEvent.click(within(dialog).getByRole("button", { name: label }))
  await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument())
  return dialog
}

beforeEach(() => {
  fetcher.mockReset()
  fetcher.mockImplementation(async (_url: string, init?: RequestInit) => (init?.method === "DELETE" ? new Response(null, { status: 204 }) : Response.json([])))
})

describe("removing a channel agent", () => {
  it("asks first; Cancel keeps the agent, confirm removes it", async () => {
    const refresh = vi.fn()
    wrap(<ConversationAgents workspaceId="ws" conversationId="room" canManage refresh={refresh}
      agents={[{ agent_id: "ag1", name: "Casey", slug: "casey", joined_at: "" }]} />)
    fireEvent.click(screen.getByRole("button", { name: "Remove" }))
    const dialog = await answer("Remove Casey from this channel?", "Cancel")
    expect(dialog).toHaveTextContent("can no longer be asked to respond here")
    expect(deletes()).toHaveLength(0)
    expect(refresh).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "Remove" }))
    await answer("Remove Casey from this channel?", "Remove agent")
    await waitFor(() => expect(refresh).toHaveBeenCalled())
    expect(String(deletes()[0][0])).toBe("/api/v1/conversations/room/agents/ag1?workspace_id=ws")
  })
})

describe("removing a conversation member", () => {
  const group = { id: "room", title: "Engineering", kind: "group", access_scope: "participants", created_by: "alice" } as WorkspaceConversation
  const participants = [{ user_id: "alice", name: "Alice", role: "owner" }, { user_id: "bob", name: "Bob", role: "member" }]

  it("asks first; Cancel keeps the person, confirm removes them", async () => {
    const refresh = vi.fn()
    wrap(<ConversationMembers workspaceId="ws" userId="alice" conversation={group} participants={participants} refresh={refresh} />)
    fireEvent.click(screen.getByRole("button", { name: "Remove Bob" }))
    const dialog = await answer("Remove Bob from this conversation?", "Cancel")
    expect(dialog).toHaveTextContent("They lose access to this group and its history")
    expect(deletes()).toHaveLength(0)
    expect(refresh).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "Remove Bob" }))
    await answer("Remove Bob from this conversation?", "Remove member")
    await waitFor(() => expect(refresh).toHaveBeenCalled())
    expect(String(deletes()[0][0])).toBe("/api/v1/conversations/room/participants/bob?workspace_id=ws")
  })

  it("says a channel stays open to the workspace", async () => {
    wrap(<ConversationMembers workspaceId="ws" userId="alice" conversation={{ ...group, kind: "channel" } as WorkspaceConversation} participants={participants} refresh={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: "Remove Bob" }))
    const dialog = await screen.findByRole("alertdialog", { name: "Remove Bob from this conversation?" })
    expect(dialog).toHaveTextContent("everyone in the workspace can still read and write here")
  })
})
