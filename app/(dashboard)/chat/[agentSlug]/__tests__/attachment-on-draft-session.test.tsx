import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// A brand-new conversation must accept a file before it accepts a word.
//
// Sister suite to session-on-first-send: that one pins "arriving creates
// nothing, sending creates exactly one row". This one pins the case that fell
// through the gap between them — ATTACHING to that same draft.
//
// The page opens an agent with no chats on a locally minted draft id and writes
// nothing server-side. The upload endpoint resolves the chat row before it will
// take a byte (`SELECT agent_id FROM chats WHERE id = ?`,
// internal/api/proxy_attachments.go) and answers 404 Chat not found when there
// is none — so "photograph the receipt, attach it, send" died on the attach for
// every first conversation.
//
// Attaching is an intent to converse, so it creates the row the same way
// sending does: through ChatPanel's own `ensureSession()`, which is already
// handed to the composer. That is the whole point of running this suite against
// the REAL ChatPanel and the REAL composer — a stand-in for either would be
// testing the fix's mock rather than the fix. What is asserted is what the
// network saw, in order:
//
//   POST /agents/agent-1/chats           {session_id: <draft>}   ← exactly one
//   POST /agents/agent-1/chats/<draft>/attachments               ← then bytes
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

const { chatStub } = vi.hoisted(() => ({
  chatStub: {
    turns: [] as unknown[],
    sendMessage: vi.fn(),
    stopGeneration: vi.fn(),
    regenerateLastTurn: vi.fn(),
    editAndResend: vi.fn(),
    loadHistory: vi.fn(),
    markHistoryUnavailable: vi.fn(),
    resubscribeSession: vi.fn(),
    isStreaming: false,
    connectionStatus: "connected",
  },
}))
vi.mock("@/hooks/use-chat", () => ({ useChat: () => chatStub }))
vi.mock("@/hooks/use-auth", () => ({
  useSession: () => ({ data: { user: { id: "user-1" } } }),
}))

vi.mock("@/components/features/chat/right-panel", () => ({ RightPanel: () => null }))
vi.mock("@/components/features/chat/right-rail", () => ({ RightRail: () => null }))
vi.mock("@/components/features/chat/right-drawer", () => ({ RightDrawer: () => null }))
vi.mock("@/components/features/chat/artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("@/components/features/chat/composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("@/components/features/chat/search/conversation-search", () => ({ ConversationSearch: () => null }))
vi.mock("@/components/features/chat/export/export-dialog", () => ({ ExportDialog: () => null }))
vi.mock("@/components/features/chat/composer/mention-autocomplete", () => ({ MentionAutocomplete: () => null }))

const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    error: (m: string, opts?: unknown) => toastError(m, opts),
    success: vi.fn(), info: vi.fn(), warning: vi.fn(), message: vi.fn(),
  },
}))

vi.mock("@/components/features/chat/chat-tree-sidebar", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/components/features/chat/chat-tree-sidebar")>()
  return {
    ...mod,
    ChatTreeSidebar: ({
      threadsByAgent,
      activeThreadId,
    }: {
      threadsByAgent: Record<string, { id: string }[]>
      activeThreadId: string | null
    }) => (
      <div
        data-testid="sidebar"
        data-active={activeThreadId ?? ""}
        data-ids={(threadsByAgent["agent-1"] ?? []).map((s) => s.id).join(",")}
      />
    ),
  }
})

import { ChatPageClient } from "../chat-page-client"
import { useComposerStore } from "@/stores/composer-store"

const mockAgents = [
  {
    id: "agent-1", name: "Filip", slug: "filip", status: "IDLE", role_title: "Data Analyst",
    avatar_seed: "filip", avatar_style: null, suggested_prompts: null, ask_forms: null,
    crew: { name: "Research", slug: "research", avatar_style: null },
  },
]

/** Every write this render made, in the order the network saw it. `kind` is all
 *  the assertions need; `id` is the chat the request was about. */
type Wrote = { kind: "create" | "upload"; id: string }
let wrote: Wrote[] = []
let existingChats: Record<string, unknown>[] = []
let serverMessages: Record<string, { id: string; role: string; content: string; ts: string }[]> = {}
/** Chats the fake server will accept bytes for — the same precondition
 *  proxy_attachments.go enforces. Anything else is 404 Chat not found. */
