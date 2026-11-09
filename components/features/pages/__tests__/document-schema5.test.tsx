import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"
import { Editor } from "@tiptap/core"
import StarterKit from "@tiptap/starter-kit"
import { Table, TableCell, TableHeader, TableRow } from "@tiptap/extension-table"
import { DocumentReadonly } from "../document-readonly/document-readonly"
import { documentEditorExtensions, isFormattedSchema, isRichSchema, isStyledSchema, stableBlockTypes } from "../document-rich-extensions"

afterEach(cleanup)

const styled = { type: "doc", content: [{ type: "paragraph", attrs: { id: "p" }, content: [
  { type: "text", text: "under", marks: [{ type: "underline", attrs: {} }] },
  { type: "text", text: " " },
  { type: "text", text: "red on yellow", marks: [{ type: "textColor", attrs: { color: "red" } }, { type: "highlight", attrs: { color: "yellow" } }] },
] }] }

function editor(schema: number, content: string | object) {
  return new Editor({
    extensions: [StarterKit.configure({ undoRedo: false, bold: false, italic: false, strike: false, code: false, codeBlock: false, blockquote: false, horizontalRule: false, hardBreak: false, link: false, underline: false, trailingNode: false }),
      Table, TableRow, TableCell, TableHeader, ...documentEditorExtensions(schema)],
    content,
  })
}

describe("Docs schema 5 in the browser", () => {
  it("belongs to the rich and formatted families and adds the style marks only at 5", () => {
    expect([isRichSchema(5), isFormattedSchema(5), isStyledSchema(5), isStyledSchema(4)]).toEqual([true, true, true, false])
    expect(stableBlockTypes(5)).toEqual(stableBlockTypes(4))
    const names = (schema: number) => documentEditorExtensions(schema).map(extension => extension.name)
    expect(names(5)).toEqual(expect.arrayContaining(["underline", "textColor", "highlight"]))
    expect(names(4)).not.toEqual(expect.arrayContaining(["underline"]))
  })

  it("renders saved underline, colour and highlight as palette names, never inline CSS", () => {
    render(<DocumentReadonly schema={5} content={styled} accessibleLabel="Saved document" />)
    const region = screen.getByRole("region", { name: "Saved document" })
    expect(region.querySelector("u")!.textContent).toBe("under")
    const colour = region.querySelector('[data-text-color="red"]')!
    expect(colour.textContent).toBe("red on yellow")
    expect(region.querySelector('mark[data-highlight="yellow"]')!.textContent).toBe("red on yellow")
    expect(region.querySelector("[style]")).toBeNull()
  })

  it("refuses schema 5 content in an older reader instead of dropping the marks", () => {
    render(<DocumentReadonly schema={4} content={styled} accessibleLabel="Saved document" />)
    expect(screen.getByRole("status").textContent).toContain("Document unavailable")
  })

  it("keeps pasted underline and palette marks, and drops pasted CSS colours", () => {
    const pasted = editor(5, '<p><u>u</u> <span data-text-color="blue">b</span> <mark data-highlight="green">g</mark> <span style="color:#f00">css</span> <mark>plain</mark> <span data-text-color="#f00">bad</span></p>')
    const json = pasted.getJSON()
    const marks = (json.content![0].content ?? []).map(node => [(node as { text?: string }).text, (node.marks ?? []).map(mark => `${mark.type}${mark.attrs?.color ? ":" + mark.attrs.color : ""}`)])
    expect(marks).toEqual([["u", ["underline"]], [" ", []], ["b", ["textColor:blue"]], [" ", []], ["g", ["highlight:green"]], [" css plain bad", []]])
    pasted.destroy()
    const older = editor(4, "<p><u>u</u></p>")
    expect(older.getJSON().content![0].content![0].marks).toBeUndefined()
    older.destroy()
  })

  it("keeps dates and mentions as atoms, renders a neutral label without a name, and an older editor refuses them", () => {
    const atoms = { type: "doc", content: [{ type: "paragraph", attrs: { id: "p" }, content: [
      { type: "text", text: "Due " }, { type: "date", attrs: { value: "2026-11-20" } }, { type: "text", text: " for " }, { type: "mention", attrs: { userId: "u1" } },
    ] }] }
    render(<DocumentReadonly schema={5} content={atoms} accessibleLabel="Saved document" />)
    const region = screen.getByRole("region", { name: "Saved document" })
    expect(region.querySelector("time[data-date]")!.getAttribute("datetime")).toBe("2026-11-20")
    expect(region.querySelector("[data-mention]")!.textContent).toBe("@Unknown member")
    expect(region.textContent).not.toContain("u1")
    const pasted = editor(5, '<p><time data-date="2026-11-20">20 Nov</time> <span data-mention="u1">@Ada</span> <time data-date="2026-02-30">bad</time> <span data-mention="a b">@Bad</span></p>')
    const nodes = pasted.getJSON().content![0].content!.map(node => node.type === "text" ? `text:${(node as { text?: string }).text}` : `${node.type}:${JSON.stringify((node as { attrs?: unknown }).attrs)}`)
    expect(nodes).toEqual(['date:{"value":"2026-11-20"}', "text: ", 'mention:{"userId":"u1"}', "text: bad @Bad"])
    pasted.destroy()
    cleanup()
    render(<DocumentReadonly schema={4} content={atoms} accessibleLabel="Saved document" />)
    expect(screen.getByRole("status").textContent).toContain("Document unavailable")
  })
})
