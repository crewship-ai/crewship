import { describe, expect, it } from "vitest"
import * as Y from "yjs"
import { designLinkTarget, designNodes, designScreenOf, removeDesignNode, setDesignNodeLink, upsertDesignNode } from "../design-document"

function threeScreens() {
 const doc = new Y.Doc()
 for (const [index, id] of ["a", "b", "c"].entries()) upsertDesignNode(doc, { id, type: "frame", name: `Screen ${id.toUpperCase()}`, x: index * 400, y: 0, width: 360, height: 640, fill: "#ffffff" })
 upsertDesignNode(doc, { id: "go-b", type: "rect", parentId: "a", name: "Next", x: 24, y: 24, width: 120, height: 48, fill: "#2563eb" })
 upsertDesignNode(doc, { id: "go-c", type: "text", parentId: "b", text: "Continue", x: 24, y: 24, width: 120, height: 48, fill: "#111111" })
 return doc
}

describe("Design prototype links", () => {
 it("round-trips a link through a standard Yjs update and removes it as a key", () => {
  const doc = threeScreens()
  setDesignNodeLink(doc, "go-b", "b")
  const copy = new Y.Doc()
  Y.applyUpdate(copy, Y.encodeStateAsUpdate(doc))
  const nodes = designNodes(copy)
  expect(nodes.find(node => node.id === "go-b")).toMatchObject({ linkTo: "b" })
  expect(designLinkTarget(nodes, nodes.find(node => node.id === "go-b")!)).toMatchObject({ kind: "screen", screen: { id: "b" } })
  setDesignNodeLink(copy, "go-b", null)
  Y.applyUpdate(doc, Y.encodeStateAsUpdate(copy))
  expect(doc.getMap<Y.Map<unknown>>("nodes").get("go-b")!.has("linkTo")).toBe(false)
  expect(designLinkTarget(designNodes(doc), designNodes(doc).find(node => node.id === "go-b")!)).toEqual({ kind: "none" })
 })
 it("reads an old document without links unchanged", () => {
  const doc = threeScreens()
  const nodes = designNodes(doc)
  expect(nodes.every(node => !("linkTo" in node))).toBe(true)
  for (const node of nodes) expect(designLinkTarget(nodes, node)).toEqual({ kind: "none" })
  expect(designScreenOf(nodes, nodes.find(node => node.id === "go-c")!)).toBe("b")
 })
 it("reports a link to a deleted screen as missing without throwing", () => {
  const doc = threeScreens()
  setDesignNodeLink(doc, "go-b", "b")
  removeDesignNode(doc, "b")
  const nodes = designNodes(doc)
  expect(designLinkTarget(nodes, nodes.find(node => node.id === "go-b")!)).toEqual({ kind: "missing", target: "b" })
 })
 it("treats a link to a non-screen layer, or a malformed value as unusable", () => {
  const doc = threeScreens()
  upsertDesignNode(doc, { id: "layer", type: "rect", parentId: "a", x: 0, y: 0, width: 10, height: 10, fill: "#000000", linkTo: "go-c" })
  doc.getMap<Y.Map<unknown>>("nodes").get("go-b")!.set("linkTo", 42)
  const nodes = designNodes(doc)
  expect(designLinkTarget(nodes, nodes.find(node => node.id === "layer")!)).toEqual({ kind: "missing", target: "go-c" })
  expect(designLinkTarget(nodes, nodes.find(node => node.id === "go-b")!)).toEqual({ kind: "none" })
 })
 it("never links a screen itself or a node that no longer exists", () => {
  const doc = threeScreens()
  let updates = 0
  doc.on("update", () => { updates++ })
  setDesignNodeLink(doc, "a", "b")
  setDesignNodeLink(doc, "gone", "b")
  expect(updates).toBe(0)
  expect(designNodes(doc).some(node => node.id === "gone")).toBe(false)
 })
})
