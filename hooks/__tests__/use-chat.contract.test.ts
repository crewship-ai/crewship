import { act, renderHook } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
const socket = vi.hoisted(() => ({ message: (_value: unknown) => {}, connect: () => {}, send: vi.fn() }))
vi.mock("@/hooks/use-websocket", () => ({
  useWebSocket: (options: { onMessage: (value: unknown) => void; onConnect?: () => void }) => { socket.message = options.onMessage; socket.connect = options.onConnect ?? (() => {}); return { status: "connected", send: socket.send } },
  encodedByteLength: (s: string) => new TextEncoder().encode(s).length, WS_MAX_OUTBOUND_FRAME_BYTES: 65536,
}))
import { messagesToTurns, useChat, type ChatMessage } from "../use-chat"

describe("canonical chat history identity", () => {
  it("preserves server part IDs across checkpoint replacement without deduplicating equal text", () => {
    const message = {
      id: "message-1", role: "assistant", content: "same same",
      timestamp: new Date("2026-10-18T12:00:00Z"),
      parts: [
        { id: "part-a", type: "text", content: "same", run_id: "run-1", turn_id: "turn-1", first_seq: 1, last_seq: 1 },
        { id: "part-b", type: "text", content: "same", run_id: "run-1", turn_id: "turn-1", first_seq: 2, last_seq: 2 },
      ],
    } satisfies ChatMessage
    expect(messagesToTurns([message])[0].parts.map(part => part.id)).toEqual(["part-a", "part-b"])
  })

  it("never exposes raw legacy thinking in renderable history", () => {
    const turns = messagesToTurns([{
      id: "legacy", role: "assistant", content: "answer", timestamp: new Date(),
      parts: [{ type: "thinking", content: "private legacy reasoning" }, { type: "text", content: "answer" }],
    }])
    expect(turns[0].parts.find(part => part.type === "thinking")?.content).toBe("")
    expect(turns[0].parts.find(part => part.type === "text")?.content).toBe("answer")
  })
})

function envelope(seq: number, content = "same") {
  return { version: 1, event_id: `event-${seq}`, run_id: "run-1", turn_id: "turn-1", part_id: "part-a", seq, occurred_at: "2026-10-18T12:00:00Z", delivery: "durable", type: "text", payload: { text: { content, operation: "delta" } } }
}
function send(event: unknown) { act(() => socket.message({ type: "chat_event", channel: "session:s", payload: { event } })) }
function setup() {
  const reset = vi.fn()
  const hook = renderHook(() => useChat({ sessionId: "s", wsUrl: "ws://test", getToken: async () => null, onStreamReset: reset }))
  act(() => hook.result.current.loadHistory([]))
  return { ...hook, reset }
}
describe("canonical live ingestion", () => {
  it("deduplicates replay by event identity, not equal text, and uses the server part identity", () => {
    const hook = setup()
    send(envelope(1)); send(envelope(1)); send(envelope(2))
    expect(hook.result.current.turns[0].parts).toMatchObject([{ id: "part-a", content: "samesame", first_seq: 1, last_seq: 2 }])
  })
  it("keeps Stop partial until the server acknowledges a terminal outcome", () => {
    const hook = setup()
    send(envelope(1))
    act(() => hook.result.current.stopGeneration())
    expect(hook.result.current.isStreaming).toBe(true)
    expect(hook.result.current.turns[0].cancelRequested).toBe(true)
    send({ ...envelope(2), type: "turn", part_id: undefined, payload: { turn: { state: "interrupted" } } })
    expect(hook.result.current.isStreaming).toBe(false)
    expect(hook.result.current.turns[0].turnState).toBe("interrupted")
  })
  it("shows Stopping until the terminal state and sends one cancel per stop", () => {
    const hook = setup()
    send(envelope(1))
    socket.send.mockClear()
    act(() => hook.result.current.stopGeneration())
    act(() => hook.result.current.stopGeneration())
    expect(socket.send.mock.calls.filter(([frame]) => frame.type === "cancel_message")).toHaveLength(1)
    expect(hook.result.current.stopping).toBe(true)
    send({ ...envelope(2), type: "turn", part_id: undefined, payload: { turn: { state: "cancelled" } } })
    expect(hook.result.current.stopping).toBe(false)
    expect(hook.result.current.turns[0].parts[0].content).toBe("same")
  })
  it("requests authorized history for a gap without skipping to the live tail", () => {
    const hook = setup()
    send(envelope(2, "second"))
    expect(hook.result.current.turns).toEqual([])
    expect(hook.reset).toHaveBeenCalledTimes(1)
    send(envelope(1, "first"))
    expect(hook.result.current.turns[0].parts[0].content).toBe("firstsecond")
  })
  it("folds pending events over a history checkpoint once, preserving the message ID", () => {
    const hook = setup()
    send(envelope(2, "second"))
    act(() => hook.result.current.loadHistory([{
      id: "message-1", role: "assistant", content: "first", timestamp: new Date(), runId: "run-1",
      checkpoint: { version: 1, run_id: "run-1", turn_id: "turn-1", through_seq: 1, turn_state: "running" },
      parts: [{ id: "part-a", type: "text", content: "first", run_id: "run-1", turn_id: "turn-1", first_seq: 1, last_seq: 1 }],
    }]))
    expect(hook.result.current.turns).toHaveLength(1)
    expect(hook.result.current.turns[0]).toMatchObject({ id: "message-1", parts: [{ id: "part-a", content: "firstsecond" }] })
  })
  it("rejects malformed v1 frames without rendering their legacy/raw fallback", () => {
    const hook = setup()
    act(() => socket.message({ type: "chat_event", channel: "session:s", payload: { event: { ...envelope(1), version: 2 }, type: "text", content: "must not render" } }))
    expect(hook.result.current.turns).toEqual([])
  })
})

