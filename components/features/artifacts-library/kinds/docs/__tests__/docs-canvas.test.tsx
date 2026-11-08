import * as React from "react"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentSurface } from "@/lib/documents/surface"
import { DocsCanvas, pageBlocks, threadsByBlock } from "../docs-canvas"
import type { DocumentComment } from "@/hooks/use-document-comments"

afterEach(cleanup)

function fixture() {
  const doc = new Y.Doc({ gc: false })
  const fragment = doc.getXmlFragment("default")
  const block = (type: string, id: string, text: string) => {
    const node = new Y.XmlElement(type); node.setAttribute("id", id)
    const run = new Y.XmlText(); run.insert(0, text); node.insert(0, [run]); return node
  }
  fragment.insert(0, [block("heading", "title", "Plan"), block("paragraph", "intro", "Shared text"), block("paragraph", "outro", "Last")])
  return doc
}

const world = () => document.querySelector("[data-canvas-world]") as HTMLElement
const canvas = () => screen.getByRole("application", { name: "Canvas" })
const card = (id: string) => document.querySelector(`[data-placement="${id}"]`) as HTMLElement
// jsdom's WheelEvent ignores modifier keys in its init dictionary.
const wheel = (target: Element, init: { deltaX?: number; deltaY: number; ctrlKey?: boolean; clientX?: number; clientY?: number }) => {
  const event = new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaX: init.deltaX ?? 0, deltaY: init.deltaY })
  for (const key of ["ctrlKey", "clientX", "clientY"] as const) Object.defineProperty(event, key, { value: init[key] ?? (key === "ctrlKey" ? false : 0) })
  act(() => { target.dispatchEvent(event) })
}
const drag = (target: Element, from: { x: number; y: number }, to: { x: number; y: number }, button = 0) => {
  fireEvent.pointerDown(target, { pointerId: 1, button, clientX: from.x, clientY: from.y })
  fireEvent.pointerMove(target, { pointerId: 1, button, clientX: to.x, clientY: to.y })
  fireEvent.pointerUp(target, { pointerId: 1, button, clientX: to.x, clientY: to.y })
}

