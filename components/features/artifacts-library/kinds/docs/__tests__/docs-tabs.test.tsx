import * as React from "react"
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentTabs, newTabId, readTabRows, shownTab, tabFromHash, tabRows } from "@/lib/documents/tabs"
import { DocsTabs, useTabRows } from "../docs-tabs"

const shownLog: string[] = []
afterEach(() => { cleanup(); shownLog.length = 0 })

const ORIGIN = "tabs-test"

/** A one-tab document: the existing `default` page with one paragraph. */
function page(): Y.Doc {
  const doc = new Y.Doc({ gc: false })
  const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", "intro")
  const text = new Y.XmlText(); text.insert(0, "First page")
  paragraph.insert(0, [text])
  doc.getXmlFragment("default").insert(0, [paragraph])
  return doc
}

function Harness({ doc, editable = true, initial = "default", onSelect }: { doc: Y.Doc; editable?: boolean; initial?: string; onSelect?: (id: string) => void }) {
  // As in the Docs view: one tab list, and the shown tab from the shared rule.
  const [selected, setSelected] = React.useState(initial)
  const tabs = useTabRows(doc, true)
  const shown = shownTab(tabs.rows, selected)
  shownLog.push(shown.id)
  return <DocsTabs doc={doc} tabs={tabs} selected={shown.id} editable={editable} origin={ORIGIN} onSelect={id => { setSelected(id); onSelect?.(id) }} />
}

const tree = () => screen.getByRole("navigation", { name: "Tabs" })
const items = () => within(tree()).getAllByRole("listitem").map(item => `${"  ".repeat(Number(item.dataset.depth))}${item.textContent}`)
const selectedName = () => within(tree()).queryAllByRole("button").find(button => button.getAttribute("aria-current") === "page")?.textContent
const addNamed = (name: string, button = "Add tab") => {
  fireEvent.click(screen.getByRole("button", { name: button }))
  const field = screen.getByRole("textbox", { name: "Tab name" })
  fireEvent.change(field, { target: { value: name } })
  fireEvent.keyDown(field, { key: "Enter" })
}

