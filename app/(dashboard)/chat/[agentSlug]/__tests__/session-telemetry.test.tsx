import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { useEffect, useRef } from "react"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// The two session events the page owns.
//
//   · `chat_session_created` — the explicit "New session" control. This is the
//     other half of the pair: chat-panel.tsx emits `source: "composer"` when a
//     draft turns into a row because somebody typed, and this emits
//     `source: "sidebar"` when somebody asked for a fresh conversation outright.
//     Without both, "how do conversations start here" has no answer.
//
//   · `chat_session_titled` — after the auto-title PATCH is ACCEPTED. The title
//     is derived from the first message, so the title is content and it is not
//     recorded; what is recorded is that a name was written and that nobody
//     typed it.
//
// Both must fire exactly once for the action, and never for a request the
// server refused: a funnel that counts failures as successes is worse than no
// funnel, because it is believed.
//
// The behaviour underneath is pinned in session-auto-title.test.tsx; the
// harness here is deliberately the same one, so a change to the flow breaks
// both files rather than silently un-measuring it in this one.
// =============================================================================

let searchParams = new URLSearchParams()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), back: vi.fn(), forward: vi.fn(), prefetch: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => searchParams,
  useParams: () => ({}),
  usePathname: () => "/",
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

const { toastError } = vi.hoisted(() => ({ toastError: vi.fn() }))

vi.mock("sonner", () => ({
  toast: { error: toastError, success: vi.fn(), info: vi.fn(), warning: vi.fn(), message: vi.fn() },
}))

vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEventSafe: () => {},
}))

vi.mock("@/components/features/chat/chat-tree-sidebar", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/components/features/chat/chat-tree-sidebar")>()
  return {
    ...mod,
    ChatTreeSidebar: ({
      threadsByAgent,
    }: {
      threadsByAgent: Record<string, { id: string; title: string | null }[]>
    }) => {
      const sessions = threadsByAgent["agent-1"] ?? []
      return <div data-testid="sidebar" data-ids={sessions.map((s) => s.id).join(",")} />
    },
  }
})

// Stand-in for ChatPanel: it POSTs the row into existence the way the real one
// does, then reports the send. The real panel's own `chat_session_created` is
// therefore NOT in play here — which is what makes this file able to attribute
// every event it sees to the page.
vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({
    agentId, sessionId, initialInput, autoSendInitial, onSend,
  }: {
    agentId: string
    sessionId: string
    initialInput?: string
    autoSendInitial?: boolean
    onSend?: (sid: string, text: string) => void
  }) => {
    const createdRef = useRef(false)
    const autoSentRef = useRef(false)
    const send = async (text: string) => {
      const { apiFetch } = await import("@/lib/api-fetch")
      if (!createdRef.current) {
        createdRef.current = true
        await apiFetch(`/api/v1/agents/${agentId}/chats?workspace_id=ws-test`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ session_id: sessionId, origin: "UI" }),
        })
      }
      sentMessages.push(text)
      onSend?.(sessionId, text)
    }
    useEffect(() => {
      if (!autoSendInitial || !initialInput || autoSentRef.current) return
      autoSentRef.current = true
      void send(initialInput)
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [autoSendInitial, initialInput])
    return (
      <div data-testid="chat-panel" data-session-id={sessionId}>
        <button type="button" onClick={() => void send(FIRST_MESSAGE)}>send</button>
        <button type="button" onClick={() => void send("and what about staging?")}>send again</button>
      </div>
    )
  },
}))

import { ChatPageClient } from "../chat-page-client"
import { useComposerStore } from "@/stores/composer-store"
import { resetChatTelemetry, setChatTelemetrySink, type ChatEvent } from "@/lib/telemetry"

/** Deliberately content-heavy: the title derived from it is a secret, and the
 *  event about it must not be. */
const FIRST_MESSAGE = "Why did the Vodafone invoice fail to reconcile?"
const SERVER_TITLE = "Why did the Vodafone invoice fail to reconcile? [server]"

const mockAgents = [
  {
    id: "agent-1", name: "Filip", slug: "filip", status: "IDLE", role_title: "Data Analyst",
    avatar_seed: "filip", avatar_style: null,
    crew: { name: "Research", slug: "research", avatar_style: null },
  },
]

interface RecordedCall { method: string; url: string; body: Record<string, unknown> }
let calls: RecordedCall[] = []
let sentMessages: string[] = []
let existingChats: Record<string, unknown>[] = []
let patchStatus = 200
let createStatus = 200
let events: ChatEvent[] = []

const named = (n: string) => events.filter((e) => e.name === n)

