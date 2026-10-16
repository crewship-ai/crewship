import React from "react"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import fixture from "@/internal/api/testdata/chat-e2/decision-escalation-pending.json"
import { DecisionRefSchema } from "@/lib/chat-e2-cards"

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
vi.mock("../right-panel-tabs/agent-work-tab", () => ({ AgentWorkTab: () => <p>Canonical Work</p> }))
vi.mock("../right-panel-tabs/agent-artifacts-tab", () => ({ AgentArtifactsTab: () => null }))
vi.mock("../right-panel-tabs/preferences-tab", () => ({ PreferencesTab: () => null }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../composer/chat-composer", () => ({ ChatComposer: () => null }))
vi.mock("../turn-renderer", () => ({ TurnRenderer: ({ entityCards }: { entityCards: React.ReactNode }) => <div>{entityCards}</div> }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))

import answeredFixture from "@/internal/api/testdata/chat-e2/decision-escalation-answered.json"
import { ChatPanel } from "../chat-panel"
import { useDrawerStore } from "@/stores/drawer-store"
const base = DecisionRefSchema.parse(fixture)
const answered = DecisionRefSchema.parse(answeredFixture)
let current = base
let readStatus = 200
let writeStatus = 200
let profile = "trusted"
let calls: { url: string; body: string; key: string | null }[] = []
const props = { agentId: "entity-agent", agentSlug: "jamie", sessionId: "chat", agentName: "Jamie", askForms: null }
beforeEach(() => {
  current = base; readStatus = 200; writeStatus = 200; profile = "trusted"; calls = []; state.mobile = false
  useDrawerStore.setState({ open: true, activeTab: "work" })
  vi.stubGlobal("fetch", vi.fn(async (url: string, options?: RequestInit) => {
    const path = String(url)
    let body: unknown = []
    let status = 200
    if (path.includes("/execution-profile")) body = { mode: profile }
    else if (path.includes("/messages")) body = { messages: [] }
    else if (path.includes("/participants")) body = { participants: [] }
    else if (path.includes("/restricted-files")) body = { files: [] }
    else if (path.includes("/restricted-context")) body = { entries: [] }
    else if (path.includes("/answer?")) {
      calls.push({ url: path, body: String(options?.body), key: new Headers(options?.headers).get("Idempotency-Key") })
      status = writeStatus
      if (status === 200) { current = answered; body = answered }
    } else if (path.includes("/decisions?")) {
      status = readStatus
      body = status === 200 ? { contract_version: 1, reset: true, snapshot_id: "snapshot", next_cursor: "next", snapshot_complete: true, has_more: false, refs: [current] } : {}
    } else if (path.includes("/entity-refs?") || path.includes("/proposals?")) body = { contract_version: 1, reset: true, snapshot_id: "empty", next_cursor: "next", snapshot_complete: true, has_more: false, refs: [] }
    return new Response(JSON.stringify(body), { status })
  }))
})
afterEach(() => { cleanup(); vi.unstubAllGlobals(); sessionStorage.clear() })
function setup(mobilePanel?: "chat" | "work") {
  state.mobile = !!mobilePanel
  const client = new QueryClient()
  const view = render(<QueryClientProvider client={client}><ChatPanel {...props} mobilePanel={mobilePanel} /></QueryClientProvider>)
  const refresh = () => act(async () => { await client.invalidateQueries({ queryKey: ["chat-decisions"] }) })
  return { ...view, refresh }
}

it("uses the same source in chat and Work, keeps the focused draft on 500, and reflects an Inbox answer", async () => {
  const { refresh } = setup()
  const card = await screen.findByRole("region", { name: "Decision in chat" })
  const waiting = await screen.findByRole("region", { name: "Waiting for you" })
  fireEvent.change(within(card).getByLabelText(/Run now/), { target: { value: "later" } })
  const opener = within(waiting).getByRole("button", { name: base.decision.title })
  opener.focus(); fireEvent.click(opener)
  const panel = await screen.findByRole("region", { name: "Decision detail" })
  await waitFor(() => expect(within(panel).getByRole("button", { name: "Back to Work" })).toHaveFocus())
  const note = within(panel).getByLabelText("Note") as HTMLTextAreaElement
  note.focus(); fireEvent.change(note, { target: { value: "Keep this draft" } }); note.setSelectionRange(4,4)
  readStatus = 500
  await refresh()
  expect(note).toHaveFocus(); expect(note.selectionStart).toBe(4)
  expect(note).toHaveValue("Keep this draft")
  await waitFor(() => expect(within(panel).getByRole("button", { name: "Send answer" })).toBeDisabled())
  expect(note).toBeEnabled()
  expect(note).toHaveFocus()
  expect(note.selectionStart).toBe(4)
  readStatus = 200; current = answered
  await refresh()
  await waitFor(() => expect(within(panel).getByRole("status")).toHaveTextContent("answered"))
  expect(within(panel).queryByLabelText("Note")).toBeNull()
  fireEvent.click(within(panel).getByRole("button", { name: "Back to Work" }))
  await waitFor(() => expect(screen.queryByRole("region", { name: "Waiting for you" })).toBeNull())
  expect(within(card).getByRole("status")).toHaveTextContent("answered")
  expect(calls).toHaveLength(0)
})

it("retries the original source/body/key after reload, without an automatic second response", async () => {
  writeStatus = 500
  const first = setup()
  const card = await screen.findByRole("region", { name: "Decision in chat" })
  fireEvent.change(within(card).getByLabelText(/Run now/), { target: { value: "later" } })
  fireEvent.click(within(card).getByRole("button", { name: "Send answer" }))
  await within(card).findByText("The outcome is unknown. Your response is kept.")
  expect(calls).toHaveLength(1)
  const initial = calls[0]
  first.unmount()
  writeStatus = 200
  setup()
  const restored = await screen.findByRole("region", { name: "Decision in chat" })
  expect(calls).toHaveLength(1)
  fireEvent.click(within(restored).getByRole("button", { name: "Retry the same response" }))
  await waitFor(() => expect(within(restored).getByRole("status")).toHaveTextContent("answered"))
  expect(calls).toEqual([initial, initial])
  expect(initial.key).toBeTruthy()
  expect(JSON.parse(initial.body)).toMatchObject({ source_id: base.decision.source.id, expected_version: "1", values: { timing: "later" } })
  expect(Object.keys(sessionStorage).some(key => key.startsWith("chat-decision:"))).toBe(false)
})

it("clears a denied source and detail immediately after answer denial, even when the following list fails", async () => {
  setup()
  const card = await screen.findByRole("region", { name: "Decision in chat" })
  fireEvent.click(within(card).getByRole("button", { name: "Open on right" }))
  const panel = await screen.findByRole("region", { name: "Decision detail" })
  fireEvent.change(within(panel).getByLabelText(/Run now/), { target: { value: "later" } })
  writeStatus = 403; readStatus = 500
  fireEvent.click(within(panel).getByRole("button", { name: "Send answer" }))
  await waitFor(() => expect(screen.queryByRole("region", { name: "Decision detail" })).toBeNull())
  expect(screen.queryByRole("region", { name: "Decision in chat" })).toBeNull()
  expect(screen.queryByRole("region", { name: "Waiting for you" })).toBeNull()
  expect(Object.keys(sessionStorage).some(key => key.startsWith("chat-decision:"))).toBe(false)
})

it("opens the decision from mobile Work and returns keyboard focus on close", async () => {
  setup("work")
  const waiting = await screen.findByRole("region", { name: "Waiting for you" })
  const opener = within(waiting).getByRole("button", { name: base.decision.title })
  opener.focus(); fireEvent.click(opener)
  const dialog = await screen.findByRole("dialog", { name: "Decision detail" })
  expect(within(dialog).getByLabelText(/Run now/)).toBeVisible()
  fireEvent.click(within(dialog).getByRole("button", { name: "Back to Work" }))
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Decision detail" })).toBeNull())
  await waitFor(() => expect(opener).toHaveFocus())
})

it.each(["pending", "restricted"])("does not fetch decision sources for %s execution", async mode => {
  profile = mode
  setup()
  await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).includes("execution-profile"))).toBe(true))
  await act(async () => { await new Promise(resolve => setTimeout(resolve,10)) })
  expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).includes("/decisions?"))).toBe(false)
  expect(screen.queryByRole("region", { name: "Waiting for you" })).toBeNull()
})