describe("typed persisted checkpoints", () => {
  it("retains canonical activity/output types, state and references", () => {
    const turns = messagesToTurns([{
      id: "t", role: "assistant", runId: "r", content: "", timestamp: new Date(),
      checkpoint: { version: 1, run_id: "r", turn_id: "t", through_seq: 3, turn_state: "failed" },
      parts: [
        { id: "a", run_id: "r", turn_id: "t", first_seq: 1, last_seq: 2, type: "activity", content: "Read", tool_call_id: "tool-a", activity: { kind: "read", state: "failed" } },
        { id: "o", run_id: "r", turn_id: "t", first_seq: 3, last_seq: 3, type: "output", content: "", output: { state: "partial", ref: { kind: "document", id: "doc-a" } } },
      ],
    }])
    expect(turns[0]).toMatchObject({ turnState: "failed", isStreaming: false, parts: [
      { id: "a", type: "activity", tool_call_id: "tool-a", activity: { state: "failed" } },
      { id: "o", type: "output", output: { state: "partial", ref: { id: "doc-a" } } },
    ] })
  })
  it("does not replace already-applied text with an older checkpoint", () => {
    const hook = setup()
    send(envelope(1, "first")); send(envelope(2, "second"))
    act(() => hook.result.current.loadHistory([{
      id: "turn-1", role: "assistant", runId: "run-1", content: "first", timestamp: new Date(),
      checkpoint: { version: 1, run_id: "run-1", turn_id: "turn-1", through_seq: 1, turn_state: "running" },
      parts: [{ id: "part-a", run_id: "run-1", turn_id: "turn-1", type: "text", content: "first" }],
    }]))
    expect(hook.result.current.turns[0].parts[0].content).toBe("firstsecond")
    send(envelope(2, "second"))
    expect(hook.result.current.turns[0].parts[0].content).toBe("firstsecond")
  })
  it("keeps a checkpoint-only running turn pending on Stop", () => {
    const hook = setup()
    act(() => hook.result.current.loadHistory([{
      id: "turn-1", role: "assistant", runId: "run-1", content: "first", timestamp: new Date(),
      checkpoint: { version: 1, run_id: "run-1", turn_id: "turn-1", through_seq: 1, turn_state: "running" },
      parts: [{ id: "part-a", run_id: "run-1", turn_id: "turn-1", type: "text", content: "first" }],
    }]))
    act(() => hook.result.current.stopGeneration())
    expect(hook.result.current.isStreaming).toBe(true)
  })
})

