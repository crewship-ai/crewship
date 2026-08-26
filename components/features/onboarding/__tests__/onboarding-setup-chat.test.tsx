import { readFileSync } from "fs"
import { join } from "path"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

import { OnboardingSetupChat } from "../onboarding-setup-chat"
import type { ChatTurn } from "@/hooks/use-chat"
import type { OnboardingProposal } from "../setup-agent-api"
import type { ProvisioningCrewState } from "@/hooks/use-provisioning-status"
// Shared character-scanner (blanks comments, keeps line numbers) so a
// `.not.toMatch` assertion against this component's own source can't be
// tricked by a comment that merely mentions the forbidden pattern — see its
// own doc comment in dead-agent-routes.test.ts for the false-negative this
// already caused once (agent-card.tsx, #agent-card-dead-link).
import { stripComments } from "../../../../app/(onboarding)/onboarding/__tests__/dead-agent-routes.test"

const { apiFetchMock } = vi.hoisted(() => ({ apiFetchMock: vi.fn() }))
vi.mock("@/lib/api-fetch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-fetch")>()),
  apiFetch: (...args: unknown[]) => apiFetchMock(...args),
}))

// hooks/use-chat.ts is the shared chat transport (streaming reassembly,
// reconnect/resume) — exactly what CLAUDE.md's "reuse, don't rebuild" asks
// for, and exactly what this test must NOT re-verify. Mocked at the module
// boundary so this file only asserts what onboarding-setup-chat.tsx itself
// is responsible for: rendering turns, materialising a proposal from a
// suggestion, calling the apply endpoint from exactly one place, and — the
// bug this file now also pins — never leaving a deferred send silent.
const useChatMock = vi.fn()
vi.mock("@/hooks/use-chat", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/use-chat")>("@/hooks/use-chat")
  return { ...actual, useChat: (...args: unknown[]) => useChatMock(...args) }
})

// TurnRenderer's assistant branch (via AssistantTurn's feedback row) reads
// the authenticated user to bind the reactions store — real hook throws
// without an AuthProvider, which this isolated render doesn't mount. Same
// stand-in chat-hotkey-ownership.test.tsx uses.
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ session: { user: { id: "user-1" } }, signOut: vi.fn() }),
  useSession: () => ({ data: { user: { id: "user-1" } } }),
}))

// Global module-level store (no provider needed) — stubbed to a fixed id so
// CrewProvisioningCard/AttachmentZone/RealtimeProvider don't each fire a real
// GET /api/v1/workspaces on mount (vitest.setup.ts fails the test on any
// unmocked network call escaping it).
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))

// AgentAvatar's own render is pure (generates an inline data: URI from the
// seed), but it also fire-and-forgets a backfill POST when given an agentId
// with no stored render — exactly what the header's real-agent avatar does.
// Stubbed for the same reason as useWorkspace above: no real fetch in a unit
// test, even a fire-and-forget one.
vi.mock("@/lib/agent-avatar-persist", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/agent-avatar-persist")>()),
  queueAvatarBackfill: vi.fn(),
}))

// The onboarding route has no RealtimeProvider in its layout (only the
// dashboard route mounts one); onboarding-setup-chat.tsx supplies its own so
// useProvisioningStatus (called both by this component and by the
// CrewProvisioningCard TurnRenderer renders) doesn't throw. That provider's
// own behaviour (WS connect/reconnect) is hooks/use-realtime.tsx's business,
// not this component's — stood in for here the same way the real chat's own
// composer tests stand in for AttachmentZone.
vi.mock("@/hooks/use-realtime", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/use-realtime")>("@/hooks/use-realtime")
  return {
    ...actual,
    RealtimeProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  }
})

// Controls what the crew-provisioning poll reports, independent of the chat
// transport — this is how the tests drive "the build finished" without a
// real WebSocket or a real crews API.
const useProvisioningStatusMock = vi.fn()
vi.mock("@/hooks/use-provisioning-status", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/use-provisioning-status")>("@/hooks/use-provisioning-status")
  return { ...actual, useProvisioningStatus: (...args: unknown[]) => useProvisioningStatusMock(...args) }
})

