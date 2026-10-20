import { afterEach, describe, expect, it, vi } from "vitest"
import fixture from "@/testdata/slides-schema-v1.json"
import type { SlideDeck } from "../model"
import type { PatchRequest } from "../operations"

const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
import { createHttpSlidesClient } from "../http-client"

afterEach(() => request.mockReset())
const deck = () => structuredClone(fixture.deck) as SlideDeck
const head = { deck: deck(), rev: 3, order_version: 1 }
const patch = (): PatchRequest => ({ page_id: "p1", schema_version: 1, base_rev: 3, mutation_id: "m_1", operations: [] })
const text404 = () => new Response("404 page not found\n", { status: 404, headers: { "content-type": "text/plain; charset=utf-8" } })

describe("http slides client", () => {
  const client = createHttpSlidesClient("ws1")
  it("loads from the id-addressed route with the workspace scope", async () => {
    request.mockResolvedValue(Response.json(head))
    const result = await client.load("p1")
    expect(result).toMatchObject({ rev: 3, orderVersion: 1 })
    expect(request.mock.calls[0][0]).toBe("/api/v1/pages/%40p1/slides?workspace_id=ws1")
  })
  it("reports a missing route as not implemented, and a JSON 404 as not found", async () => {
    request.mockResolvedValueOnce(text404())
    expect(await client.load("p1")).toEqual({ status: "unavailable", reason: "not_implemented" })
    request.mockResolvedValueOnce(Response.json({ error: "no such page" }, { status: 404 }))
    expect(await client.load("p1")).toEqual({ status: "unavailable", reason: "not_found" })
    request.mockResolvedValueOnce(new Response("", { status: 405 }))
    expect(await client.patch(patch())).toEqual({ status: "unavailable", reason: "not_implemented" })
  })
  it("never treats an unverifiable 2xx as saved", async () => {
    request.mockResolvedValueOnce(Response.json({ deck: deck(), rev: 4, order_version: 1, mutation_id: "other" }))
    expect(await client.patch(patch())).toMatchObject({ status: "error", retryable: true })
    request.mockResolvedValueOnce(Response.json({ deck: deck(), rev: 3, order_version: 1, mutation_id: "m_1" }))
    expect(await client.patch(patch())).toMatchObject({ status: "error" })
    request.mockResolvedValueOnce(Response.json({ ok: true }))
    expect(await client.patch(patch())).toMatchObject({ status: "error" })
    request.mockResolvedValueOnce(Response.json({ deck: deck(), rev: 4, order_version: 1, mutation_id: "m_1" }))
    expect(await client.patch(patch())).toMatchObject({ status: "saved", snapshot: { rev: 4 }, mutationId: "m_1" })
  })
  it("maps 409 to the fresh head, 400 to the server text, 503 and network errors to retryable", async () => {
    request.mockResolvedValueOnce(Response.json({ ...head, rev: 9 }, { status: 409 }))
    expect(await client.patch(patch())).toMatchObject({ status: "conflict", current: { rev: 9 } })
    request.mockResolvedValueOnce(Response.json({ error: "operation 0: bad" }, { status: 400 }))
    expect(await client.patch(patch())).toEqual({ status: "error", retryable: false, message: "operation 0: bad" })
    request.mockResolvedValueOnce(Response.json({ error: "x" }, { status: 503 }))
    expect(await client.patch(patch())).toMatchObject({ status: "error", retryable: true })
    request.mockRejectedValueOnce(new TypeError("offline"))
    expect(await client.patch(patch())).toMatchObject({ status: "error", retryable: true })
  })
  it("rejects a malformed deck from the server", async () => {
    request.mockResolvedValue(Response.json({ deck: { schema_version: 1 }, rev: 1, order_version: 1 }))
    expect(await client.load("p1")).toMatchObject({ status: "error", retryable: false })
  })
})
