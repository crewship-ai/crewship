import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

// Capture the onMessage handler useChat registers so tests can drive
// stream events directly; stub the socket so nothing touches the network.
let captured: ((msg: unknown) => void) | undefined
const mockSend = vi.fn()
vi.mock("@/hooks/use-websocket", () => ({
  useWebSocket: (opts: { onMessage?: (m: unknown) => void }) => {
    captured = opts.onMessage
    return { status: "connected", send: mockSend }
  },
}))

import { useChat, assignmentField, type ChatMessage } from "@/hooks/use-chat"

const SESSION = "s1"

function chatEvent(
  type: string,
  content = "",
  metadata?: Record<string, unknown>,
  session = SESSION,
) {
  return {
    type: "chat_event",
    channel: `session:${session}`,
    payload: { type, content, metadata },
  }
}

let rafCbs: Array<FrameRequestCallback | undefined>

beforeEach(() => {
  vi.clearAllMocks()
  rafCbs = []
  vi.stubGlobal("crypto", {
    randomUUID: () => "uuid-" + Math.random().toString(36).slice(2, 10),
  })
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    rafCbs.push(cb)
    return rafCbs.length
  })
  vi.stubGlobal("cancelAnimationFrame", (id: number) => {
    rafCbs[id - 1] = undefined
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  captured = undefined
})

function dispatch(msg: unknown) {
  act(() => {
    captured?.(msg)
  })
}

function flushFrames() {
  act(() => {
    const pending = rafCbs.slice()
    rafCbs = []
    for (const cb of pending) cb?.(0)
  })
}

function setup(sessionId = SESSION) {
  return renderHook(
    (props: { sessionId: string }) =>
      useChat({ wsUrl: "ws://x/ws", getToken: async () => "tok", sessionId: props.sessionId }),
    { initialProps: { sessionId } },
  )
}

describe("uuid fallback (no crypto.randomUUID)", () => {
  it("generates a v4-shaped uuid via Math.random when crypto.randomUUID is unavailable", () => {
    vi.stubGlobal("crypto", {}) // typeof crypto.randomUUID !== "function"
    const { result } = setup()
    act(() => {
      result.current.sendMessage("hi")
    })
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].id).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    )
  })
})

describe("assignmentField remaining branches", () => {
  it("stringifies non-object primitives that are not string/number/boolean", () => {
    expect(assignmentField(BigInt(42))).toBe("42")
  })

  it("returns empty string for an object without slug/name/id", () => {
    expect(assignmentField({ foo: "bar" })).toBe("")
  })

  it("prefers slug over name over id and accepts each fallback", () => {
    expect(assignmentField({ slug: "viktor", name: "Viktor", id: "a1" })).toBe("viktor")
    expect(assignmentField({ name: "Viktor", id: "a1" })).toBe("Viktor")
    expect(assignmentField({ id: "a1" })).toBe("a1")
  })
})

describe("loadHistory conversions", () => {
  it("converts system history messages, mapping eventType error to an error part", () => {
    const { result } = setup()
    const ts = new Date()
    act(() => {
      result.current.loadHistory([
        { id: "1", role: "system", content: "boom", eventType: "error", timestamp: ts },
        { id: "2", role: "system", content: "fyi", timestamp: ts },
      ] as ChatMessage[])
    })
    expect(result.current.turns).toHaveLength(2)
    expect(result.current.turns[0].role).toBe("system")
    expect(result.current.turns[0].parts[0].type).toBe("error")
    expect(result.current.turns[1].parts[0].type).toBe("text")
    expect(result.current.turns[1].parts[0].content).toBe("fyi")
  })

  it("groups consecutive assistant/tool history messages into one turn with typed parts", () => {
    const { result } = setup()
    const ts = new Date()
    act(() => {
      result.current.loadHistory([
        { id: "1", role: "assistant", content: "thinking...", eventType: "thinking", timestamp: ts },
        { id: "2", role: "tool", content: "Read", eventType: "tool_call", timestamp: ts },
        { id: "3", role: "tool", content: "data", eventType: "tool_result", timestamp: ts },
        { id: "4", role: "assistant", content: "answer", timestamp: ts },
      ] as ChatMessage[])
    })
    expect(result.current.turns).toHaveLength(1)
    const parts = result.current.turns[0].parts
    expect(parts.map((p) => p.type)).toEqual(["thinking", "tool_call", "tool_result", "text"])
    expect(parts[3].content).toBe("answer")
    // Derived flat messages map tool parts back to the tool role.
    const roles = result.current.messages.map((m) => m.role)
    expect(roles).toEqual(["assistant", "tool", "tool", "assistant"])
  })
})

