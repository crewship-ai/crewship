// Canvas presence (#5149 C06): this person's pointer (canvas coordinates) and selection go out with the
// document's presence heartbeat; other people's pointers and selected cards are drawn in their colour.
import * as React from "react"
import { cleanup, fireEvent, render } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentSurface } from "@/lib/documents/surface"
import { canvasPresenceBody, parseCanvasPresence, parsePresenceReply, type CanvasPresence, type PresencePeer } from "@/lib/documents/presence"
import { DocsCanvas, canvasPeers } from "../docs-canvas"

afterEach(cleanup)

function fixture() {
  const doc = new Y.Doc({ gc: false })
  const node = new Y.XmlElement("paragraph"); node.setAttribute("id", "intro")
  const run = new Y.XmlText(); run.insert(0, "Shared text"); node.insert(0, [run])
  doc.getXmlFragment("default").insert(0, [node])
  documentSurface.place(doc, "intro", { x: 100, y: 200, w: 320, h: 120, z: 1 })
  documentSurface.putItem(doc, "note-1", { kind: "note", x: 600, y: 200, w: 200, h: 160, z: 2, text: "Idea", tone: "yellow" })
  return doc
}

const world = () => document.querySelector("[data-canvas-world]") as HTMLElement
const application = () => document.querySelector('[role="application"]') as HTMLElement
/** The world transform the canvas shows: screen = (world - origin) * scale. */
const transform = () => {
  const match = /scale\(([-\d.e]+)\) translate\(([-\d.e]+)px, ([-\d.e]+)px\)/.exec(world().style.transform)!
  return { scale: Number(match[1]), x: -Number(match[2]), y: -Number(match[3]) }
}
const peer = (over: Partial<PresencePeer>): PresencePeer => ({ client: "tab-erin-0001", userId: "erin", name: "Erin Editor", color: "#db2777", tabs: 1, ...over })

describe("canvas presence on the wire", () => {
  it("sends whole canvas units within bounds, unique valid cards, and nothing when there is nothing to show", () => {
    expect(canvasPresenceBody(null)).toEqual({})
    expect(canvasPresenceBody({ selection: [] })).toEqual({})
    expect(canvasPresenceBody({ pointer: { x: 10.4, y: -3.6 }, selection: ["intro", "intro", "bad id"] })).toEqual({ canvas: { pointer: { x: 10, y: -4 }, selection: ["intro"] } })
    expect(canvasPresenceBody({ pointer: { x: 5e6, y: -5e6 }, selection: [] })).toEqual({ canvas: { pointer: { x: 1_000_000, y: -1_000_000 } } })
    expect(canvasPresenceBody({ pointer: { x: Number.NaN, y: 1 }, selection: ["a", "b", "c", "d", "e", "f", "g", "h", "i"] })).toEqual({ canvas: { selection: ["a", "b", "c", "d", "e", "f", "g", "h"] } })
  })

  it("reads a peer's canvas presence and drops anything out of bounds", () => {
    expect(parseCanvasPresence({ pointer: { x: 1, y: 2 }, selection: ["intro"] })).toEqual({ pointer: { x: 1, y: 2 }, selection: ["intro"] })
    expect(parseCanvasPresence({ pointer: { x: 2e6, y: 0 }, selection: ["intro"] })).toEqual({ selection: ["intro"] })
    expect(parseCanvasPresence({ selection: ["<img>"] })).toBeUndefined()
    expect(parseCanvasPresence({ pointer: { x: "1", y: 2 } })).toBeUndefined()
    expect(parseCanvasPresence([1, 2])).toBeUndefined()
    const reply = parsePresenceReply({ peers: [{ client: "tab-erin-0001", user_id: "erin", name: "Erin", color: 1, canvas: { pointer: { x: 5, y: 6 } } }, { client: "tab-rory-0001", user_id: "rory", name: "Rory", color: 2 }] })
    expect(reply.peers.map(p => p.canvas)).toEqual([{ pointer: { x: 5, y: 6 }, selection: [] }, undefined])
  })

  it("groups peers: one pointer per tab, one name per person on a card", () => {
    const { pointers, selections } = canvasPeers([
      peer({ canvas: { pointer: { x: 1, y: 2 }, selection: ["intro"] } }),
      peer({ client: "tab-erin-0002", canvas: { selection: ["intro"] } }),
      peer({ client: "tab-rory-0001", userId: "rory", name: "Rory Reader", color: "#059669", canvas: { pointer: { x: 3, y: 4 }, selection: ["intro", "note-1"] } }),
      peer({ client: "tab-page-0001", userId: "pat", name: "Pat on the Page" }),
    ])
    expect(pointers.map(p => `${p.name}@${p.x},${p.y}`)).toEqual(["Erin Editor@1,2", "Rory Reader@3,4"])
    expect(selections.get("intro")!.map(p => p.name)).toEqual(["Erin Editor", "Rory Reader"])
    expect(selections.get("note-1")!.map(p => p.name)).toEqual(["Rory Reader"])
  })
})

