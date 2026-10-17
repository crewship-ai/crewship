import React from "react"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import fixture from "@/internal/api/testdata/chat-e2/proposal-draft.json"
import { ProposalRefSchema } from "@/lib/chat-e2-cards"

const state = vi.hoisted(() => ({ mobile: false }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => state.mobile }))
const chat = vi.hoisted(() => ({
  turns: [{ id: "trigger-message", role: "user", parts: [{ type: "text", content: "Plan the work" }], timestamp: new Date(), isStreaming: false },
    { id: "reply", role: "assistant", runId: "entity-run", parts: [{ type: "text", content: "A proposal" }], timestamp: new Date(), isStreaming: false }],
  sendMessage: vi.fn(), stopGeneration: vi.fn(), regenerateLastTurn: vi.fn(), editAndResend: vi.fn(),
  loadHistory: vi.fn(), markHistoryUnavailable: vi.fn(), resubscribeSession: vi.fn(), isStreaming: false, connectionStatus: "connected",
}))
vi.mock("@/hooks/use-chat", () => ({ useChat: () => chat }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "viewer" } } }), useSession: () => ({ data: { user: { id: "viewer" } } }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "test-workspace-id", loading: false, workspaces: [] }) }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: ({ children }: { children: React.ReactNode }) => children }))
vi.mock("../right-panel", () => ({ RightPanel: () => <p>Canonical Work</p> }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../composer/chat-composer", () => ({ ChatComposer: () => null }))
vi.mock("../turn-renderer", () => ({ TurnRenderer: ({ entityCards }: { entityCards: React.ReactNode }) => <div>{entityCards}</div> }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))

import { ChatPanel } from "../chat-panel"
import { useDrawerStore } from "@/stores/drawer-store"
const base = ProposalRefSchema.parse(fixture)
let current = base
let readStatus = 200
const props = { agentId: "entity-agent", agentSlug: "jamie", sessionId: "chat", agentName: "Jamie", askForms: null }
beforeEach(() => {
  current = base; readStatus = 200; state.mobile = false
  chat.sendMessage.mockReset().mockReturnValue(true)
  useDrawerStore.setState({ open: true, activeTab: "work" })
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const path = String(url)
    let body: unknown = []
    let status = 200
    if (path.includes("/execution-profile")) body = { mode: "trusted" }
    else if (path.includes("/messages")) body = { messages: [] }
    else if (path.includes("/participants")) body = { participants: [] }
    else if (path.includes("/proposals?")) {
      status = readStatus
      body = status === 200 ? { contract_version: 1, reset: true, snapshot_id: "snapshot", next_cursor: "next", snapshot_complete: true, has_more: false, refs: [current] } : {}
    } else if (path.includes("/entity-refs?")) body = { contract_version: 1, reset: true, snapshot_id: "issues", next_cursor: "next", snapshot_complete: true, has_more: false, refs: [] }
    return new Response(JSON.stringify(body), { status })
  }))
})
afterEach(() => { cleanup(); vi.unstubAllGlobals(); sessionStorage.clear() })
function setup(mobile = false, onSend?: (sessionId: string, text: string) => void, agentSlug = props.agentSlug) {
  state.mobile = mobile
  const client = new QueryClient()
  render(<QueryClientProvider client={client}><ChatPanel {...props} agentSlug={agentSlug} onSend={onSend} mobilePanel={mobile ? "chat" : undefined} /></QueryClientProvider>)
  const refresh = () => act(async () => { await client.invalidateQueries({ queryKey: ["chat-e2-cards"] }) })
  return { refresh }
}