describe("assignment lifecycle events", () => {
  it("renders a system turn for each assignment event type", () => {
    const { result } = setup()
    dispatch({ type: "assignment_created", channel: `session:${SESSION}`, payload: { target: { slug: "viktor" }, task: "do x" } })
    dispatch({ type: "assignment_running", channel: `session:${SESSION}`, payload: { target: "viktor" } })
    dispatch({ type: "assignment_completed", channel: `session:${SESSION}`, payload: { target: "viktor", result: "done!" } })
    dispatch({ type: "assignment_failed", channel: `session:${SESSION}`, payload: { target: "viktor", error: "kaput" } })

    expect(result.current.turns).toHaveLength(4)
    expect(result.current.turns.every((t) => t.role === "system")).toBe(true)
    expect(result.current.turns[0].parts[0].content).toBe("[Assignment] Assigning task to @viktor: do x")
    expect(result.current.turns[1].parts[0].content).toBe("[Assignment] @viktor is working on the task...")
    expect(result.current.turns[2].parts[0].content).toBe("[Assignment] @viktor completed the task.\nResult: done!")
    expect(result.current.turns[3].parts[0].content).toBe("[Assignment] @viktor failed: kaput")
  })

  it("omits the Result suffix when completed carries no result", () => {
    const { result } = setup()
    dispatch({ type: "assignment_completed", channel: `session:${SESSION}`, payload: { target: "viktor" } })
    expect(result.current.turns[0].parts[0].content).toBe("[Assignment] @viktor completed the task.")
  })

  it("ignores assignment events addressed to a different session", () => {
    const { result } = setup()
    dispatch({ type: "assignment_created", channel: "session:other", payload: { target: "v" } })
    expect(result.current.turns).toHaveLength(0)
  })

  it("treats a non-object payload as empty", () => {
    const { result } = setup()
    dispatch({ type: "assignment_running", channel: `session:${SESSION}`, payload: "bogus" })
    expect(result.current.turns[0].parts[0].content).toBe("[Assignment] @ is working on the task...")
  })

  it("flushes buffered stream text before the assignment turn so order is preserved", () => {
    const { result } = setup()
    dispatch(chatEvent("text", "partial")) // buffered, frame not yet flushed
    dispatch({ type: "assignment_created", channel: `session:${SESSION}`, payload: { target: "v", task: "t" } })
    expect(result.current.turns).toHaveLength(2)
    expect(result.current.turns[0].role).toBe("assistant")
    expect(result.current.turns[0].parts[0].content).toBe("partial")
    expect(result.current.turns[1].role).toBe("system")
  })
})

describe("non-chat events", () => {
  it("ignores unrelated websocket message types", () => {
    const { result } = setup()
    dispatch({ type: "pong" })
    dispatch({ type: "workspace_event", payload: { type: "text", content: "x" } })
    expect(result.current.turns).toHaveLength(0)
  })
})

describe("status events", () => {
  it("appends a status part to an already-streaming assistant turn", () => {
    const { result } = setup()
    dispatch(chatEvent("text", "working"))
    flushFrames()
    dispatch(chatEvent("status", "Running tests..."))
    expect(result.current.turns).toHaveLength(1)
    const parts = result.current.turns[0].parts
    expect(parts.map((p) => p.type)).toEqual(["text", "status"])
    expect(parts[1].content).toBe("Running tests...")
  })
})

describe("thinking events", () => {
  it("first streaming thinking delta replaces status parts inside the open turn", () => {
    const { result } = setup()
    dispatch(chatEvent("status", "Starting..."))
    dispatch(chatEvent("thinking", "hmm", { streaming: true }))
    expect(result.current.turns).toHaveLength(1)
    const parts = result.current.turns[0].parts
    expect(parts).toHaveLength(1)
    expect(parts[0].type).toBe("thinking")
    expect(parts[0].isStreaming).toBe(true)
    expect(parts[0].content).toBe("hmm")
  })

  it("complete thinking block after status removes the status parts", () => {
    const { result } = setup()
    dispatch(chatEvent("status", "Starting..."))
    dispatch(chatEvent("thinking", "full block"))
    const parts = result.current.turns[0].parts
    expect(parts).toHaveLength(1)
    expect(parts[0].type).toBe("thinking")
    expect(parts[0].isStreaming).toBe(false)
  })

  it("creating a new thinking turn drops orphaned status-only assistant turns", () => {
    const { result } = setup()
    dispatch(chatEvent("status", "Starting...")) // status-only streaming turn
    // An assignment system turn lands so the status turn is no longer last.
    dispatch({ type: "assignment_running", channel: `session:${SESSION}`, payload: { target: "v" } })
    dispatch(chatEvent("thinking", "fresh", { streaming: true }))
    // Orphaned status-only turn removed; system + new assistant turn remain.
    expect(result.current.turns.map((t) => t.role)).toEqual(["system", "assistant"])
    expect(result.current.turns[1].parts[0].type).toBe("thinking")
    expect(result.current.turns[1].parts[0].content).toBe("fresh")
  })
})

