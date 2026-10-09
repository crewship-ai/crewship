import React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent, act } from "@testing-library/react"

// =============================================================================
// #2898 — the first send of a draft conversation used to deadlock.
//
// A draft has no `chats` row, so its execution-profile probe answers 404 and
// the profile stays "pending". Realtime is off while the profile is pending,
// so the socket never opens, the composer saw a connection that was not
// "connected", and it disabled Send — the one action that would have created
// the row and resolved the profile.
//
// The useChat stand-in below behaves like the real hook in the part that
// matters: the socket is closed while the profile is pending, opens a moment
// after the profile resolves to trusted, and restricted sends report
// connected because they are HTTP.
// =============================================================================

const sendMessage = vi.fn()
const resubscribeSession = vi.fn()
const socket = vi.hoisted(() => ({ outcome: "connected" as "connected" | "error" | "never", delayMs: 30 }))

vi.mock("@/hooks/use-chat", async () => {
  const React = await import("react")
  return {
    useChat: (opts: { executionProfile?: string; sessionId: string }) => {
      const [status, setStatus] = React.useState<string>("disconnected")
      const profile = opts.executionProfile
      React.useEffect(() => {
        if (profile !== "trusted") { setStatus("disconnected"); return }
        if (socket.outcome === "never") { setStatus("connecting"); return }
        setStatus("connecting")
        const t = setTimeout(() => setStatus(socket.outcome), socket.delayMs)
        return () => clearTimeout(t)
      }, [profile])
      return {
        turns: [], sendMessage, stopGeneration: vi.fn(), regenerateLastTurn: vi.fn(), editAndResend: vi.fn(),
        loadHistory: vi.fn(), markHistoryUnavailable: vi.fn(), resubscribeSession, isStreaming: false,
        connectionStatus: profile === "restricted" ? "connected" : status,
      }
    },
  }
})
vi.mock("@/hooks/use-auth", () => ({
  useSessionSafe: () => ({ data: { user: { id: "user-1" } } }), useSession: () => ({ data: { user: { id: "user-1" } } }),
}))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test", loading: false }) }))
vi.mock("../right-panel", () => ({ RightPanel: () => null }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: () => null }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../search/conversation-search", () => ({ ConversationSearch: () => null }))
vi.mock("../export/export-dialog", () => ({ ExportDialog: () => null }))
vi.mock("../composer/mention-autocomplete", () => ({ MentionAutocomplete: () => null }))
const { toastError } = vi.hoisted(() => ({ toastError: vi.fn() }))
vi.mock("sonner", () => ({ toast: { error: toastError, success: vi.fn(), info: vi.fn(), warning: vi.fn(), message: vi.fn() } }))

import { ChatPanel } from "../chat-panel"

const panelProps = { agentId: "agent-1", sessionId: "draft-1", agentName: "Riley", agentSlug: "riley", agentRole: "Data Analyst", askForms: null }

/** Rows that exist. A draft's profile is 404 until its create POST lands. */
let rows: Set<string>
/** The profile a created row resolves to; null means the host refuses it. */
let createdProfile: "trusted" | "restricted" | null
let createStatus: number
let creates: string[]
let holdCreate: Promise<void> | null

function installFetch() {
  global.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url)
    const method = (init?.method ?? "GET").toUpperCase()
    const json = (status: number, body: unknown) => ({ ok: status < 400, status, json: async () => body }) as unknown as Response
    if (u.includes("/execution-profile")) {
      const id = decodeURIComponent(u.split("/chats/")[1].split("/")[0])
      if (!rows.has(id) || !createdProfile) return json(404, { error: "not found" })
      return json(200, { mode: createdProfile })
    }
    if (u.includes("/messages")) return json(404, { error: "Chat not found" })
    if (u.includes("/participants")) return json(200, { participants: [] })
    if (u.includes("/chats") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}"))
      creates.push(body.session_id)
      if (holdCreate) await holdCreate
      if (createStatus >= 400) return json(createStatus, { error: "nope" })
      rows.add(body.session_id)
      return json(201, { id: body.session_id })
    }
    return json(200, [])
  }) as unknown as typeof fetch
}