describe("bounded canonical recovery", () => {
  it("limits history requests while a gap remains unfilled", () => {
    const hook = setup()
    for (let seq = 2; seq < 20; seq++) send(envelope(seq))
    expect(hook.reset).toHaveBeenCalledTimes(2)
    expect(hook.result.current.turns).toEqual([])
  })
  it("opens the canonical gate after history failure without skipping a missing seq", () => {
    const hook = renderHook(() => useChat({ sessionId: "s", wsUrl: "ws://test", getToken: async () => null }))
    send(envelope(1, "kept"))
    expect(hook.result.current.turns).toEqual([])
    act(() => hook.result.current.markHistoryUnavailable())
    expect(hook.result.current.turns[0].parts[0].content).toBe("kept")
  })
})

describe("current human authority", () => {
  it("preserves an absent in-flight turn on refresh but clears it on explicit reset", () => {
    const hook = setup()
    send(envelope(1, "partial"))
    act(() => hook.result.current.loadHistory([]))
    expect(hook.result.current.turns[0].parts[0].content).toBe("partial")
    act(() => hook.result.current.loadHistory([], { reset: true }))
    expect(hook.result.current.turns).toEqual([])
  })
  it("masks old private parts on the first render under a different human and rejects the old loader", () => {
    const frames: { human: string; text: string }[] = []
    const hook = renderHook(({ human, workspace }) => {
      const chat = useChat({ sessionId: "s", wsUrl: "ws://test", getToken: async () => null, currentUserId: human, workspaceId: workspace })
      frames.push({ human, text: chat.turns.flatMap(turn => turn.parts.map(part => part.content)).join("") })
      return chat
    }, { initialProps: { human: "human-a", workspace: "w" } })
    act(() => hook.result.current.loadHistory([]))
    send(envelope(1, "private-a"))
    const oldLoad = hook.result.current.loadHistory
    const oldMessage = socket.message
    hook.rerender({ human: "human-b", workspace: "w" })
    expect(frames.find(frame => frame.human === "human-b")?.text).toBe("")
    act(() => oldLoad([{ id: "old", role: "assistant", content: "private-a", timestamp: new Date() }]))
    act(() => oldMessage({ type: "chat_event", channel: "session:s", payload: { event: envelope(2, "private-late") } }))
    expect(hook.result.current.turns).toEqual([])
  })
  it("clears canonical pending state on workspace change", () => {
    const hook = renderHook(({ workspace }) => useChat({ sessionId: "s", wsUrl: "ws://test", getToken: async () => null, currentUserId: "human", workspaceId: workspace }), { initialProps: { workspace: "a" } })
    act(() => hook.result.current.loadHistory([]))
    send(envelope(1, "workspace-a")); send(envelope(3, "pending-a"))
    hook.rerender({ workspace: "b" })
    act(() => hook.result.current.loadHistory([]))
    send(envelope(2, "late-a"))
    expect(hook.result.current.turns).toEqual([])
  })
  it("clears and gates private parts after a definitive session revocation", () => {
    const hook = setup()
    send(envelope(1, "private"))
    act(() => window.dispatchEvent(new CustomEvent("auth:session-expired")))
    expect(hook.result.current.turns).toEqual([])
    expect(hook.result.current.isStreaming).toBe(false)
    send(envelope(2, "late private"))
    expect(hook.result.current.turns).toEqual([])
  })
})

it("keeps a known principal's transcript gated while identity is unknown", () => {
  const hook = renderHook(({ human }: { human: string | undefined }) => useChat({ sessionId: "s", currentUserId: human, workspaceId: "w", wsUrl: "ws://test", getToken: async () => null }), { initialProps: { human: "human-a" as string | undefined } })
  act(() => hook.result.current.loadHistory([]))
  send(envelope(1, "private-a"))
  hook.rerender({ human: undefined })
  expect(hook.result.current.turns).toEqual([])
  send(envelope(1, "late-private"))
  act(() => hook.result.current.loadHistory([{ id: "late", role: "assistant", content: "private-a", timestamp: new Date() }]))
  expect(hook.result.current.turns).toEqual([])
})

it.each(["assistant", "system", "tool"] as const)("never exposes raw flat legacy thinking from %s history", role => {
  const turns = messagesToTurns([{ id: "legacy", role, eventType: "thinking", content: "private flat reasoning", timestamp: new Date() }])
  expect(JSON.stringify(turns)).not.toContain("private flat reasoning")
  expect(turns[0].parts[0].type).toBe("thinking")
})

