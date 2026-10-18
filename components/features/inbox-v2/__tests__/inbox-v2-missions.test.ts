import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"

import { fetchAllMissions } from "../inbox-v2-missions"

const mission = (id: string) => ({ id }) as never
const page = (ids: string[]) => ({ ok: true, status: 200, json: async () => ids.map(mission) }) as unknown as Response
const full = (prefix: string) => page(Array.from({ length: 100 }, (_, i) => `${prefix}${i}`))

describe("fetchAllMissions", () => {
  let fetchMock: ReturnType<typeof vi.fn>
  beforeEach(() => {
    fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)
  })
  afterEach(() => vi.unstubAllGlobals())

  it("walks full pages until a short page", async () => {
    fetchMock.mockResolvedValueOnce(full("a")).mockResolvedValueOnce(page(["z"]))
    const rows = await fetchAllMissions("ws-1")
    expect(rows).toHaveLength(101)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("terminates when the server ignores offset and repeats the same full page", async () => {
    // Bounded so a regression fails the assertion rather than hanging.
    for (let i = 0; i < 20; i++) fetchMock.mockResolvedValueOnce(full("a"))
    fetchMock.mockResolvedValue(page([]))
    const rows = await fetchAllMissions("ws-1")
    expect(fetchMock.mock.calls.length).toBeLessThanOrEqual(2)
    expect(rows).toHaveLength(100)
  })
})