async function typeAndSend(text: string) {
  const input = await screen.findByRole("textbox")
  fireEvent.change(input, { target: { value: text } })
  const submit = screen.getByRole("button", { name: /submit/i })
  await waitFor(() => expect(submit).not.toBeDisabled())
  fireEvent.click(submit)
  return { input, submit }
}

describe("ChatPanel — the first send of a draft (#2898)", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    rows = new Set()
    createdProfile = "trusted"
    createStatus = 201
    creates = []
    holdCreate = null
    socket.outcome = "connected"
    socket.delayMs = 30
    installFetch()
  })

  it("offers Send on a draft and sends once the row exists and the socket is open", async () => {
    render(<ChatPanel {...panelProps} />)
    const { input } = await typeAndSend("what changed yesterday?")

    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    expect(sendMessage).toHaveBeenCalledWith("what changed yesterday?")
    expect(creates).toEqual(["draft-1"])
    expect(toastError).not.toHaveBeenCalled()
    await waitFor(() => expect((input as HTMLTextAreaElement).value).toBe(""))
  })

  it("sends a restricted first message without waiting for a socket", async () => {
    createdProfile = "restricted"
    socket.outcome = "never"
    render(<ChatPanel {...panelProps} />)
    await typeAndSend("summarise the brief")

    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    expect(toastError).not.toHaveBeenCalled()
  })

  it("does not send when the created row resolves to no profile, and keeps the draft", async () => {
    createdProfile = null
    render(<ChatPanel {...panelProps} />)
    const { input } = await typeAndSend("hello")

    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1))
    expect(sendMessage).not.toHaveBeenCalled()
    expect((input as HTMLTextAreaElement).value).toBe("hello")
  })

  it("does not send when the create is refused", async () => {
    createStatus = 403
    render(<ChatPanel {...panelProps} />)
    await typeAndSend("hello")

    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1))
    expect(sendMessage).not.toHaveBeenCalled()
  })

  it("does not send when the socket fails to open after the profile resolves", async () => {
    socket.outcome = "error"
    render(<ChatPanel {...panelProps} />)
    const { input } = await typeAndSend("hello")

    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1))
    expect(sendMessage).not.toHaveBeenCalled()
    expect((input as HTMLTextAreaElement).value).toBe("hello")
  })

  it("sends once when Send is pressed again while the create and connection are pending", async () => {
    let release!: () => void
    holdCreate = new Promise<void>((resolve) => { release = resolve })
    render(<ChatPanel {...panelProps} />)
    const { submit } = await typeAndSend("once please")
    await waitFor(() => expect(creates).toHaveLength(1))

    fireEvent.click(submit)
    release()

    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    expect(creates).toHaveLength(1)
  })

  it("drops the send without a toast when the user switches conversations mid-create", async () => {
    let release!: () => void
    holdCreate = new Promise<void>((resolve) => { release = resolve })
    const { rerender } = render(<ChatPanel {...panelProps} />)
    await typeAndSend("for the first chat")
    await waitFor(() => expect(creates).toHaveLength(1))

    rerender(<ChatPanel {...panelProps} sessionId="draft-2" />)
    await act(async () => { release() })

    await new Promise((r) => setTimeout(r, 100))
    expect(sendMessage).not.toHaveBeenCalled()
    expect(toastError).not.toHaveBeenCalled()
  })

  it("keeps Send disabled while the profile probe is still in flight", async () => {
    let releaseProbe!: () => void
    const probe = new Promise<void>((resolve) => { releaseProbe = resolve })
    const base = global.fetch
    global.fetch = vi.fn(async (url: string, init?: RequestInit) => {
      if (String(url).includes("/execution-profile")) await probe
      return base(url, init)
    }) as unknown as typeof fetch
    render(<ChatPanel {...panelProps} />)
    const input = await screen.findByRole("textbox")
    fireEvent.change(input, { target: { value: "too early" } })
    expect(screen.getByRole("button", { name: /submit/i })).toBeDisabled()

    await act(async () => { releaseProbe() })
    await waitFor(() => expect(screen.getByRole("button", { name: /submit/i })).not.toBeDisabled())
  })
})
