import { afterEach, describe, expect, it, vi } from "vitest"
import fixture from "@/testdata/slides-schema-v1.json"
import capture from "./real-server-capture.json"
import type { SlideDeck } from "../model"
import type { PatchRequest } from "../operations"
import type { SlidesInitializeRequest } from "../save-client"

const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
import { createHttpSlidesClient } from "../http-client"

afterEach(() => request.mockReset())
const PAGE = "fixture_page"
const HASH = "a".repeat(64)
const deck = (id = PAGE) => ({ ...structuredClone(fixture.deck), deck_id: id }) as SlideDeck
// The backend read body (PR #3837, decision F5-7): exactly five fields, no page_id, no rev.
const body = (extra: Record<string, unknown> = {}) => ({ schema_version: 1, revision: 3, order_version: 1, content_hash: HASH, deck: deck(), ...extra })
const without = (key: string) => { const copy: Record<string, unknown> = body(); delete copy[key]; return copy }
const patch = (): PatchRequest => ({ page_id: PAGE, schema_version: 1, base_rev: 3, mutation_id: "m_1", operations: [] })
const init = (): SlidesInitializeRequest => ({ page_id: PAGE, schema_version: 1, base_rev: 0, mutation_id: "m_init", deck: deck() })
const receipt = (extra: Record<string, unknown> = {}) => ({ mutation_id: "m_init", revision: 1, order_version: 1, content_hash: HASH, duplicate: false, ...extra })
const text404 = () => new Response("404 page not found\n", { status: 404, headers: { "content-type": "text/plain; charset=utf-8" } })
const jsonError = (status: number, error = "x") => Response.json({ error }, { status })
const refused = { status: "error", retryable: false }

describe("slides read (GET /pages/@id/slides)", () => {
  const client = createHttpSlidesClient("ws1")
  it("reads the id-addressed route with the workspace scope and maps revision to rev", async () => {
    request.mockResolvedValue(Response.json(body({ revision: 5, order_version: 2 })))
    expect(await client.load(PAGE)).toMatchObject({ rev: 5, orderVersion: 2, contentHash: HASH })
    expect(request.mock.calls[0][0]).toBe("/api/v1/pages/%40fixture_page/slides?workspace_id=ws1")
    expect(request.mock.calls[0][1]).toMatchObject({ cache: "no-store" })
  })
  it("refuses a deck whose deck_id is not the requested Page, as an error state", async () => {
    request.mockResolvedValueOnce(Response.json(body({ deck: deck("another_page") })))
    expect(await client.load(PAGE)).toEqual({ status: "error", retryable: false, message: "The presentation does not belong to this Page." })
  })
  it.each(["schema_version", "revision", "order_version", "content_hash", "deck"])("refuses a body without %s", async key => {
    request.mockResolvedValueOnce(Response.json(without(key)))
    expect(await client.load(PAGE)).toMatchObject(refused)
  })
  it.each([["page_id", PAGE], ["rev", 3], ["other", true]])("refuses a body with the extra field %s", async (key, value) => {
    request.mockResolvedValueOnce(Response.json(body({ [key]: value })))
    expect(await client.load(PAGE)).toMatchObject(refused)
  })
  it.each([
    ["a zero revision", { revision: 0 }], ["a fractional revision", { revision: 1.5 }], ["a zero order_version", { order_version: 0 }],
    ["a short hash", { content_hash: "abc" }], ["an upper-case hash", { content_hash: "A".repeat(64) }],
    ["a schema_version that differs from the deck", { schema_version: 2 }], ["an unsupported schema", { schema_version: 3 }],
  ])("refuses %s", async (_name, extra) => {
    request.mockResolvedValueOnce(Response.json(body(extra)))
    expect(await client.load(PAGE)).toMatchObject(refused)
  })
  it("rejects a malformed deck from the server", async () => {
    request.mockResolvedValue(Response.json(body({ deck: { schema_version: 1 } })))
    expect(await client.load(PAGE)).toMatchObject(refused)
  })
  it("reports a missing route (flag off: Go's plain 404, or 405/501) as not implemented, and a JSON 404 as not found", async () => {
    request.mockResolvedValueOnce(text404())
    expect(await client.load(PAGE)).toEqual({ status: "unavailable", reason: "not_implemented" })
    request.mockResolvedValueOnce(new Response("", { status: 405 }))
    expect(await client.load(PAGE)).toEqual({ status: "unavailable", reason: "not_implemented" })
    request.mockResolvedValueOnce(new Response("", { status: 501 }))
    expect(await client.load(PAGE)).toEqual({ status: "unavailable", reason: "not_implemented" })
    request.mockResolvedValueOnce(jsonError(404, "Slides not found"))
    expect(await client.load(PAGE)).toEqual({ status: "unavailable", reason: "not_found" })
  })
  it("maps 401 and 403, 400, 409 (unsupported schema) and 5xx", async () => {
    request.mockResolvedValueOnce(jsonError(401))
    expect(await client.load(PAGE)).toEqual({ status: "unavailable", reason: "signed_out" })
    request.mockResolvedValueOnce(jsonError(403))
    expect(await client.load(PAGE)).toEqual({ status: "unavailable", reason: "forbidden" })
    request.mockResolvedValueOnce(jsonError(409, "Slides schema unavailable"))
    expect(await client.load(PAGE)).toMatchObject(refused)
    request.mockResolvedValueOnce(jsonError(503))
    expect(await client.load(PAGE)).toMatchObject({ status: "error", retryable: true })
    request.mockRejectedValueOnce(new TypeError("offline"))
    expect(await client.load(PAGE)).toMatchObject({ status: "error", retryable: true })
  })
})