describe("Docs tabs", () => {
  it("shows an older document as one tab without writing anything", () => {
    const doc = page(), writes: unknown[] = []
    doc.on("update", (_update: Uint8Array, origin: unknown) => writes.push(origin))
    render(<Harness doc={doc} />)
    expect(items()).toEqual(["Tab 1"])
    expect(selectedName()).toBe("Tab 1")
    expect(writes).toEqual([])
  })

  it("adds, renames, reorders and nests tabs as metadata only, every write with the session's origin", () => {
    const doc = page(), origins = new Set<unknown>()
    const before = doc.getXmlFragment("default").toJSON()
    doc.on("update", (_update: Uint8Array, origin: unknown) => origins.add(origin))
    const chosen: string[] = []
    render(<Harness doc={doc} onSelect={id => chosen.push(id)} />)
    addNamed("Plan")
    addNamed("Budget")
    expect(items()).toEqual(["Tab 1", "Plan", "Budget"])
    expect(selectedName()).toBe("Budget")
    // A new tab is chosen and named straight away; its ID is stable from then on.
    const budget = chosen.at(-1)!
    expect(documentTabs.tree(doc).find(tab => tab.id === budget)?.name).toBe("Budget")
    fireEvent.click(screen.getByRole("button", { name: "Move tab up" }))
    expect(items()).toEqual(["Tab 1", "Budget", "Plan"])
    fireEvent.click(screen.getByRole("button", { name: "Move tab down" }))
    expect(items()).toEqual(["Tab 1", "Plan", "Budget"])
    fireEvent.click(screen.getByRole("button", { name: "Nest tab" }))
    expect(items()).toEqual(["Tab 1", "Plan", "  Budget"])
    fireEvent.click(screen.getByRole("button", { name: "Un-nest tab" }))
    expect(items()).toEqual(["Tab 1", "Plan", "Budget"])
    fireEvent.click(screen.getByRole("button", { name: "Rename tab" }))
    const field = screen.getByRole("textbox", { name: "Tab name" })
    fireEvent.change(field, { target: { value: "Costs" } })
    fireEvent.keyDown(field, { key: "Enter" })
    expect(items()).toEqual(["Tab 1", "Plan", "Costs"])
    expect(chosen.at(-1)).toBe(budget)
    // The first page's content is untouched; only the tab maps and the new tabs' own pages were written.
    expect(doc.getXmlFragment("default").toJSON()).toBe(before)
    expect([...origins]).toEqual([ORIGIN])
  })

  it("adds a sub-tab under the chosen tab and un-nests it after its parent", () => {
    const doc = page()
    render(<Harness doc={doc} />)
    addNamed("Plan")
    addNamed("Week 1", "Add sub-tab")
    addNamed("Week 2", "Add sub-tab")
    expect(items()).toEqual(["Tab 1", "Plan", "  Week 1", "    Week 2"])
    fireEvent.click(screen.getByRole("button", { name: "Un-nest tab" }))
    expect(items()).toEqual(["Tab 1", "Plan", "  Week 1", "  Week 2"])
    fireEvent.click(screen.getByRole("button", { name: "Un-nest tab" }))
    expect(items()).toEqual(["Tab 1", "Plan", "  Week 1", "Week 2"])
  })

  it("deletes a tab only after confirmation, moves its sub-tabs up and never offers to delete the first tab", () => {
    const doc = page()
    render(<Harness doc={doc} />)
    expect(screen.queryByRole("button", { name: "Delete tab" })).toBeNull()
    addNamed("Plan")
    addNamed("Week 1", "Add sub-tab")
    fireEvent.click(within(tree()).getByText("Plan"))
    fireEvent.click(screen.getByRole("button", { name: "Delete tab" }))
    const dialog = screen.getByRole("alertdialog", { name: "Delete tab" })
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
    expect(items()).toEqual(["Tab 1", "Plan", "  Week 1"])
    fireEvent.click(screen.getByRole("button", { name: "Delete tab" }))
    fireEvent.click(within(screen.getByRole("alertdialog", { name: "Delete tab" })).getByRole("button", { name: "Delete" }))
    expect(items()).toEqual(["Tab 1", "Week 1"])
    expect(selectedName()).toBe("Tab 1")
  })

  it("saves: the tabs, their names, order and nesting come back with the same IDs from the stored state", () => {
    const doc = page()
    const view = render(<Harness doc={doc} />)
    addNamed("Plan")
    addNamed("Week 1", "Add sub-tab")
    fireEvent.click(within(tree()).getByText("Tab 1"))
    addNamed("Notes")
    const saved = tabRows(doc)
    view.unmount()
    // What the server stores is the encoded state; a reload opens a new replica from it.
    const reloaded = new Y.Doc({ gc: false })
    Y.applyUpdate(reloaded, Y.encodeStateAsUpdate(doc))
    expect(tabRows(reloaded)).toEqual(saved)
    render(<Harness doc={reloaded} initial={saved.find(row => row.name === "Week 1")!.id} />)
    expect(items()).toEqual(["Tab 1", "Plan", "  Week 1", "Notes"])
    expect(selectedName()).toBe("Week 1")
    expect(documentTabs.validateTabs(reloaded)).toBeTruthy()
  })

  it("follows another person's changes live", () => {
    const doc = page(), other = new Y.Doc({ gc: false })
    Y.applyUpdate(other, Y.encodeStateAsUpdate(doc))
    render(<Harness doc={doc} />)
    act(() => {
      documentTabs.createTab(other, { id: "remote", name: "From a colleague", blockId: "remote-p" })
      Y.applyUpdate(doc, Y.encodeStateAsUpdate(other, Y.encodeStateVector(doc)), "remote")
    })
    expect(items()).toEqual(["Tab 1", "From a colleague"])
    act(() => {
      documentTabs.renameTab(other, "remote", "Renamed")
      Y.applyUpdate(doc, Y.encodeStateAsUpdate(other, Y.encodeStateVector(doc)), "remote")
    })
    expect(items()).toEqual(["Tab 1", "Renamed"])
  })

  it("shows the first tab when the chosen one is unknown or deleted elsewhere", () => {
    const doc = page(), chosen = vi.fn()
    render(<Harness doc={doc} initial="missing" onSelect={chosen} />)
    expect(selectedName()).toBe("Tab 1")
    expect(shownLog.at(-1)).toBe("default")
    addNamed("Plan")
    const plan = chosen.mock.calls.at(-1)![0] as string
    expect(shownLog.at(-1)).toBe(plan)
    act(() => { documentTabs.deleteTab(doc, plan, "remote") })
    expect(items()).toEqual(["Tab 1"])
    expect(selectedName()).toBe("Tab 1")
    expect(shownLog.at(-1)).toBe("default")
  })

  it("shows unreadable tab metadata as the first tab only, with a notice and no tab editing", () => {
    const doc = page()
    doc.getMap(documentTabs.TABS).set("bad", { parent: 5, order: "1" })
    expect(readTabRows(doc).unreadable).toBe(true)
    render(<Harness doc={doc} />)
    expect(items()).toEqual(["Tab 1"])
    expect(screen.getByRole("alert").textContent).toBe("This document's tabs could not be read; only its first tab is shown.")
    expect(screen.queryByRole("button", { name: "Add tab" })).toBeNull()
  })

  it("cancels a rename with Escape, even when the field then loses focus", () => {
    const doc = page()
    render(<Harness doc={doc} />)
    fireEvent.click(screen.getByRole("button", { name: "Rename tab" }))
    const field = screen.getByRole("textbox", { name: "Tab name" })
    fireEvent.change(field, { target: { value: "Not this" } })
    fireEvent.keyDown(field, { key: "Escape" })
    fireEvent.blur(field)
    expect(items()).toEqual(["Tab 1"])
    expect(tabRows(doc)[0].name).toBe("Tab 1")
  })

  it("lets a read-only person switch tabs but not add, rename, move or delete them", () => {
    const doc = page()
    documentTabs.createTab(doc, { id: "plan", name: "Plan", blockId: "plan-p" })
    const writes: unknown[] = []
    doc.on("update", (_update: Uint8Array, origin: unknown) => writes.push(origin))
    const chosen = vi.fn()
    render(<Harness doc={doc} editable={false} onSelect={chosen} />)
    expect(screen.queryByRole("button", { name: "Add tab" })).toBeNull()
    expect(screen.queryByRole("toolbar", { name: "Tab actions" })).toBeNull()
    fireEvent.click(within(tree()).getByText("Plan"))
    expect(chosen).toHaveBeenLastCalledWith("plan")
    fireEvent.doubleClick(within(tree()).getByText("Plan"))
    expect(screen.queryByRole("textbox", { name: "Tab name" })).toBeNull()
    expect(writes).toEqual([])
  })

  it("explains a refused change instead of failing silently", () => {
    const doc = page()
    render(<Harness doc={doc} />)
    fireEvent.click(screen.getByRole("button", { name: "Rename tab" }))
    const field = screen.getByRole("textbox", { name: "Tab name" })
    fireEvent.change(field, { target: { value: "x".repeat(121) } })
    // maxLength stops typing past 120 in a browser; a pasted or programmatic value is still refused by the model.
    fireEvent.keyDown(field, { key: "Enter" })
    expect(screen.getByRole("alert").textContent).toBe("Give the tab a name of at most 120 characters.")
    expect(screen.getByRole("textbox", { name: "Tab name" })).toBeTruthy()
    expect(tabRows(doc).map(row => row.name)).toEqual(["Tab 1"])
  })
})

