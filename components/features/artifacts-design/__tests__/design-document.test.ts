import { describe, expect, it } from "vitest"
import * as Y from "yjs"
import { designConflicts, designNodes, upsertDesignNode, removeDesignNode } from "../design-document"

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