function outer(seq: number, event: unknown) { act(() => socket.message({ type: "chat_event", channel: "session:s", seq, payload: { event } })) }
function resumeCursor(): number {
  const frames = socket.send.mock.calls.map(call => call[0] as { type?: string; payload?: string }).filter(frame => frame.type === "resume")
  return (JSON.parse(frames.at(-1)?.payload ?? "{}") as { last_seq: number }).last_seq
}
describe("independent session transport replay cursor", () => {
  it("resumes only after contiguous canonical frames and recovers ephemeral replay exactly once", () => {
    const hook = setup()
    act(() => socket.connect())
    act(() => socket.message({ type: "run_begin", channel: "session:s", seq: 100 }))
    outer(101, { ...envelope(1, "A"), delivery: "ephemeral" })
    outer(103, { ...envelope(3, "C"), delivery: "ephemeral" })
    expect(hook.result.current.turns[0].parts[0].content).toBe("A")
    act(() => socket.connect())
    expect(resumeCursor()).toBe(101)
    outer(102, { ...envelope(2, "B"), delivery: "ephemeral" })
    outer(103, { ...envelope(3, "C"), delivery: "ephemeral" })
    act(() => hook.result.current.loadHistory([]))
    expect(hook.result.current.turns[0].parts[0].content).toBe("ABC")
    expect(hook.result.current.turns[0].streamIncomplete).not.toBe(true)
    act(() => socket.connect())
    expect(resumeCursor()).toBe(103)
  })
  it("does not mistake a new run's seq1 for the session cursor", () => {
    const hook = setup()
    act(() => socket.connect())
    act(() => socket.message({ type: "run_begin", channel: "session:s", seq: 200 }))
    outer(201, envelope(1, "new run"))
    act(() => socket.connect())
    expect(resumeCursor()).toBe(201)
    expect(hook.result.current.turns[0].parts[0].content).toBe("new run")
  })
  it("keeps an unrecoverable ephemeral gap explicitly partial and Stop pending", () => {
    const hook = setup()
    act(() => socket.connect())
    act(() => socket.message({ type: "run_begin", channel: "session:s", seq: 300 }))
    outer(301, { ...envelope(1, "A"), delivery: "ephemeral" })
    outer(303, { ...envelope(3, "C"), delivery: "ephemeral" })
    act(() => socket.message({ type: "resume_reset", channel: "session:s" }))
    act(() => hook.result.current.loadHistory([{
      id: "turn-1", role: "assistant", content: "A", runId: "run-1", timestamp: new Date(),
      checkpoint: { version: 1, run_id: "run-1", turn_id: "turn-1", through_seq: 1, turn_state: "running" },
      parts: [{ id: "part-a", run_id: "run-1", turn_id: "turn-1", type: "text", content: "A" }],
    }]))
    expect(hook.result.current.turns[0].streamIncomplete).toBe(true)
    expect(hook.result.current.turns[0].parts[0].content).toBe("A")
    act(() => hook.result.current.stopGeneration())
    expect(hook.result.current.isStreaming).toBe(true)
    act(() => socket.connect())
    expect(resumeCursor()).toBe(301)
  })
})

it("keeps legacy control result/done scoped without terminating canonical turns", () => {
  const hook = setup()
  send(envelope(1, "canonical"))
  for (const [type, runId] of [["result", "other-run"], ["result", "run-1"], ["done", "run-1"]]) {
    act(() => socket.message({ type: "chat_event", channel: "session:s", payload: { type, content: "", metadata: { run_id: runId, model: "safe-model", total_cost_usd: 0.1 } } }))
  }
  expect(hook.result.current.turns).toHaveLength(1)
  expect(hook.result.current.turns[0].id).toBe("turn-1")
  expect(hook.result.current.turns[0].isStreaming).toBe(true)
  expect(hook.result.current.isStreaming).toBe(true)
  expect(hook.result.current.turns[0].parts.filter(part => part.type === "result")).toHaveLength(1)
  expect(hook.result.current.turns[0].parts.find(part => part.type === "result")?.metadata).toMatchObject({ run_id: "run-1", model: "safe-model", total_cost_usd: 0.1 })
})