function mockProvisioning(detail: Partial<ProvisioningCrewState>[] = []) {
  useProvisioningStatusMock.mockReturnValue({
    needsProvision: 0,
    building: 0,
    failed: 0,
    pendingRestart: 0,
    recentlyCompleted: 0,
    total: 0,
    detail: detail as ProvisioningCrewState[],
    acknowledge: vi.fn(),
  })
}

// setup-agent-api's own parsing/mapping logic is real (already covered by
// setup-agent-api.test.ts); only the three network calls are stubbed.
const startSetupAgentSessionMock = vi.fn()
const createOnboardingProposalMock = vi.fn()
const applyOnboardingProposalMock = vi.fn()
vi.mock("../setup-agent-api", async () => {
  const actual = await vi.importActual<typeof import("../setup-agent-api")>("../setup-agent-api")
  return {
    ...actual,
    startSetupAgentSession: (...args: unknown[]) => startSetupAgentSessionMock(...args),
    createOnboardingProposal: (...args: unknown[]) => createOnboardingProposalMock(...args),
    applyOnboardingProposal: (...args: unknown[]) => applyOnboardingProposalMock(...args),
  }
})

function turn(overrides: Partial<ChatTurn> & Pick<ChatTurn, "role">): ChatTurn {
  return {
    id: overrides.id ?? Math.random().toString(36),
    role: overrides.role,
    parts: overrides.parts ?? [],
    isStreaming: overrides.isStreaming ?? false,
    timestamp: overrides.timestamp ?? new Date(),
    ...overrides,
  }
}

const SUGGESTION_METADATA = {
  onboarding_proposal_suggestion: {
    crew_name: "Seznam Listing Scraper",
    template_slug: "software-development",
    llm_model: "claude-sonnet-5",
  },
}

const PROPOSAL: OnboardingProposal = {
  id: "prop_1",
  crewName: "Seznam Listing Scraper",
  crewSlug: "seznam-listing-scraper",
  templateSlug: "software-development",
  agents: [
    { name: "Scraper Lead", role: "Lead", model: "claude-sonnet-5" },
    { name: "Data Cleaner", role: "Engineer", model: "claude-sonnet-5" },
  ],
  egressDomains: [],
  status: "PENDING",
}

function mockUseChat(turns: ChatTurn[], overrides: Partial<ReturnType<typeof import("@/hooks/use-chat").useChat>> = {}) {
  const sendMessage = vi.fn()
  useChatMock.mockReturnValue({
    turns,
    messages: [],
    sendMessage,
    stopGeneration: vi.fn(),
    regenerateLastTurn: vi.fn(),
    editAndResend: vi.fn(),
    loadHistory: vi.fn(),
    markHistoryUnavailable: vi.fn(),
    resubscribeSession: vi.fn(),
    isStreaming: false,
    connectionStatus: "connected",
    ...overrides,
  })
  return sendMessage
}

beforeEach(() => {
  useChatMock.mockReset()
  useProvisioningStatusMock.mockReset()
  mockProvisioning([])
  startSetupAgentSessionMock.mockReset()
  createOnboardingProposalMock.mockReset()
  applyOnboardingProposalMock.mockReset()
  apiFetchMock.mockReset()
  apiFetchMock.mockResolvedValue({
    ok: true,
    json: async () => ({ messages: [] }),
  })
})

