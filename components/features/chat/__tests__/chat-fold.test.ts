import { describe, expect, it } from "vitest"

import { foldsFor, readTotal, type ChatTreeThread } from "../chat-tree-data"

const thread = (id: string): ChatTreeThread => ({
  id, title: id, status: "ACTIVE", message_count: 1, started_at: "2026-09-03T10:00:00Z",
})

describe("foldsFor", () => {
  it("says how many more each agent has than the page holds", () => {
    const threads = { riley: [thread("a"), thread("b")], alex: [thread("c")] }
    expect(foldsFor(threads, { riley: 19, alex: 1 })).toEqual({ riley: 17 })
  })

  it("stays silent for an agent the server said nothing about", () => {
    expect(foldsFor({ riley: [thread("a")] }, {})).toEqual({})
  })

  it("counts an agent whose page is empty but whose total is not", () => {
    // The fan-out can fail one agent while the count arrived: nothing loaded,
    // three exist. That is three more, not zero.
    expect(foldsFor({}, { sam: 3 })).toEqual({ sam: 3 })
  })
})

describe("readTotal", () => {
  it("reads the header and refuses nonsense", () => {
    expect(readTotal("19")).toBe(19)
    expect(readTotal("0")).toBe(0)
    expect(readTotal(null)).toBeNull()
    expect(readTotal("many")).toBeNull()
    expect(readTotal("-1")).toBeNull()
  })
})