describe("the shown tab", () => {
  it("is the chosen tab while the tree shows it, the first tab otherwise", () => {
    const doc = page()
    documentTabs.createTab(doc, { id: "plan", name: "Plan", blockId: "plan-p" })
    expect(shownTab(tabRows(doc), "plan")).toEqual({ id: "plan", field: "tab:plan" })
    expect(shownTab(tabRows(doc), "default")).toEqual({ id: "default", field: "default" })
    expect(shownTab(tabRows(doc), "gone")).toEqual({ id: "default", field: "default" })
    expect(shownTab([], "plan")).toEqual({ id: "default", field: "default" })
  })

  it("stays the tab the list shows when a delete and a rename of it meet (only its name survives)", () => {
    const a = page()
    documentTabs.createTab(a, { id: "x", name: "X", blockId: "x-p" })
    const b = new Y.Doc({ gc: false }); Y.applyUpdate(b, Y.encodeStateAsUpdate(a))
    documentTabs.deleteTab(a, "x")
    documentTabs.renameTab(b, "x", "Renamed")
    Y.applyUpdate(a, Y.encodeStateAsUpdate(b, Y.encodeStateVector(a)))
    const rows = tabRows(a)
    // Whatever the model shows, the editor binds to exactly that: never the first page in its place.
    expect(rows.map(row => row.id)).toEqual(["default", "x"])
    expect(shownTab(rows, "x")).toEqual({ id: "x", field: "tab:x" })
  })
})

describe("tab URL fragments and IDs", () => {
  it("reads only #tab=<id> with a valid ID", () => {
    expect(tabFromHash("#tab=plan-1")).toBe("plan-1")
    expect(tabFromHash("#tab=")).toBeNull()
    expect(tabFromHash("#tab=a/b")).toBeNull()
    expect(tabFromHash("#comments")).toBeNull()
    expect(tabFromHash("")).toBeNull()
  })

  it("makes IDs the model accepts", () => {
    const ids = new Set(Array.from({ length: 50 }, () => newTabId("t")))
    expect(ids.size).toBe(50)
    for (const id of ids) expect(id).toMatch(/^t[a-z0-9]{9}$/)
  })
})