let serverChats = new Set<string>()
let uploadFails = false
let createFails = false

function installFetch() {
  global.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url)
    const method = (init?.method ?? "GET").toUpperCase()

    if (u.includes("/attachments") && method === "POST") {
      const id = u.split("/chats/")[1].split("/")[0]
      wrote.push({ kind: "upload", id })
      // The endpoint resolves the row first. This is the 404 the whole finding
      // is about, and the fake server must answer it the way the real one does
      // or the suite certifies the bug.
      if (!serverChats.has(id)) {
        return { ok: false, status: 404, json: async () => ({ error: "Chat not found" }) } as unknown as Response
      }
      if (uploadFails) {
        return { ok: false, status: 500, json: async () => ({ error: "disk full" }) } as unknown as Response
      }
      // The filename comes off the multipart body, exactly as the handler
      // reads it — the URL carries only the chat id.
      const sent = init?.body as FormData | undefined
      const name = (sent?.get?.("file") as File | null)?.name ?? "file"
      return {
        ok: true, status: 200,
        json: async () => ({ path: `attachments/${id}/${name}`, agent_path: `/output/filip/attachments/${id}/${name}` }),
      } as unknown as Response
    }
    if (u.includes("/messages")) {
      const id = u.split("/chats/")[1].split("/")[0]
      return { ok: true, status: 200, json: async () => ({ messages: serverMessages[id] ?? [] }) } as unknown as Response
    }
    if (u.includes("/participants")) {
      return { ok: true, status: 200, json: async () => ({ participants: [] }) } as unknown as Response
    }
    if (u.includes("/api/v1/agents") && !u.includes("/chats") && !u.includes("/files")) {
      return { ok: true, status: 200, json: async () => mockAgents } as unknown as Response
    }
    if (u.includes("/chats") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}"))
      const id = String(body.session_id ?? "server-minted-1")
      wrote.push({ kind: "create", id })
      if (createFails) {
        return { ok: false, status: 500, json: async () => ({ error: "nope" }) } as unknown as Response
      }
      serverChats.add(id)
      return { ok: true, status: 201, json: async () => ({ id }) } as unknown as Response
    }
    if (u.includes("/chats") && method === "GET") {
      return { ok: true, status: 200, json: async () => existingChats } as unknown as Response
    }
    return { ok: true, status: 200, json: async () => ({}) } as unknown as Response
  }) as unknown as typeof fetch
}

async function renderSettled() {
  const view = render(<ChatPageClient />)
  await waitFor(() => expect(screen.getByTestId("ask-rail")).toBeInTheDocument(), { timeout: 3000 })
  await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())
  return view
}

function activeSessionId(): string {
  return screen.getByTestId("sidebar").getAttribute("data-active") ?? ""
}

function file(name: string, type = "image/jpeg") {
  return new File(["bytes"], name, { type })
}

/** The composer's paperclip. Its input is the one with no aria-label (the
 *  PromptInput ships an unused labelled one of its own). */
function fileInputs(): HTMLInputElement[] {
  return Array.from(
    document.querySelectorAll<HTMLInputElement>('input[type="file"]:not([aria-label])'),
  )
}

function pickFiles(files: File[], which = 0) {
  const input = fileInputs()[which]
  Object.defineProperty(input, "files", { value: files, configurable: true })
  fireEvent.change(input)
}

/** Two files dragged onto the composer in one gesture. */
function dropFiles(files: File[]) {
  const zone = document.querySelector("form")!.closest(".relative")!
  fireEvent.drop(zone, { dataTransfer: { files, types: ["Files"] } })
}

function attachments(sid: string) {
  return useComposerStore.getState().attachments[sid] ?? []
}

const creates = () => wrote.filter((w) => w.kind === "create")
const uploads = () => wrote.filter((w) => w.kind === "upload")

