import { describe, expect, it, vi } from "vitest"
import { createDocumentPeople, formatDocumentDate, UNKNOWN_MEMBER } from "../people"

const ok = (people: unknown) => ({ ok: true, json: async () => ({ people }) }) as Response

describe("document people", () => {
  it("asks once for a batch of ids, names only what the server names, and is neutral otherwise", async () => {
    const fetcher = vi.fn(async (url: string) => {
      void url
      return ok([{ id: "bea", name: "Bea Member" }, { id: "evil id", name: "x" }, { id: "long", name: "x".repeat(201) }])
    })
    const people = createDocumentPeople("ws1", "@page-1", fetcher)
    const updated = vi.fn()
    people.subscribe(updated)
    expect([people.label("bea"), people.label("ghost"), people.label("long"), people.label("bad id")]).toEqual([UNKNOWN_MEMBER, UNKNOWN_MEMBER, UNKNOWN_MEMBER, UNKNOWN_MEMBER])
    await vi.waitFor(() => expect(updated).toHaveBeenCalled())
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(decodeURIComponent(fetcher.mock.calls[0][0])).toContain("/api/v1/pages/@page-1/document/people?ids=bea,ghost,long")
    expect([people.label("bea"), people.label("ghost"), people.label("long")]).toEqual(["Bea Member", UNKNOWN_MEMBER, UNKNOWN_MEMBER])
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it("shows every name as Unknown member when the lookup fails", async () => {
    const people = createDocumentPeople("ws1", "doc", async () => { throw new Error("offline") })
    const updated = vi.fn(); people.subscribe(updated)
    people.label("bea")
    await vi.waitFor(() => expect(updated).toHaveBeenCalled())
    expect(people.label("bea")).toBe(UNKNOWN_MEMBER)
    await expect(createDocumentPeople("ws1", "doc", async () => ({ ok: false }) as Response).search("b")).rejects.toThrow()
  })

  it("formats a calendar day without shifting it, including years 1-99", () => {
    const format = (value: string) => formatDocumentDate(value, "en-US")
    expect(format("2026-12-01")).toBe("Dec 1, 2026")
    expect(format("0050-01-01")).toMatch(/^Jan 1, 0*50$/)
    expect(format("not a date")).toBe("not a date")
  })
})
