import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent, act } from "@testing-library/react"

// An upload started on the desktop layout must survive the window crossing the
// chat breakpoint: ChatClient swaps its whole layout tree and the composer
// remounts underneath it.

vi.mock("next/navigation", () => ({
  useSearchParams: () => new URLSearchParams(window.location.search.replace(/^\?/, "")),
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false }),
  useCurrentWorkspaceId: () => "ws-1",
}))
vi.mock("@/hooks/use-auth", async (orig) => ({
  ...(await orig<typeof import("@/hooks/use-auth")>()),
  useSessionSafe: () => ({ data: { user: { id: "user-a", name: "a", email: "" } }, status: "authenticated" }),
}))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: () => {} }))
vi.mock("@/lib/telemetry", async (orig) => ({
  ...(await orig<typeof import("@/lib/telemetry")>()),
  emitChatEvent: vi.fn(),
}))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))
vi.mock("@/components/features/chat/composer/mention-autocomplete", () => ({ MentionAutocomplete: () => null }))
vi.mock("@/components/features/chat/conversations-sidebar", async () => {
  const actual = await vi.importActual<typeof import("@/components/features/chat/conversations-sidebar")>(
    "@/components/features/chat/conversations-sidebar",
  )
  return { ...actual, ConversationsSidebar: () => <div data-testid="sidebar" /> }
})
// The panel is reduced to the real composer, which is what owns the upload.
vi.mock("@/components/features/chat/chat-panel", async () => {
  const { ChatComposer } = await import("@/components/features/chat/composer/chat-composer")
  return {
    ChatPanel: ({ sessionId, agentId }: { sessionId: string; agentId: string }) => (
      <ChatComposer
        agentId={agentId}
        sessionId={sessionId}
        isStreaming={false}
        connectionStatus="connected"
        variant="desktop"
        stopGeneration={() => {}}
        ensureSession={async () => true}
        sendMessage={() => {}}
      />
    ),
  }
})

const apiFetchMock = vi.fn()
vi.mock("@/lib/api-fetch", async (orig) => ({
  ...(await orig<typeof import("@/lib/api-fetch")>()),
  apiFetch: (...a: unknown[]) => apiFetchMock(...a),
}))

import { ChatClient } from "../chat-client"
import { useComposerStore } from "@/stores/composer-store"
import { __resetForTests } from "@/lib/chat-attachment-jobs"

const ok = (body: unknown) =>
  Promise.resolve({ ok: true, status: 200, headers: new Headers(), json: () => Promise.resolve(body) } as Response)
const agent = { id: "agent-1", name: "Filip", slug: "filip", status: "IDLE", role_title: "Analyst", avatar_seed: "f", avatar_style: null, crew_id: "c1" }
const thread = { id: "sess-1", title: "T", status: "ACTIVE", message_count: 1, started_at: "2026-08-20T10:00:00Z", last_activity_at: "2026-08-20T10:05:00Z", ended_at: null, origin: null }

describe("ChatClient: crossing the breakpoint mid-upload", () => {
  const listeners: Array<() => void> = []
  beforeEach(() => {
    __resetForTests()
    useComposerStore.setState({ attachments: {}, drafts: {} })
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      matches: false,
      addEventListener: (_: string, cb: () => void) => { listeners.push(cb) },
      removeEventListener: () => {},
    })))
    Object.defineProperty(window, "innerWidth", { value: 1280, writable: true, configurable: true })
    Object.defineProperty(window, "location", { configurable: true, writable: true, value: { ...window.location, pathname: "/chat/filip", search: "?session=sess-1" } })
    window.history.replaceState = vi.fn()
  })
  afterEach(() => vi.restoreAllMocks())

  it("keeps the upload running and does not cancel the chip", async () => {
    const calls: Array<{ release: () => void; signal?: AbortSignal }> = []
    apiFetchMock.mockImplementation((url: string, init?: RequestInit) => {
      const u = String(url)
      if (/\/attachments\?/.test(u)) {
        return new Promise((resolve, reject) => {
          init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))
          calls.push({
            signal: init?.signal ?? undefined,
            release: () => resolve({ ok: true, status: 200, json: async () => ({ path: "attachments/sess-1/a.txt", agent_path: "/o/a.txt" }) }),
          })
        })
      }
      if (/\/chats\?/.test(u)) return ok([thread])
      if (/\/api\/v1\/agents\?/.test(u)) return ok([agent])
      return ok({})
    })
    render(<ChatClient />)
    await waitFor(() => expect(document.querySelector('input[type="file"]:not([aria-label])')).toBeTruthy())
    const input = document.querySelector<HTMLInputElement>('input[type="file"]:not([aria-label])')!
    Object.defineProperty(input, "files", { value: [new File(["x"], "a.txt", { type: "text/plain" })], configurable: true })
    fireEvent.change(input)
    await waitFor(() => expect(calls).toHaveLength(1))

    // Cross to the compact (phone) layout: the whole tree is replaced.
    Object.defineProperty(window, "innerWidth", { value: 500, writable: true, configurable: true })
    await act(async () => listeners.forEach((cb) => cb()))
    await waitFor(() => expect(screen.getByRole("tablist", { name: "Chat panel" })).toBeTruthy())

    expect(calls[0].signal?.aborted).toBe(false)
    const status = () => Object.values(useComposerStore.getState().attachments).flat().map((a) => a.status)
    expect(status()).toEqual(["uploading"])
    await act(async () => calls[0].release())
    await waitFor(() => expect(status()).toEqual(["ready"]))
    expect(screen.queryByText(/Send is off/)).toBeNull()
  })
})
