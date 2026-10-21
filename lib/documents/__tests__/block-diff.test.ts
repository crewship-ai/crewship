import { describe, expect, it } from "vitest"
import { diffBlocks, summarizeDiff } from "../block-diff"

const p = (id: string, text: string) => ({ type: "paragraph", attrs: { id }, content: [{ type: "text", text }] })
const doc = (...content: unknown[]) => ({ type: "doc", content })

describe("diffBlocks", () => {
  it("reports no change for identical documents", () => {
    const d = diffBlocks(doc(p("a", "one")), doc(p("a", "one")))
    expect(d.every(entry => entry.kind === "same")).toBe(true)
    expect(summarizeDiff(d)).toEqual({ added: 0, removed: 0, changed: 0, moved: 0 })
  })
  it("reports added, removed and changed blocks by stable ID", () => {
    const d = diffBlocks(doc(p("a", "one"), p("b", "two")), doc(p("a", "one!"), p("c", "three")))
    expect(d.map(e => [e.kind, e.id])).toEqual([["changed", "a"], ["removed", "b"], ["added", "c"]])
    expect(d[0]).toMatchObject({ before: "one", after: "one!" })
  })
  it("reports a reorder as moved, not as delete and insert", () => {
    const d = diffBlocks(doc(p("a", "one"), p("b", "two")), doc(p("b", "two"), p("a", "one")))
    expect(summarizeDiff(d)).toMatchObject({ added: 0, removed: 0, moved: 1 })
  })
  it("reads text from nested blocks such as tables and lists", () => {
    const table = (text: string) => ({ type: "table", attrs: { id: "t" }, content: [{ type: "tableRow", content: [{ type: "tableCell", content: [p("x", text)] }] }] })
    const d = diffBlocks(doc(table("old")), doc(table("new")))
    expect(d[0]).toMatchObject({ kind: "changed", before: "old", after: "new", type: "table" })
  })
  it("never throws on odd input and shows an edited id-less block as removed plus added", () => {
    expect(summarizeDiff(diffBlocks(doc({ type: "paragraph", content: [{ type: "text", text: "a" }] }), doc({ type: "paragraph", content: [{ type: "text", text: "b" }] })))).toMatchObject({ removed: 1, added: 1 })
    expect(diffBlocks(undefined, null)).toEqual([])
  })
  it("sees a link target change even though the text is identical", () => {
    const link = (href: string) => doc({ type: "paragraph", attrs: { id: "a" }, content: [{ type: "text", text: "docs", marks: [{ type: "link", attrs: { href } }] }] })
    const d = diffBlocks(link("https://a.example"), link("https://evil.example"))
    expect(d[0]).toMatchObject({ kind: "changed", before: "docs", after: "docs" })
    expect(d[0].beforeFormat).toContain("https://a.example")
    expect(d[0].afterFormat).toContain("https://evil.example")
  })
  it("sees heading level, code language and task state changes", () => {
    const h = (level: number) => doc({ type: "heading", attrs: { id: "h", level }, content: [{ type: "text", text: "T" }] })
    expect(diffBlocks(h(1), h(2))[0]).toMatchObject({ kind: "changed", beforeFormat: expect.stringContaining("level=1"), afterFormat: expect.stringContaining("level=2") })
  })
  it("matches blocks without an ID by content, so a top insert is one addition", () => {
    const np = (text: string) => ({ type: "paragraph", content: [{ type: "text", text }] })
    const d = diffBlocks(doc(np("a"), np("b"), np("c")), doc(np("new"), np("a"), np("b"), np("c")))
    expect(summarizeDiff(d)).toEqual({ added: 1, removed: 0, changed: 0, moved: 0 })
  })
  it("keeps duplicate id-less blocks distinct", () => {
    const np = (text: string) => ({ type: "paragraph", content: [{ type: "text", text }] })
    expect(summarizeDiff(diffBlocks(doc(np("x"), np("x")), doc(np("x"))))).toMatchObject({ removed: 1, added: 0 })
  })
  it("reports one moved block when the last block goes to the top", () => {
    const d = diffBlocks(doc(p("a", "1"), p("b", "2"), p("c", "3"), p("d", "4")), doc(p("d", "4"), p("a", "1"), p("b", "2"), p("c", "3")))
    expect(summarizeDiff(d).moved).toBe(1)
  })
  it("compares a thousand fully replaced blocks without blocking", () => {
    const many = (prefix: string) => doc(...Array.from({ length: 1000 }, (_, i) => p(`${prefix}${i}`, `${prefix} text ${i}`)))
    const started = performance.now()
    const d = diffBlocks(many("a"), many("b"))
    expect(summarizeDiff(d)).toMatchObject({ added: 1000, removed: 1000 })
    expect(performance.now() - started).toBeLessThan(500)
  })
})
