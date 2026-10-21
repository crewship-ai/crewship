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

const singleTable = (content: unknown[], nested = false) => ({type:"table",attrs:{id:"table"},content:[{type:"tableRow",attrs:{id:"row"},content:[{type:"tableCell",attrs:{id:"cell"},content:nested?[{type:"bulletList",attrs:{id:"list"},content:[{type:"listItem",attrs:{id:"item"},content:[{type:"paragraph",attrs:{id:"cell-text"},content}]}]}]:[{type:"paragraph",attrs:{id:"cell-text"},content}]}]}]})
describe("Markdown backslash encoding follows the real GFM parser", () => {
 it.each([0,1,2,3])("preserves code with %i backslashes or explicitly refuses an unrepresentable table pipe", count => {
  for(const delimiter of ["|","`"]){const text="a"+"\\".repeat(count)+delimiter+"b",marks=[{type:"code",attrs:{}}];expect(parsed([paragraph(text,marks)]).querySelector("code")?.textContent).toBe(text)
   for(const nested of [false,true]){const body=[singleTable([{type:"text",text,marks}],nested)];if(delimiter==="|"&&count%2){expect(()=>engine.markdown(doc(body))).toThrow("odd backslash run");continue}const html=parsed(body);expect(html.querySelectorAll("th,td")).toHaveLength(1);expect(html.querySelector("code")?.textContent).toBe(text)}
  }
 })
 it.each([0,1,2,3])("preserves link label and decoded destination with %i query backslashes",count=>{
  const text="label"+"\\".repeat(count)+"|`end &amp; &#124;",href="https://example.invalid/?q="+"\\".repeat(count)+"|value%2Fkept&literal=%255C&entity=&amp;",marks=[{type:"link",attrs:{href}}];expect(engine.validLinkHref(href)).toBe(true)
  for(const content of [[paragraph(text,marks)],[singleTable([{type:"text",text,marks}])],[singleTable([{type:"text",text,marks}],true)]]){const html=parsed(content),anchor=html.querySelector("a")!;expect(anchor.textContent).toBe(text);const actual=new URL(anchor.getAttribute("href")!);expect(actual.origin+actual.pathname).toBe(new URL(href).origin+new URL(href).pathname);expect([...actual.searchParams]).toEqual([...new URL(href).searchParams]);expect(anchor.getAttribute("href")).toContain("%2Fkept");expect(anchor.getAttribute("href")).toContain("%255C");if(content[0].type==="table")expect(html.querySelectorAll("th,td")).toHaveLength(1)}
 })
 it("preserves padding and refuses inline CR/LF in direct and nested table code",()=>{
  for(const text of [" ","  a\\\\|b ","`a\\\\|b`"]){const html=parsed([singleTable([{type:"text",text,marks:[{type:"code",attrs:{}}]}],true)]);expect(html.querySelector("code")?.textContent).toBe(text)}
  for(const text of ["a\nb","a\rb"]){for(const body of [[paragraph(text,[{type:"code",attrs:{}}])],[singleTable([{type:"text",text,marks:[{type:"code",attrs:{}}]}],true)]])expect(()=>engine.markdown(doc(body))).toThrow("line breaks")}
 })
})
