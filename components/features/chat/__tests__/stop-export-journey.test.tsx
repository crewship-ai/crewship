import React, { useEffect, useState } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const socket = vi.hoisted(() => ({ message: (_value: unknown) => {}, send: vi.fn() }))
vi.mock("@/hooks/use-websocket", async importOriginal => ({
  ...await importOriginal<typeof import("@/hooks/use-websocket")>(),
  useWebSocket: (options: { onMessage: (value: unknown) => void }) => {
    socket.message = options.onMessage
    return { status: "connected", send: socket.send, disconnect: vi.fn(), reconnect: vi.fn() }
  },
}))
// Authentication and socket I/O are fixture inputs; the hook, renderer,
// assistant status note, export dialog and clipboard action are production code.
vi.mock("@/hooks/use-auth", () => ({
  useSession: () => ({ data: null, status: "unauthenticated" }),
  useSessionSafe: () => ({ data: null, status: "unauthenticated" }),
}))
vi.mock("@/lib/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { useChat } from "@/hooks/use-chat"
import { TurnRenderer } from "../turn-renderer"
import { ExportDialog } from "../export/export-dialog"

let current: ReturnType<typeof useChat>
let frames: Map<number, FrameRequestCallback>
let nextFrame: number
const writeText = vi.fn<(value: string) => Promise<void>>()

function Journey() {
  const chat = useChat({ sessionId: "stop-export", wsUrl: "ws://fixture", getToken: async () => "fixture" })
  const [exporting, setExporting] = useState(false)
  current = chat
  const { loadHistory } = chat
  useEffect(() => { loadHistory([]) }, [loadHistory])
  return <>
    <button onClick={() => { void chat.sendMessage("Please reply") }}>Send fixture message</button>
    <button onClick={chat.stopGeneration}>Stop fixture reply</button>
    <button onClick={() => setExporting(true)}>Export fixture transcript</button>
    {chat.turns.map(turn => <TurnRenderer key={turn.id} turn={turn} onCopy={async () => true} onFileClick={() => {}} />)}
    <ExportDialog turns={chat.turns} agentName="Riley" open={exporting} onOpenChange={setExporting} />
  </>
}
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(<QueryClientProvider client={client}><Journey /></QueryClientProvider>)
}
function legacy(type: string, content = "") {
  act(() => socket.message({ type: "chat_event", channel: "session:stop-export", payload: { type, content } }))
}
function flushFrames() {
  act(() => {
    const pending = [...frames.values()]
    frames.clear()
    for (const callback of pending) callback(0)
  })
}
function canonical(seq: number, type: string, payload: Record<string, unknown>, part_id?: string) {
  act(() => socket.message({ type: "chat_event", channel: "session:stop-export", payload: { event: {
    version: 1, event_id: `event-${seq}`, run_id: "canonical-run", turn_id: "canonical-turn",
    seq, occurred_at: "2026-11-17T00:00:00Z", delivery: "durable", type, payload,
    ...(part_id ? { part_id } : {}),
  } } }))
}
async function copyTranscript() {
  fireEvent.click(screen.getByRole("button", { name: "Export fixture transcript" }))
  fireEvent.click(screen.getByRole("button", { name: "Copy to clipboard" }))
  await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1))
  return writeText.mock.calls[0][0]
}

beforeEach(() => {
  frames = new Map(); nextFrame = 0
  socket.send.mockReset().mockReturnValue(true)
  writeText.mockReset().mockResolvedValue()
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => { frames.set(++nextFrame, callback); return nextFrame })
  vi.stubGlobal("cancelAnimationFrame", (id: number) => { frames.delete(id) })
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } })
})
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

