import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { conversationHitHref, conversationHitSnippet, searchConversations, type ConversationHit } from "@/lib/conversation-search"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const hit: ConversationHit = {
  id: "message", session_id: "session?x=1", agent_id: "agent", agent_slug: "agent /?#",
  role: "assistant", content: "Hello\n  world", ts: "2026-10-02T00:00:00Z",
}

beforeEach(() => { fetchMock.mockReset() })

describe("conversation search", () => {
  it("opens the matched thread with independently escaped agent and session identifiers", () => {
    expect(conversationHitHref(hit)).toBe("/chat/agent%20%2F%3F%23?session=session%3Fx%3D1")
    expect(conversationHitHref({ ...hit, agent_slug: "" })).toBeNull()
    expect(conversationHitHref({ ...hit, session_id: "" })).toBeNull()
  })

  it("normalizes previews, falls back to tool summaries and truncates long messages", () => {
    expect(conversationHitSnippet(hit)).toBe("Hello world")
    expect(conversationHitSnippet({ ...hit, content: "", tool_summary: "  Read\n file  " })).toBe("Read file")
    expect(conversationHitSnippet({ ...hit, content: "" })).toBe("")
    expect(conversationHitSnippet({ ...hit, content: "x".repeat(140) })).toBe("x".repeat(140))
    expect(conversationHitSnippet({ ...hit, content: "x".repeat(141) })).toBe(`${"x".repeat(137)}…`)
  })

  it.each([
    ["", "workspace"], [" a ", "workspace"], ["ab", ""],
  ])("avoids requests for an incomplete query or missing workspace (%s, %s)", async (query, workspaceId) => {
    expect(await searchConversations(query, { workspaceId })).toEqual([])
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("searches the whole workspace with a trimmed query and propagates cancellation", async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ hits: [hit] })))
    const ctrl = new AbortController()
    expect(await searchConversations("  deployment logs  ", { workspaceId: "ws /&", signal: ctrl.signal })).toEqual([hit])
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/conversations/search?workspace_id=ws%20%2F%26", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ query: "deployment logs", limit: 8 }), signal: ctrl.signal,
    })
  })

  it("honors an explicit result limit", async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ hits: [] })))
    await searchConversations("logs", { workspaceId: "ws", limit: 3 })
    expect(JSON.parse(fetchMock.mock.calls[0][1]!.body as string)).toEqual({ query: "logs", limit: 3 })
  })

  it.each([null, {}, { hits: null }, { hits: "invalid" }])("tolerates an optional endpoint's empty or malformed payload %j", async (body) => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify(body)))
    expect(await searchConversations("logs", { workspaceId: "ws" })).toEqual([])
  })

  it("silently handles unavailable endpoints, invalid JSON, network failures and cancellation", async () => {
    fetchMock.mockResolvedValueOnce(new Response("unavailable", { status: 503 }))
      .mockResolvedValueOnce(new Response("invalid json"))
      .mockRejectedValueOnce(new Error("network"))
      .mockRejectedValueOnce(new DOMException("aborted", "AbortError"))
    for (let i = 0; i < 4; i++) {
      expect(await searchConversations("logs", { workspaceId: "ws" })).toEqual([])
    }
  })
})
