import { afterEach, expect, it, vi } from "vitest"
const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
import { DuplicateError, duplicatePage } from "../catalog-duplicate"
afterEach(() => request.mockReset())

it("posts to the proposed route and returns the new identity", async () => {
  request.mockResolvedValue(Response.json({ page: { id: "new-id", slug: "copy" } }, { status: 201 }))
  expect(await duplicatePage("w 1", "src/id")).toEqual({ id: "new-id", slug: "copy" })
  expect(request.mock.calls[0][0]).toBe("/api/v1/pages/src%2Fid/duplicate?workspace_id=w+1")
  expect(request.mock.calls[0][1].method).toBe("POST")
})
it.each([403, 404, 409])("keeps status %i", async status => {
  request.mockResolvedValue(Response.json({ error: "no" }, { status }))
  await expect(duplicatePage("w", "x")).rejects.toMatchObject({ status, message: "no" })
  await expect(duplicatePage("w", "x")).rejects.toBeInstanceOf(DuplicateError)
})