describe("slides initialize (POST /pages/@id/slides/initialize)", () => {
  const client = createHttpSlidesClient("ws1")
  it("posts exactly the five contract fields and maps the receipt", async () => {
    request.mockResolvedValue(Response.json(receipt()))
    expect(await client.initialize(init())).toEqual({ status: "initialized", receipt: { mutationId: "m_init", revision: 1, orderVersion: 1, contentHash: HASH, duplicate: false } })
    const [url, options] = request.mock.calls[0]
    expect(url).toBe("/api/v1/pages/%40fixture_page/slides/initialize?workspace_id=ws1")
    expect(options.method).toBe("POST")
    expect(Object.keys(JSON.parse(options.body)).sort()).toEqual(["base_rev", "deck", "mutation_id", "page_id", "schema_version"])
    expect(JSON.parse(options.body)).toMatchObject({ page_id: PAGE, schema_version: 1, base_rev: 0, mutation_id: "m_init" })
  })
  it("maps a duplicate receipt", async () => {
    request.mockResolvedValue(Response.json(receipt({ duplicate: true, revision: 4 })))
    expect(await client.initialize(init())).toMatchObject({ status: "initialized", receipt: { duplicate: true, revision: 4 } })
  })
  it("sends nothing for a request the contract refuses", async () => {
    const bad: unknown[] = [
      { ...init(), deck: deck("another_page") }, { ...init(), base_rev: 1 }, { ...init(), schema_version: 2 }, { ...init(), mutation_id: "" },
      { ...init(), mutation_id: "has space" }, { ...init(), page_id: "bad id" }, { ...init(), extra: 1 }, { ...init(), deck: { schema_version: 1 } },
      (({ mutation_id: _omit, ...rest }) => rest)(init()),
    ]
    for (const value of bad) expect(await client.initialize(value as SlidesInitializeRequest)).toMatchObject(refused)
    expect(request).not.toHaveBeenCalled()
  })
  it.each(["mutation_id", "revision", "order_version", "content_hash", "duplicate"])("refuses a receipt without %s", async key => {
    const copy: Record<string, unknown> = receipt(); delete copy[key]
    request.mockResolvedValueOnce(Response.json(copy))
    expect(await client.initialize(init())).toMatchObject({ status: "error", retryable: true })
  })
  it.each([
    ["an extra field", { revision: 1, page_id: PAGE }], ["another mutation_id", { mutation_id: "other" }], ["revision 0", { revision: 0 }],
    ["a short hash", { content_hash: "x" }], ["duplicate as a string", { duplicate: "false" }], ["order_version 0", { order_version: 0 }],
  ])("never reports initialized for a receipt with %s", async (_name, extra) => {
    request.mockResolvedValueOnce(Response.json(receipt(extra)))
    expect(await client.initialize(init())).toMatchObject({ status: "error", retryable: true })
  })
  it("maps the documented statuses", async () => {
    request.mockResolvedValueOnce(text404())
    expect(await client.initialize(init())).toEqual({ status: "unavailable", reason: "not_implemented" })
    request.mockResolvedValueOnce(jsonError(404))
    expect(await client.initialize(init())).toEqual({ status: "unavailable", reason: "not_found" })
    request.mockResolvedValueOnce(jsonError(400, "invalid Slides initialization"))
    expect(await client.initialize(init())).toEqual({ status: "error", retryable: false, message: "invalid Slides initialization" })
    request.mockResolvedValueOnce(jsonError(409))
    expect(await client.initialize(init())).toMatchObject({ status: "error", retryable: false })
    request.mockResolvedValueOnce(jsonError(429))
    expect(await client.initialize(init())).toMatchObject({ status: "error", retryable: false })
    request.mockResolvedValueOnce(jsonError(401))
    expect(await client.initialize(init())).toEqual({ status: "unavailable", reason: "signed_out" })
    request.mockResolvedValueOnce(jsonError(403))
    expect(await client.initialize(init())).toEqual({ status: "unavailable", reason: "forbidden" })
    request.mockResolvedValueOnce(jsonError(503))
    expect(await client.initialize(init())).toMatchObject({ status: "error", retryable: true })
    request.mockRejectedValueOnce(new TypeError("offline"))
    expect(await client.initialize(init())).toMatchObject({ status: "error", retryable: true })
  })
  it("bounds the server error text", async () => {
    request.mockResolvedValueOnce(jsonError(400, "x".repeat(5000)))
    expect((await client.initialize(init()) as { message: string }).message.length).toBeLessThanOrEqual(300)
  })
})

