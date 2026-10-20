import { afterEach, expect, it, vi } from "vitest"
import { checkpointFetcher, fetchArtifactPreview, fetchLinks, fetchWorkCards, parseLibrary, parseLinks, parseWorkCards } from "../client"

afterEach(() => vi.restoreAllMocks())
const respond = (status: number, body?: unknown, type = "application/json") =>
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(body === undefined ? null : typeof body === "string" ? body : JSON.stringify(body), { status, headers: { "content-type": type } }))

it.each([[404, "404 page not found", "text/plain"], [405, undefined, "text/plain"], [501, undefined, "text/plain"]] as const)("a list endpoint answering %i is unsupported", async (status, body, type) => {
  respond(status, body, type)
  expect(await fetchLinks("w", "c")).toEqual({ status: "unsupported" })
  expect(await fetchWorkCards("w", "c", "u")).toEqual({ status: "unsupported" })
})
it("a JSON 404 on an item call means absent, a plain 404 means the route is missing", async () => {
  respond(404, { error: "not found" })
  expect(await fetchArtifactPreview("w", "c", "p")).toEqual({ status: "absent" })
  respond(404, "404 page not found", "text/plain")
  expect(await fetchArtifactPreview("w", "c", "p")).toEqual({ status: "unsupported" })
})
it("separates denied, temporary and malformed answers", async () => {
  respond(403, {})
  expect(await fetchLinks("w", "c")).toEqual({ status: "denied" })
  respond(503, {})
  expect(await fetchLinks("w", "c")).toEqual({ status: "error" })
  respond(200, { links: "nope" })
  expect(await fetchLinks("w", "c")).toEqual({ status: "error" })
  vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("network"))
  expect(await fetchLinks("w", "c")).toEqual({ status: "error" })
})
it("a restricted work card exposes no name, type or open target", () => {
  const [entry] = parseWorkCards({ cards: [{ id: "k", run_state: "applied", saved: { revision: 3, checkpoint_id: "cp" }, freshness: "current", problem: null, page_id: "page", access: "restricted", executor: "Secret agent", can: { open: true } }] }, "w", "u")!
  expect(entry.model.access).toEqual({ status: "restricted" })
  expect(entry.title).toBeNull()
  expect(entry.target).toBeNull()
})
it("a saved card opens the exact checkpoint when it has one, otherwise the latest", () => {
  const card = (saved: object) => ({ id: "k", run_state: "applied", saved, freshness: "current", problem: null, page_id: "page", access: { title: "T", type: "Document" }, executor: "A", on_behalf_of_user_id: "u", can: { open: true } })
  const [pinned, latest] = parseWorkCards({ cards: [card({ revision: 3, checkpoint_id: "cp-3" }), card({ revision: 3 })] }, "w", "u")!
  expect(pinned.target).toEqual({ workspaceId: "w", pageId: "page", mode: "revision", revision: 3, checkpointId: "cp-3" })
  expect(latest.target).toEqual({ workspaceId: "w", pageId: "page", mode: "latest" })
  expect(pinned.model.requester).toBe("you")
  expect(pinned.model.can.compare).toBe(false)
})
it("drops cards and links the contract does not describe instead of guessing", () => {
  expect(parseWorkCards({ cards: [{ id: "k", run_state: "mystery" }] }, "w", "u")).toEqual([])
  expect(parseLinks({ links: [{ link_id: "x", page_id: "p", name: "Hidden", accessible: false }, { link_id: "y", page_id: "p", name: "Shown", accessible: true }] }, "w")!.map(link => link.title)).toEqual(["Shown"])
})
it("library rows without a name or flagged restricted carry only their id", () => {
  const rows = parseLibrary({ items: [{ link_id: "a", kind: "file", name: "n.csv", page_id: "p" }, { link_id: "b", kind: "file", restricted: true, name: "leak" }, { link_id: "c", kind: "tool" }] }, "w")!
  expect(rows.map(row => row.item)).toEqual([
    { status: "readable", linkId: "a", kind: "files", name: "n.csv", revisionKey: "p:latest" },
    { status: "restricted", linkId: "b" }, { status: "restricted", linkId: "c" },
  ])
})
it("the checkpoint reader is called without a revision query", async () => {
  const spy = respond(200, { page_id: "p" })
  await checkpointFetcher("w")("/api/v1/pages/%40p/document/checkpoints/cp-1")
  const url = String(spy.mock.calls[0][0])
  expect(url).toBe("/api/v1/pages/%40p/document/checkpoints/cp-1?workspace_id=w")
})