it("mounts the real card/panel with separate drafts, keeps focused fields through refresh/outage, and evicts revoked data", async () => {
  const { refresh } = setup()
  const title = await screen.findByLabelText("Issue 1 title")
  fireEvent.change(title, { target: { value: "Card draft" } })
  fireEvent.change(screen.getByLabelText("Comment to agent"), { target: { value: "Separate comment" } })
  const opener = screen.getByRole("button", { name: "Open on right" })
  opener.focus(); fireEvent.click(opener)
  const panel = await screen.findByRole("region", { name: "Proposal detail" })
  await waitFor(() => expect(within(panel).getByRole("button", { name: "Back to Work" })).toHaveFocus())
  const panelTitle = within(panel).getByLabelText("Issue 1 title")
  expect(panelTitle).toHaveValue("Profile API")
  panelTitle.focus(); fireEvent.change(panelTitle, { target: { value: "Panel draft" } })
  current = { ...base, proposal: { ...base.proposal, version: 2, title: "Agent revised proposal" } }
  await refresh()
  expect(within(panel).getByLabelText("Issue 1 title")).toBe(panelTitle)
  expect(panelTitle).toHaveValue("Panel draft")
  expect(panelTitle).toHaveFocus()
  expect(title).toHaveValue("Card draft")
  readStatus = 500
  await refresh()
  expect(panelTitle).toHaveFocus()
  expect(title).toHaveValue("Card draft")
  expect(within(panel).getByRole("button", { name: "Discard proposal" })).toBeDisabled()
  readStatus = 403
  await refresh()
  await waitFor(() => expect(screen.queryByRole("region", { name: "Proposal detail" })).toBeNull())
  expect(screen.queryByLabelText("Issue 1 title")).toBeNull()
  expect(Object.keys(sessionStorage).some(key => key.startsWith("chat-proposal:"))).toBe(false)
  readStatus = 500
  await refresh()
  expect(screen.queryByLabelText("Issue 1 title")).toBeNull()
})

it("opens the same proposal detail in the mobile sheet and returns focus on close", async () => {
  setup(true)
  const opener = await screen.findByRole("button", { name: "Open on right" })
  opener.focus(); fireEvent.click(opener)
  const dialog = await screen.findByRole("dialog", { name: "Proposal detail" })
  expect(within(dialog).getByLabelText("Issue 1 title")).toHaveValue("Profile API")
  fireEvent.click(within(dialog).getByRole("button", { name: "Back to Work" }))
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Proposal detail" })).toBeNull())
  await waitFor(() => expect(opener).toHaveFocus())
})

it("addresses proposal comments to the canonical agent through the real card and sender", async () => {
  const onSend = vi.fn()
  setup(false, onSend)
  const field = await screen.findByLabelText("Comment to agent")
  fireEvent.change(field, { target: { value: "Please revise the first step" } })
  fireEvent.click(screen.getByRole("button", { name: "Send comment to agent" }))
  const expected = `@jamie Regarding proposal ${base.entity.id}:\nPlease revise the first step`
  await waitFor(() => expect(chat.sendMessage).toHaveBeenCalledExactlyOnceWith(expected))
  expect(onSend).toHaveBeenCalledExactlyOnceWith("chat", expected)
  await waitFor(() => expect(field).toHaveValue(""))
})

it("keeps an agent-directed comment draft when the sender rejects it", async () => {
  chat.sendMessage.mockReturnValue(false)
  const onSend = vi.fn()
  setup(false, onSend)
  const field = await screen.findByLabelText("Comment to agent")
  fireEvent.change(field, { target: { value: "Keep this requested revision" } })
  fireEvent.click(screen.getByRole("button", { name: "Send comment to agent" }))
  await screen.findByText("Could not send. Your comment draft is kept.")
  expect(field).toHaveValue("Keep this requested revision")
  expect(onSend).not.toHaveBeenCalled()
})

it("keeps the comment when no canonical agent target is available", async () => {
  setup(false, undefined, "")
  const field = await screen.findByLabelText("Comment to agent")
  fireEvent.change(field, { target: { value: "Do not send without a target" } })
  fireEvent.click(screen.getByRole("button", { name: "Send comment to agent" }))
  await screen.findByText("Could not send. Your comment draft is kept.")
  expect(field).toHaveValue("Do not send without a target")
  expect(chat.sendMessage).not.toHaveBeenCalled()
})