it("accepts a covering authoritative checkpoint without inventing a session watermark", () => {
  const hook = setup()
  act(() => socket.connect())
  act(() => socket.message({ type: "run_begin", channel: "session:s", seq: 400 }))
  outer(401, envelope(1, "A"))
  outer(403, envelope(3, "C"))
  act(() => socket.message({ type: "resume_reset", channel: "session:s" }))
  act(() => hook.result.current.loadHistory([{ id: "turn-1", role: "assistant", runId: "run-1", content: "ABC", timestamp: new Date(), checkpoint: { version: 1, run_id: "run-1", turn_id: "turn-1", through_seq: 3, turn_state: "running" }, parts: [{ id: "part-a", type: "text", content: "ABC" }] }]))
  expect(hook.result.current.turns[0].streamIncomplete).toBe(false)
  expect(hook.result.current.turns[0].parts[0].content).toBe("ABC")
  act(() => socket.connect())
  expect(resumeCursor()).toBe(401)
})

it("folds safe pre-event init and result into the one stable canonical turn", () => {
  const hook = setup()
  const control = (type: string, extra: Record<string, unknown>) => act(() => socket.message({ type: "chat_event", channel: "session:s", payload: { type, content: "", metadata: { event_contract_version: 1, run_id: "run-1", turn_id: "turn-1", ...extra } } }))
  control("system", { subtype: "init", model: "safe-model" })
  expect(hook.result.current.turns).toHaveLength(0)
  send(envelope(1, "answer"))
  control("result", { total_cost_usd: 0.1, usage: { input_tokens: 12, output_tokens: 3 } })
  expect(hook.result.current.turns).toHaveLength(1)
  expect(hook.result.current.turns[0].isStreaming).toBe(true)
  expect(hook.result.current.turns[0].id).toBe("turn-1")
  expect(hook.result.current.turns[0].parts.find(part => part.type === "system_init")?.metadata).toMatchObject({ model: "safe-model", run_id: "run-1" })
  expect(hook.result.current.turns[0].parts.find(part => part.type === "result")?.metadata).toMatchObject({ total_cost_usd: 0.1, usage: { input_tokens: 12, output_tokens: 3 } })
  send({ ...envelope(2), type: "turn", payload: { turn: { state: "completed" } } })
  expect(hook.result.current.turns).toHaveLength(1)
  expect(hook.result.current.turns[0].isStreaming).toBe(false)
  expect(hook.result.current.isStreaming).toBe(false)
})

it("folds pending init over authoritative history and clears it on principal change", () => {
  const hook = renderHook(({ human }) => useChat({ sessionId: "s", currentUserId: human, workspaceId: "w", wsUrl: "ws://test", getToken: async () => null }), { initialProps: { human: "a" } })
  act(() => hook.result.current.loadHistory([]))
  const init = () => act(() => socket.message({ type: "chat_event", channel: "session:s", payload: { type: "system", content: "", metadata: { event_contract_version: 1, run_id: "run-1", turn_id: "turn-1", subtype: "init", model: "safe-model" } } }))
  init()
  act(() => hook.result.current.loadHistory([{ id: "turn-1", role: "assistant", runId: "run-1", content: "answer", timestamp: new Date(), parts: [{ id: "part-a", type: "text", content: "answer" }], checkpoint: { version: 1, run_id: "run-1", turn_id: "turn-1", through_seq: 1, turn_state: "running" } }]))
  expect(hook.result.current.turns).toHaveLength(1)
  expect(hook.result.current.turns[0].parts.find(part => part.type === "system_init")?.metadata?.model).toBe("safe-model")
  init()
  hook.rerender({ human: "b" })
  act(() => hook.result.current.loadHistory([]))
  send(envelope(1, "new authority"))
  expect(hook.result.current.turns[0].parts.some(part => part.type === "system_init")).toBe(false)
})

describe("conversation scope isolation", () => {
  it("masks the previous conversation during the first render of a new session", () => {
    const frames: { session: string; text: string }[] = []
    const hook = renderHook(({ session }) => {
      const chat = useChat({ sessionId: session, currentUserId: "human", workspaceId: "workspace", wsUrl: "ws://test", getToken: async () => null })
      frames.push({ session, text: chat.turns.flatMap(turn => turn.parts.map(part => part.content)).join("") })
      return chat
    }, { initialProps: { session: "s" } })
    act(() => hook.result.current.loadHistory([]))
    send(envelope(1, "Private conversation A"))
    expect(hook.result.current.turns[0].parts[0].content).toBe("Private conversation A")
    const oldMessage = socket.message
    hook.rerender({ session: "other" })
    expect(frames.find(frame => frame.session === "other")?.text).toBe("")
    act(() => oldMessage({ type: "chat_event", channel: "session:s", payload: { event: envelope(2, "late private A") } }))
    expect(hook.result.current.turns).toEqual([])
  })
})

