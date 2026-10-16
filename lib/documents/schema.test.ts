import { describe, expect, it } from "vitest"
import { marked } from "marked"
import * as Y from "yjs"
import { documentSchema as engine, normalizeDocumentLink } from "./schema"
const paragraph = (text: string, marks?: unknown[]) => ({type:"paragraph",attrs:{id:"text"},content:[{type:"text",text,...(marks?{marks}:{})}]})
const link = {type:"link",attrs:{href:"https://example.invalid/a|b"}}
const doc = (content: unknown[]) => ({type:"doc",content})
const parsed = (content: unknown[]) => { engine.validateJSON(doc(content),3); const holder = document.createElement("div"); holder.innerHTML = String(marked.parse(engine.markdown(doc(content)))); return holder }
describe("browser uses the identical closed Docs schema implementation", () => {
 it("binds this browser's Yjs classes and preserves legacy binary/hash and anchors", async () => {
  const state=engine.create(doc([paragraph("legacy 🐎")]),2);expect(state).toBeInstanceOf(Y.Doc);const before=engine.snapshot(state),hash=await engine.hash(engine.validate(state,2));expect(await engine.hash(engine.validate(state,3))).toBe(hash);expect(engine.snapshot(state)).toBe(before);state.destroy()
 })
 it("accepts explicit3 but rejects legacy2 and malformed/deleted formatting", () => {
  const good=engine.create(doc([paragraph("linked",[link])]),3);expect(()=>engine.validate(good,2)).toThrow();good.destroy()
  for(const attributes of [{bold:{}},{link:{href:"javascript:alert(1)"}},{link:{href:"https://example.invalid/",target:"_blank"}},{"link--abcdefgh":link.attrs}]){const state=engine.create(doc([paragraph("private")]),2),text=engine.find(state,"text").toArray()[0];if(!(text instanceof Y.XmlText))throw new Error("Expected typed text child");text.format(0,text.length,attributes);expect(()=>engine.validate(state,3)).toThrow();text.delete(0,text.length);const copy=engine.load(engine.snapshot(state));expect(()=>engine.validate(copy,3)).toThrow();copy.destroy();state.destroy()}
 })
 it("normalizes common user hrefs without weakening the shared canonical validator", () => {
  expect(normalizeDocumentLink("https://example.com")).toBe("https://example.com/");expect(normalizeDocumentLink(" https://example.com ")).toBe("https://example.com/");expect(engine.validLinkHref("https://example.com")).toBe(false);expect(normalizeDocumentLink("javascript:alert(1)")).toBeUndefined();expect(normalizeDocumentLink("https://user:secret@example.com/")).toBeUndefined()
 })
})
describe("real Markdown parser preserves rich text instead of silently changing it", () => {
 it("keeps all-space and boundary-space inline code exact", () => {
  for(const text of [" ","   "," a","a "," a ","a`b"]){const html=parsed([paragraph(text,[{type:"code",attrs:{}}])]);expect(html.querySelector("code")?.textContent).toBe(text)}
 })
 it("keeps literal pipes in inline code, link labels and destinations within one table cell", () => {
  const table={type:"table",attrs:{id:"table"},content:[{type:"tableRow",attrs:{id:"row"},content:[{type:"tableCell",attrs:{id:"cell"},content:[{type:"paragraph",attrs:{id:"cell-text"},content:[{type:"text",text:"a|b",marks:[{type:"code",attrs:{}}]},{type:"text",text:"c|d",marks:[link]}]}]}]}]}
  const html=parsed([table]);expect(html.querySelectorAll("th,td")).toHaveLength(1);expect(html.querySelector("code")?.textContent).toBe("a|b");expect(html.querySelector("a")?.textContent).toBe("c|d");expect(html.querySelector("a")?.getAttribute("href")).toBe("https://example.invalid/a%7Cb")
 })
 it("preserves literal fenced code including embedded closing fences", () => {
  const text="<script>literal()</script>\n```";const html=parsed([{type:"codeBlock",attrs:{id:"code"},content:[{type:"text",text}]}]);expect(html.querySelector("pre code")?.textContent).toBe(text+"\n");expect(html.querySelector("script")).toBeNull()
 })
})

it("reports unsupported inline newlines and table code blocks instead of lossy Markdown", () => {
 expect(()=>engine.markdown(doc([paragraph("a\nb",[{type:"code",attrs:{}}])]))).toThrow("line breaks")
 const table={type:"table",attrs:{id:"table"},content:[{type:"tableRow",attrs:{id:"row"},content:[{type:"tableCell",attrs:{id:"cell"},content:[{type:"codeBlock",attrs:{id:"code"},content:[{type:"text",text:"a\nb"}]}]}]}]}
 expect(()=>engine.markdown(doc([table]))).toThrow("code block inside a simple table")
})

it("propagates table export context through nested lists for escaping and unsupported reports", () => {
 const list = (content: unknown[]) => ({type:"bulletList",attrs:{id:"list"},content:[{type:"listItem",attrs:{id:"item"},content}]})
 const table = (content: unknown[]) => ({type:"table",attrs:{id:"table"},content:[{type:"tableRow",attrs:{id:"row"},content:[{type:"tableCell",attrs:{id:"cell"},content}]}]})
 const rich=list([paragraph("a|b",[{type:"code",attrs:{}}])]);const html=parsed([table([rich])]);expect(html.querySelectorAll("th,td")).toHaveLength(1);expect(html.querySelector("code")?.textContent).toBe("a|b")
 const unsupported=doc([table([list([paragraph("before"),{type:"codeBlock",attrs:{id:"code"},content:[{type:"text",text:"a\nb"}]}])])]);engine.validateJSON(unsupported,3);expect(()=>engine.markdown(unsupported)).toThrow("code block inside a simple table")
})
