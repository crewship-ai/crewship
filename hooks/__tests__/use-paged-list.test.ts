import { describe, it, expect } from "vitest"
import { pagedUrl, readTotalCount } from "@/hooks/use-paged-list"

describe("usePagedList helpers", () => {
  it("appends limit/offset whether or not the url already has a query", () => {
    expect(pagedUrl("/api/v1/crews?workspace_id=ws", 100, 200)).toBe("/api/v1/crews?workspace_id=ws&limit=100&offset=200")
    expect(pagedUrl("/api/v1/crews", 50, 0)).toBe("/api/v1/crews?limit=50&offset=0")
  })

  it("reads X-Total-Count and treats its absence as 'not paged yet', not zero", () => {
    expect(readTotalCount(new Headers({ "X-Total-Count": "103" }))).toBe(103)
    expect(readTotalCount(new Headers())).toBeNull()
    expect(readTotalCount(new Headers({ "X-Total-Count": "nope" }))).toBeNull()
  })
})