describe("save", () => {
  it("has no HTTP route: patch makes no request and is never reported as saved", async () => {
    expect(await createHttpSlidesClient("ws1").patch(patch())).toEqual({ status: "unavailable", reason: "not_implemented" })
    expect(request).not.toHaveBeenCalled()
  })
})

// real-server-capture.json holds bodies recorded from a server built from PR #3837
// (CREWSHIP_SLIDES_ENABLED=true): the read, the first receipt and the exact-retry receipt.
describe("bodies recorded from the real server", () => {
  const client = createHttpSlidesClient("ws1")
  it("reads the recorded head and rejects it for any other Page", async () => {
    request.mockImplementation(async () => Response.json(capture.read))
    expect(await client.load(capture.pageId)).toMatchObject({ rev: 1, orderVersion: 1, contentHash: capture.read.content_hash })
    expect(await client.load("another_page")).toMatchObject({ status: "error", message: "The presentation does not belong to this Page." })
  })
  it("maps the recorded receipts, first and duplicate", async () => {
    const send = { page_id: capture.pageId, schema_version: 1, base_rev: 0 as const, mutation_id: capture.receipt.mutation_id, deck: capture.read.deck as SlideDeck }
    request.mockResolvedValueOnce(Response.json(capture.receipt))
    expect(await client.initialize(send)).toEqual({ status: "initialized", receipt: { mutationId: "m_probe_1", revision: 1, orderVersion: 1, contentHash: capture.receipt.content_hash, duplicate: false } })
    request.mockResolvedValueOnce(Response.json(capture.duplicate))
    expect(await client.initialize(send)).toMatchObject({ status: "initialized", receipt: { duplicate: true, contentHash: capture.receipt.content_hash } })
  })
})