describe("OnboardingSetupChat — falling back when the setup agent is unavailable", () => {
  it("shows a loading state, then calls onUnavailable(reason) when the session cannot start", async () => {
    startSetupAgentSessionMock.mockResolvedValue({ ok: false, reason: "unavailable" })
    const onUnavailable = vi.fn()
    render(<OnboardingSetupChat onUnavailable={onUnavailable} onProposalApplied={vi.fn()} />)
    expect(screen.getByText(/Waking up Crewship Guide/)).toBeTruthy()
    await waitFor(() => expect(onUnavailable).toHaveBeenCalledTimes(1))
    expect(onUnavailable).toHaveBeenCalledWith("unavailable")
  })

  // The task's own fallback requirement: a REAL failure (not the expected
  // "no credential yet" precondition) must still fire the same fallback the
  // UI falls back to the template grid on.
  it("calls onUnavailable('unavailable') on a real failure, not silently", async () => {
    startSetupAgentSessionMock.mockResolvedValue({ ok: false, reason: "unavailable" })
    const onUnavailable = vi.fn()
    render(<OnboardingSetupChat onUnavailable={onUnavailable} onProposalApplied={vi.fn()} />)
    await waitFor(() => expect(onUnavailable).toHaveBeenCalledWith("unavailable"))
    expect(useChatMock).not.toHaveBeenCalled()
  })

  it("calls onUnavailable('credential_required') when the workspace has no model token yet", async () => {
    startSetupAgentSessionMock.mockResolvedValue({ ok: false, reason: "credential_required" })
    const onUnavailable = vi.fn()
    render(<OnboardingSetupChat onUnavailable={onUnavailable} onProposalApplied={vi.fn()} />)
    await waitFor(() => expect(onUnavailable).toHaveBeenCalledWith("credential_required"))
    expect(useChatMock).not.toHaveBeenCalled()
  })

  it("never calls useChat (never opens a socket) when no session could be started", async () => {
    startSetupAgentSessionMock.mockResolvedValue({ ok: false, reason: "unavailable" })
    render(<OnboardingSetupChat onUnavailable={vi.fn()} onProposalApplied={vi.fn()} />)
    await waitFor(() => expect(startSetupAgentSessionMock).toHaveBeenCalledTimes(1))
    expect(useChatMock).not.toHaveBeenCalled()
  })

  it("starts the setup session exactly once, even under a double-effect remount", async () => {
    startSetupAgentSessionMock.mockResolvedValue({ ok: true, session: { agentId: "a1", sessionId: "s1", workspaceId: "ws-test" } })
    mockUseChat([])
    render(<OnboardingSetupChat onUnavailable={vi.fn()} onProposalApplied={vi.fn()} />)
    await waitFor(() => expect(startSetupAgentSessionMock).toHaveBeenCalledTimes(1))
  })
})

