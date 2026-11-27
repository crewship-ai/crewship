import { describe, expect, it, vi, beforeEach } from "vitest"
import { render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ChatTurn } from "@/hooks/use-chat"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", async (original) => ({ ...(await original<typeof import("@/lib/api-fetch")>()), apiFetch: (...args: unknown[]) => apiFetch(...args) }))
const pages: Record<string, { id: string; name: string; artifactKind?: string; nativeCraftApplication?: string } | undefined> = {}
vi.mock("@/hooks/use-pages", async (original) => ({
  ...(await original<typeof import("@/hooks/use-pages")>()),
  usePage: (_workspace: string, slug: string) => ({ page: pages[slug.replace(/^@/, "")] }),
}))
vi.mock("../../assistant-turn", () => ({ AssistantTurn: () => null }))

import { messageSheetIds } from "../../artifact-context/sheet/use-conversation-sheets"
import { TurnRenderer } from "../../turn-renderer"

const refsPage = (refs: Record<string, unknown>[]) => ({ refs })

describe("messageSheetIds", () => {
  it("takes what the message carried, then what the server recorded for that message id, each once", () => {
    const server = [refsPage([
      { page_id: "sheet-a", message_id: "m1", application: "gridcraft", source: "message" },
      { page_id: "sheet-b", message_id: "m2", application: "gridcraft", source: "message" },
      { page_id: "sheet-c", message_id: "m1", source: "message" },
      { page_id: "doc-d", message_id: "m1", application: "doccraft", source: "message" },
      { page_id: "sheet-e", message_id: "", application: "gridcraft", source: "upload" },
    ])]
    expect(messageSheetIds([{ page_id: "sheet-a", version: "3" }], server, "m1")).toEqual(["sheet-a", "sheet-c"])
    expect(messageSheetIds(undefined, server, "m2")).toEqual(["sheet-b"])
    expect(messageSheetIds([{ page_id: "sheet-x" }], server, undefined), "a message not saved yet has only what it carried").toEqual(["sheet-x"])
    expect(messageSheetIds("not a list", [{ refs: "nope" }, null], "m1")).toEqual([])
  })
})

function userTurn(partial: Partial<ChatTurn>): ChatTurn {
  return { id: "u1", role: "user", parts: [{ id: "p1", type: "text", content: "Look at the budget, please.", timestamp: new Date() }], isStreaming: false, timestamp: new Date(), ...partial }
}

function renderTurn(turn: ChatTurn) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><TurnRenderer turn={turn} workspaceId="ws" chatId="chat-1" onCopy={async () => true} /></QueryClientProvider>)
}

// A message sent with a Sheet showed the Sheet in the composer, and nothing in the sent bubble.
describe("a person's message sent with a Sheet", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    for (const key of Object.keys(pages)) delete pages[key]
    pages["sheet-a"] = { id: "sheet-a", name: "Budget 2026", artifactKind: "sheet" }
  })

  it("shows the Sheet under the bubble right after sending, from what the message carried", async () => {
    apiFetch.mockResolvedValue(new Response(JSON.stringify({ refs: [] }), { status: 200 }))
    renderTurn(userTurn({ metadata: { artifact_refs: [{ page_id: "sheet-a", version: "4" }] } }))
    const list = await screen.findByRole("list", { name: "Sheets sent with this message" })
    expect(within(list).getByText("Budget 2026")).toBeInTheDocument()
  })

  it("shows it again after a reload, from the server's record of that message", async () => {
    apiFetch.mockResolvedValue(new Response(JSON.stringify({ refs: [{ page_id: "sheet-a", message_id: "m1", application: "gridcraft", source: "message", name: "Budget 2026", slug: "budget", created_at: "2026-11-27T08:00:00Z" }] }), { status: 200 }))
    renderTurn(userTurn({ id: "m1", originalMessageId: "m1" }))
    const list = await screen.findByRole("list", { name: "Sheets sent with this message" })
    expect(within(list).getByText("Budget 2026")).toBeInTheDocument()
    expect(String(apiFetch.mock.calls[0][0])).toContain("/api/v1/chats/chat-1/artifact-refs?")
  })

  it("shows nothing for a message without a Sheet, or one the reader does not see as a Sheet", async () => {
    apiFetch.mockResolvedValue(new Response(JSON.stringify({ refs: [{ page_id: "hidden", message_id: "m1", application: "gridcraft", source: "message" }] }), { status: 200 }))
    renderTurn(userTurn({ id: "m1", originalMessageId: "m1" }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    expect(screen.queryByText("Budget 2026")).toBeNull()
    expect(screen.queryAllByRole("listitem")).toHaveLength(0)
  })
})
