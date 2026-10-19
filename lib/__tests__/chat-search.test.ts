import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import {
  applyClientFilters, hitMessageHref, parseChatSearchQuery, runChatSearch, snippetSegments,
  type ChatSearchRoster,
} from "@/lib/chat-search"
import type { ConversationHit } from "@/lib/conversation-search"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
beforeEach(() => fetchMock.mockReset())

const roster: ChatSearchRoster = [{ id: "a1", slug: "ada", name: "Ada" }, { id: "a2", slug: "bob", name: "Bob Agent" }]
const hit = (over: Partial<ConversationHit> = {}): ConversationHit => ({
  id: "m1", session_id: "s1", agent_id: "a1", agent_slug: "ada", agent_name: "Ada", role: "assistant",
  content: "deploy pipeline is green", ts: "2026-10-10T12:00:00Z", ...over,
})

describe("parseChatSearchQuery", () => {
  it("extracts operators and keeps the free text", () => {
    const q = parseChatSearchQuery("deploy from:ada in:bob has:link after:2026-10-01 before:2026-10-31 pipeline")
    expect(q.text).toBe("deploy pipeline")
    expect(q.filters).toEqual({ from: "ada", in: "bob", has: ["link"], after: "2026-10-01", before: "2026-10-31" })
    expect(q.invalid).toEqual([])
  })
  it("reports unknown operators and bad dates instead of dropping them silently", () => {
    const q = parseChatSearchQuery("x has:video after:soon color:red")
    expect(q.invalid).toEqual(["has:video", "after:soon"])
    expect(q.text).toBe("x color:red")
  })
  it("is bounded", () => {
    const q = parseChatSearchQuery("a ".repeat(500) + "has:link ".repeat(50))
    expect(q.text.length).toBeLessThanOrEqual(200)
    expect(q.filters.has?.length ?? 0).toBeLessThanOrEqual(3)
  })
})

describe("applyClientFilters", () => {
  const hits = [hit(), hit({ id: "m2", role: "user", agent_id: "a2", agent_slug: "bob", agent_name: "Bob Agent", content: "see https://x.dev", ts: "2026-09-01T00:00:00Z" }), hit({ id: "m3", tool_summary: "Read file", content: "" })]
  it("filters from, has and date locally", () => {
    expect(applyClientFilters(hits, { from: "user" }).map((h) => h.id)).toEqual(["m2"])
    expect(applyClientFilters(hits, { from: "ada" }).map((h) => h.id)).toEqual(["m1", "m3"])
    expect(applyClientFilters(hits, { has: ["link"] }).map((h) => h.id)).toEqual(["m2"])
    expect(applyClientFilters(hits, { has: ["tool"] }).map((h) => h.id)).toEqual(["m3"])
    expect(applyClientFilters(hits, { after: "2026-10-01" }).map((h) => h.id)).toEqual(["m1", "m3"])
    expect(applyClientFilters(hits, { before: "2026-10-01" }).map((h) => h.id)).toEqual(["m2"])
  })
})

describe("runChatSearch", () => {
  it("sends agent_id for In and reports complete when below the limit", async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ hits: [hit()] })))
    const r = await runChatSearch({ raw: "deploy in:ada", scope: "all", workspaceId: "ws", roster })
    expect(JSON.parse(fetchMock.mock.calls[0][1]!.body as string)).toEqual({ query: "deploy", limit: 50, agent_id: "a1" })
    expect(r).toMatchObject({ status: "complete", hits: [{ id: "m1" }] })
  })
  it("This chat keeps only the open session and is partial when the server page was full", async () => {
    const page = Array.from({ length: 50 }, (_, i) => hit({ id: `m${i}`, session_id: i === 0 ? "s1" : "other" }))
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ hits: page })))
    const r = await runChatSearch({ raw: "deploy", scope: "this", workspaceId: "ws", roster, current: { agentId: "a1", sessionId: "s1" } })
    expect(r.hits.map((h) => h.id)).toEqual(["m0"])
    expect(r.status).toBe("partial")
  })
  it("a server-side filter that empties a full page is partial, never a clean zero", async () => {
    const page = Array.from({ length: 50 }, (_, i) => hit({ id: `m${i}` }))
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ hits: page })))
    const r = await runChatSearch({ raw: "deploy has:link", scope: "all", workspaceId: "ws", roster })
    expect(r.hits).toEqual([])
    expect(r.status).toBe("partial")
  })
  it("maps 503, other failures and network errors to unavailable/error", async () => {
    fetchMock.mockResolvedValueOnce(new Response("{}", { status: 503 }))
    expect((await runChatSearch({ raw: "deploy", scope: "all", workspaceId: "ws", roster })).status).toBe("unavailable")
    fetchMock.mockResolvedValueOnce(new Response("{}", { status: 500 }))
    expect((await runChatSearch({ raw: "deploy", scope: "all", workspaceId: "ws", roster })).status).toBe("error")
    fetchMock.mockRejectedValueOnce(new Error("net"))
    expect((await runChatSearch({ raw: "deploy", scope: "all", workspaceId: "ws", roster })).status).toBe("error")
  })
  it("In with an unknown agent is an error state, not an empty list", async () => {
    const r = await runChatSearch({ raw: "deploy in:nobody", scope: "all", workspaceId: "ws", roster })
    expect(r.status).toBe("error")
    expect(fetchMock).not.toHaveBeenCalled()
  })
  it("does not search below the minimum text length", async () => {
    expect((await runChatSearch({ raw: "d", scope: "all", workspaceId: "ws", roster })).status).toBe("idle")
  })
})

describe("hitMessageHref / snippetSegments", () => {
  it("adds the message id to the thread link", () => {
    expect(hitMessageHref(hit({ id: "m /1" }))).toBe("/chat/ada?session=s1&message=m%20%2F1")
    expect(hitMessageHref(hit({ agent_slug: "" }))).toBeNull()
  })
  it("highlights terms as plain text segments (never markup)", () => {
    const segs = snippetSegments("Deploy <b>now</b>", "deploy")
    expect(segs).toEqual([{ text: "Deploy", match: true }, { text: " <b>now</b>", match: false }])
  })
  it("strips control characters", () => {
    expect(snippetSegments("a\u0000b‮c", "zz")).toEqual([{ text: "abc", match: false }])
  })
})
