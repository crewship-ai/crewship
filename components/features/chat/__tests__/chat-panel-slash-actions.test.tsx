import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react"

// =============================================================================
// The other half of the palette's honesty contract (audit P0.8).
//
// slash-palette-contract.test.tsx proves each row is enabled / disabled with a
// reason / not rendered. "Enabled" for a delegated row only means the palette
// called `onCommand`. THIS file proves the panel on the other end of that call
// actually does the advertised thing — the exact seam the audit found broken:
// the palette offered branch / search / export / run-task and the panel's
// handler covered regenerate and clear.
//
// It walks PANEL_HANDLED_COMMAND_IDS, so a new delegated row is red here until
// somebody implements its effect.
//
// It also pins the composition the finding named: the live panel must hand the
// palette a workspaceId (without one the server catalogue query never runs and
// the Actions group cannot appear at all) and an action callback, and must own
// the action modal's lifecycle.
// =============================================================================

const chatStub = vi.hoisted(() => ({
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
}))

vi.mock("@/hooks/use-chat", () => ({ useChat: () => chatStub }))
vi.mock("@/hooks/use-auth", () => ({ useSession: () => ({ data: { user: { id: "user-1" } } }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test", loading: false }) }))

// Neighbours that open their own surfaces and are not what this file is about.
// conversation-search and export-dialog are deliberately NOT mocked: whether
// they open is the assertion.
vi.mock("../right-panel", () => ({ RightPanel: () => null }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: () => null }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/mention-autocomplete", () => ({ MentionAutocomplete: () => null }))

// The palette itself is stubbed to a prop recorder — this file is about what
// the panel does with the calls, not about the palette's own rendering.
const palette = vi.hoisted(() => ({ props: null as Record<string, unknown> | null }))
vi.mock("../composer/slash-palette", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../composer/slash-palette")>()
  return {
    ...actual,
    SlashPalette: (props: Record<string, unknown>) => {
      palette.props = props
      return null
    },
  }
})

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))

import { ChatPanel } from "../chat-panel"
import { PANEL_HANDLED_COMMAND_IDS } from "../composer/slash-palette"

const panelProps = {
  agentId: "agent-1",
  sessionId: "sess-1",
  agentName: "Riley",
  agentSlug: "riley",
  askForms: null,
}

function turn(id: string, role: "user" | "assistant", content: string) {
  return { id, role, parts: [{ type: "text", content }], isStreaming: false, timestamp: new Date() }
}

function stubFetch() {
  global.fetch = vi.fn((url: string) => {
    const u = String(url)
    if (u.includes("/messages")) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ messages: [] }) }) as unknown as Promise<Response>
    }
    if (u.includes("/participants")) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ participants: [] }) }) as unknown as Promise<Response>
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ id: "created-1" }) }) as unknown as Promise<Response>
  }) as unknown as typeof fetch
}

/** What each delegated row must actually DO once the panel receives it. A row
 *  in PANEL_HANDLED_COMMAND_IDS with no entry here fails the walk below. */
const PANEL_EFFECT: Record<string, () => void | Promise<void>> = {
  clear: () => {
    expect(chatStub.loadHistory).toHaveBeenCalledWith([])
  },
  regenerate: () => {
    expect(chatStub.regenerateLastTurn).toHaveBeenCalled()
  },
  search: async () => {
    await waitFor(() => expect(screen.getByRole("search")).toBeInTheDocument())
  },
  export: async () => {
    await waitFor(() => expect(screen.getByText("Export conversation")).toBeInTheDocument())
  },
}

beforeEach(() => {
  palette.props = null
  chatStub.turns = [turn("t1", "user", "How do I close the month?"), turn("t2", "assistant", "Run the close checklist.")]
  chatStub.isStreaming = false
  stubFetch()
})

describe("ChatPanel — the palette's composition", () => {
  it("gives the palette the workspace the server catalogue is scoped to", async () => {
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(palette.props).not.toBeNull())
    expect(palette.props!.workspaceId).toBe("ws-test")
  })

  it("gives the palette an action handler, so the Actions group is not a dead end", async () => {
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(palette.props).not.toBeNull())
    expect(typeof palette.props!.onAction).toBe("function")
  })
})

describe("ChatPanel — every delegated command does its advertised thing", () => {
  it.each(PANEL_HANDLED_COMMAND_IDS)("handles /%s", async (id) => {
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(palette.props).not.toBeNull())

    const effect = PANEL_EFFECT[id]
    expect(effect, `the palette delegates "${id}" to the panel but this test declares no effect for it`).toBeDefined()

    act(() => {
      (palette.props!.onCommand as (id: string) => void)(id)
    })
    await effect()
  })

  it("tells the palette which rows the current conversation cannot support", async () => {
    chatStub.turns = []
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(palette.props).not.toBeNull())

    const disabled = palette.props!.disabledCommands as Record<string, string>
    for (const id of PANEL_HANDLED_COMMAND_IDS) {
      expect(disabled[id], `"${id}" does nothing on an empty conversation but is offered anyway`).toBeTruthy()
    }
  })
})

describe("ChatPanel — the action modal's lifecycle", () => {
  const skill = {
    id: "skill",
    label: "Create skill from this conversation",
    capability: "skill.create",
    form_schema: [
      { name: "slug", type: "slug", required: true },
      { name: "prompt", type: "textarea", required: true },
    ],
  }

  it("opens the picked action's form, pre-filled from the conversation", async () => {
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(palette.props).not.toBeNull())

    act(() => {
      (palette.props!.onAction as (c: unknown) => void)(skill)
    })

    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Create skill from this conversation" })).toBeInTheDocument(),
    )
    // "from this conversation" has to mean something: the transcript is the
    // form's raw material, not a label nobody honoured. (The label carries a
    // required marker, hence the anchored regex.)
    const prompt = screen.getByLabelText(/^prompt/) as HTMLTextAreaElement
    expect(prompt.value).toContain("close the month")
    expect(prompt.value).toContain("Run the close checklist.")
  })

  it("submits it to the endpoint the action names, scoped to the workspace", async () => {
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(palette.props).not.toBeNull())

    act(() => {
      (palette.props!.onAction as (c: unknown) => void)(skill)
    })
    await waitFor(() => expect(screen.getByLabelText(/^slug/)).toBeInTheDocument())

    fireEvent.change(screen.getByLabelText(/^slug/), { target: { value: "month-close" } })
    // The panel has other forms on screen (the composer); submit the modal's.
    fireEvent.submit(screen.getByLabelText(/^slug/).closest("form")!)

    await waitFor(() => {
      const calls = (global.fetch as unknown as { mock: { calls: unknown[][] } }).mock.calls.map((c) => String(c[0]))
      expect(calls).toContain("/api/v1/workspaces/ws-test/skills/generate")
    })
  })

  it("closes the form again", async () => {
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(palette.props).not.toBeNull())

    act(() => {
      (palette.props!.onAction as (c: unknown) => void)(skill)
    })
    await waitFor(() => expect(screen.getByLabelText(/^slug/)).toBeInTheDocument())

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByLabelText(/^slug/)).toBeNull())
  })
})