it("does not regain a revoked human's authority by switching conversations", () => {
  const hook = renderHook(({ session }) => useChat({ sessionId: session, currentUserId: "human", workspaceId: "workspace", wsUrl: "ws://test", getToken: async () => null }), { initialProps: { session: "s" } })
  act(() => hook.result.current.loadHistory([]))
  send(envelope(1, "Private conversation A"))
  act(() => window.dispatchEvent(new CustomEvent("auth:session-expired")))
  hook.rerender({ session: "other" })
  act(() => hook.result.current.loadHistory([{ id: "denied", role: "assistant", content: "Must remain hidden", timestamp: new Date() }]))
  expect(hook.result.current.turns).toEqual([])
  let sent: unknown
  act(() => { sent = hook.result.current.sendMessage("Must not dispatch") })
  expect(sent).toBe(false)
})

it("clears a denied conversation and rejects late data without denying another chat", () => {
  const hook = renderHook(({ session }) => useChat({ sessionId: session, currentUserId: "human", workspaceId: "workspace", wsUrl: "ws://test", getToken: async () => null }), { initialProps: { session: "s" } })
  act(() => hook.result.current.loadHistory([]))
  send(envelope(1, "Private denied conversation"))
  const oldLoader = hook.result.current.loadHistory
  const oldMessage = socket.message
  act(() => hook.result.current.markHistoryDenied())
  expect(hook.result.current.turns).toEqual([])
  expect(hook.result.current.isStreaming).toBe(false)
  act(() => oldLoader([{ id: "late", role: "assistant", content: "Private late history", timestamp: new Date() }]))
  act(() => oldMessage({ type: "chat_event", channel: "session:s", payload: { event: envelope(2, "Private late event") } }))
  expect(hook.result.current.turns).toEqual([])
  const oldDeny = hook.result.current.markHistoryDenied
  hook.rerender({ session: "readable" })
  act(() => oldDeny())
  act(() => hook.result.current.loadHistory([{ id: "allowed", role: "assistant", content: "Authorized other chat", timestamp: new Date() }]))
  expect(hook.result.current.turns[0].parts[0].content).toBe("Authorized other chat")
})


it("exposes sticky conversation denial without denying a new chat or principal", () => {
  const hook = renderHook(({ session, human, workspace }) => useChat({ sessionId: session, currentUserId: human, workspaceId: workspace, wsUrl: "ws://test", getToken: async () => null }), { initialProps: { session: "s", human: "human", workspace: "workspace" } })
  expect(hook.result.current.historyDenied).toBe(false)
  act(() => hook.result.current.markHistoryDenied())
  expect(hook.result.current.historyDenied).toBe(true)
  act(() => hook.result.current.loadHistory([]))
  act(() => hook.result.current.markHistoryUnavailable())
  expect(hook.result.current.historyDenied).toBe(true)
  const oldDeny = hook.result.current.markHistoryDenied
  hook.rerender({ session: "other", human: "human", workspace: "workspace" })
  expect(hook.result.current.historyDenied).toBe(false)
  act(() => oldDeny())
  expect(hook.result.current.historyDenied).toBe(false)
  act(() => hook.result.current.markHistoryDenied())
  hook.rerender({ session: "other", human: "other-human", workspace: "workspace" })
  expect(hook.result.current.historyDenied).toBe(false)
})

it("exposes principal denial across chats and clears it only for another principal scope", () => {
  const hook = renderHook(({ session, human, workspace }) => useChat({ sessionId: session, currentUserId: human, workspaceId: workspace, wsUrl: "ws://test", getToken: async () => null }), { initialProps: { session: "s", human: "human", workspace: "workspace" } })
  act(() => window.dispatchEvent(new CustomEvent("auth:session-expired")))
  expect(hook.result.current.historyDenied).toBe(true)
  hook.rerender({ session: "other", human: "human", workspace: "workspace" })
  expect(hook.result.current.historyDenied).toBe(true)
  hook.rerender({ session: "other", human: "human", workspace: "other-workspace" })
  expect(hook.result.current.historyDenied).toBe(false)
})
