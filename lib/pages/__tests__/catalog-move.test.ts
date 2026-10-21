import { afterEach, expect, it, vi } from "vitest"
const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
import { MoveError, movePage } from "../catalog-move"
afterEach(() => request.mockReset())

it("posts the target and both versions to the proposed route", async () => {
  request.mockResolvedValue(Response.json({ page: { id: "p" } }))
  await movePage("w", { id: "p/1", targetFolder: "ops", pagesVersion: 2, aclVersion: 5 })
  expect(request.mock.calls[0][0]).toBe("/api/v1/pages/p%2F1/move?workspace_id=w")
  expect(JSON.parse(request.mock.calls[0][1].body)).toEqual({ target_folder: "ops", pages_version: 2, acl_version: 5 })
})
it.each([403, 404, 409, 503])("keeps status %i", async status => {
  request.mockResolvedValue(Response.json({ error: "no" }, { status }))
  await expect(movePage("w", { id: "p", targetFolder: "ops", pagesVersion: 1, aclVersion: 1 })).rejects.toMatchObject({ status, message: "no" })
  await expect(movePage("w", { id: "p", targetFolder: "ops", pagesVersion: 1, aclVersion: 1 })).rejects.toBeInstanceOf(MoveError)
})
