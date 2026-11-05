import { describe, expect, it } from "vitest"
import * as Y from "yjs"
import { designConflictIdentity, designConflicts, designNodes, mergeDesignConflictRecord, restoreDesignConflict, upsertDesignNode, removeDesignNode, type DesignConflict } from "../design-document"

describe("Design Yjs document", () => {
 it("merges concurrent changes to different properties and preserves children", () => {
  const a = new Y.Doc(), b = new Y.Doc()
  upsertDesignNode(a, { id: "screen", type: "frame", name: "Checkout", x: 0, y: 0, width: 360, height: 640, fill: "#ffffff" })
  upsertDesignNode(a, { id: "label", type: "text", parentId: "screen", text: "Hello", x: 24, y: 24, width: 200, height: 40, fill: "#111111" })
  Y.applyUpdate(b, Y.encodeStateAsUpdate(a))
  upsertDesignNode(a, { id: "label", text: "Cart" })
  upsertDesignNode(b, { id: "label", fill: "#ff0000" })
  const updateA = Y.encodeStateAsUpdate(a), updateB = Y.encodeStateAsUpdate(b)
  Y.applyUpdate(a, updateB); Y.applyUpdate(b, updateA)
  expect(designNodes(a)).toEqual(designNodes(b))
  expect(designNodes(a)[1]).toMatchObject({ text: "Cart", fill: "#ff0000", parentId: "screen" })
 })
 it("does not resurrect a concurrently deleted node from a stale property edit", () => {
  const doc = new Y.Doc()
  upsertDesignNode(doc, { id: "gone", fill: "#ff0000" })
  expect(designNodes(doc)).toEqual([])
  expect(doc.getArray("order").length).toBe(0)
 })
 it("removes the frame and its descendants in one transaction", () => {
  const doc = new Y.Doc()
  upsertDesignNode(doc, { id: "frame", type: "frame" })
  upsertDesignNode(doc, { id: "rect", type: "rect", parentId: "frame" })
  removeDesignNode(doc, "frame")
  expect(designNodes(doc)).toEqual([])
  expect(doc.getArray("order").toArray()).toEqual([])
 })
})

describe("Design text conflicts after a stale canvas", () => {
 const seeded = () => {
  const base = new Y.Doc()
  upsertDesignNode(base, { id: "label", type: "text", name: "Title", text: "Base", x: 0, y: 0, width: 200, height: 40, fill: "#111111" })
  const mine = new Y.Doc(), theirs = new Y.Doc()
  const state = Y.encodeStateAsUpdate(base)
  Y.applyUpdate(mine, state); Y.applyUpdate(theirs, state)
  return { base: designNodes(base), mine, theirs }
 }
 const merged = (mine: Y.Doc, theirs: Y.Doc) => {
  const doc = new Y.Doc()
  Y.applyUpdate(doc, Y.encodeStateAsUpdate(mine)); Y.applyUpdate(doc, Y.encodeStateAsUpdate(theirs))
  return designNodes(doc)
 }
 it("names the text that lost, whichever side won the merge", () => {
  const { base, mine, theirs } = seeded()
  upsertDesignNode(mine, { id: "label", text: "Mine" })
  upsertDesignNode(theirs, { id: "label", text: "Theirs" })
  const after = merged(mine, theirs)
  const [conflict, ...rest] = designConflicts(base, designNodes(mine), designNodes(theirs), after)
  expect(rest).toEqual([])
  expect(conflict).toMatchObject({ id: "label", name: "Title", mine: "Mine", theirs: "Theirs", kept: after[0].text === "Mine" ? "mine" : "theirs" })
  expect(conflict.restore.text).toBe(conflict.kept === "mine" ? "Theirs" : "Mine")
 })
 it("stays silent when only one side changed the text or both typed the same", () => {
  const { base, mine, theirs } = seeded()
  upsertDesignNode(mine, { id: "label", text: "Mine" })
  expect(designConflicts(base, designNodes(mine), designNodes(theirs), merged(mine, theirs))).toEqual([])
  upsertDesignNode(theirs, { id: "label", text: "Mine" })
  expect(designConflicts(base, designNodes(mine), designNodes(theirs), merged(mine, theirs))).toEqual([])
 })
 it("keeps a text whose layer the other person deleted", () => {
  const { base, mine, theirs } = seeded()
  upsertDesignNode(mine, { id: "label", text: "Mine" })
  removeDesignNode(theirs, "label")
  const after = merged(mine, theirs)
  expect(after).toEqual([])
  expect(designConflicts(base, designNodes(mine), designNodes(theirs), after)).toMatchObject([{ id: "label", mine: "Mine", theirs: "", kept: "theirs", restore: { id: "label", type: "text", text: "Mine" } }])
 })
})

