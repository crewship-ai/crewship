import { describe, it, expect } from "vitest"
import { sheetIdsFromRefs } from "../use-conversation-sheets"

describe("Sheets a conversation carried, as the server lists them", () => {
  it("keeps Sheets only, each once, in the server's order, and adds those opened here after them", () => {
    const page = { refs: [
      { page_id: "a", application: "gridcraft" }, { page_id: "w", application: "wordcraft" },
      { page_id: "a", application: "gridcraft" }, { page_id: "b" },
    ] }
    expect(sheetIdsFromRefs([page], ["c", "a"])).toEqual(["a", "b", "c"])
  })
  it("ignores what it does not understand", () => {
    expect(sheetIdsFromRefs([{ nope: 1 }, { refs: [{ page_id: 5 }, null] }], [])).toEqual([])
  })
})
