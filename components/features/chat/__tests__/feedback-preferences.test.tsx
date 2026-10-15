import React from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { TurnPreferenceFeedback } from "../turn-preference-feedback"
import { PreferencesTab } from "../right-panel-tabs/preferences-tab"
import { useFeedbackStore } from "@/stores/feedback-store"

const apiFetch = vi.fn()
let currentUser = "user-1"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock("@/hooks/use-auth", () => ({ useSession: () => ({ data: { user: { id: currentUser } } }) }))
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }))

const props = { agentId: "agent-1", workspaceId: "ws-1", userId: "user-1", messageId: "message-1", signal: "helpful" as const }
const reason = "Next time, keep answers concise."
const entry = { id: "entry-1", revision: 3, feedback_id: "feedback-1", source_revision: 2, chat_id: "chat-1", message_id: "message-1", reason, value: "prefers concise answers", status: "saved", source_type: "feedback_reason" }
const response = (body: unknown, status = 200) => new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
function mount(ui: React.ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
  return { client, ...render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>) }
}
beforeEach(() => { currentUser = "user-1"; apiFetch.mockReset(); useFeedbackStore.getState().setUser(null); useFeedbackStore.getState().setUser(currentUser) })
afterEach(cleanup)

describe("Optional explicit feedback preferences", () => {
  it("sends nothing until the user selects a visible future preference, then shows server status", async () => {
    const row = { id: "feedback-1", workspace_id: "ws-1", user_id: "user-1", message_id: "message-1", signal: "helpful", reason: "", revision: 1, memory: { status: "feedback_only", entry_id: "entry-1" } }
    apiFetch.mockImplementation(async (_url: string, init?: RequestInit) => {
      if (init?.method === "POST") { const body = JSON.parse(String(init.body)); row.reason = body.reason; row.memory.status = "processing"; return response({ id: row.id, revision: 2, memory: row.memory }, 201) }
      return response({ feedback: [row] })
    })
    mount(<TurnPreferenceFeedback {...props} />)
    expect(apiFetch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: /What should change/ }))
    expect(await screen.findByText("Feedback only")).toBeInTheDocument()
    expect(apiFetch.mock.calls.every(([, init]) => !init?.method)).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: reason }))
    expect(await screen.findByText("Processing")).toBeInTheDocument()
    const post = apiFetch.mock.calls.find(([, init]) => init?.method === "POST")
    expect(JSON.parse(post![1].body)).toMatchObject({ message_id: "message-1", signal: "helpful", reason })
    row.memory.status = "saved"
    fireEvent.click(screen.getByRole("button", { name: "Refresh status" }))
    expect(await screen.findByText(/Remembered for your next private chat/)).toBeInTheDocument()
  })

  for (const [status, label] of [["disabled", "Memory off"], ["not_eligible", "Not eligible for memory"]]) {
    it(`shows ${status} without claiming the preference was remembered`, async () => {
      apiFetch.mockImplementation(async (_url: string, init?: RequestInit) => response(init?.method === "POST" ? { id: "f" } : { feedback: [{ id: "f", workspace_id: "ws-1", user_id: "user-1", signal: "helpful", reason, memory: { status } }] }))
      mount(<TurnPreferenceFeedback {...props} />)
      fireEvent.click(screen.getByRole("button", { name: /What should change/ }))
      fireEvent.click(await screen.findByRole("button", { name: reason }))
      expect(await screen.findByText(new RegExp(label))).toBeInTheDocument()
      expect(screen.queryByText(/Remembered/)).toBeNull()
    })
  }

  it("does not claim remembered after a failed preference request", async () => {
    apiFetch.mockImplementation(async (_url: string, init?: RequestInit) => response(init?.method === "POST" ? { error: "storage unavailable" } : { feedback: [] }, init?.method === "POST" ? 500 : 200))
    mount(<TurnPreferenceFeedback {...props} />)
    fireEvent.click(screen.getByRole("button", { name: /What should change/ }))
    fireEvent.click(await screen.findByRole("button", { name: reason }))
    expect(await screen.findByText("Unable to verify memory status.")).toBeInTheDocument()
    expect(screen.queryByText(/Remembered/)).toBeNull()
  })
})

describe("Agent preferences self-service", () => {
  it("shows source evidence and sends current revision for correction and deletion", async () => {
    let saved = [{ ...entry }]
    apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.includes("peer-consent")) return response({ opted_out: false })
      if (init?.method === "PATCH") { saved = [{ ...entry, reason: "Next time, start with the conclusion.", revision: 4 }]; return response(null, 204) }
      if (init?.method === "DELETE") { saved = []; return response(null, 204) }
      return response({ preferences: saved })
    })
    mount(<PreferencesTab agentId="agent-1" workspaceId="ws-1" />)
    expect(await screen.findByText(reason)).toBeInTheDocument()
    fireEvent.click(screen.getByText("Source"))
    expect(screen.getByText(/Feedback feedback-1, source revision 2/)).toBeVisible()
    fireEvent.click(screen.getByRole("button", { name: "Change preference" }))
    fireEvent.click(screen.getByRole("button", { name: "Next time, start with the conclusion." }))
    await waitFor(() => expect(apiFetch.mock.calls.find(([, init]) => init?.method === "PATCH")).toBeDefined())
    expect(JSON.parse(apiFetch.mock.calls.find(([, init]) => init?.method === "PATCH")![1].body)).toMatchObject({ entry_id: "entry-1", revision: 3 })
    await screen.findByText("Next time, start with the conclusion.")
    fireEvent.click(screen.getByRole("button", { name: /^Forget$/ }))
    fireEvent.click(screen.getByRole("button", { name: /^Forget preference$/ }))
    expect(await screen.findByText("No preferences saved for this agent yet.")).toBeInTheDocument()
    const deletion = apiFetch.mock.calls.find(([, init]) => init?.method === "DELETE")
    expect(deletion?.[0]).toContain("/peers/user-1?workspace_id=ws-1&entry_id=entry-1&revision=4")
  })

  it("does not reuse another account's cached preferences", async () => {
    apiFetch.mockImplementation(async (url: string) => response(url.includes("peer-consent") ? { opted_out: false } : { preferences: currentUser === "user-1" ? [entry] : [] }))
    const { client, rerender } = mount(<PreferencesTab agentId="agent-1" workspaceId="ws-1" />)
    expect(await screen.findByText(reason)).toBeInTheDocument()
    currentUser = "user-2"
    rerender(<QueryClientProvider client={client}><PreferencesTab agentId="agent-1" workspaceId="ws-1" /></QueryClientProvider>)
    expect(screen.queryByText(reason)).toBeNull()
    expect(await screen.findByText("No preferences saved for this agent yet.")).toBeInTheDocument()
  })
})