describe("OnboardingSetupChat — once connected", () => {
  async function renderConnected(turns: ChatTurn[]) {
    startSetupAgentSessionMock.mockResolvedValue({ ok: true, session: { agentId: "a1", sessionId: "s1", workspaceId: "ws-test" } })
    const sendMessage = mockUseChat(turns)
    const onProposalApplied = vi.fn()
    const onProposalPrepared = vi.fn()
    render(
      <OnboardingSetupChat
        onUnavailable={vi.fn()}
        onProposalApplied={onProposalApplied}
        onProposalPrepared={onProposalPrepared}
      />,
    )
    await waitFor(() => expect(useChatMock).toHaveBeenCalled())
    return { sendMessage, onProposalApplied, onProposalPrepared }
  }

  it("loads persisted history and releases useChat's frame gate before chatting", async () => {
    startSetupAgentSessionMock.mockResolvedValue({
      ok: true,
      session: { agentId: "a1", sessionId: "s1", workspaceId: "ws-history" },
    })
    const loadHistory = vi.fn()
    mockUseChat([], { loadHistory })
    apiFetchMock.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        messages: [
          { id: "m1", role: "user", content: "hello", ts: "2026-08-22T10:00:00Z" },
          {
            id: "m2",
            role: "assistant",
            content: "proposal",
            ts: "2026-08-22T10:00:01Z",
            metadata: SUGGESTION_METADATA,
          },
        ],
      }),
    })

    render(<OnboardingSetupChat onUnavailable={vi.fn()} onProposalApplied={vi.fn()} />)
    await waitFor(() => expect(loadHistory).toHaveBeenCalledTimes(1))
    expect(apiFetchMock).toHaveBeenCalledWith(
      "/api/v1/chats/s1/messages?workspace_id=ws-history",
    )
    expect(loadHistory.mock.calls[0][0]).toEqual([
      expect.objectContaining({ id: "m1", role: "user", content: "hello" }),
      expect.objectContaining({ id: "m2", role: "assistant", metadata: SUGGESTION_METADATA }),
    ])
  })

  it("renders the assistant's text turns", async () => {
    await renderConnected([
      turn({
        role: "assistant",
        parts: [{ id: "p1", type: "text", content: "What do you need help with?", timestamp: new Date() }],
      }),
    ])
    expect(await screen.findByText("What do you need help with?")).toBeTruthy()
  })

  it("turns a suggestion into a real proposal (via createOnboardingProposal) and renders it, every agent as its own row", async () => {
    createOnboardingProposalMock.mockResolvedValue(PROPOSAL)
    const { onProposalPrepared } = await renderConnected([
      turn({
        role: "assistant",
        parts: [{ id: "p1", type: "text", content: "", timestamp: new Date(), metadata: SUGGESTION_METADATA }],
      }),
    ])
    expect(await screen.findByTestId("onboarding-proposal-card")).toBeTruthy()
    expect(createOnboardingProposalMock).toHaveBeenCalledTimes(1)
    expect(createOnboardingProposalMock).toHaveBeenCalledWith({
      crewName: "Seznam Listing Scraper",
      templateSlug: "software-development",
      llmModel: "claude-sonnet-5",
    }, "ws-test")
    expect(screen.getByText("Seznam Listing Scraper")).toBeTruthy()
    expect(screen.getAllByTestId("onboarding-proposal-agent-row")).toHaveLength(2)
    expect(onProposalPrepared).toHaveBeenLastCalledWith(PROPOSAL)
  })

  it("materialises the same suggestion only once, even across re-renders", async () => {
    createOnboardingProposalMock.mockResolvedValue(PROPOSAL)
    const turns = [
      turn({
        role: "assistant",
        parts: [{ id: "p1", type: "text", content: "", timestamp: new Date(), metadata: SUGGESTION_METADATA }],
      }),
    ]
    await renderConnected(turns)
    await screen.findByTestId("onboarding-proposal-card")
    // A second identical-content array (as a live socket delivering the same
    // history again after a resubscribe would produce) must not re-fire.
    useChatMock.mockReturnValue({
      ...useChatMock.mock.results[0].value,
      turns: [...turns],
    })
    await new Promise((r) => setTimeout(r, 0))
    expect(createOnboardingProposalMock).toHaveBeenCalledTimes(1)
  })

  it("does not call applyOnboardingProposal merely because a proposal was materialised", async () => {
    createOnboardingProposalMock.mockResolvedValue(PROPOSAL)
    await renderConnected([
      turn({
        role: "assistant",
        parts: [{ id: "p1", type: "text", content: "", timestamp: new Date(), metadata: SUGGESTION_METADATA }],
      }),
    ])
    await screen.findByTestId("onboarding-proposal-card")
    expect(applyOnboardingProposalMock).not.toHaveBeenCalled()
  })

  it("calls applyOnboardingProposal only when Create is clicked, and reports success", async () => {
    createOnboardingProposalMock.mockResolvedValue(PROPOSAL)
    applyOnboardingProposalMock.mockResolvedValue({ crewId: "crew_1", crewName: "Seznam Listing Scraper" })
    const { onProposalApplied } = await renderConnected([
      turn({
        role: "assistant",
        parts: [{ id: "p1", type: "text", content: "", timestamp: new Date(), metadata: SUGGESTION_METADATA }],
      }),
    ])
    await screen.findByTestId("onboarding-proposal-card")
    fireEvent.click(screen.getByRole("button", { name: /create/i }))
    expect(applyOnboardingProposalMock).toHaveBeenCalledTimes(1)
    expect(applyOnboardingProposalMock).toHaveBeenCalledWith("prop_1", "ws-test")
    await waitFor(() => expect(onProposalApplied).toHaveBeenCalledTimes(1))
    expect(onProposalApplied).toHaveBeenCalledWith(
      { crewId: "crew_1", crewName: "Seznam Listing Scraper" },
      expect.objectContaining({ id: "prop_1", crewName: "Seznam Listing Scraper" }),
    )
  })

  it("shows the newest proposal only, when the agent revises its offer", async () => {
    const revised: OnboardingProposal = {
      ...PROPOSAL,
      id: "prop_2",
      crewName: "Revised Crew",
      agents: [{ name: "Solo Agent", role: "Lead", model: "claude-sonnet-5" }],
    }
    // The whole transcript (both suggestions) is present from the first
    // render, so the component's backward scan finds the NEWEST suggestion
    // first and never calls createOnboardingProposal for the superseded one
    // at all — one call, for the revised offer only.
    createOnboardingProposalMock.mockResolvedValue(revised)
    await renderConnected([
      turn({
        role: "assistant",
        parts: [{ id: "p1", type: "text", content: "", timestamp: new Date(), metadata: SUGGESTION_METADATA }],
      }),
      turn({ role: "user", parts: [{ id: "u1", type: "text", content: "different please", timestamp: new Date() }] }),
      turn({
        role: "assistant",
        parts: [{
          id: "p2",
          type: "text",
          content: "",
          timestamp: new Date(),
          metadata: {
            onboarding_proposal_suggestion: {
              crew_name: "Revised Crew",
              template_slug: "devops-sre",
            },
          },
        }],
      }),
    ])
    expect(await screen.findByText("Revised Crew")).toBeTruthy()
    expect(screen.queryByText("Seznam Listing Scraper")).toBeNull()
    expect(screen.getAllByTestId("onboarding-proposal-card")).toHaveLength(1)
    expect(createOnboardingProposalMock).toHaveBeenCalledTimes(1)
    expect(createOnboardingProposalMock).toHaveBeenCalledWith(
      expect.objectContaining({ crewName: "Revised Crew", templateSlug: "devops-sre" }),
      "ws-test",
    )
  })

  it("sends a message when the composer's Send is clicked, and clears the input", async () => {
    const { sendMessage } = await renderConnected([])
    const input = screen.getByRole("textbox")
    fireEvent.change(input, { target: { value: "I need to scrape listings" } })
    fireEvent.click(screen.getByRole("button", { name: /submit/i }))
    await waitFor(() => expect(sendMessage).toHaveBeenCalledWith("I need to scrape listings"))
    await waitFor(() => expect((input as HTMLTextAreaElement).value).toBe(""))
  })
})

