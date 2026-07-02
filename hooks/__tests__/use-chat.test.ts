import { describe, it, expect, vi, beforeEach } from "vitest"

// Mock useWebSocket to avoid real WebSocket connections
const mockSend = vi.fn()
const mockStatus = { current: "connected" as string }

interface UseWebSocketArgs {
  onMessage?: (msg: unknown) => void
}

vi.mock("@/hooks/use-websocket", () => ({
  useWebSocket: vi.fn(({ onMessage }: UseWebSocketArgs) => {
    // Expose onMessage for testing
    if (onMessage) {
      ;(globalThis as Record<string, unknown>).__testOnMessage = onMessage
    }
    return {
      status: mockStatus.current,
      send: mockSend,
      disconnect: vi.fn(),
      reconnect: vi.fn(),
    }
  }),
}))

// Must mock crypto.randomUUID for test environment
vi.stubGlobal("crypto", {
  randomUUID: () => "test-uuid-" + Math.random().toString(36).slice(2, 8),
})

import { renderHook, act } from "@testing-library/react"
import { useChat } from "@/hooks/use-chat"

describe("useChat", () => {
  // useChat now batches streamed text tokens into one commit per
  // animation frame. Capture scheduled frames so tests can flush them
  // deterministically; non-text events still commit text synchronously
  // (the hook flushes before handling them), so only text-only
  // assertions need an explicit flushFrames().
  let rafQueue: Array<FrameRequestCallback | undefined>
  beforeEach(() => {
    vi.clearAllMocks()
    mockStatus.current = "connected"
    rafQueue = []
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      rafQueue.push(cb)
      return rafQueue.length
    })
    vi.stubGlobal("cancelAnimationFrame", (id: number) => {
      rafQueue[id - 1] = undefined
    })
  })

  function getOnMessage(): (msg: unknown) => void {
    return (globalThis as Record<string, unknown>).__testOnMessage as (msg: unknown) => void
  }

  function flushFrames(): void {
    act(() => {
      const pending = rafQueue
      rafQueue = []
      for (const cb of pending) cb?.(0)
    })
  }

  it("starts with empty turns and not streaming", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    expect(result.current.turns).toHaveLength(0)
    expect(result.current.messages).toHaveLength(0)
    expect(result.current.isStreaming).toBe(false)
  })

  it("sendMessage adds user turn and calls ws send", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )

    act(() => {
      result.current.sendMessage("hello")
    })

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("user")
    expect(result.current.turns[0].parts[0].content).toBe("hello")
    expect(result.current.isStreaming).toBe(true)
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({ type: "send_message" }),
    )
  })

  it("ignores empty messages", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )

    // Ignore the subscribe/resume the hook sends on mount; assert the empty
    // sendMessage itself sends nothing.
    mockSend.mockClear()
    act(() => {
      result.current.sendMessage("")
    })

    expect(result.current.turns).toHaveLength(0)
    expect(mockSend).not.toHaveBeenCalled()
  })

  it("groups text events into single assistant turn", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "Hello " },
      })
    })

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "world" },
      })
    })
    flushFrames()

    // Should be ONE assistant turn with ONE text part (accumulated)
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("assistant")
    expect(result.current.turns[0].parts).toHaveLength(1)
    expect(result.current.turns[0].parts[0].type).toBe("text")
    expect(result.current.turns[0].parts[0].content).toBe("Hello world")
    expect(result.current.turns[0].isStreaming).toBe(true)
  })

  it("groups thinking + text into one assistant turn", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    // First: thinking event
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "thinking", content: "Let me analyze..." },
      })
    })

    // Then: text event
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "Here is the answer" },
      })
    })
    flushFrames()

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("assistant")
    expect(result.current.turns[0].parts).toHaveLength(2)
    expect(result.current.turns[0].parts[0].type).toBe("thinking")
    expect(result.current.turns[0].parts[0].content).toBe("Let me analyze...")
    expect(result.current.turns[0].parts[1].type).toBe("text")
    expect(result.current.turns[0].parts[1].content).toBe("Here is the answer")
  })

  // Regression: the CLAUDE_CODE/haiku adapter chunks a single reasoning pass
  // into many `thinking` events that do NOT carry metadata.streaming. Each one
  // must accumulate into ONE thinking part — not spawn its own "Thought for a
  // few seconds" card. (Live UI showed 7 blocks chopping one sentence apart.)
  it("accumulates consecutive thinking chunks into a single thinking part", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    for (const content of ["The user ", "is asking ", "in Czech ", "what to do."]) {
      act(() => {
        onMessage({
          type: "chat_event",
          channel: "session:s1",
          payload: { type: "thinking", content },
        })
      })
    }

    expect(result.current.turns).toHaveLength(1)
    const thinkingParts = result.current.turns[0].parts.filter((p) => p.type === "thinking")
    expect(thinkingParts).toHaveLength(1)
    expect(thinkingParts[0].content).toBe("The user is asking in Czech what to do.")
  })

  // Regression: text arriving AFTER a tool_result must open a NEW text part in
  // its correct position, not merge back into the pre-tool text bubble. The old
  // findLastIndex(text && isStreaming) matched the first (never-finalized)
  // streaming text part, so the final answer got crammed into the bubble ABOVE
  // the tool call and nothing rendered after it — the agent "looked at the tool
  // and stopped" with no reply.
  it("keeps text after a tool_result as its own part (final answer not lost)", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()
    const emit = (payload: Record<string, unknown>) =>
      act(() => onMessage({ type: "chat_event", channel: "session:s1", payload }))

    emit({ type: "thinking", content: "Let me check the calendar." })
    emit({ type: "text", content: "Checking your calendar." })
    emit({ type: "tool_call", content: "GOOGLECALENDAR_EVENTS_LIST", metadata: { tool_name: "GOOGLECALENDAR_EVENTS_LIST", tool_id: "t1" } })
    emit({ type: "tool_result", content: "{}", metadata: { tool_use_id: "t1" } })
    emit({ type: "text", content: "Your calendar is empty." })
    flushFrames()

    expect(result.current.turns).toHaveLength(1)
    const parts = result.current.turns[0].parts
    expect(parts.map((p) => p.type)).toEqual([
      "thinking",
      "text",
      "tool_call",
      "tool_result",
      "text",
    ])
    // The pre-tool bubble must NOT swallow the final answer.
    expect(parts[1].content).toBe("Checking your calendar.")
    // The final answer is its own part, after the tool result.
    expect(parts[4].content).toBe("Your calendar is empty.")
  })

  // --- Resumable-stream reassembly (seq ordering + dedup + resume) ---------

  it("reassembles seq'd events in order, deduping and reordering", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()
    const emit = (m: Record<string, unknown>) =>
      act(() => onMessage({ channel: "session:s1", ...m }))

    // History present → the reassembler is allowed to apply live events.
    act(() => result.current.loadHistory([]))

    emit({ type: "run_begin", seq: 1, payload: { from_seq: 0 } })
    emit({ type: "chat_event", seq: 3, payload: { type: "text", content: "B" } }) // out of order
    emit({ type: "chat_event", seq: 2, payload: { type: "text", content: "A" } })
    emit({ type: "chat_event", seq: 2, payload: { type: "text", content: "A" } }) // duplicate
    flushFrames()

    expect(result.current.turns).toHaveLength(1)
    const textParts = result.current.turns[0].parts.filter((p) => p.type === "text")
    expect(textParts).toHaveLength(1)
    // Applied in seq order (2 then 3), duplicate dropped → "AB", not "BA"/"ABA".
    expect(textParts[0].content).toBe("AB")
  })

  it("rebases the seq baseline from run_begin when joining mid-counter", () => {
    // A fresh client on a chat whose channel already streamed earlier runs must
    // NOT wait forever for seq 1..50 — run_begin rebases it to the run's start.
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()
    const emit = (m: Record<string, unknown>) =>
      act(() => onMessage({ channel: "session:s1", ...m }))
    act(() => result.current.loadHistory([]))

    emit({ type: "run_begin", seq: 51, payload: { from_seq: 50 } })
    emit({ type: "chat_event", seq: 52, payload: { type: "text", content: "hi" } })
    flushFrames()

    const textParts = result.current.turns[0]?.parts.filter((p) => p.type === "text") ?? []
    expect(textParts).toHaveLength(1)
    expect(textParts[0].content).toBe("hi")
  })

  it("holds resumed events until history loads, then applies them on top", () => {
    // The mount race: resume/live events can arrive before the history fetch
    // resolves. They must be buffered, not dropped, and must not be clobbered
    // when loadHistory replaces the turns.
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()
    const emit = (m: Record<string, unknown>) =>
      act(() => onMessage({ channel: "session:s1", ...m }))

    // Events arrive BEFORE history has loaded.
    emit({ type: "run_begin", seq: 1, payload: { from_seq: 0 } })
    emit({ type: "chat_event", seq: 2, payload: { type: "text", content: "still working" } })
    flushFrames()
    // Nothing rendered yet — held pending until history is the base.
    expect(result.current.turns).toHaveLength(0)

    // History resolves (empty for a mid-run return) → pending drains on top.
    act(() => result.current.loadHistory([]))
    flushFrames()
    const textParts = result.current.turns[0]?.parts.filter((p) => p.type === "text") ?? []
    expect(textParts).toHaveLength(1)
    expect(textParts[0].content).toBe("still working")
  })

  it("finalizes an open thinking block when the turn errors", () => {
    // Complete thinking blocks now stay isStreaming until a later event or done.
    // An error must finalize them, else the Thought card spins forever on a turn
    // that has actually failed.
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()
    const emit = (payload: Record<string, unknown>) =>
      act(() => onMessage({ type: "chat_event", channel: "session:s1", payload }))

    emit({ type: "thinking", content: "let me think" })
    emit({ type: "error", content: "boom" })

    const turn = result.current.turns[0]
    const thinking = turn.parts.find((p) => p.type === "thinking")
    expect(thinking?.isStreaming).toBe(false)
    expect(turn.isStreaming).toBe(false)
  })

  it("opens the streaming gate via markHistoryUnavailable when history load fails", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()
    const emit = (m: Record<string, unknown>) =>
      act(() => onMessage({ channel: "session:s1", ...m }))

    emit({ type: "run_begin", seq: 1, payload: { from_seq: 0 } })
    emit({ type: "chat_event", seq: 2, payload: { type: "text", content: "hi" } })
    flushFrames()
    expect(result.current.turns).toHaveLength(0) // gated: history hasn't settled

    // History fetch failed outright → gate must still open so the stream renders.
    act(() => result.current.markHistoryUnavailable())
    flushFrames()
    const textParts = result.current.turns[0]?.parts.filter((p) => p.type === "text") ?? []
    expect(textParts[0]?.content).toBe("hi")
  })

  it("resyncs to the live tail after resume_reset (truncated buffer)", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()
    const emit = (m: Record<string, unknown>) =>
      act(() => onMessage({ channel: "session:s1", ...m }))

    act(() => result.current.loadHistory([]))
    emit({ type: "run_begin", seq: 1, payload: { from_seq: 0 } })
    emit({ type: "chat_event", seq: 2, payload: { type: "text", content: "start" } })
    flushFrames()

    // Server buffer overflowed → resume_reset; then history reload completes.
    emit({ type: "resume_reset" })
    act(() => result.current.loadHistory([]))

    // Live stream continues far beyond the dropped gap; adopt-next must let it
    // render instead of freezing waiting for the lost seq 3..99.
    emit({ type: "chat_event", seq: 100, payload: { type: "text", content: "tail" } })
    flushFrames()

    const textParts = result.current.turns[0]?.parts.filter((p) => p.type === "text") ?? []
    expect(textParts.map((p) => p.content).join("")).toContain("tail")
  })

  it("collapses repeated status events into a single live status line", () => {
    // Internal progress chatter (thinking_tokens, task_started, task_progress,
    // …) arrives as a burst of status events. They must NOT stack into a column
    // of rows — only one quiet status line is shown, reflecting the latest.
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    for (const content of ["Starting container...", "thinking_tokens", "task_started", "task_progress"]) {
      act(() => {
        onMessage({
          type: "chat_event",
          channel: "session:s1",
          payload: { type: "status", content },
        })
      })
    }

    const turn = result.current.turns[result.current.turns.length - 1]
    const statusParts = turn.parts.filter((p) => p.type === "status")
    expect(statusParts).toHaveLength(1)
    expect(statusParts[0].content).toBe("task_progress")
  })

  it("renders a broadcast user_message from another participant, attributed", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "user_message", content: "hi from Petr", metadata: { author_user_id: "u-petr" } },
      })
    })
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("user")
    expect(result.current.turns[0].parts[0].content).toBe("hi from Petr")
    expect(result.current.turns[0].authorUserId).toBe("u-petr")
  })

  it("drops the echo of the local user's OWN broadcast user_message", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1", currentUserId: "me" }),
    )
    const onMessage = getOnMessage()
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "user_message", content: "mine", metadata: { author_user_id: "me" } },
      })
    })
    // Own echo is dropped (we already rendered it optimistically) — no dup turn.
    expect(result.current.turns).toHaveLength(0)
  })

  it("merges consecutive complete thinking blocks into one part", () => {
    // The backend chunks one reasoning pass into several `thinking` events
    // (adapter deltas + scrubber tail flushes) with no metadata.streaming flag.
    // Consecutive chunks must fold into a single card — not stack into a column
    // of "Thought for a few seconds" blocks that chop the sentence apart.
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    for (const content of ["First thinking block. ", "Second thinking block."]) {
      act(() => {
        onMessage({
          type: "chat_event",
          channel: "session:s1",
          payload: { type: "thinking", content },
        })
      })
    }

    // ONE turn, ONE merged thinking part.
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts).toHaveLength(1)
    expect(result.current.turns[0].parts[0].type).toBe("thinking")
    expect(result.current.turns[0].parts[0].content).toBe(
      "First thinking block. Second thinking block.",
    )
  })

  it("accumulates streaming thinking deltas into one part", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    // Streaming delta (metadata.streaming = true)
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "thinking", content: "analyzing", metadata: { streaming: true } },
      })
    })

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "thinking", content: " the code", metadata: { streaming: true } },
      })
    })

    // Should be ONE part with accumulated content
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts).toHaveLength(1)
    expect(result.current.turns[0].parts[0].content).toBe("analyzing the code")
  })

  it("handles tool_call + tool_result parts in one turn", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "tool_call", content: "Read", metadata: { tool_name: "Read", tool_id: "t1" } },
      })
    })

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "tool_result", content: "file contents", metadata: { tool_use_id: "t1" } },
      })
    })

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts).toHaveLength(2)
    expect(result.current.turns[0].parts[0].type).toBe("tool_call")
    expect(result.current.turns[0].parts[1].type).toBe("tool_result")
  })

  it("status events appear before text", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "status", content: "Starting container..." },
      })
    })

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts[0].type).toBe("status")
    expect(result.current.turns[0].parts[0].content).toBe("Starting container...")

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "Response" },
      })
    })
    flushFrames()

    // Status part is removed when text arrives (transient indicator)
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts).toHaveLength(1)
    expect(result.current.turns[0].parts[0].type).toBe("text")
  })

  it("done event marks turn as not streaming and removes status parts", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      result.current.sendMessage("hello")
    })

    act(() => {
      onMessage({ type: "chat_event", channel: "session:s1", payload: { type: "status", content: "Setting up..." } })
    })

    act(() => {
      onMessage({ type: "chat_event", channel: "session:s1", payload: { type: "text", content: "response" } })
    })

    act(() => {
      onMessage({ type: "chat_event", channel: "session:s1", payload: { type: "done" } })
    })

    expect(result.current.isStreaming).toBe(false)
    const assistantTurn = result.current.turns[result.current.turns.length - 1]
    expect(assistantTurn.isStreaming).toBe(false)
    // Status parts should be removed after done
    const statusParts = assistantTurn.parts.filter((p) => p.type === "status")
    expect(statusParts).toHaveLength(0)
  })

  it("handles error event", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "error", content: "Something went wrong" },
      })
    })

    // Error creates a system turn
    const lastTurn = result.current.turns[result.current.turns.length - 1]
    expect(lastTurn).toBeDefined()
    expect(lastTurn.parts[0].type).toBe("error")
    expect(lastTurn.parts[0].content).toBe("Something went wrong")
  })

  it("ignores events for different session", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s2",
        payload: { type: "text", content: "wrong session" },
      })
    })

    expect(result.current.turns).toHaveLength(0)
  })

  it("stopGeneration sends cancel_message", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )

    act(() => {
      result.current.stopGeneration()
    })

    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({ type: "cancel_message" }),
    )
  })

  it("stopGeneration clears part-level isStreaming flags on the open turn", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    // Open an assistant turn with a streaming text part.
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "Hello" },
      })
    })
    flushFrames()
    expect(result.current.turns[0].isStreaming).toBe(true)
    expect(result.current.turns[0].parts[0].isStreaming).toBe(true)

    act(() => {
      result.current.stopGeneration()
    })

    // Turn AND every streaming part are flipped off in one update.
    expect(result.current.turns[0].isStreaming).toBe(false)
    expect(result.current.turns[0].parts[0].isStreaming).toBe(false)
  })

  it("stopGeneration drops late deltas so cancelled stream cannot resurrect", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    // Open an assistant turn.
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "Hello" },
      })
    })
    flushFrames()

    act(() => {
      result.current.stopGeneration()
    })

    // After cancel, late deltas race against the server's cancel ack.
    // They must NOT extend the cancelled turn or create a new one.
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: " — late delta" },
      })
    })

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts).toHaveLength(1)
    expect(result.current.turns[0].parts[0].content).toBe("Hello")
    expect(result.current.turns[0].isStreaming).toBe(false)

    // sendMessage clears the cancelled gate so the next stream flows again.
    act(() => {
      result.current.sendMessage("again")
    })
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "fresh reply" },
      })
    })
    flushFrames()
    const lastTurn = result.current.turns[result.current.turns.length - 1]
    expect(lastTurn.role).toBe("assistant")
    expect(lastTurn.parts[0].content).toBe("fresh reply")
  })

  it("loadHistory converts flat messages to turns", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )

    act(() => {
      result.current.loadHistory([
        { id: "1", role: "user", content: "hello", timestamp: new Date() },
        { id: "2", role: "assistant", content: "hi there", timestamp: new Date() },
      ])
    })

    expect(result.current.turns).toHaveLength(2)
    expect(result.current.turns[0].role).toBe("user")
    expect(result.current.turns[1].role).toBe("assistant")
    // flat messages should also work
    expect(result.current.messages).toHaveLength(2)
  })
})
