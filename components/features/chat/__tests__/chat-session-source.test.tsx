import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { ChatSessionSource } from "../chat-session-source"
import { useDrawerStore } from "@/stores/drawer-store"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
beforeEach(() => { api.mockReset(); useDrawerStore.setState({ workSource: null, open: false }) })

describe("ChatSessionSource", () => {
  it("opens the recorded source in Work without inferring it from a session title", async () => {
    const source = { kind: "routine" as const, id: "routine-1", slug: "weekly", name: "Weekly review", run_id: "run-1", step_id: "review" }
    api.mockResolvedValue(Response.json([{ id: "chat-1", title: "Renamed", source }]))
    render(<ChatSessionSource workspaceId="ws" agentId="agent" sessionId="chat-1" />)
    fireEvent.click(await screen.findByRole("button", { name: /Routine · Weekly review/ }))
    expect(useDrawerStore.getState()).toMatchObject({ open: true, activeTab: "work", workSource: { workspaceId: "ws", agentId: "agent", source } })
    expect(api.mock.calls[0][0]).toContain("chat_id=chat-1&source=1")
  })
  it("discards a late source after changing workspace and leaves unlinked legacy chats without guessed links", async () => {
    let finish!: (value: Response) => void
    api.mockImplementationOnce(() => new Promise<Response>((resolve) => { finish = resolve })).mockResolvedValue(Response.json([{ id: "new", title: "Weekly · step" }]))
    const view = render(<ChatSessionSource workspaceId="old" agentId="agent" sessionId="old" />)
    view.rerender(<ChatSessionSource workspaceId="new" agentId="agent" sessionId="new" />)
    await act(async () => { finish(Response.json([{ id: "old", source: { kind: "routine", id: "secret", name: "Private old workspace" } }])) })
    await waitFor(() => expect(api).toHaveBeenCalledTimes(2))
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
    expect(useDrawerStore.getState().workSource).toBeNull()
  })
})
