import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"
import { DocumentReadonly } from "./document-readonly"

afterEach(cleanup)
const text = (value: string) => ({ type: "text", text: value })
const paragraph = (value: string) => ({ type: "paragraph", attrs: { id: "p1" }, content: [text(value)] })
const doc = (...content: unknown[]) => ({ type: "doc", content })
const label = "Saved Document body"

it.each([2, 3] as const)("renders schema %s as semantic, non-editable DOM", schema => {
  render(<DocumentReadonly schema={schema} content={doc(
    { type: "heading", attrs: { id: "h1", level: 2 }, content: [text("Saved heading")] },
    paragraph("<script>literal text</script>"),
  )} accessibleLabel={label} />)
  const body = screen.getByRole("region", { name: label })
  expect(screen.getByRole("heading", { name: "Saved heading", level: 2 })).toBeInTheDocument()
  expect(body).toHaveTextContent("<script>literal text</script>")
  expect(body.querySelector("script,textarea,input,[contenteditable],.ProseMirror")).toBeNull()
})

it("preserves closed schema 3 links and code blocks without interpreting text as HTML", () => {
  render(<DocumentReadonly schema={3} accessibleLabel={label} content={doc(
    { type: "paragraph", attrs: { id: "p1" }, content: [{ ...text("Reference"), marks: [{ type: "link", attrs: { href: "https://example.com/" } }] }] },
    { type: "codeBlock", attrs: { id: "code1" }, content: [text("<img src=x onerror=alert(1)>\nsecond line")] },
  )} />)
  const link = screen.getByRole("link", { name: "Reference" })
  expect(link).toHaveAttribute("href", "https://example.com/")
  expect(link).toHaveAttribute("rel", "noopener noreferrer")
  expect(link).toHaveAttribute("target", "_blank")
  const body = screen.getByRole("region", { name: label })
  expect(body.querySelector("pre > code")).toHaveTextContent("<img src=x onerror=alert(1)>")
  expect(body.querySelector("img")).toBeNull()
})

it("preserves list and simple table semantics from the existing schema", () => {
  render(<DocumentReadonly schema={2} accessibleLabel={label} content={doc(
    { type: "bulletList", attrs: { id: "list1" }, content: [{ type: "listItem", attrs: { id: "item1" }, content: [paragraph("List entry")] }] },
    { type: "table", attrs: { id: "table1" }, content: [{ type: "tableRow", attrs: { id: "row1" }, content: [{ type: "tableCell", attrs: { id: "cell1" }, content: [{ ...paragraph("Cell entry"), attrs: { id: "p2" } }] }] }] },
  )} />)
  expect(screen.getByRole("listitem")).toHaveTextContent("List entry")
  expect(screen.getByRole("cell")).toHaveTextContent("Cell entry")
})

it.each(["javascript:alert(1)", "data:text/html,hello", "https://user:pass@example.com/", "/relative", "https://example.com/\n"])("fails closed for unsafe link %s", href => {
  render(<DocumentReadonly schema={3} accessibleLabel={label} content={doc({
    type: "paragraph", attrs: { id: "p1" }, content: [{ ...text("Private linked text"), marks: [{ type: "link", attrs: { href } }] }],
  })} />)
  expect(screen.getByRole("status")).toHaveTextContent("Document unavailable")
  expect(screen.queryByText("Private linked text")).toBeNull()
  expect(screen.queryByRole("link")).toBeNull()
})

it.each([null, {}, doc(), text("Non-document root"), paragraph("Non-document root"), doc({ type: "image", attrs: { src: "https://example.com/image" } }), doc(paragraph("first"), paragraph("duplicate id")), doc({ type: "paragraph", attrs: { id: "p1", onclick: "bad" } })])("rejects malformed content without retaining a prior body", content => {
  const { rerender } = render(<DocumentReadonly schema={2} accessibleLabel={label} content={doc(paragraph("Previous private body"))} />)
  rerender(<DocumentReadonly schema={3} accessibleLabel={label} content={content} />)
  expect(screen.queryByText("Previous private body")).toBeNull()
  expect(screen.queryByRole("region", { name: label })).toBeNull()
  expect(screen.getByRole("status")).toHaveTextContent("Document unavailable")
})

it("rejects future schemas at runtime and recovers on a new valid read", () => {
  const { rerender } = render(<DocumentReadonly schema={2} content={doc(paragraph("Old body"))} accessibleLabel={label} />)
  rerender(<DocumentReadonly schema={99 as 3} content={doc(paragraph("Future private body"))} accessibleLabel={label} />)
  expect(screen.queryByText("Old body")).toBeNull()
  expect(screen.queryByText("Future private body")).toBeNull()
  expect(screen.getByRole("status")).toHaveTextContent("Document unavailable")
  rerender(<DocumentReadonly schema={3} content={doc(paragraph("New saved body"))} accessibleLabel="New read" />)
  expect(screen.getByRole("region", { name: "New read" })).toHaveTextContent("New saved body")
  expect(screen.queryByRole("status")).toBeNull()
})

it("does not change or remount an adjacent draft when saved content changes", () => {
  const view = (content: unknown) => <><textarea aria-label="Current draft" defaultValue="Draft" /><DocumentReadonly schema={3} content={content} accessibleLabel={label} /></>
  const { rerender } = render(view(doc(paragraph("Saved"))))
  const draft = screen.getByRole("textbox", { name: "Current draft" })
  fireEvent.change(draft, { target: { value: "Unsent human edits" } })
  rerender(view(null))
  expect(screen.getByRole("textbox", { name: "Current draft" })).toBe(draft)
  expect(draft).toHaveValue("Unsent human edits")
})
