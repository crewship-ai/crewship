import { describe, it, expect, vi, beforeEach } from "vitest"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

import { importXlsx, initializeFromImport, XlsxImportError, bytesToBase64, XLSX_IMPORT_LIMIT } from "../craft-xlsx-import"

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
const file = new Blob([new Uint8Array([1, 2, 3])])

describe("importXlsx", () => {
  beforeEach(() => { apiFetch.mockReset(); vi.useRealTimers() })

  it("uploads, waits for the conversion, returns the import id and the notes; the document is never downloaded", async () => {
    const states: string[] = []
    apiFetch
      .mockResolvedValueOnce(json(202, { id: "imp1", state: "converting" }))
      .mockResolvedValueOnce(json(200, { id: "imp1", state: "ready", cells: 12, archive: { notes: ["Macros were not imported."] } }))
    const result = await importXlsx({ workspaceId: "ws", page: "@pg_1", file, onState: s => states.push(s) })
    expect(result.importId).toBe("imp1")
    expect(apiFetch).toHaveBeenCalledTimes(2)
    expect(result.notes).toEqual(["Macros were not imported."])
    expect(states).toEqual(["uploading", "converting"])
    expect(apiFetch.mock.calls[0][0]).toBe("/api/v1/pages/%40pg_1/craft/import?workspace_id=ws")
    expect(apiFetch.mock.calls[0][1].method).toBe("POST")
  })

  it("polls while converting, and says why a workbook was refused", async () => {
    vi.useFakeTimers()
    apiFetch
      .mockResolvedValueOnce(json(202, { id: "imp1" }))
      .mockResolvedValueOnce(json(200, { state: "converting" }))
      .mockResolvedValueOnce(json(200, { state: "failed", code: "too_large" }))
    const done = importXlsx({ workspaceId: "ws", page: "p", file })
    const settled = done.catch(e => e)
    await vi.advanceTimersByTimeAsync(1500)
    const error = await settled
    expect(error).toBeInstanceOf(XlsxImportError)
    expect(error.code).toBe("too_large")
    expect(error.message).toContain("1,000,000")
  })

  it.each([["not_xlsx", "not a readable .xlsx"], ["truncated", "nothing was imported"], ["failed", "failed"], ["surprise", "failed"]])("maps the code %s to a sentence", async (code, text) => {
    apiFetch.mockResolvedValueOnce(json(202, { id: "i" })).mockResolvedValueOnce(json(200, { state: "failed", code }))
    await expect(importXlsx({ workspaceId: "ws", page: "p", file })).rejects.toThrow(text)
  })

  it.each([[409, "not_empty"], [403, "forbidden"], [413, "too_large"], [503, "busy"]])("a %s on upload is %s", async (status, code) => {
    apiFetch.mockResolvedValueOnce(json(status, {}))
    await expect(importXlsx({ workspaceId: "ws", page: "p", file })).rejects.toMatchObject({ code })
  })

  it("never sends an empty or oversized file", async () => {
    await expect(importXlsx({ workspaceId: "ws", page: "p", file: new Blob([]) })).rejects.toMatchObject({ code: "too_large" })
    const big = { size: XLSX_IMPORT_LIMIT + 1 } as Blob
    await expect(importXlsx({ workspaceId: "ws", page: "p", file: big })).rejects.toMatchObject({ code: "too_large" })
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("stops waiting when cancelled", async () => {
    vi.useFakeTimers()
    apiFetch.mockResolvedValueOnce(json(202, { id: "i" })).mockResolvedValue(json(200, { state: "converting" }))
    const controller = new AbortController()
    const settled = importXlsx({ workspaceId: "ws", page: "p", file, signal: controller.signal }).catch(e => e)
    await vi.advanceTimersByTimeAsync(10)
    controller.abort()
    await vi.advanceTimersByTimeAsync(10)
    expect((await settled).name).toBe("AbortError")
  })
})

describe("bytesToBase64", () => {
  it("encodes large input without overflowing", () => {
    const bytes = new Uint8Array(200_000).fill(65)
    expect(atob(bytesToBase64(bytes)).length).toBe(200_000)
  })
})

describe("initializeFromImport", () => {
  it("asks the server to initialize the page from the conversion, with no body", async () => {
    apiFetch.mockResolvedValueOnce(json(201, { session_token: "t" }))
    const response = await initializeFromImport({ workspaceId: "ws", page: "@pg_1", importId: "imp 1" })
    expect(response.status).toBe(201)
    expect(apiFetch.mock.calls[0][0]).toBe("/api/v1/pages/%40pg_1/craft/import/imp%201/initialize?workspace_id=ws")
    expect(apiFetch.mock.calls[0][1]).toMatchObject({ method: "POST", redirect: "error" })
    expect(apiFetch.mock.calls[0][1].body).toBeUndefined()
  })
})

const refusedWith = async (body: unknown, headers: Record<string, string> = {}) => {
  apiFetch.mockResolvedValueOnce(new Response(JSON.stringify(body), { status: 503, headers }))
  return importXlsx({ workspaceId: "ws", page: "page", file: new Blob(["PK"]) }).catch((e: unknown) => e)
}

describe("a refused import (503)", () => {
  beforeEach(() => { apiFetch.mockReset(); vi.useRealTimers() })
  it("says busy, with the server's Retry-After", async () => {
    const e = await refusedWith({ error: "too many", code: "busy" }, { "Retry-After": "30" })
    expect(e).toBeInstanceOf(XlsxImportError)
    expect(e).toMatchObject({ code: "busy", message: "The server is converting other workbooks. Try again in a moment.", retryAfterSeconds: 30 })
  })
  it("says Sheet is not available, without a retry", async () => {
    const e = await refusedWith({ error: "off", code: "sheet_off" })
    expect(e).toMatchObject({ code: "sheet_off", message: "Sheet is not available on this server.", retryAfterSeconds: undefined })
  })
  it("treats an unknown or missing code as busy", async () => {
    expect(await refusedWith({ code: "new_reason" })).toMatchObject({ code: "busy" })
    expect(await refusedWith(null)).toMatchObject({ code: "busy" })
  })
})
