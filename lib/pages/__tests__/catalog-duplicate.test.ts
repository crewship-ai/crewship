import { afterEach, expect, it, vi } from "vitest"
const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
import { DuplicateError, duplicatePage, newMutationId } from "../catalog-duplicate"
afterEach(() => request.mockReset())

const req = { id: "src/id", mutationId: "m-1", pagesVersion: 7 }

it("posts the mutation identity and the expected source", async () => {
  request.mockResolvedValue(Response.json({ page: { id: "new-id", slug: "copy" } }, { status: 201 }))
  expect(await duplicatePage("w 1", req)).toEqual({ id: "new-id", slug: "copy" })
  const [url, init] = request.mock.calls[0]
  expect(url).toBe("/api/v1/pages/src%2Fid/duplicate?workspace_id=w+1")
  expect(init.method).toBe("POST")
  expect(init.headers["Idempotency-Key"]).toBe("m-1")
  expect(JSON.parse(init.body)).toEqual({ mutation_id: "m-1", expected_source: { id: "src/id", pages_version: 7 } })
})
it("sends the same key on a retry after a lost response", async () => {
  request.mockRejectedValueOnce(new TypeError("network")).mockResolvedValue(Response.json({ page: { id: "n", slug: "c" } }, { status: 201 }))
  await expect(duplicatePage("w", req)).rejects.toThrow("network")
  await duplicatePage("w", req)
  expect(request.mock.calls.map(c => c[1].headers["Idempotency-Key"])).toEqual(["m-1", "m-1"])
})
it("creates distinct keys per action", () => {
  expect(newMutationId()).not.toBe(newMutationId())
})
it.each([403, 404, 409])("keeps status %i", async status => {
  request.mockResolvedValue(Response.json({ error: "no" }, { status }))
  await expect(duplicatePage("w", req)).rejects.toMatchObject({ status, message: "no" })
  await expect(duplicatePage("w", req)).rejects.toBeInstanceOf(DuplicateError)
})
