import { beforeEach, describe, expect, it, vi } from "vitest"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { apiFetch } from "@/lib/api-fetch"
import { fetchAllRoutinePages } from "@/lib/routine-list"

const mockedFetch = vi.mocked(apiFetch)

describe("fetchAllRoutinePages", () => {
  beforeEach(() => mockedFetch.mockReset())

  it("loads pages using the server cursor and combines them", async () => {
    mockedFetch
      .mockResolvedValueOnce(new Response(JSON.stringify([{ id: "a" }]), {
        headers: { "X-Next-Offset": "1" },
      }))
      .mockResolvedValueOnce(new Response(JSON.stringify([{ id: "b" }])))
    const rows = await fetchAllRoutinePages<{ id: string }>("/api/routines", new AbortController().signal)
    expect(rows.map((row) => row.id)).toEqual(["a", "b"])
    expect(mockedFetch.mock.calls.map(([url]) => url)).toEqual([
      "/api/routines?limit=200&offset=0",
      "/api/routines?limit=200&offset=1",
    ])
  })

  it("accepts a legacy full-array response without pagination headers", async () => {
    mockedFetch.mockResolvedValueOnce(new Response(JSON.stringify([{ id: "legacy" }])))
    await expect(fetchAllRoutinePages("/api/routines", new AbortController().signal))
      .resolves.toEqual([{ id: "legacy" }])
  })

  it("rejects a non-advancing cursor to avoid an infinite request loop", async () => {
    mockedFetch.mockResolvedValueOnce(new Response(JSON.stringify([{ id: "a" }]), {
      headers: { "X-Next-Offset": "0" },
    }))
    await expect(fetchAllRoutinePages("/api/routines", new AbortController().signal))
      .rejects.toThrow("invalid pagination cursor")
  })
})