describe("text flush edge cases", () => {
  it("accumulates a second flushed text burst into the existing streaming text part", () => {
    const { result } = setup()
    dispatch(chatEvent("text", "Hello "))
    flushFrames()
    dispatch(chatEvent("text", "world"))
    flushFrames()
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts).toHaveLength(1)
    expect(result.current.turns[0].parts[0].content).toBe("Hello world")
  })

  it("creating a new text turn drops orphaned status-only assistant turns", () => {
    const { result } = setup()
    dispatch(chatEvent("status", "Booting..."))
    dispatch({ type: "assignment_running", channel: `session:${SESSION}`, payload: { target: "v" } })
    dispatch(chatEvent("text", "answer"))
    flushFrames()
    expect(result.current.turns.map((t) => t.role)).toEqual(["system", "assistant"])
    expect(result.current.turns[1].parts[0].content).toBe("answer")
  })

  it("falls back to a synchronous flush when requestAnimationFrame is unavailable", () => {
    vi.stubGlobal("requestAnimationFrame", undefined)
    const { result } = setup()
    dispatch(chatEvent("text", "sync"))
    // No frame to flush — the commit already happened synchronously.
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts[0].content).toBe("sync")
  })
})

describe("tool events", () => {
  it("appends tool_call to an already-streaming assistant turn", () => {
    const { result } = setup()
    dispatch(chatEvent("text", "let me check"))
    flushFrames()
    dispatch(chatEvent("tool_call", "Read", { tool_id: "t1" }))
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts.map((p) => p.type)).toEqual(["text", "tool_call"])
  })

  it("tool_result without a streaming turn opens a new assistant turn", () => {
    const { result } = setup()
    dispatch(chatEvent("tool_result", "output", { tool_use_id: "t9" }))
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("assistant")
    expect(result.current.turns[0].isStreaming).toBe(true)
    expect(result.current.turns[0].parts[0].type).toBe("tool_result")
  })

  it("tool_result only marks the matching tool_call completed, leaving others untouched", () => {
    const { result } = setup()
    dispatch(chatEvent("tool_call", "Read", { tool_id: "t1" }))
    dispatch(chatEvent("tool_call", "Bash", { tool_id: "t2" }))
    dispatch(chatEvent("tool_result", "ok", { tool_use_id: "t2" }))
    const parts = result.current.turns[0].parts
    expect(parts[0].metadata?.completed).toBeUndefined()
    expect(parts[1].metadata?.completed).toBe(true)
    expect(parts[2].type).toBe("tool_result")
  })

  it("tool_result without tool_use_id leaves all tool_call parts unchanged", () => {
    const { result } = setup()
    dispatch(chatEvent("tool_call", "Read", { tool_id: "t1" }))
    dispatch(chatEvent("tool_result", "ok"))
    const parts = result.current.turns[0].parts
    expect(parts[0].metadata?.completed).toBeUndefined()
    expect(parts[1].type).toBe("tool_result")
  })
})

describe("image events", () => {
  it("appends an image part to the streaming turn", () => {
    const { result } = setup()
    dispatch(chatEvent("text", "screenshot:"))
    flushFrames()
    dispatch(chatEvent("image", "data:image/png;base64,AAAA", { mime: "image/png" }))
    const parts = result.current.turns[0].parts
    expect(parts.map((p) => p.type)).toEqual(["text", "image"])
    expect(parts[1].metadata).toEqual({ mime: "image/png" })
  })

  it("opens a new assistant turn for an image when none is streaming", () => {
    const { result } = setup()
    dispatch(chatEvent("image", "data:image/png;base64,BBBB"))
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts[0].type).toBe("image")
    expect(result.current.turns[0].isStreaming).toBe(true)
  })
})

