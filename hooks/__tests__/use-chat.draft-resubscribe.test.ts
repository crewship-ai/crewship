import { describe, it, expect, vi, beforeEach } from "vitest"

// =============================================================================
// A draft session's mount-time `subscribe` is DENIED, and nothing retried it.
//
// Sessions are no longer created on mount (PRD chat-as-a-primary-surface,
// Step 3): an unsent conversation is a client-side draft and the `chats` row
// only appears on the first send. The WS channel authorizer requires that row
// to exist — `isSessionOwner` in internal/ws/channel_auth.go looks the chat up
// and refuses the channel when it is missing — so the `subscribe` this hook
// fires on mount for a draft id is rejected, silently, with no client-visible
// error.
//
// The first reply still arrives, which is why this went unnoticed: a run
// started by this socket is served straight back to the sending client
// (internal/ws/client.go:470-474) without going through the channel fan-out.
// Everything from ANY OTHER origin in that session — a teammate's message in a
// group chat, a CLI run, a webhook — is dropped until the socket reconnects or
// the user switches sessions and back.
//
// The fix is a re-subscribe at the moment the draft stops being a draft, i.e.
// when ChatPanel's ensureSession() has POSTed the row into existence. It must
// fire exactly once per session (a second `resume` would ask the server to
// replay an in-flight run a second time) and must NOT open another socket.
// =============================================================================

const mockSend = vi.fn()
const mockStatus = { current: "connected" as string }
/** Counts distinct useWebSocket INSTANCES, i.e. sockets — not renders. */
let socketInstances = 0

vi.mock("@/hooks/use-websocket", async () => {
  const React = await import("react")
  return {
    useWebSocket: ({ onConnect }: { onConnect?: () => void }) => {
      const idRef = React.useRef<number | null>(null)
      if (idRef.current === null) {
        socketInstances += 1
        idRef.current = socketInstances
      }
      const connectedRef = React.useRef(false)
      if (mockStatus.current === "connected" && !connectedRef.current) {
        connectedRef.current = true
        onConnect?.()
      }
      return {
        status: mockStatus.current,
        send: mockSend,
        disconnect: vi.fn(),
        reconnect: vi.fn(),
      }
    },
    encodedByteLength: (s: string) => new TextEncoder().encode(s).length,
    WS_MAX_OUTBOUND_FRAME_BYTES: 64 * 1024,
  }
})

vi.stubGlobal("crypto", {
  randomUUID: () => "test-uuid-" + Math.random().toString(36).slice(2, 8),
})

import { renderHook, act } from "@testing-library/react"
import { useChat } from "@/hooks/use-chat"

function subscribesTo(channel: string): number {
  return mockSend.mock.calls.filter(
    ([frame]) =>
      (frame as { type?: string; channel?: string })?.type === "subscribe" &&
      (frame as { channel?: string })?.channel === channel,
  ).length
}

function resumesFor(sessionId: string): number {
  return mockSend.mock.calls.filter(([frame]) => {
    const f = frame as { type?: string; payload?: string }
    if (f?.type !== "resume") return false
    try {
      return (JSON.parse(f.payload ?? "{}") as { session_id?: string }).session_id === sessionId
    } catch {
      return false
    }
  }).length
}

const opts = { wsUrl: "ws://localhost:8080/ws", getToken: async () => "t" }

describe("useChat — draft session re-subscribe", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockStatus.current = "connected"
    socketInstances = 0
  })

  it("re-subscribes to the session channel once when the draft becomes real", () => {
    const { result } = renderHook(() => useChat({ ...opts, sessionId: "draft-1" }))

    // Mount-time subscribe: the one the authorizer denies for a draft.
    expect(subscribesTo("session:draft-1")).toBe(1)

    // The row now exists (ensureSession POSTed it) — take the channel again.
    act(() => { result.current.resubscribeSession() })
    expect(subscribesTo("session:draft-1")).toBe(2)
    expect(resumesFor("draft-1")).toBe(2)
  })

  it("is idempotent — repeated calls for the same session send nothing more", () => {
    const { result, rerender } = renderHook(() => useChat({ ...opts, sessionId: "draft-1" }))
    act(() => { result.current.resubscribeSession() })
    const after = subscribesTo("session:draft-1")

    act(() => { result.current.resubscribeSession() })
    act(() => { result.current.resubscribeSession() })
    rerender()
    act(() => { result.current.resubscribeSession() })

    expect(subscribesTo("session:draft-1")).toBe(after)
  })

  it("opens no additional socket", () => {
    const { result, rerender } = renderHook(() => useChat({ ...opts, sessionId: "draft-1" }))
    expect(socketInstances).toBe(1)
    act(() => { result.current.resubscribeSession() })
    rerender()
    expect(socketInstances).toBe(1)
  })

  it("re-arms for the next session — a second draft can also be promoted", () => {
    const { result, rerender } = renderHook(
      ({ sid }) => useChat({ ...opts, sessionId: sid }),
      { initialProps: { sid: "draft-1" } },
    )
    act(() => { result.current.resubscribeSession() })
    expect(subscribesTo("session:draft-1")).toBe(2)

    rerender({ sid: "draft-2" })
    act(() => { result.current.resubscribeSession() })
    expect(subscribesTo("session:draft-2")).toBe(2)
    // …and the first session is not re-taken by the second promotion.
    expect(subscribesTo("session:draft-1")).toBe(2)
    expect(socketInstances).toBe(1)
  })
})