describe("restoring a conflict's text", () => {
 const layer = { id: "label", type: "text" as const, name: "Title", text: "Merged", x: 10, y: 20, width: 200, height: 40, fill: "#111111", linkTo: "screen-b" }
 const conflict = (over: Partial<DesignConflict> = {}): DesignConflict => ({ id: "label", name: "Title", mine: "Lost", theirs: "Merged", kept: "theirs", left: "Merged", restore: { ...layer, name: "Stale", x: 0, y: 0, fill: "#000000", linkTo: undefined, text: "Lost" }, ...over })
 const start = () => { const doc = new Y.Doc(); upsertDesignNode(doc, layer); return doc }
 it("changes only the text of a layer that still exists", () => {
  const doc = start()
  upsertDesignNode(doc, { id: "label", x: 99, fill: "#ff0000", name: "Renamed" })
  expect(restoreDesignConflict(doc, conflict())).toBe("text")
  expect(designNodes(doc)).toMatchObject([{ id: "label", text: "Lost", x: 99, y: 20, fill: "#ff0000", name: "Renamed", linkTo: "screen-b" }])
 })
 it("restores the other person's text the same way when this side won", () => {
  const doc = start()
  upsertDesignNode(doc, { id: "label", text: "Mine" })
  expect(restoreDesignConflict(doc, conflict({ mine: "Mine", theirs: "Theirs", kept: "mine", left: "Mine" }))).toBe("text")
  expect(designNodes(doc)[0].text).toBe("Theirs")
 })
 it("does not replace text changed after the conflict: it goes into a separate layer", () => {
  const doc = start()
  upsertDesignNode(doc, { id: "label", text: "Newer" })
  expect(restoreDesignConflict(doc, conflict())).toBe("layer")
  const [kept, restored] = designNodes(doc)
  expect(kept).toMatchObject({ id: "label", text: "Newer", name: "Title" })
  expect(restored).toMatchObject({ type: "text", text: "Lost", x: 10, y: 68, width: 200, height: 40, fill: "#111111", name: "Title (obnovený text)" })
  expect(restored.id).not.toBe("label")
  expect(restored.linkTo).toBeUndefined()
 })
 it("adds nothing when the text is already back", () => {
  const doc = start()
  upsertDesignNode(doc, { id: "label", text: "Lost" })
  expect(restoreDesignConflict(doc, conflict())).toBe("none")
  expect(designNodes(doc)).toHaveLength(1)
 })
 it("recreates a deleted layer from the full snapshot", () => {
  const doc = start()
  removeDesignNode(doc, "label")
  expect(restoreDesignConflict(doc, conflict({ left: undefined }))).toBe("recreated")
  expect(designNodes(doc)).toMatchObject([{ id: "label", text: "Lost", name: "Stale", x: 0, fill: "#000000" }])
 })
 it("recreates a layer whose screen is gone without a dangling parent", () => {
  const doc = new Y.Doc()
  expect(restoreDesignConflict(doc, conflict({ restore: { ...layer, parentId: "gone", text: "Lost" } }))).toBe("recreated")
  expect(designNodes(doc)[0].parentId).toBeUndefined()
 })
 it("says what the text is when a text lost to a deletion", () => {
  const base = [layer], mine = [{ ...layer, text: "Mine" }]
  const [found] = designConflicts(base, mine, [], [])
  expect(found).toMatchObject({ mine: "Mine", kept: "theirs", left: undefined, restore: { text: "Mine" } })
 })
})

describe("the durable conflict record", () => {
 const one = (id: string, mine = "Mine"): DesignConflict => ({ id, name: id, mine, theirs: "Theirs", kept: "theirs", left: "Theirs", restore: { id, type: "text", text: mine, x: 0, y: 0, width: 1, height: 1, fill: "#000" } })
 const merged = (existing: string, add: DesignConflict[], remove: string[]) => JSON.parse(mergeDesignConflictRecord(existing, add, remove) || "[]") as DesignConflict[]
 it("identity depends on the layer and the two texts", () => {
  expect(designConflictIdentity(one("a"))).toBe(designConflictIdentity({ ...one("a"), name: "Renamed" }))
  expect(designConflictIdentity(one("a"))).not.toBe(designConflictIdentity(one("a", "Other")))
 })
 it("adds and removes against what is stored", () => {
  const stored = JSON.stringify([one("a"), one("b")])
  expect(merged(stored, [one("c")], [designConflictIdentity(one("a"))]).map(item => item.id)).toEqual(["b", "c"])
 })
 it("adds one record once", () => {
  expect(merged(JSON.stringify([one("a")]), [one("a")], [])).toHaveLength(1)
 })
 it("stores nothing once the list is empty", () => {
  expect(mergeDesignConflictRecord(JSON.stringify([one("a")]), [], [designConflictIdentity(one("a"))])).toBe("")
 })
 it("refuses to overwrite a record it cannot read", () => {
  expect(() => mergeDesignConflictRecord("not json", [one("a")], [])).toThrow()
 })
})
