import { describe, expect, it } from "vitest"
import * as Y from "yjs"
import { createDesignUndo, designNodes, removeDesignNode, upsertDesignNode } from "../design-document"

/** Two people's documents, kept in sync like the server does. */
function pair() {
 const a = new Y.Doc(), b = new Y.Doc()
 a.on("update", (update: Uint8Array, origin: unknown) => { if (origin !== "remote") Y.applyUpdate(b, update, "remote") })
 b.on("update", (update: Uint8Array, origin: unknown) => { if (origin !== "remote") Y.applyUpdate(a, update, "remote") })
 upsertDesignNode(a, { id: "r1", type: "rect", name: "One", x: 100, y: 100, width: 50, height: 50, fill: "#000000" })
 upsertDesignNode(a, { id: "r2", type: "rect", name: "Two", x: 300, y: 300, width: 50, height: 50, fill: "#000000" })
 return { a, b }
}
const node = (doc: Y.Doc, id: string) => designNodes(doc).find(item => item.id === id)

describe("createDesignUndo", () => {
 it("takes back only this person's own move, never a peer's", () => {
  const { a, b } = pair()
  const undoA = createDesignUndo(a)
  upsertDesignNode(a, { id: "r1", x: 180 })
  upsertDesignNode(b, { id: "r2", y: 380 })
  undoA.undo()
  expect(node(a, "r1")?.x).toBe(100)
  expect(node(b, "r1")?.x).toBe(100)
  expect(node(a, "r2")?.y).toBe(380)
  undoA.redo()
  expect(node(b, "r1")?.x).toBe(180)
 })

 it("does not overwrite a value a peer wrote after this person's edit", () => {
  const { a, b } = pair()
  const undoA = createDesignUndo(a)
  upsertDesignNode(a, { id: "r1", x: 180 })
  upsertDesignNode(b, { id: "r1", x: 250 })
  undoA.undo()
  expect(node(a, "r1")?.x).toBe(250)
  expect(node(b, "r1")?.x).toBe(250)
 })

 it("has nothing to undo after only a peer's change, and brings back this person's deleted layer", () => {
  const { a, b } = pair()
  const undoA = createDesignUndo(a)
  upsertDesignNode(b, { id: "r2", y: 999 })
  expect(undoA.canUndo()).toBe(false)
  removeDesignNode(a, "r1")
  expect(node(b, "r1")).toBeUndefined()
  undoA.undo()
  expect(node(b, "r1")).toMatchObject({ x: 100, name: "One" })
  expect(a.getArray<string>("order").toArray()).toEqual(["r1", "r2"])
 })

 it.each([[1, 2], [2, 1]])("never deletes or overwrites a value after concurrent edits of the same property (A clientID %i, B %i)", (idA, idB) => {
  const a = new Y.Doc(), b = new Y.Doc()
  a.clientID = idA; b.clientID = idB
  upsertDesignNode(a, { id: "r1", type: "rect", name: "One", x: 100, y: 100, width: 50, height: 50, fill: "#000000" })
  Y.applyUpdate(b, Y.encodeStateAsUpdate(a), "remote")
  const undoA = createDesignUndo(a)
  // Offline on both sides: A and B set x at the same time.
  upsertDesignNode(a, { id: "r1", x: 180 })
  upsertDesignNode(b, { id: "r1", x: 250 })
  Y.applyUpdate(a, Y.encodeStateAsUpdate(b), "remote")
  Y.applyUpdate(b, Y.encodeStateAsUpdate(a), "remote")
  const merged = node(a, "r1")?.x
  expect(merged).toBe(node(b, "r1")?.x)
  a.on("update", (update: Uint8Array) => Y.applyUpdate(b, update, "remote"))
  undoA.undo()
  // Whoever won, A's undo leaves a value both sides agree on and never deletes it.
  expect(node(a, "r1")?.x).toBe(merged)
  expect(node(b, "r1")?.x).toBe(merged)
 })
 it("keeps this person's step when a peer changes another property of the same layer", () => {
  const { a, b } = pair()
  const undoA = createDesignUndo(a)
  upsertDesignNode(a, { id: "r1", x: 150 })
  undoA.stopCapturing()
  upsertDesignNode(b, { id: "r1", height: 130 })
  expect(undoA.canUndo()).toBe(true)
  undoA.undo()
  expect(node(b, "r1")).toMatchObject({ x: 100, height: 130 })
 })
})