function installFetch() {
  global.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url)
    const method = (init?.method ?? "GET").toUpperCase()
    if (u.includes("/api/v1/agents") && !u.includes("/chats")) {
      return { ok: true, status: 200, json: async () => mockAgents } as unknown as Response
    }
    if (u.includes("/chats") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}"))
      calls.push({ method, url: u, body })
      if (createStatus >= 400) {
        return { ok: false, status: createStatus, json: async () => ({ error: "nope" }) } as unknown as Response
      }
      return { ok: true, status: 200, json: async () => ({ id: body.session_id ?? "server-minted-1" }) } as unknown as Response
    }
    if (u.includes("/chats/") && method === "PATCH") {
      const body = JSON.parse(String(init?.body ?? "{}"))
      calls.push({ method, url: u, body })
      if (patchStatus !== 200) {
        return { ok: false, status: patchStatus, json: async () => ({ error: "nope" }) } as unknown as Response
      }
      return {
        ok: true, status: 200,
        json: async () => ({ id: u.split("/chats/")[1].split("?")[0], title: SERVER_TITLE }),
      } as unknown as Response
    }
    if (u.includes("/chats")) {
      calls.push({ method, url: u, body: {} })
      return { ok: true, status: 200, json: async () => existingChats } as unknown as Response
    }
    return { ok: true, status: 200, json: async () => ({}) } as unknown as Response
  }) as unknown as typeof fetch
}

async function renderSettled() {
  const view = render(<ChatPageClient />)
  await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument(), { timeout: 3000 })
  return view
}

const clickSend = () => fireEvent.click(screen.getByRole("button", { name: "send" }))
const clickSendAgain = () => fireEvent.click(screen.getByRole("button", { name: "send again" }))
const clickNewSession = () => fireEvent.click(screen.getByRole("button", { name: "New session" }))

describe("<ChatPageClient> — starting and naming a session are measured", () => {
  beforeEach(() => {
    calls = []
    sentMessages = []
    existingChats = []
    patchStatus = 200
    createStatus = 200
    toastError.mockClear()
    searchParams = new URLSearchParams()
    useComposerStore.setState({ attachments: {} })
    Object.defineProperty(window, "location", {
      configurable: true, writable: true,
      value: { ...window.location, pathname: "/chat/filip", search: "" },
    })
    installFetch()
    resetChatTelemetry()
    events = []
    setChatTelemetrySink((e) => events.push(e))
  })

  afterEach(() => { vi.restoreAllMocks() })

  it("emits chat_session_created once when New session is used", async () => {
    await renderSettled()
    expect(named("chat_session_created")).toEqual([])

    clickNewSession()

    await waitFor(() => expect(named("chat_session_created")).toHaveLength(1))
    expect(named("chat_session_created")[0].payload).toEqual({
      session_id: "server-minted-1",
      agent_id: "agent-1",
      source: "sidebar",
    })
  })

  it("emits no creation event when the server refuses the new session", async () => {
    createStatus = 500
    await renderSettled()

    clickNewSession()

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(named("chat_session_created")).toEqual([])
  })

  it("emits chat_session_titled once, after the title is accepted", async () => {
    await renderSettled()
    const draftId = screen.getByTestId("chat-panel").getAttribute("data-session-id")!

    clickSend()

    await waitFor(() => expect(named("chat_session_titled")).toHaveLength(1))
    expect(named("chat_session_titled")[0].payload).toEqual({
      session_id: draftId,
      source: "auto",
    })
  })

  it("does not emit a second titling on the second message", async () => {
    await renderSettled()
    clickSend()
    await waitFor(() => expect(named("chat_session_titled")).toHaveLength(1))

    clickSendAgain()
    await new Promise((r) => setTimeout(r, 50))
    expect(named("chat_session_titled")).toHaveLength(1)
    expect(sentMessages).toHaveLength(2)
  })

  it("emits nothing when the title PATCH is refused", async () => {
    patchStatus = 500
    await renderSettled()

    clickSend()

    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true))
    await new Promise((r) => setTimeout(r, 50))
    // The session is still untitled. Saying otherwise would be a metric that
    // disagrees with the sidebar the user is looking at.
    expect(named("chat_session_titled")).toEqual([])
  })

  it("never emits the title itself, nor the message it came from", async () => {
    await renderSettled()

    clickSend()
    await waitFor(() => expect(named("chat_session_titled")).toHaveLength(1))

    const serialized = JSON.stringify(events)
    expect(serialized).not.toContain("Vodafone")
    expect(serialized).not.toContain("invoice")
    expect(serialized).not.toContain("[server]")
    // …and no key that could hold one later, either. (The event *name* is
    // `chat_session_titled`; it is the payload that must stay content-free.)
    const payloadKeys = events.flatMap((e) => Object.keys(e.payload))
    expect(payloadKeys.filter((k) => /title|text|name|query/.test(k))).toEqual([])
  })
})
