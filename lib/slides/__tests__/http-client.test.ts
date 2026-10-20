import { afterEach, describe, expect, it, vi } from "vitest"
import fixture from "@/testdata/slides-schema-v1.json"
import type { SlideDeck } from "../model"
import type { PatchRequest } from "../operations"

const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
import { createHttpSlidesClient } from "../http-client"

afterEach(() => request.mockReset())
const PAGE = "fixture_page"
const deck = () => structuredClone(fixture.deck) as SlideDeck
const head = { deck: deck(), rev: 3, order_version: 1 }
const patch = (): PatchRequest => ({ page_id: PAGE, schema_version: 1, base_rev: 3, mutation_id: "m_1", operations: [] })
const text404 = () => new Response("404 page not found\n", { status: 404, headers: { "content-type": "text/plain; charset=utf-8" } })

describe("http slides client", () => {
  const client = createHttpSlidesClient("ws1")
  it("loads from the id-addressed route with the workspace scope", async () => {
    request.mockResolvedValue(Response.json(head))
    const result = await client.load(PAGE)
    expect(result).toMatchObject({ rev: 3, orderVersion: 1 })
    expect(request.mock.calls[0][0]).toBe("/api/v1/pages/%40fixture_page/slides?workspace_id=ws1")
  })
  it("reports a missing route as not implemented, and a JSON 404 as not found", async () => {
    request.mockResolvedValueOnce(text404())
    expect(await client.load(PAGE)).toEqual({ status: "unavailable", reason: "not_implemented" })
    request.mockResolvedValueOnce(Response.json({ error: "no such page" }, { status: 404 }))
    expect(await client.load(PAGE)).toEqual({ status: "unavailable", reason: "not_found" })
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
  it("treats 501 as a missing route", async () => {
    request.mockResolvedValueOnce(new Response("", { status: 501 }))
    expect(await client.load(PAGE)).toEqual({ status: "unavailable", reason: "not_implemented" })
    request.mockResolvedValueOnce(new Response("", { status: 501 }))
    expect(await client.patch(patch())).toEqual({ status: "unavailable", reason: "not_implemented" })
  })
  it("maps 401 on save to signed_out and JSON 403/404 to forbidden/not_found", async () => {
    request.mockResolvedValueOnce(Response.json({ error: "x" }, { status: 401 }))
    expect(await client.patch(patch())).toEqual({ status: "unavailable", reason: "signed_out" })
    request.mockResolvedValueOnce(Response.json({ error: "x" }, { status: 403 }))
    expect(await client.patch(patch())).toEqual({ status: "unavailable", reason: "forbidden" })
    request.mockResolvedValueOnce(Response.json({ error: "x" }, { status: 404 }))
    expect(await client.patch(patch())).toEqual({ status: "unavailable", reason: "not_found" })
  })
  it("refuses a deck that belongs to another Page on load, 409 and save", async () => {
    const other = { ...deck(), deck_id: "another_page" }
    const mismatch = { status: "error", retryable: false, message: "The presentation does not belong to this Page." }
    request.mockResolvedValueOnce(Response.json({ deck: other, rev: 3, order_version: 1 }))
    expect(await client.load(PAGE)).toEqual(mismatch)
    request.mockResolvedValueOnce(Response.json({ deck: other, rev: 9, order_version: 1 }, { status: 409 }))
    expect(await client.patch(patch())).toEqual(mismatch)
    request.mockResolvedValueOnce(Response.json({ deck: other, rev: 4, order_version: 1, mutation_id: "m_1" }))
    expect(await client.patch(patch())).toEqual(mismatch)
  })
  it("bounds the server error text shown to users", async () => {
    request.mockResolvedValueOnce(Response.json({ error: "x".repeat(5000) }, { status: 400 }))
    const result = await client.patch(patch())
    expect(result.status).toBe("error")
    expect((result as { message: string }).message.length).toBeLessThanOrEqual(300)
  })
  it("rejects a malformed deck from the server", async () => {
    request.mockResolvedValue(Response.json({ deck: { schema_version: 1 }, rev: 1, order_version: 1 }))
    expect(await client.load(PAGE)).toMatchObject({ status: "error", retryable: false })
  })
})
