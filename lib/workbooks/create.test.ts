import { describe, expect, it, vi } from "vitest"
import { createWorkbook, workbookCreationEnabled, WorkbookCreateError } from "./create"

const answer = (body: unknown, status = 200) => Promise.resolve({ ok: status < 400, status, json: async () => body } as Response)

describe("workbook creation", () => {
  it("posts name and slug to the workbook route and reads the staged Page", async () => {
    const fetch = vi.fn(() => answer({ page_id: "p1", slug: "price-model", name: "Price model" }, 201))
    await expect(createWorkbook("ws 1", { name: "Price model", slug: "price-model" }, fetch)).resolves.toEqual({ pageId: "p1", slug: "price-model", name: "Price model" })
    expect(fetch).toHaveBeenCalledWith("/api/v1/workbooks?workspace_id=ws+1", expect.objectContaining({ method: "POST", body: JSON.stringify({ name: "Price model", slug: "price-model" }) }))
  })

  it("carries the server's refusal and rejects an unconfirmed answer", async () => {
    await expect(createWorkbook("w", { name: "A", slug: "a" }, () => answer({ error: "conflict" }, 409))).rejects.toMatchObject({ status: 409, message: "conflict" })
    await expect(createWorkbook("w", { name: "A", slug: "a" }, () => answer({ slug: "a" }, 201))).rejects.toBeInstanceOf(WorkbookCreateError)
  })

  it("is available only with the workspace's live editor flag", async () => {
    await expect(workbookCreationEnabled("w", undefined, () => answer([{ key: "gridcraft_editor", enabled: false, override_enabled: true }]))).resolves.toBe(true)
    await expect(workbookCreationEnabled("w", undefined, () => answer([{ key: "gridcraft_editor", enabled: true, override_enabled: false }]))).resolves.toBe(false)
    await expect(workbookCreationEnabled("w", undefined, () => answer([], 500))).resolves.toBe(false)
  })
})
