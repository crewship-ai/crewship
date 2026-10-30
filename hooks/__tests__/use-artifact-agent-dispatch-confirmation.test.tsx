import { act, renderHook, waitFor } from "@testing-library/react"
import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, describe, expect, it, vi } from "vitest"

const transport = vi.hoisted(() => ({ send: vi.fn(), onMessage: undefined as undefined | ((message: unknown) => void) }))
vi.mock("@/hooks/use-websocket", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/hooks/use-websocket")>(),
  useWebSocket: (options: { onMessage?: (message: unknown) => void }) => {
    transport.onMessage = options.onMessage
    return { status: "connected", send: transport.send, retry: vi.fn() }
  },
}))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "user-1" } } }) }))
vi.mock("@/lib/server-base", () => ({ resolveWsBase: () => "ws://t" }))
vi.mock("@/lib/api-fetch", async (importOriginal) => ({ ...await importOriginal<typeof import("@/lib/api-fetch")>(), apiFetch: vi.fn(async (url: string) => new Response(JSON.stringify(url.includes("execution-profile") ? { mode: "trusted" } : { token: "t" }), { status: 200 })) }))
import { useArtifactAgentDispatch } from "../use-artifact-agent-dispatch"

const comment = { id: "c1", version: 3, path: "index.html", selector: "#hero", quote: "Hero", text: "Bolder", task_id: "t1" }
const origin = { chat_id: "chat-1", agent_id: "a1" }
function mount() {
  const client = new QueryClient()
  const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children)
  return renderHook(() => useArtifactAgentDispatch("w", "p", origin, true, "design"), { wrapper })
}
afterEach(() => { vi.useRealTimers(); transport.send.mockReset() })

describe("agent dispatch confirmation", () => {
  it("confirms delivery from the sequenced echo although this hook loads no history", async () => {
    const hook = mount()
    await waitFor(() => expect(hook.result.current.ready).toBe(true))
    let delivered: Promise<boolean> | undefined
    act(() => { delivered = hook.result.current.dispatch(comment) })
    await waitFor(() => expect(transport.send).toHaveBeenCalled())
    act(() => {
      transport.onMessage?.({ type: "run_begin", channel: "session:chat-1", seq: 40, payload: { from_seq: 40 } })
      transport.onMessage?.({ type: "chat_event", channel: "session:chat-1", seq: 41, payload: { type: "user_message", content: "x", metadata: { author_user_id: "user-1", artifact_project_intent: { kind: "design", submission_id: "artifact-comment-c1" } } } })
      transport.onMessage?.({ type: "chat_event", channel: "session:chat-1", seq: 42, payload: { type: "done", content: "", metadata: {} } })
    })
    await expect(delivered).resolves.toBe(true)
    await waitFor(() => expect(hook.result.current.ready).toBe(true))
  })

  it("settles at once as busy when the server refuses the send", async () => {
    const hook = mount()
    await waitFor(() => expect(hook.result.current.ready).toBe(true))
    let delivered: Promise<boolean> | undefined
    act(() => { delivered = hook.result.current.dispatch(comment) })
    await waitFor(() => expect(transport.send).toHaveBeenCalled())
    act(() => { transport.onMessage?.({ type: "error", channel: "session:chat-1", payload: { error: "a message is already being processed" } }) })
    await expect(delivered).resolves.toBe(false)
    expect(hook.result.current.refusal("c1")).toBe("busy")
  })
})
