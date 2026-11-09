import { describe, expect, it } from "vitest"
import * as Y from "yjs"
import { designNodes, reorderDesignNode, upsertDesignNode } from "../design-document"

function fixture() {
 const doc = new Y.Doc()
 upsertDesignNode(doc, { id: "s1", type: "frame", name: "S1", x: 0, y: 0, width: 100, height: 100, fill: "#fff" })
 upsertDesignNode(doc, { id: "a", type: "rect", parentId: "s1", x: 0, y: 0, width: 10, height: 10, fill: "#000" })
 upsertDesignNode(doc, { id: "s2", type: "frame", name: "S2", x: 0, y: 0, width: 100, height: 100, fill: "#fff" })
 upsertDesignNode(doc, { id: "b", type: "rect", parentId: "s1", x: 0, y: 0, width: 10, height: 10, fill: "#000" })
 upsertDesignNode(doc, { id: "x", type: "rect", parentId: "s2", x: 0, y: 0, width: 10, height: 10, fill: "#000" })
 upsertDesignNode(doc, { id: "c", type: "rect", parentId: "s1", x: 0, y: 0, width: 10, height: 10, fill: "#000" })
 return doc
}
const ids = (doc: Y.Doc) => designNodes(doc).map(node => node.id)

describe("reorderDesignNode", () => {
 it("moves a layer among the layers of its own screen only", () => {
  const doc = fixture()
  expect(reorderDesignNode(doc, "a", "forward")).toBe(true)
  expect(ids(doc)).toEqual(["s1", "b", "s2", "a", "x", "c"])
  expect(reorderDesignNode(doc, "a", "front")).toBe(true)
  expect(ids(doc)).toEqual(["s1", "b", "s2", "c", "x", "a"])
  expect(reorderDesignNode(doc, "a", "front")).toBe(false)
  expect(reorderDesignNode(doc, "a", "back")).toBe(true)
  expect(ids(doc)).toEqual(["s1", "a", "s2", "b", "x", "c"])
  expect(reorderDesignNode(doc, "a", "backward")).toBe(false)
 })

 it("reorders screens among screens and keeps every layer's fields", () => {
  const doc = fixture()
  const before = designNodes(doc)
  expect(reorderDesignNode(doc, "s2", "back")).toBe(true)
  expect(ids(doc)).toEqual(["s2", "a", "s1", "b", "x", "c"])
  expect([...designNodes(doc)].sort((p, q) => p.id.localeCompare(q.id))).toEqual([...before].sort((p, q) => p.id.localeCompare(q.id)))
 })

 it("leaves a concurrent edit in another place intact", () => {
  const a = fixture(), b = new Y.Doc()
  Y.applyUpdate(b, Y.encodeStateAsUpdate(a))
  reorderDesignNode(a, "c", "back")
  upsertDesignNode(b, { id: "d", type: "rect", parentId: "s2", x: 0, y: 0, width: 1, height: 1, fill: "#000" })
  Y.applyUpdate(a, Y.encodeStateAsUpdate(b)); Y.applyUpdate(b, Y.encodeStateAsUpdate(a))
  expect(ids(a)).toEqual(ids(b))
  expect(ids(a).filter(id => ["a", "b", "c"].includes(id))).toEqual(["c", "a", "b"])
  expect(ids(a)).toContain("d")
 })
})
