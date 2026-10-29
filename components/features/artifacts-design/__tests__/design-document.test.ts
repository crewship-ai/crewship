import { describe, expect, it } from "vitest"
import * as Y from "yjs"
import { designNodes, upsertDesignNode, removeDesignNode } from "../design-document"

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
