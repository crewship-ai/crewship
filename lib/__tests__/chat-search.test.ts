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
    expect(applyClientFilters(hits, { from: "ada" }).map((h) => h.id)).toEqual(["m1", "m3"])
    expect(applyClientFilters(hits, { from: "agent" }).map((h) => h.id)).toEqual(["m1", "m3"])
    expect(applyClientFilters(hits, { has: ["link"] }).map((h) => h.id)).toEqual(["m2"])
    expect(applyClientFilters(hits, { has: ["tool"] }).map((h) => h.id)).toEqual(["m3"])
    expect(applyClientFilters(hits, { after: "2026-10-01" }).map((h) => h.id)).toEqual(["m1", "m3"])
    expect(applyClientFilters(hits, { before: "2026-10-01" }).map((h) => h.id)).toEqual(["m2"])
  })
})

describe("from: matches the message author, not the conversation", () => {
  it("from:ada does not return a user message in Ada's conversation", () => {
    const mine = hit({ id: "u1", role: "user" })
    const reply = hit({ id: "a1", role: "assistant" })
    expect(applyClientFilters([mine, reply], { from: "ada" }).map((h) => h.id)).toEqual(["a1"])
    expect(applyClientFilters([mine, reply], { from: "Ada" }).map((h) => h.id)).toEqual(["a1"])
  })
  it("an unknown from: value is an explicit unsupported hint and sends no request", async () => {
    const r = await runChatSearch({ raw: "deploy from:nobody", scope: "all", workspaceId: "ws", roster })
    expect(r.status).toBe("error")
    expect(r.note).toMatch(/from:nobody.*not supported/i)
    expect(fetchMock).not.toHaveBeenCalled()
  })
  it("from:me/user/you are not supported: group chats return other people's messages and the hit has no author id", async () => {
    for (const f of ["me", "user", "you"]) {
      const r = await runChatSearch({ raw: `deploy from:${f}`, scope: "all", workspaceId: "ws", roster })
      expect(r.status).toBe("error")
      expect(r.note).toMatch(new RegExp(`from:${f} is not supported.*needs message author from the server`, "i"))
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })
  it("accepts agent and a roster agent name", async () => {
    fetchMock.mockImplementation(async () => new Response(JSON.stringify({ hits: [hit({ id: "u1", role: "user" }), hit()] })))
    for (const f of ["agent", "ada"]) {
      const r = await runChatSearch({ raw: `deploy from:${f}`, scope: "all", workspaceId: "ws", roster })
      expect(r.status).toBe("complete")
      expect(r.hits.map((h) => h.id)).toEqual(["m1"])
    }
  })
  it("a roster agent named Agent or User wins over the keyword", () => {
    const named = [{ id: "a9", slug: "user", name: "User" }]
    const hits = [hit({ id: "x", agent_slug: "user", agent_name: "User" }), hit({ id: "y" })]
    expect(applyClientFilters(hits, { from: "user" }, named).map((h) => h.id)).toEqual(["x"])
  })
})

describe("runChatSearch", () => {
  it("49 hits are complete, 50 are truncated (no pagination to prove more do not exist)", async () => {
    const page = (n: number) => Array.from({ length: n }, (_, i) => hit({ id: `m${i}` }))
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ hits: page(49) })))
    expect((await runChatSearch({ raw: "deploy", scope: "all", workspaceId: "ws", roster })).status).toBe("complete")
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ hits: page(50) })))
    const r = await runChatSearch({ raw: "deploy", scope: "all", workspaceId: "ws", roster })
    expect(r.status).toBe("truncated")
    expect(r.hits).toHaveLength(50)
    expect(r.note).toBe("First 50 results, more may exist.")
  })

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
  it("rejects in: together with This chat instead of ignoring it", async () => {
    const r = await runChatSearch({ raw: "deploy in:ada", scope: "this", workspaceId: "ws", roster, current: { agentId: "a1", sessionId: "s1" } })
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
  it("windows the snippet around a match deep in a long message", () => {
    const segs = snippetSegments(`${"filler ".repeat(60)}needle ${"tail ".repeat(60)}`, "needle")
    expect(segs.find((s) => s.match)?.text).toBe("needle")
    expect(segs.map((s) => s.text).join("").length).toBeLessThanOrEqual(142)
    expect(segs[0].text.startsWith("\u2026")).toBe(true)
  })
  it("strips control characters", () => {
    expect(snippetSegments("a\u0000b‮c", "zz")).toEqual([{ text: "abc", match: false }])
  })
})