describe("DocsCanvas presence", () => {
  it("reports the pointer in canvas coordinates whatever the pan and zoom, the selection, and nothing once it leaves", () => {
    const reports: (CanvasPresence | null)[] = []
    const view = render(<DocsCanvas doc={fixture()} editable={false} origin="me" onPresence={value => reports.push(value && { ...value, selection: [...value.selection] })} />)
    const t = transform()
    fireEvent.pointerMove(application(), { pointerId: 1, pointerType: "mouse", isPrimary: true, clientX: 300, clientY: 150 })
    expect(reports.at(-1)!.pointer!.x).toBeCloseTo(300 / t.scale + t.x)
    expect(reports.at(-1)!.pointer!.y).toBeCloseTo(150 / t.scale + t.y)
    // Zoom in: the same screen point is a different canvas point, and that is what is shared.
    fireEvent.click(view.getByRole("button", { name: "Zoom in" }))
    const zoomed = transform()
    expect(zoomed.scale).not.toBe(t.scale)
    fireEvent.pointerMove(application(), { pointerId: 1, pointerType: "mouse", isPrimary: true, clientX: 300, clientY: 150 })
    expect(reports.at(-1)!.pointer!.x).toBeCloseTo(300 / zoomed.scale + zoomed.x)
    // Selecting a card (a read-only person can select) shares it.
    fireEvent.focus(document.querySelector('[data-placement="intro"]')!)
    expect(reports.at(-1)!.selection).toEqual(["intro"])
    // A second touch of a pinch is not this person's pointer: it is not shared.
    const shared = reports.at(-1)!.pointer
    fireEvent.pointerMove(application(), { pointerId: 2, pointerType: "touch", isPrimary: false, clientX: 900, clientY: 900 })
    expect(reports.at(-1)!.pointer).toEqual(shared)
    fireEvent.pointerLeave(application())
    expect(reports.at(-1)!.pointer).toBeUndefined()
    expect(reports.at(-1)!.selection).toEqual(["intro"])
    view.unmount()
    expect(reports.at(-1)).toBeNull()
  })

  it("draws other people's pointers at their canvas point and outlines the cards they selected, in their colour", () => {
    const peers = [
      peer({ canvas: { pointer: { x: 150, y: 260 }, selection: ["intro"] } }),
      peer({ client: "tab-rory-0001", userId: "rory", name: "Rory Reader", color: "#059669", canvas: { selection: ["note-1"] } }),
      peer({ client: "tab-pat-0001", userId: "pat", name: "Pat on the Page" }),
    ]
    render(<DocsCanvas doc={fixture()} editable origin="me" peers={peers} />)
    const pointer = document.querySelector('[data-peer-pointer="Erin Editor"]') as HTMLElement
    expect(pointer.style.transform).toContain("translate(150px, 260px)")
    // Inside the world layer: it pans and zooms with the cards, so it points at the same place for everyone.
    expect(world().contains(pointer)).toBe(true)
    expect(pointer.textContent).toBe("Erin Editor")
    expect(pointer.querySelector("path")!.getAttribute("fill")).toBe("#db2777")
    expect(document.querySelectorAll("[data-peer-pointer]")).toHaveLength(1)
    const intro = document.querySelector('[data-placement="intro"]') as HTMLElement
    expect(intro.dataset.peerSelected).toBe("Erin Editor")
    expect(intro.style.outline).toMatch(/#db2777/)
    expect((document.querySelector('[data-item="note-1"]') as HTMLElement).dataset.peerSelected).toBe("Rory Reader")
    expect((document.querySelector('[data-peer-selection="note-1"]') as HTMLElement).textContent).toBe("Rory Reader")
    // A peer only on the Page shows nowhere on the canvas.
    expect(document.body.textContent).not.toContain("Pat on the Page")
  })

  it("never writes to the document for presence", () => {
    const doc = fixture(), writes = vi.fn()
    doc.on("update", writes)
    render(<DocsCanvas doc={doc} editable origin="me" onPresence={() => {}} peers={[peer({ canvas: { pointer: { x: 1, y: 1 }, selection: ["intro"] } })]} />)
    fireEvent.pointerMove(application(), { pointerId: 1, pointerType: "mouse", isPrimary: true, clientX: 30, clientY: 40 })
    fireEvent.pointerLeave(application())
    expect(writes).not.toHaveBeenCalled()
  })
})