// The composer's `ensureSession` on this surface is not a create — the setup
// session's row was written server-side by startSetupAgentSession before this
// component existed. The only question it has ever answered is "has the
// transcript base landed yet", which is a WAIT, and a wait must not be
// reported as a refusal: `useMessageSubmit` drops the send silently on a
// `false` (chat-panel's toast is the other caller's), and the attachment
// upload turns the same `false` into an error chip that says the conversation
// could not be started. Neither is true while a local GET is still in flight.
//
// These tests drive the window directly: history is held open, the user sends
// inside it, and the send has to survive. That window is also the whole
// content of the "sends a message when the composer's Send is clicked" flake
// above — on a loaded runner the click lands before the history effect
// resolves, so the assertion there is really an assertion about this.
describe("OnboardingSetupChat — sending inside the history-load window", () => {
  /** Renders with the transcript GET held open, and hands back the lever
   *  that lets it finish. `outcome` is what that GET then does — a normal
   *  200, or the failure the component degrades on rather than deadlocks. */
  async function renderWithHeldHistory(outcome: "ok" | "error" = "ok") {
    startSetupAgentSessionMock.mockResolvedValue({
      ok: true,
      session: { agentId: "a1", sessionId: "s1", workspaceId: "ws-test" },
    })
    const sendMessage = mockUseChat([])
    let release!: () => void
    const held = new Promise<void>((resolve) => { release = resolve })
    apiFetchMock.mockImplementation(async (url: unknown) => {
      if (typeof url === "string" && url.includes("/messages")) {
        await held
        if (outcome === "error") throw new Error("network down")
      }
      return { ok: true, json: async () => ({ messages: [] }) }
    })

    render(<OnboardingSetupChat onUnavailable={vi.fn()} onProposalApplied={vi.fn()} />)
    await waitFor(() => expect(useChatMock).toHaveBeenCalled())
    // Precondition: the window is genuinely open, not already closed by the
    // time the composer is on screen — otherwise these tests prove nothing.
    expect(screen.getByText(/Loading setup chat/i)).toBeTruthy()
    return { sendMessage, release }
  }

  function typeAndSend(text: string) {
    const input = screen.getByRole("textbox")
    fireEvent.change(input, { target: { value: text } })
    fireEvent.click(screen.getByRole("button", { name: /submit/i }))
    return input as HTMLTextAreaElement
  }

  it("holds a send typed before history has settled, then sends it once it has", async () => {
    const { sendMessage, release } = await renderWithHeldHistory()
    const input = typeAndSend("I need to scrape listings")

    // Not yet: useChat buffers every sequenced frame until the transcript
    // base is in place, so a message that went out now would be answered
    // into a gate that is still shut.
    await Promise.resolve()
    expect(sendMessage).not.toHaveBeenCalled()

    release()
    await waitFor(() => expect(sendMessage).toHaveBeenCalledWith("I need to scrape listings"))
    // And it went out as a real send: the draft is consumed, not stranded.
    await waitFor(() => expect(input.value).toBe(""))
  })

  it("still sends it when history could not be loaded at all", async () => {
    const { sendMessage, release } = await renderWithHeldHistory("error")
    typeAndSend("I need to scrape listings")

    release()
    // The failure path opens useChat's gate deliberately (markHistoryUnavailable)
    // rather than deadlocking live chat behind a missing transcript — so the
    // waiting send is released by it too, and only OLD messages are missing.
    await waitFor(() => expect(sendMessage).toHaveBeenCalledWith("I need to scrape listings"))
    expect(await screen.findByText(/Previous setup messages could not be loaded/i)).toBeTruthy()
  })
})