describe("result events", () => {
  it("appends a result part with cost metadata to the streaming turn", () => {
    const { result } = setup()
    dispatch(chatEvent("text", "answer"))
    flushFrames()
    dispatch(chatEvent("result", "done", { cost_usd: 0.01 }))
    const parts = result.current.turns[0].parts
    expect(parts[1].type).toBe("result")
    expect(parts[1].metadata).toEqual({ cost_usd: 0.01 })
  })

  it("opens a new turn for a result event and defaults empty content", () => {
    const { result } = setup()
    dispatch(chatEvent("result", ""))
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts[0].type).toBe("result")
    expect(result.current.turns[0].parts[0].content).toBe("")
  })
})

describe("system events", () => {
  it("init creates a system_init turn and removes the preceding status-only turn", () => {
    const { result } = setup()
    dispatch(chatEvent("status", "Starting container..."))
    dispatch(chatEvent("system", "session start", { subtype: "init", model: "m1" }))
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("system")
    expect(result.current.turns[0].parts[0].type).toBe("system_init")
    expect(result.current.turns[0].parts[0].content).toBe("session start")
  })

  it("only renders the init turn once per session", () => {
    const { result } = setup()
    dispatch(chatEvent("system", "", { subtype: "init" }))
    dispatch(chatEvent("system", "", { subtype: "init" }))
    expect(result.current.turns).toHaveLength(1)
    // Empty content falls back to the literal "init".
    expect(result.current.turns[0].parts[0].content).toBe("init")
  })

  it("non-init system events append status parts to the streaming turn", () => {
    const { result } = setup()
    dispatch(chatEvent("text", "hi"))
    flushFrames()
    dispatch(chatEvent("system", "keeper: allowed", { subtype: "security" }))
    const parts = result.current.turns[0].parts
    expect(parts[1].type).toBe("status")
    expect(parts[1].content).toBe("keeper: allowed")
  })

  it("non-init system events are dropped when no assistant turn is streaming", () => {
    const { result } = setup()
    dispatch(chatEvent("system", "orphan log", { subtype: "security" }))
    expect(result.current.turns).toHaveLength(0)
  })
})

describe("done event edge cases", () => {
  it("removes orphaned status-only turns entirely", () => {
    const { result } = setup()
    dispatch(chatEvent("status", "Starting..."))
    dispatch(chatEvent("done"))
    expect(result.current.turns).toHaveLength(0)
    expect(result.current.isStreaming).toBe(false)
  })

  it("lifts trace_id from done metadata onto the finalized turn", () => {
    const { result } = setup()
    dispatch(chatEvent("text", "answer"))
    flushFrames()
    dispatch(chatEvent("done", "", { trace_id: "tr-123" }))
    expect(result.current.turns[0].isStreaming).toBe(false)
    expect(result.current.turns[0].metadata?.trace_id).toBe("tr-123")
  })

  it("is a no-op on turn content when nothing is streaming", () => {
    const { result } = setup()
    act(() => {
      result.current.loadHistory([
        { id: "1", role: "user", content: "q", timestamp: new Date() },
      ] as ChatMessage[])
    })
    dispatch(chatEvent("done"))
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("user")
  })
})

describe("crew_provisioning events", () => {
  it("renders a system turn carrying the crew_id metadata and stops streaming", () => {
    const { result } = setup()
    act(() => {
      result.current.sendMessage("provision please")
    })
    expect(result.current.isStreaming).toBe(true)
    dispatch(chatEvent("crew_provisioning", "Building image", { crew_id: "crew-9" }))
    const last = result.current.turns[result.current.turns.length - 1]
    expect(last.role).toBe("system")
    expect(last.parts[0].type).toBe("crew_provisioning")
    expect(last.parts[0].content).toBe("Building image")
    expect(last.parts[0].metadata).toEqual({ crew_id: "crew-9" })
    expect(result.current.isStreaming).toBe(false)
  })

  it("falls back to the default building message when content is empty", () => {
    const { result } = setup()
    dispatch(chatEvent("crew_provisioning", ""))
    expect(result.current.turns[0].parts[0].content).toBe("Building crew image…")
  })
})