describe("real stopped turn state through transcript and Markdown export", () => {
  it.each([false, true])("retains Stop before any reply (status received: %s)", async status => {
    mount()
    fireEvent.click(screen.getByRole("button", { name: "Send fixture message" }))
    if (status) legacy("status", "Preparing reply")
    fireEvent.click(screen.getByRole("button", { name: "Stop fixture reply" }))
    const stopped = current.turns.filter(turn => turn.role === "assistant")
    expect(stopped).toHaveLength(1)
    expect(stopped[0]).toMatchObject({ turnState: "cancelled", isStreaming: false, parts: [] })
    expect(screen.getByText("Stopped before any reply.")).toBeInTheDocument()
    legacy("text", "late reply must not reopen")
    flushFrames()
    expect(screen.queryByText("late reply must not reopen")).not.toBeInTheDocument()
    expect(await copyTranscript()).toContain("## Riley\nStopped before any reply.")
  })

  it("preserves the partial legacy reply and exports its cutoff", async () => {
    mount()
    fireEvent.click(screen.getByRole("button", { name: "Send fixture message" }))
    legacy("text", "Already received")
    flushFrames()
    fireEvent.click(screen.getByRole("button", { name: "Stop fixture reply" }))
    expect(screen.getByText("Stopped. The reply above is partial.")).toBeInTheDocument()
    const md = await copyTranscript()
    expect(md).toContain("Already received")
    expect(md).toContain("Stopped. The reply above is partial.")
  })

  it("keeps the legacy pending-frame drop policy and never exports raw thinking", async () => {
    mount()
    fireEvent.click(screen.getByRole("button", { name: "Send fixture message" }))
    legacy("thinking", "private raw reasoning must not be exported")
    legacy("text", "Visible partial reply")
    flushFrames()
    legacy("text", " buffered text not yet published")
    fireEvent.click(screen.getByRole("button", { name: "Stop fixture reply" }))
    flushFrames()
    const md = await copyTranscript()
    expect(md).toContain("Visible partial reply")
    expect(md).not.toContain("private raw reasoning")
    expect(md).not.toContain("buffered text not yet published")
  })

  it("does not invent a terminal turn for idle Stop", () => {
    mount()
    fireEvent.click(screen.getByRole("button", { name: "Stop fixture reply" }))
    expect(current.turns).toEqual([])
    expect(screen.queryByText("Stopped before any reply.")).not.toBeInTheDocument()
    expect(socket.send.mock.calls.filter(([frame]) => frame.type === "cancel_message")).toEqual([])
  })

  it("stops an actually observed legacy reply even without a local pending send", async () => {
    mount()
    legacy("text", "Observed reply from another tab")
    flushFrames()
    expect(current.isStreaming).toBe(false)
    expect(current.turns[0].isStreaming).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: "Stop fixture reply" }))
    expect(current.turns[0]).toMatchObject({ isStreaming: false, turnState: "cancelled" })
    expect(screen.getByText("Stopped. The reply above is partial.")).toBeInTheDocument()
    expect(await copyTranscript()).toContain("Observed reply from another tab\nStopped. The reply above is partial.")
  })

  it("respects observed legacy completion before React publishes the finished turn", () => {
    mount()
    legacy("text", "Finished observed reply")
    flushFrames()
    socket.send.mockClear()
    act(() => {
      socket.message({ type: "chat_event", channel: "session:stop-export", payload: { type: "done" } })
      current.stopGeneration()
    })
    expect(current.turns[0].turnState).not.toBe("cancelled")
    expect(socket.send.mock.calls.filter(([frame]) => frame.type === "cancel_message")).toEqual([])
  })

  it("does not keep an observed legacy activity flag after confirmed history replaces the stream", () => {
    mount()
    legacy("status", "Preparing a reply")
    socket.send.mockClear()
    act(() => {
      current.loadHistory([{ id: "finished", role: "assistant", content: "Confirmed reply", timestamp: new Date() }])
      current.stopGeneration()
    })
    expect(current.turns.every(turn => turn.turnState !== "cancelled")).toBe(true)
    expect(socket.send.mock.calls.filter(([frame]) => frame.type === "cancel_message")).toEqual([])
  })

  it.each([false, true])("does not cancel a completed legacy response even before React publishes done (text: %s)", partial => {
    mount()
    fireEvent.click(screen.getByRole("button", { name: "Send fixture message" }))
    if (partial) { legacy("text", "Completed reply"); flushFrames() }
    act(() => {
      socket.message({ type: "chat_event", channel: "session:stop-export", payload: { type: "done", metadata: { no_reply: !partial } } })
      current.stopGeneration()
    })
    expect(current.turns.every(turn => turn.turnState !== "cancelled")).toBe(true)
    expect(screen.queryByText("Stopped before any reply.")).not.toBeInTheDocument()
    expect(screen.queryByText("Stopped. The reply above is partial.")).not.toBeInTheDocument()
  })

  it("preserves received text, safe thinking and tool-call Markdown in their original order", async () => {
    mount()
    act(() => current.loadHistory([{
      id: "history", role: "assistant", content: "Answer", timestamp: new Date(),
      parts: [
        { type: "thinking", content: "", thinking: { visibility: "summary", state: "completed", summary: "Checked the file" } },
        { type: "tool_call", content: "Read", tool_name: "Read", tool_id: "read-1" },
        { type: "tool_result", content: "tool output not exported", tool_id: "read-1" },
        { type: "text", content: "Answer" },
      ],
    }]))
    expect(await copyTranscript()).toContain("## Riley\n> _Thinking:_ Checked the file\n> _Tool:_ `Read`\nAnswer\n")
    expect(writeText.mock.calls[0][0]).not.toContain("tool output not exported")
  })

  it.each([
    ["cancelled", false, "Stopped before any reply."],
    ["cancelled", true, "Stopped. The reply above is partial."],
    ["interrupted", false, "Interrupted before any reply."],
    ["interrupted", true, "Interrupted. The reply above is partial."],
    ["failed", false, "The run failed before any reply."],
    ["failed", true, "The run failed. The reply above may be incomplete."],
  ] as const)("exports canonical %s (partial: %s)", async (state, partial, note) => {
    mount()
    if (partial) canonical(1, "text", { text: { content: "Partial canonical reply", operation: "delta" } }, "text-part")
    canonical(partial ? 2 : 1, "turn", { turn: { state } })
    expect(screen.getByText(note)).toBeInTheDocument()
    const md = await copyTranscript()
    if (partial) expect(md).toContain("Partial canonical reply")
    expect(md).toContain(note)
  })

  it("keeps canonical Stop pending and does not relabel a completed response", async () => {
    mount()
    canonical(1, "text", { text: { content: "Complete reply", operation: "delta" } }, "text-part")
    fireEvent.click(screen.getByRole("button", { name: "Stop fixture reply" }))
    expect(current.stopping).toBe(true)
    expect(screen.getByText("Stopping…")).toBeInTheDocument()
    canonical(2, "turn", { turn: { state: "completed" } })
    const cancels = socket.send.mock.calls.filter(([frame]) => frame.type === "cancel_message").length
    fireEvent.click(screen.getByRole("button", { name: "Stop fixture reply" }))
    expect(current.turns[0].turnState).toBe("completed")
    expect(current.stopping).toBe(false)
    expect(socket.send.mock.calls.filter(([frame]) => frame.type === "cancel_message")).toHaveLength(cancels)
    const md = await copyTranscript()
    expect(md).toContain("Complete reply")
    expect(md).not.toMatch(/Stopped|Interrupted|failed|Stopping/)
  })

  it("does not send Stop or arm pending cancellation when canonical completion arrives in the same React batch", () => {
    mount()
    canonical(1, "text", { text: { content: "Completed just now", operation: "delta" } }, "text-part")
    socket.send.mockClear()
    act(() => {
      socket.message({ type: "chat_event", channel: "session:stop-export", payload: { event: {
        version: 1, event_id: "event-2", run_id: "canonical-run", turn_id: "canonical-turn", seq: 2,
        occurred_at: "2026-11-17T00:00:00Z", delivery: "durable", type: "turn", payload: { turn: { state: "completed" } },
      } } })
      current.stopGeneration()
    })
    expect(current.turns[0].turnState).toBe("completed")
    expect(current.stopping).toBe(false)
    expect(socket.send.mock.calls.filter(([frame]) => frame.type === "cancel_message")).toEqual([])
  })
})