describe("DocsCanvas", () => {
  it("shows a placed block's Page text, not a copy: a Page edit appears on the card", () => {
    const doc = fixture()
    documentSurface.place(doc, "intro", { x: 0, y: 0, w: 320, h: 120, z: 1 })
    render(<DocsCanvas doc={doc} editable origin="me" />)
    expect(card("intro").textContent).toContain("Shared text")
    act(() => { ((doc.getXmlFragment("default").get(1) as Y.XmlElement).get(0) as Y.XmlText).insert(0, "Edited ") })
    expect(card("intro").textContent).toContain("Edited Shared text")
    expect(pageBlocks(doc).map(b => b.id)).toEqual(["title", "intro", "outro"])
  })

  it("moves a placement with one write per gesture: geometry only, as the person's origin", () => {
    const doc = fixture()
    documentSurface.place(doc, "intro", { x: 0, y: 0, w: 320, h: 120, z: 1 })
    const updates: unknown[] = []
    doc.on("update", (_u: Uint8Array, origin: unknown) => updates.push(origin))
    render(<DocsCanvas doc={doc} editable origin="me" />)
    const before = doc.getXmlFragment("default").toJSON()
    fireEvent.pointerDown(card("intro"), { pointerId: 1, button: 0, clientX: 10, clientY: 10 })
    for (let i = 1; i <= 5; i++) fireEvent.pointerMove(card("intro"), { pointerId: 1, clientX: 10 + i * 10, clientY: 10 + i * 4 })
    fireEvent.pointerUp(card("intro"), { pointerId: 1, clientX: 60, clientY: 30 })
    const view = documentSurface.project(doc)
    expect(view.placements[0]).toMatchObject({ id: "intro", w: 320, h: 120 })
    expect(view.placements[0].x).not.toBe(0)
    expect(updates).toEqual(["me"])
    expect(doc.getXmlFragment("default").toJSON()).toBe(before)
  })

  it("lets read-only people pan and zoom but never move a shared block", () => {
    const doc = fixture()
    documentSurface.place(doc, "intro", { x: 0, y: 0, w: 320, h: 120, z: 1 })
    const updates = vi.fn()
    doc.on("update", updates)
    render(<DocsCanvas doc={doc} editable={false} origin="me" />)
    const transform = world().style.transform
    drag(card("intro"), { x: 10, y: 10 }, { x: 200, y: 100 })
    expect(updates).not.toHaveBeenCalled()
    expect(world().style.transform).not.toBe(transform)
    fireEvent.keyDown(card("intro"), { key: "ArrowRight" })
    fireEvent.keyDown(card("intro"), { key: "Delete" })
    expect(updates).not.toHaveBeenCalled()
    expect(screen.queryByRole("button", { name: "Add note" })).toBeNull()
    expect(screen.queryByText(/Add blocks/)).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Zoom in" }))
    expect(document.querySelector("[data-zoom]")!.textContent).not.toBe("100%")
  })

  it("pans instead of moving on a secondary-button drag over a card", () => {
    const doc = fixture()
    documentSurface.place(doc, "intro", { x: 0, y: 0, w: 320, h: 120, z: 1 })
    render(<DocsCanvas doc={doc} editable origin="me" />)
    const transform = world().style.transform
    drag(card("intro"), { x: 10, y: 10 }, { x: 110, y: 60 }, 2)
    expect(documentSurface.project(doc).placements[0]).toMatchObject({ x: 0, y: 0 })
    expect(world().style.transform).not.toBe(transform)
  })

  it("zooms around the pointer with Ctrl + wheel and pans with a plain wheel", () => {
    const doc = fixture()
    render(<DocsCanvas doc={doc} editable origin="me" />)
    wheel(canvas(), { deltaY: -200, ctrlKey: true, clientX: 100, clientY: 100 })
    expect(document.querySelector("[data-zoom]")!.textContent).not.toBe("100%")
    const zoom = document.querySelector("[data-zoom]")!.textContent
    const transform = world().style.transform
    wheel(canvas(), { deltaY: 50, deltaX: 20 })
    expect(document.querySelector("[data-zoom]")!.textContent).toBe(zoom)
    expect(world().style.transform).not.toBe(transform)
  })

  it("adds a Page block, removes it from the canvas only, and Escape cancels a drag", () => {
    const doc = fixture()
    render(<DocsCanvas doc={doc} editable origin="me" />)
    fireEvent.click(screen.getByText(/Add blocks \(3\)/))
    fireEvent.click(screen.getByRole("button", { name: "Last" }))
    expect(documentSurface.project(doc).placements.map(p => p.id)).toEqual(["outro"])
    fireEvent.pointerDown(card("outro"), { pointerId: 1, button: 0, clientX: 0, clientY: 0 })
    fireEvent.pointerMove(card("outro"), { pointerId: 1, clientX: 300, clientY: 300 })
    fireEvent.keyDown(canvas(), { key: "Escape" })
    fireEvent.pointerUp(card("outro"), { pointerId: 1, clientX: 300, clientY: 300 })
    expect(documentSurface.project(doc).placements[0]).toMatchObject({ x: 0, y: 0 })
    fireEvent.keyDown(card("outro"), { key: "Delete" })
    expect(documentSurface.project(doc).placements).toEqual([])
    expect(pageBlocks(doc).map(b => b.id)).toContain("outro")
  })

  it("refuses to render, and so to write, a surface it cannot validate", () => {
    const doc = fixture()
    doc.getMap(documentSurface.PLACEMENTS).set("intro", { x: 0, y: 0, w: 320, h: 120, z: 1, html: "<b>" })
    render(<DocsCanvas doc={doc} editable origin="me" />)
    expect(screen.getByRole("alert").textContent).toMatch(/cannot read/)
  })

  describe("block-anchored comments (C05)", () => {
    const thread = (id: string, block: string, extra: Partial<DocumentComment> = {}): DocumentComment =>
      ({ id, body: "Check", actor: "u1", rev: 1, version: 1, resolved: false, created_at: "2026-11-09T00:00:00Z", block, quote: "Shared", status: "attached", ...extra })
    const nested = () => {
      const doc = fixture()
      const list = new Y.XmlElement("bulletList"); list.setAttribute("id", "list")
      const li = new Y.XmlElement("listItem"); li.setAttribute("id", "li")
      const p = new Y.XmlElement("paragraph"); p.setAttribute("id", "li-p")
      const run = new Y.XmlText(); run.insert(0, "Nested"); p.insert(0, [run]); li.insert(0, [p]); list.insert(0, [li])
      doc.getXmlFragment("default").insert(3, [list])
      return doc
    }

    it("counts open threads of a block and its nested blocks, never resolved, detached or replies", () => {
      const doc = nested()
      const map = threadsByBlock(doc, [thread("a", "intro"), thread("b", "li-p"), thread("c", "intro", { resolved: true }),
        thread("d", "gone", { status: "detached" }), thread("e", "intro", { parent: "a" }), thread("f", "intro", { status: "collapsed" })])
      expect(map.get("intro")).toEqual(["a", "f"]); expect(map.get("list")).toEqual(["b"]); expect(map.has("gone")).toBe(false)
    })

    it("shows the same threads on the card, keeps them across a move, and opens them for read-only people too", () => {
      const doc = fixture()
      documentSurface.place(doc, "intro", { x: 0, y: 0, w: 320, h: 120, z: 1 })
      const open = vi.fn()
      const comments = [thread("a", "intro"), thread("b", "intro")]
      const { rerender } = render(<DocsCanvas doc={doc} editable origin="me" comments={comments} onOpenComments={open} />)
      const badge = () => screen.getByRole("button", { name: "2 open comments on this block" })
      act(() => { documentSurface.move(doc, "intro", { x: 600, y: 300 }) })
      fireEvent.click(badge())
      expect(open).toHaveBeenCalledWith("intro", ["a", "b"])
      // Opening a thread never moves the card.
      expect(documentSurface.project(doc).placements[0]).toMatchObject({ x: 600, y: 300 })
      rerender(<DocsCanvas doc={doc} editable={false} origin="me" comments={comments} onOpenComments={open} />)
      fireEvent.click(badge()); expect(open).toHaveBeenCalledTimes(2)
    })

    it("drops the card and its badge when the block is deleted; the thread itself is left to the Comments drawer", () => {
      const doc = fixture()
      documentSurface.place(doc, "intro", { x: 0, y: 0, w: 320, h: 120, z: 1 })
      render(<DocsCanvas doc={doc} editable origin="me" comments={[thread("a", "intro")]} />)
      expect(screen.getByRole("button", { name: "1 open comment on this block" })).toBeTruthy()
      act(() => { documentSurface.deleteBlock(doc, "intro") })
      expect(card("intro")).toBeNull()
      expect(screen.queryByRole("button", { name: /open comment/ })).toBeNull()
    })
  })
})
