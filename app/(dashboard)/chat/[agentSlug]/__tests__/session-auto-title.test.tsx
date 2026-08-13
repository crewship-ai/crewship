import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { useEffect, useRef } from "react"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// A session names itself from its first message (PRD Step 2).
//
// `chats.title` has been in the schema since the first migration and nothing
// wrote it, so every thread in the sidebar read "Untitled session" forever.
// The first user message of an untitled session now derives a title
// (lib/chat-title.ts) and PATCHes it to
// PATCH /api/v1/agents/{agentId}/chats/{chatId}.
//
// Four properties are load-bearing and each has a test here:
//
//   · it fires ONCE — on the first send of a session that has no title, and
//     never again, because a user who renames a thread by hand must not have
//     it overwritten by the next thing they type;
//   · it never blocks or endangers the send — the message goes first, the
//     title is a follow-up, and a failed PATCH is silent (a toast about an
//     auto-title nobody asked for is worse than no title);
//   · the sidebar shows the SERVER's normalised title, without a refetch and
//     without reordering (the backend deliberately does not bump
//     last_activity_at on a rename);
//   · with the draft-session flow (PRD Step 3) the row does not exist until
//     the first send creates it, so the PATCH must land AFTER that POST or it
//     404s against a chat that isn't there yet.
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

type FakeEvent = { type: string; payload: Record<string, unknown>; timestamp: Date }

// vi.hoisted: both factories below read these at factory-execution time, which
// happens during the (hoisted) import of the page under test — before a plain
// `const` at this scope would have been initialised.
const { toastError, renameListeners } = vi.hoisted(() => ({
  toastError: vi.fn(),
  renameListeners: [] as ((event: FakeEvent) => void)[],
}))

vi.mock("sonner", () => ({
  toast: { error: toastError, success: vi.fn(), info: vi.fn(), warning: vi.fn(), message: vi.fn() },
}))

// Capture the page's chat_renamed subscription so a broadcast can be replayed
// without standing up a WebSocket or a RealtimeProvider.
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEventSafe: (type: string, cb: (event: FakeEvent) => void) => {
    if (type === "chat_renamed" && !renameListeners.includes(cb)) renameListeners.push(cb)
  },
}))

// The desktop left column is the shared agent tree now; SessionsSidebar is the
// phone drawer. The page still owns the session list and still hands it down,
// so the stand-in follows it to its new consumer — the rows, their titles and
// their order are exactly what this suite is about.
vi.mock("@/components/features/chat/chat-tree-sidebar", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/components/features/chat/chat-tree-sidebar")>()
  return {
    ...mod,
    ChatTreeSidebar: ({
      threadsByAgent,
    }: {
      threadsByAgent: Record<string, { id: string; title: string | null; last_activity_at?: string | null }[]>
    }) => {
      const sessions = threadsByAgent["agent-1"] ?? []
      return (
        <div
          data-testid="sidebar"
          data-ids={sessions.map((s) => s.id).join(",")}
          data-titles={sessions.map((s) => s.title ?? "").join("|")}
          data-activity={sessions.map((s) => s.last_activity_at ?? "").join("|")}
        />
      )
    },
  }
})

// Stand-in for ChatPanel reproducing exactly what the real one does on submit:
// ensureSession() POSTs the chat into existence with the id it was handed —
// once per session, and only ever skipped for a row the panel has CONFIRMED
// (created, or loaded real messages for), never for one an empty history
// merely implied — then the message goes out, THEN onSend() tells the page.
// The awaits are the contract this suite checks the title against; see
// hooks/use-message-submit.ts.
//
// That the real panel does this at all is not taken on trust here: it is
// pinned against the server's actual responses in
// app/(dashboard)/chat/[agentSlug]/__tests__/session-on-first-send.test.tsx
// and components/features/chat/__tests__/chat-panel-session-create.test.tsx,
// which render the real thing.
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
    // The ?prompt= handoff: the real panel fires the prefilled prompt itself
    // once the socket is up, without anybody clicking anything.
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
        <button type="button" onClick={() => void send("???")}>send junk</button>
      </div>
    )
  },
}))

import { ChatPageClient } from "../chat-page-client"
import { useComposerStore } from "@/stores/composer-store"

const FIRST_MESSAGE = "  Why did   the nightly build\nfail?  "
/** What the derivation makes of FIRST_MESSAGE before it goes on the wire. */
const DERIVED_TITLE = "Why did the nightly build fail?"
/** What the server hands back. Deliberately different from DERIVED_TITLE: the
 *  response carries the NORMALISED value and that is what must be rendered. */
