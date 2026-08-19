import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, within, cleanup, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// ⌘K conversation search is the only way to find a message that scrolled away,
// and nobody knows whether it is used. Two numbers answer that: a search ran
// and returned N, and a result at rank K was opened. A lot of searches with no
// result opened is a ranking problem; a lot of searches returning zero is a
// scope problem. They are different bugs and the events tell them apart.
//
// The search TERMS are not recorded — not the text, not its length. What
// somebody types into ⌘K is as private as the message they are looking for.
// =============================================================================

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", role: "OWNER" }),
}))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { apiFetch } from "@/lib/api-fetch"
import { resetChatTelemetry, setChatTelemetrySink, type ChatEvent } from "@/lib/telemetry"

import { CommandPalette } from "../command-palette"

const SEARCH_PATH = "/api/v1/conversations/search"
const SECRET = "please wire the deposit to the escrow account"

let hits: unknown[]
let events: ChatEvent[]
const named = (name: string) => events.filter((e) => e.name === name)

function hit(over: Record<string, unknown> = {}) {
  return {
    id: "m1",
    session_id: "chat-1",
    agent_id: "agent-1",
    agent_slug: "backend-bot",
    agent_name: "Backend Bot",
    role: "user",
    content: SECRET,
    ts: new Date(Date.now() - 3 * 3600 * 1000).toISOString(),
    ...over,
  }
}

function openPalette() {
  return render(<CommandPalette open={true} onOpenChange={vi.fn()} />)
}

function type(text: string) {
  fireEvent.change(screen.getByPlaceholderText(/search issues/i), { target: { value: text } })
}

beforeEach(() => {
  hits = []
  events = []
  resetChatTelemetry()
  setChatTelemetrySink((e) => events.push(e))
  vi.mocked(apiFetch).mockImplementation(async (url: string) => {
    if (url.includes(SEARCH_PATH)) {
      return {
        ok: true,
        status: 200,
        json: async () => ({ count: hits.length, hits }),
      } as unknown as Response
    }
    return { ok: true, status: 200, json: async () => [] } as unknown as Response
  })
})

afterEach(cleanup)

describe("a search that ran", () => {
  it("records one event with the number of results", async () => {
    hits = [hit(), hit({ id: "m2" })]
    openPalette()
    type("deposit")

    await waitFor(() => expect(named("conversation_search_run")).toHaveLength(1))
    expect(named("conversation_search_run")[0].payload).toMatchObject({
      result_count: 2,
      has_results: true,
      source: "palette",
    })
  })

  it("records a search that found nothing — that is the interesting one", async () => {
    hits = []
    openPalette()
    type("deposit")

    await waitFor(() => expect(named("conversation_search_run")).toHaveLength(1))
    expect(named("conversation_search_run")[0].payload).toMatchObject({
      result_count: 0,
      has_results: false,
    })
  })

  it("records one event per debounced request, not one per keystroke", async () => {
    hits = [hit()]
    openPalette()
    type("d")
    type("de")
    type("dep")
    type("deposit")

    await waitFor(() => expect(named("conversation_search_run")).toHaveLength(1))
    await new Promise((r) => setTimeout(r, 300))
    expect(named("conversation_search_run")).toHaveLength(1)
  })

  it("records nothing for a query too short to be sent", async () => {
    openPalette()
    type("d")
    await new Promise((r) => setTimeout(r, 400))
    expect(named("conversation_search_run")).toHaveLength(0)
  })
})

describe("a result that was opened", () => {
  it("records the rank of the row that was chosen", async () => {
    hits = [hit(), hit({ id: "m2", session_id: "chat-2" })]
    openPalette()
    type("deposit")

    const group = await screen.findByRole("group", { name: /conversations/i })
    const rows = within(group).getAllByText(new RegExp(SECRET.slice(0, 20)))
    fireEvent.click(rows[1].closest("[data-href]")!)

    expect(named("conversation_search_result_opened")).toHaveLength(1)
    expect(named("conversation_search_result_opened")[0].payload).toMatchObject({
      position: 1,
      result_count: 2,
      session_id: "chat-2",
      source: "palette",
    })
  })
})

describe("the palette carries no search terms and no message text", () => {
  it("emits neither the query nor the snippet it matched", async () => {
    hits = [hit()]
    openPalette()
    type("escrow deposit")

    const group = await screen.findByRole("group", { name: /conversations/i })
    fireEvent.click(within(group).getByText(new RegExp(SECRET.slice(0, 20))).closest("[data-href]")!)

    const serialized = JSON.stringify(events)
    expect(serialized).not.toContain("escrow")
    expect(serialized).not.toContain("deposit")
    expect(serialized).not.toContain("Backend Bot")
    expect(serialized).not.toContain("backend-bot")
  })
})