describe("error events", () => {
  it("appends the error part to a streaming assistant turn and closes it", () => {
    const { result } = setup()
    dispatch(chatEvent("text", "partial answer"))
    flushFrames()
    dispatch(chatEvent("error", "agent crashed"))
    expect(result.current.turns).toHaveLength(1)
    const turn = result.current.turns[0]
    expect(turn.isStreaming).toBe(false)
    expect(turn.parts.map((p) => p.type)).toEqual(["text", "error"])
    expect(result.current.isStreaming).toBe(false)
  })

  it("defaults the error message when content is empty", () => {
    const { result } = setup()
    dispatch(chatEvent("error", ""))
    expect(result.current.turns[0].parts[0].content).toBe("An error occurred")
  })
})

describe("regenerateLastTurn", () => {
  function seedConversation(result: { current: ReturnType<typeof useChat> }) {
    act(() => {
      result.current.sendMessage("question")
    })
    dispatch(chatEvent("text", "first answer"))
    flushFrames()
    dispatch(chatEvent("done"))
  }

  it("re-sends the last user message and truncates turns after it", () => {
    const { result } = setup()
    seedConversation(result)
    expect(result.current.turns).toHaveLength(2)
    mockSend.mockClear()

    act(() => {
      result.current.regenerateLastTurn()
    })

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("user")
    expect(result.current.isStreaming).toBe(true)
    expect(mockSend).toHaveBeenCalledWith({
      type: "send_message",
      payload: JSON.stringify({ session_id: SESSION, content: "question" }),
    })
  })

  it("does nothing while streaming", () => {
    const { result } = setup()
    act(() => {
      result.current.sendMessage("question")
    })
    mockSend.mockClear()
    act(() => {
      result.current.regenerateLastTurn()
    })
    expect(mockSend).not.toHaveBeenCalled()
  })

  it("does nothing when there is no user turn", () => {
    const { result } = setup()
    act(() => {
      result.current.regenerateLastTurn()
    })
    expect(mockSend).not.toHaveBeenCalled()
    expect(result.current.isStreaming).toBe(false)
  })
})

describe("editAndResend", () => {
  function seedConversation(result: { current: ReturnType<typeof useChat> }) {
    act(() => {
      result.current.sendMessage("original")
    })
    dispatch(chatEvent("text", "reply"))
    flushFrames()
    dispatch(chatEvent("done"))
  }

  it("replaces the user turn content, drops later turns, and re-sends", () => {
    const { result } = setup()
    seedConversation(result)
    const userTurnId = result.current.turns[0].id
    mockSend.mockClear()

    act(() => {
      result.current.editAndResend(userTurnId, "  edited question  ")
    })

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("user")
    expect(result.current.turns[0].parts[0].content).toBe("edited question")
    expect(result.current.isStreaming).toBe(true)
    expect(mockSend).toHaveBeenCalledWith({
      type: "send_message",
      payload: JSON.stringify({ session_id: SESSION, content: "edited question" }),
    })
  })

  it("ignores empty replacement content", () => {
    const { result } = setup()
    seedConversation(result)
    const userTurnId = result.current.turns[0].id
    mockSend.mockClear()
    act(() => {
      result.current.editAndResend(userTurnId, "   ")
    })
    expect(mockSend).not.toHaveBeenCalled()
    expect(result.current.turns).toHaveLength(2)
  })

  it("ignores unknown turn ids and non-user turns", () => {
    const { result } = setup()
    seedConversation(result)
    const assistantTurnId = result.current.turns[1].id
    mockSend.mockClear()

    act(() => {
      result.current.editAndResend("nope", "new")
    })
    act(() => {
      result.current.editAndResend(assistantTurnId, "new")
    })
    expect(mockSend).not.toHaveBeenCalled()
    expect(result.current.turns).toHaveLength(2)
  })
})

describe("buffered-frame cancellation", () => {
  it("stopGeneration drops buffered tokens and cancels the pending frame", () => {
    const { result } = setup()
    dispatch(chatEvent("text", "never rendered")) // buffered, frame pending
    act(() => {
      result.current.stopGeneration()
    })
    flushFrames() // cancelled callback must not commit anything
    expect(result.current.turns).toHaveLength(0)
    expect(mockSend).toHaveBeenCalledWith({
      type: "cancel_message",
      payload: JSON.stringify({ session_id: SESSION }),
    })
  })

  it("session change cancels the pending frame so old-session text never commits", () => {
    const { result, rerender } = setup()
    dispatch(chatEvent("text", "old session text")) // buffered for s1
    rerender({ sessionId: "s2" })
    flushFrames()
    expect(result.current.turns).toHaveLength(0)
    expect(result.current.isStreaming).toBe(false)
  })
})