describe("<ChatPageClient> — a file can be attached before the first message", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useComposerStore.setState({ attachments: {}, drafts: {} })
    wrote = []
    existingChats = []
    serverMessages = {}
    serverChats = new Set()
    uploadFails = false
    createFails = false
    searchParams = new URLSearchParams()
    Object.defineProperty(window, "location", {
      configurable: true, writable: true,
      value: { ...window.location, pathname: "/chat/filip", search: "" },
    })
    installFetch()
  })

  afterEach(() => { vi.restoreAllMocks() })

  it("creates the chat, once, and then uploads into it", async () => {
    await renderSettled()
    const draftId = activeSessionId()
    expect(wrote).toEqual([])

    pickFiles([file("receipt.jpg")])

    await waitFor(() => expect(attachments(draftId)[0]?.status).toBe("ready"))

    // The order is the fix: create, then bytes. Reversed (or raced) this is the
    // 404 the finding reported.
    expect(wrote).toEqual([
      { kind: "create", id: draftId },
      { kind: "upload", id: draftId },
    ])
    expect(attachments(draftId)[0].path).toBe(`attachments/${draftId}/receipt.jpg`)
  })

  it("creates exactly one chat when two files are dropped at once", async () => {
    await renderSettled()
    const draftId = activeSessionId()

    dropFiles([file("front.jpg"), file("back.jpg")])

    await waitFor(() => expect(attachments(draftId)).toHaveLength(2))
    await waitFor(() =>
      expect(attachments(draftId).every((a) => a.status === "ready")).toBe(true),
    )

    expect(creates()).toHaveLength(1)
    expect(uploads()).toHaveLength(2)
    expect(wrote[0].kind).toBe("create")
  })

  it("creates exactly one chat when a drop and a pick race each other", async () => {
    await renderSettled()
    const draftId = activeSessionId()

    // Two independent entry points firing before either create can resolve —
    // the case a per-upload create would turn into two rows (or two racing
    // upserts) rather than one.
    dropFiles([file("dropped.jpg")])
    pickFiles([file("picked.jpg")])

    await waitFor(() => expect(attachments(draftId)).toHaveLength(2))
    await waitFor(() =>
      expect(attachments(draftId).every((a) => a.status === "ready")).toBe(true),
    )

    expect(creates()).toHaveLength(1)
    expect(uploads()).toHaveLength(2)
  })

  it("still creates nothing when the picker is opened and dismissed", async () => {
    await renderSettled()

    const input = fileInputs()[0]
    Object.defineProperty(input, "files", { value: [], configurable: true })
    fireEvent.change(input)

    await new Promise((r) => setTimeout(r, 50))
    // The guarantee session-on-first-send pins for arriving holds for browsing
    // too: the row is created by an upload starting, not by a dialog opening.
    expect(wrote).toEqual([])
  })

  it("costs no create for a conversation whose history is already loaded", async () => {
    existingChats = [
      { id: "chat-newest", title: "Yesterday", status: "ACTIVE", message_count: 3, started_at: "2026-08-11T09:00:00Z", ended_at: null },
    ]
    serverMessages["chat-newest"] = [
      { id: "m1", role: "user", content: "yesterday", ts: "2026-08-11T09:00:00.000Z" },
    ]
    serverChats.add("chat-newest")
    await renderSettled()
    expect(activeSessionId()).toBe("chat-newest")

    pickFiles([file("later.jpg")])

    await waitFor(() => expect(attachments("chat-newest")[0]?.status).toBe("ready"))
    expect(creates()).toEqual([])
    expect(uploads()).toHaveLength(1)
  })

  it("uploads nothing and shows the file as not attached when the create fails", async () => {
    createFails = true
    await renderSettled()
    const draftId = activeSessionId()

    pickFiles([file("receipt.jpg")])

    await waitFor(() => expect(attachments(draftId)[0]?.status).toBe("error"))
    await new Promise((r) => setTimeout(r, 50))

    // No bytes were offered to an endpoint that has no row to put them in…
    expect(uploads()).toEqual([])
    // …the chip says so in words, on screen, after the toast is gone…
    const chipEl = screen.getByText("receipt.jpg").closest("[data-status]")!
    expect(chipEl.getAttribute("data-status")).toBe("error")
    expect(chipEl.textContent).toMatch(/not attached/i)
    // …and nothing carries a path a message could name.
    expect(attachments(draftId)[0].path).toBeFalsy()
    expect(toastError).toHaveBeenCalled()
  })
})