const SERVER_TITLE = "Why did the nightly build fail? [server]"

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
/** Status the PATCH answers with — flipped to 500 by the failure test. */
let patchStatus = 200
/** Resolves the chat-create POST; the draft test holds it open to prove the
 *  PATCH waits for the row rather than racing it. */
let releasePost: (() => void) | null = null

const patchOf = (c: RecordedCall) => c.method === "PATCH"

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
      if (releasePost) await new Promise<void>((r) => { releasePost = r })
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
        json: async () => ({
          id: u.split("/chats/")[1].split("?")[0],
          agent_id: "agent-1", workspace_id: "ws-test",
          title: SERVER_TITLE, mode: "CHAT", status: "ACTIVE", message_count: 1,
          started_at: "2026-08-12T09:00:00Z", ended_at: null, created_at: "2026-08-12T09:00:00Z",
          origin: "UI",
          // Not bumped on a rename — the sidebar must not reorder.
          last_activity_at: "2026-08-12T09:00:00Z", unread_count: 0,
        }),
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
const clickSendJunk = () => fireEvent.click(screen.getByRole("button", { name: "send junk" }))

describe("<ChatPageClient> — a session titles itself from its first message", () => {
  beforeEach(() => {
    calls = []
    sentMessages = []
    existingChats = []
    patchStatus = 200
    releasePost = null
    renameListeners.length = 0
    toastError.mockClear()
    searchParams = new URLSearchParams()
    useComposerStore.setState({ attachments: {} })
    Object.defineProperty(window, "location", {
      configurable: true, writable: true,
      value: { ...window.location, pathname: "/chat/filip", search: "" },
    })
    installFetch()
  })

  afterEach(() => { vi.restoreAllMocks() })

  it("PATCHes a derived title on the first send and renders the server's value", async () => {
    await renderSettled()
    const draftId = screen.getByTestId("chat-panel").getAttribute("data-session-id")!

    clickSend()

    await waitFor(() => expect(calls.filter(patchOf)).toHaveLength(1))
    const patch = calls.find(patchOf)!
    expect(patch.url).toContain(`/api/v1/agents/agent-1/chats/${draftId}`)
    expect(patch.url).toContain("workspace_id=ws-test")
    // Whitespace folded, trimmed — what the wire gets is one line.
    expect(patch.body).toEqual({ title: DERIVED_TITLE })

    // The sidebar repaints from the RESPONSE, not from what we sent, and not
    // from a refetch: no second GET of the chats list.
    await waitFor(() =>
      expect(screen.getByTestId("sidebar").getAttribute("data-titles")).toBe(SERVER_TITLE),
    )
    expect(calls.filter((c) => c.method === "GET" && c.url.includes("/chats"))).toHaveLength(1)
  })

  it("does not title again on the second message", async () => {
    await renderSettled()
    clickSend()
    await waitFor(() => expect(calls.filter(patchOf)).toHaveLength(1))

    clickSendAgain()
    await new Promise((r) => setTimeout(r, 50))
    expect(calls.filter(patchOf)).toHaveLength(1)
    expect(sentMessages).toHaveLength(2)
  })

  it("never PATCHes a session that already has a title", async () => {
    // A thread the user named by hand (or that was titled on an earlier
    // visit). Typing into it must not rename it.
    existingChats = [{
      id: "chat-named", title: "Q3 planning", status: "ACTIVE", message_count: 4,
      started_at: "2026-08-11T09:00:00Z", ended_at: null, last_activity_at: "2026-08-11T09:00:00Z",
    }]
    await renderSettled()
    expect(screen.getByTestId("chat-panel").getAttribute("data-session-id")).toBe("chat-named")

    clickSend()
    await waitFor(() => expect(sentMessages).toHaveLength(1))
    await new Promise((r) => setTimeout(r, 50))

    expect(calls.filter(patchOf)).toEqual([])
    expect(screen.getByTestId("sidebar").getAttribute("data-titles")).toBe("Q3 planning")
  })

  it("keeps the message and stays silent when the PATCH fails", async () => {
    patchStatus = 500
    await renderSettled()

    clickSend()

    await waitFor(() => expect(calls.filter(patchOf)).toHaveLength(1))
    // The message went out regardless — the title is a follow-up call, never
    // a precondition.
    expect(sentMessages).toEqual([FIRST_MESSAGE])
    expect(toastError).not.toHaveBeenCalled()
    // And no half-truth in the sidebar: the row stays untitled rather than
    // showing a name the server never stored.
    expect(screen.getByTestId("sidebar").getAttribute("data-titles")).toBe("")

    // No retry storm on the next message either.
    clickSendAgain()
    await new Promise((r) => setTimeout(r, 50))
    expect(calls.filter(patchOf)).toHaveLength(1)
  })

  it("waits for the draft's row to exist before renaming it", async () => {
    // The draft session (PRD Step 3) has no row until the first send POSTs it.
    // Hold that POST open: a title fired off in parallel would land here first
    // and 404 against a chat that does not exist yet.
    releasePost = () => {}
    await renderSettled()

    clickSend()
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true))
    await new Promise((r) => setTimeout(r, 50))
    expect(calls.filter(patchOf)).toEqual([])

    releasePost!()
    await waitFor(() => expect(calls.filter(patchOf)).toHaveLength(1))
    expect(calls.findIndex((c) => c.method === "POST")).toBeLessThan(calls.findIndex(patchOf))
  })

  it("does not reorder the sidebar when the title lands", async () => {
    existingChats = [
      { id: "chat-a", title: null, status: "ACTIVE", message_count: 2, started_at: "2026-08-12T09:00:00Z", ended_at: null, last_activity_at: "2026-08-12T09:00:00Z" },
      { id: "chat-b", title: "Older thread", status: "ACTIVE", message_count: 9, started_at: "2026-08-01T09:00:00Z", ended_at: null, last_activity_at: "2026-08-01T09:00:00Z" },
    ]
    await renderSettled()
    const idsBefore = screen.getByTestId("sidebar").getAttribute("data-ids")

    clickSend()
    await waitFor(() =>
      expect(screen.getByTestId("sidebar").getAttribute("data-titles")).toContain(SERVER_TITLE),
    )

    expect(screen.getByTestId("sidebar").getAttribute("data-ids")).toBe(idsBefore)
    // The rename response carries the deliberately un-bumped last_activity_at;
    // splicing the whole row in would undo the send's activity bump and drag
    // the thread back down the list. Only the title moves.
    const activity = screen.getByTestId("sidebar").getAttribute("data-activity")!.split("|")
    expect(activity[0]).not.toBe("2026-08-12T09:00:00Z")
    expect(activity[1]).toBe("2026-08-01T09:00:00Z")
  })

  it("titles the ?prompt= handoff session too", async () => {
    // routine-create-dialog sends the user here with a goal that auto-sends
    // into a session of its own. It is the one path where the row exists
    // before the first message, and it must still end up with a name.
    searchParams = new URLSearchParams("prompt=Author%20a%20routine%20that%20checks%20the%20nightly%20build")
    await renderSettled()

    await waitFor(() => expect(calls.filter(patchOf)).toHaveLength(1))
    expect(calls.find(patchOf)!.body).toEqual({
      title: "Author a routine that checks the nightly build",
    })
  })

  it("falls back to the attachment's name when the message says nothing", async () => {
    await renderSettled()
    const draftId = screen.getByTestId("chat-panel").getAttribute("data-session-id")!
    useComposerStore.getState().addAttachments(draftId, [
      { id: "att-1", name: "incident-2026-08-12.log", size: 1024, type: "text/plain", status: "ready" },
    ])

    clickSendJunk()

    await waitFor(() => expect(calls.filter(patchOf)).toHaveLength(1))
    expect(calls.find(patchOf)!.body).toEqual({ title: "incident-2026-08-12.log" })
  })

  it("leaves the session untitled when neither the text nor an attachment says anything", async () => {
    await renderSettled()

    clickSendJunk()
    await waitFor(() => expect(sentMessages).toHaveLength(1))
    await new Promise((r) => setTimeout(r, 50))

    // "???" is not a name. Untitled is the honest answer.
    expect(calls.filter(patchOf)).toEqual([])
    expect(screen.getByTestId("sidebar").getAttribute("data-titles")).toBe("")
  })

  it("repaints a row when another client renames it", async () => {
    existingChats = [{
      id: "chat-a", title: null, status: "ACTIVE", message_count: 2,
      started_at: "2026-08-12T09:00:00Z", ended_at: null, last_activity_at: "2026-08-12T09:00:00Z",
    }]
    await renderSettled()
    expect(renameListeners).not.toHaveLength(0)

    renameListeners.forEach((cb) =>
      cb({ type: "chat_renamed", payload: { agent_id: "agent-1", chat_id: "chat-a", title: "Renamed elsewhere" }, timestamp: new Date() }),
    )

    await waitFor(() =>
      expect(screen.getByTestId("sidebar").getAttribute("data-titles")).toBe("Renamed elsewhere"),
    )
  })
})