describe("OnboardingSetupChat — a deferred send must never look like silence", () => {
  // SYMPTOM this prevents: the real bug report. The server defers the first
  // message on an un-built crew (internal/ws/client.go's ErrCrewProvisioning
  // branch), streams a crew_provisioning event, and a terminal `done` — and
  // the old hand-rolled transcript rendered neither, so the user saw their
  // own bubble and then nothing at all.
  it("renders a visible building state for a crew_provisioning event, not silence", async () => {
    mockProvisioning([{ id: "crew_1", slug: "seznam-listing-scraper", name: "Seznam Listing Scraper", status: "running", featureIds: [] }])
    startSetupAgentSessionMock.mockResolvedValue({ ok: true, session: { agentId: "a1", sessionId: "s1", workspaceId: "ws-test" } })
    mockUseChat([
      turn({ role: "user", parts: [{ id: "u1", type: "text", content: "I need to scrape listings", timestamp: new Date() }] }),
      turn({
        role: "system",
        parts: [{
          id: "sp1",
          type: "crew_provisioning",
          content: "Building crew image…",
          metadata: { crew_id: "crew_1", crew_slug: "seznam-listing-scraper" },
          timestamp: new Date(),
        }],
      }),
    ])
    render(<OnboardingSetupChat onUnavailable={vi.fn()} onProposalApplied={vi.fn()} />)
    await waitFor(() => expect(useChatMock).toHaveBeenCalled())
    // The CrewProvisioningCard TurnRenderer renders for this event.
    expect(await screen.findByText(/Building Seznam Listing Scraper/i)).toBeTruthy()
  })

  // The SERVER now owns the resume (internal/api/crew_provisioning_jobs.go's
  // AttachPendingMessage/resumeMessage, exercised end-to-end by
  // internal/api's TestResumeDeferredChatMessage) — it attaches the deferred
  // message to the job and runs it itself once the build completes,
  // streaming the outcome on this SAME chat's session channel. This
  // component must therefore do NO polling and NO auto-resend of its own:
  // these tests pin that the banner is purely a function of `turns`, and
  // that nothing here ever calls sendMessage on the build's behalf — a
  // regression back to a client-side poll-and-resend is exactly what let the
  // original bug ship (it passed against a stubbed feed and never exercised
  // the real race).
  function deferredTurns() {
    return [
      turn({ role: "user", parts: [{ id: "u1", type: "text", content: "I need to scrape listings", timestamp: new Date() }] }),
      turn({
        role: "system",
        parts: [{
          id: "sp1",
          type: "crew_provisioning",
          content: "Building crew image…",
          metadata: { crew_id: "crew_1", crew_slug: "seznam-listing-scraper" },
          timestamp: new Date(),
        }],
      }),
    ]
  }

  // Sends the triggering message through a fresh render, then returns the
  // handles needed to simulate the server's side of the conversation
  // arriving over the (mocked) chat transport.
  async function sendAndDefer() {
    startSetupAgentSessionMock.mockResolvedValue({ ok: true, session: { agentId: "a1", sessionId: "s1", workspaceId: "ws-test" } })
    const sendMessage = vi.fn()
    const chatReturn = {
      turns: [] as ChatTurn[],
      messages: [],
      sendMessage,
      stopGeneration: vi.fn(),
      regenerateLastTurn: vi.fn(),
      editAndResend: vi.fn(),
      loadHistory: vi.fn(),
      markHistoryUnavailable: vi.fn(),
      resubscribeSession: vi.fn(),
      isStreaming: false,
      connectionStatus: "connected" as const,
    }
    useChatMock.mockReturnValue(chatReturn)
    const { rerender } = render(<OnboardingSetupChat onUnavailable={vi.fn()} onProposalApplied={vi.fn()} />)
    await waitFor(() => expect(useChatMock).toHaveBeenCalled())

    // Send the message that will get deferred. It is never persisted
    // server-side (ErrCrewProvisioning is pure control flow — see
    // internal/ws/client.go), so the only copy of it left afterwards is
    // whatever this component remembered having sent.
    const input = screen.getByRole("textbox")
    fireEvent.change(input, { target: { value: "I need to scrape listings" } })
    fireEvent.click(screen.getByRole("button", { name: /submit/i }))
    await waitFor(() => expect(sendMessage).toHaveBeenCalledWith("I need to scrape listings"))
    sendMessage.mockClear()

    const setTurns = (turns: ChatTurn[]) => {
      useChatMock.mockReturnValue({ ...chatReturn, turns })
      rerender(<OnboardingSetupChat onUnavailable={vi.fn()} onProposalApplied={vi.fn()} />)
    }
    return { sendMessage, setTurns }
  }

  it("shows the deferred-send banner but never calls sendMessage on the build's own behalf", async () => {
    mockProvisioning([{ id: "crew_1", slug: "seznam-listing-scraper", name: "Seznam Listing Scraper", status: "running", featureIds: [] }])
    const { sendMessage, setTurns } = await sendAndDefer()

    // The server defers it: a crew_provisioning system turn arrives, no
    // reply — same `turns` array shape a live socket delivers it in.
    setTurns(deferredTurns())
    expect(await screen.findByText(/your message will run automatically once it's ready/i)).toBeTruthy()

    // Flipping the provisioning feed to "completed" must do NOTHING here —
    // there is no effect left in this component watching it. Only a NEW
    // turn (posted by the server) can ever change what's rendered.
    mockProvisioning([{ id: "crew_1", slug: "seznam-listing-scraper", name: "Seznam Listing Scraper", status: "completed", featureIds: [] }])
    await new Promise((r) => setTimeout(r, 0))
    expect(sendMessage).not.toHaveBeenCalled()
    expect(await screen.findByText(/your message will run automatically once it's ready/i)).toBeTruthy()
  })

  it("clears the banner on its own once the server's resumed run posts a reply turn", async () => {
    mockProvisioning([{ id: "crew_1", slug: "seznam-listing-scraper", name: "Seznam Listing Scraper", status: "running", featureIds: [] }])
    const { sendMessage, setTurns } = await sendAndDefer()
    setTurns(deferredTurns())
    expect(await screen.findByText(/your message will run automatically once it's ready/i)).toBeTruthy()

    // The server resumed the message and its reply landed on this same
    // session channel: a new turn AFTER the crew_provisioning one.
    setTurns([
      ...deferredTurns(),
      turn({ role: "assistant", parts: [{ id: "a1", type: "text", content: "All set — here's a plan", timestamp: new Date() }] }),
    ])

    await waitFor(() => expect(screen.queryByText(/your message will run automatically/i)).toBeNull())
    expect(await screen.findByText("All set — here's a plan")).toBeTruthy()
    // Still never resent by this client — the reply came from the server's
    // own resumed run, not from anything this component sent.
    expect(sendMessage).not.toHaveBeenCalled()
  })

  it("lets the user manually resend a still-building message as a fallback", async () => {
    mockProvisioning([{ id: "crew_1", slug: "seznam-listing-scraper", name: "Seznam Listing Scraper", status: "running", featureIds: [] }])
    const { sendMessage, setTurns } = await sendAndDefer()
    setTurns(deferredTurns())
    fireEvent.click(await screen.findByRole("button", { name: /resend now/i }))
    expect(sendMessage).toHaveBeenCalledWith("I need to scrape listings")
  })

  it("surfaces an immediate manual retry when the build never even started (enqueue failed)", async () => {
    const { sendMessage, setTurns } = await sendAndDefer()

    // enqErr != nil (bridge.go): no job was ever created, so metadata.status
    // is "failed" from the very first frame — nothing will ever post a
    // follow-up turn, so waiting on one would spin forever.
    setTurns([
      turn({ role: "user", parts: [{ id: "u1", type: "text", content: "I need to scrape listings", timestamp: new Date() }] }),
      turn({
        role: "system",
        parts: [{
          id: "sp1",
          type: "crew_provisioning",
          content: "Could not start build",
          metadata: { crew_id: "crew_1", crew_slug: "seznam-listing-scraper", status: "failed", error: "builder offline" },
          timestamp: new Date(),
        }],
      }),
    ])

    expect(await screen.findByRole("alert")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /resend message/i }))
    expect(sendMessage).toHaveBeenCalledWith("I need to scrape listings")
  })

  it("renders a system-turn error as a visible error, not silence", async () => {
    startSetupAgentSessionMock.mockResolvedValue({ ok: true, session: { agentId: "a1", sessionId: "s1", workspaceId: "ws-test" } })
    mockUseChat([
      turn({
        role: "system",
        parts: [{ id: "e1", type: "error", content: "an error occurred processing your message", timestamp: new Date() }],
      }),
    ])
    render(<OnboardingSetupChat onUnavailable={vi.fn()} onProposalApplied={vi.fn()} />)
    await waitFor(() => expect(useChatMock).toHaveBeenCalled())
    expect(await screen.findByText("an error occurred processing your message")).toBeTruthy()
  })
})

describe("OnboardingSetupChat — reuses the real chat surface, does not reinvent it", () => {
  // Pins CLAUDE.md's "reuse, don't rebuild" and this feature's own fix: the
  // bug was caused by a hand-rolled transcript/composer that only knew about
  // text parts. Guards against a regression back to that shape.
  it("imports the shared turn renderer and composer instead of a bespoke textarea", () => {
    const src = stripComments(
      readFileSync(join(process.cwd(), "components/features/onboarding/onboarding-setup-chat.tsx"), "utf8"),
    )
    expect(src).toMatch(/from ["']@\/components\/features\/chat\/turn-renderer["']/)
    expect(src).toMatch(/from ["']@\/components\/features\/chat\/composer\/chat-composer["']/)
    expect(src).not.toMatch(/<textarea/i)
    expect(src).not.toMatch(/<Textarea/)
  })
})
