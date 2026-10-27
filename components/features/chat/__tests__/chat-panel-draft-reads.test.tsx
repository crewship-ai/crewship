import React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// #4369 — a conversation the page minted locally has no `chats` row until its
// first send, so reading it is asking for a resource that does not exist. The
// panel used to probe the execution profile, the participants and the history
// of the draft id on mount; all three answered 404. A draft makes none of
// them, and the reads that matter after the first send still happen.
// =============================================================================

const sendMessage = vi.fn()
const loadHistory = vi.fn()

vi.mock("@/hooks/use-chat", async () => {
  const React = await import("react")
  return {
    useChat: (opts: { executionProfile?: string }) => {
      const [status, setStatus] = React.useState<string>("disconnected")
      const profile = opts.executionProfile
      React.useEffect(() => {
        if (profile !== "trusted") { setStatus("disconnected"); return }
        const t = setTimeout(() => setStatus("connected"), 10)
        return () => clearTimeout(t)
      }, [profile])
      return {
        turns: [], sendMessage, stopGeneration: vi.fn(), regenerateLastTurn: vi.fn(), editAndResend: vi.fn(),
        loadHistory, markHistoryUnavailable: vi.fn(), markHistoryDenied: vi.fn(), resubscribeSession: vi.fn(), isStreaming: false,
        connectionStatus: status,
      }
    },
  }
})
vi.mock("@/hooks/use-auth", () => ({
  useSessionSafe: () => ({ data: { user: { id: "user-1" } } }), useSession: () => ({ data: { user: { id: "user-1" } } }),
}))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test", loading: false, workspaces: [] }) }))
vi.mock("../right-panel", () => ({ RightPanel: () => null }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: () => null }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../search/conversation-search", () => ({ ConversationSearch: () => null }))
vi.mock("../export/export-dialog", () => ({ ExportDialog: () => null }))
vi.mock("../composer/mention-autocomplete", () => ({ MentionAutocomplete: () => null }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn(), warning: vi.fn(), message: vi.fn() } }))

import { ChatPanel } from "../chat-panel"

const panelProps = { agentId: "agent-1", sessionId: "draft-1", agentName: "Riley", agentSlug: "riley", agentRole: "Data Analyst", askForms: null }

let rows: Set<string>
let calls: string[]
/** Chat-scoped requests the server answered 404 — the bug's symptom. */
let notFound: string[]

function installFetch() {
  global.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url)
    const method = (init?.method ?? "GET").toUpperCase()
    calls.push(`${method} ${u}`)
    const json = (status: number, body: unknown) => ({ ok: status < 400, status, json: async () => body }) as unknown as Response
    const chatId = u.includes("/chats/") ? decodeURIComponent(u.split("/chats/")[1].split(/[/?]/)[0]) : null
    if (chatId && !rows.has(chatId)) { notFound.push(`${method} ${u}`); return json(404, { error: "Chat not found" }) }
    if (u.includes("/execution-profile")) return json(200, { mode: "trusted" })
    if (u.includes("/messages")) return json(200, { messages: [] })
    if (u.includes("/participants")) return json(200, { participants: [] })
    if (u.includes("/chats") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}"))
      rows.add(body.session_id)
      return json(201, { id: body.session_id })
    }
    return json(200, [])
  }) as unknown as typeof fetch
}

const chatReads = (id: string) => calls.filter((c) => c.startsWith("GET ") && c.includes(`/api/v1/chats/${id}/`))
const readsOf = (id: string, path: string) => chatReads(id).filter((c) => c.includes(`/chats/${id}/${path}?`))

describe("ChatPanel — a draft is not read before it exists (#4369)", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    rows = new Set()
    calls = []
    notFound = []
    installFetch()
  })

  it("requests no chat-scoped resource for a draft before the first send", async () => {
    render(<ChatPanel {...panelProps} draft />)
    const input = await screen.findByRole("textbox")
    fireEvent.change(input, { target: { value: "hello" } })
    // The composer offers the first send without having probed anything.
    await waitFor(() => expect(screen.getByRole("button", { name: /submit/i })).not.toBeDisabled())
    // History counts as settled (empty) so the stream gate opens.
    await waitFor(() => expect(loadHistory).toHaveBeenCalledWith([]))
    expect(chatReads("draft-1")).toEqual([])
    expect(calls.filter((c) => c.includes("/chats/draft-1/"))).toEqual([])
    expect(notFound).toEqual([])
  })

  it("reads the profile and participants once the first send has created the row", async () => {
    render(<ChatPanel {...panelProps} draft />)
    const input = await screen.findByRole("textbox")
    fireEvent.change(input, { target: { value: "hello" } })
    const submit = screen.getByRole("button", { name: /submit/i })
    await waitFor(() => expect(submit).not.toBeDisabled())
    fireEvent.click(submit)

    await waitFor(() => expect(sendMessage).toHaveBeenCalledWith("hello"))
    const created = calls.findIndex((c) => c.startsWith("POST ") && c.includes("/agents/agent-1/chats"))
    expect(created).toBeGreaterThanOrEqual(0)
    expect(calls.findIndex((c) => c.includes("/chats/draft-1/"))).toBeGreaterThan(created)
    await waitFor(() => expect(readsOf("draft-1", "participants")).toHaveLength(1))
    expect(readsOf("draft-1", "execution-profile")).toHaveLength(1)
    // Every read came after the row existed, so none of them answered 404.
    expect(notFound).toEqual([])
  })

  it("still reads an existing conversation on mount", async () => {
    rows.add("sess-1")
    render(<ChatPanel {...panelProps} sessionId="sess-1" />)
    await waitFor(() => {
      expect(readsOf("sess-1", "execution-profile").length).toBeGreaterThan(0)
      expect(readsOf("sess-1", "participants").length).toBeGreaterThan(0)
      expect(readsOf("sess-1", "messages").length).toBeGreaterThan(0)
    })
  })
})
