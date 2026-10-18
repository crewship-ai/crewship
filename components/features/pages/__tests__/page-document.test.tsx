import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render as baseRender, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import type { Editor } from "@tiptap/core"
import { PageDocument } from "../page-document"
import { encodeUpdate } from "@/hooks/use-page-document"
const availability=vi.hoisted(()=>({enabled:true}))
const account=vi.hoisted(()=>({id:"document-test-human"}))
const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({data:availability.enabled,isPending:false}) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: account.id } } }) }))
afterEach(() => { cleanup(); request.mockReset();availability.enabled=true;account.id="document-test-human" })
it("hydrates saved CRDT content before binding the editor without manufacturing a local write", async () => {
  const saved = new Y.Doc({ gc: false })
  const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", "saved-paragraph")
  const text = new Y.XmlText(); text.insert(0, "Saved collaborative text"); paragraph.insert(0, [text]); saved.getXmlFragment("default").insert(0, [paragraph])
  request.mockImplementation(async (url: string) => ({ ok: true, json: async () => url.includes("/comments") ? { comments: [], can_comment: false } : url.includes("/requests") ? { requests: [], can_dispatch: false, eligible_chats: [] } : { page_id:"page", schema: 2, rev: 1, epoch: "go:node:1", can_write: true, snapshot: encodeUpdate(Y.encodeStateAsUpdate(saved)) } }))
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
  expect(await screen.findByText("Saved collaborative text")).toBeInTheDocument()
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 1700)) })
  expect(document.querySelectorAll('[data-block-id="saved-paragraph"]')).toHaveLength(1)
  expect(request.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
  saved.destroy()
})

it("offers an unsaved draft download after a real editor update is rejected", async () => {
  const saved = new Y.Doc({ gc: false })
  const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", "recovery-paragraph")
  const text = new Y.XmlText(); text.insert(0, "Original text"); paragraph.insert(0, [text]); saved.getXmlFragment("default").insert(0, [paragraph])
  request.mockImplementation(async (url: string) => ({ ok: !url.includes("/patch"), status: url.includes("/patch") ? 400 : 200, json: async () => url.includes("/patch") ? { error: "unsupported content" } : url.includes("/comments") ? { comments: [], can_comment: false } : url.includes("/requests") ? { requests: [], can_dispatch: false, eligible_chats: [] } : { page_id:"page", schema: 2, rev: 1, epoch: "go:node:1", can_write: true, snapshot: encodeUpdate(Y.encodeStateAsUpdate(saved)) } }))
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
  await screen.findByText("Original text")
  const editor = (screen.getByLabelText("Document editor") as HTMLElement & { editor: Editor }).editor
  await waitFor(() => expect(editor.isEditable).toBe(true))
  act(() => { editor.commands.insertContent("Private rejected draft ") })
  expect(await screen.findByRole("button", { name: "Download unsaved draft" })).toBeEnabled()
  await waitFor(() => expect(editor.isEditable).toBe(false))
  expect(editor.getText()).toContain("Private rejected draft")
  saved.destroy()
})

it("renders native schema3 links/code and normalizes only explicit user link input", async () => {
  const {documentSchema}=await import("@/lib/documents/schema")
  const saved=documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"rich-paragraph"},content:[{type:"text",text:"linked",marks:[{type:"link",attrs:{href:"https://example.invalid/"}}]}]},{type:"codeBlock",attrs:{id:"rich-code"},content:[{type:"text",text:"<script>literal()</script>"}]}]},3)
  request.mockImplementation(async(url:string)=>({ok:true,json:async()=>url.includes("/comments")?{comments:[],can_comment:false}:url.includes("/requests")?{requests:[],can_dispatch:false,eligible_chats:[]}:{page_id:"page",schema:3,rev:1,epoch:"go:node:1",can_write:true,snapshot:documentSchema.snapshot(saved)}}))
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
  expect(await screen.findByText("linked")).toHaveAttribute("href","https://example.invalid/")
  expect(screen.getByText("<script>literal()</script>").closest("pre")).toBeInTheDocument()
  expect(document.querySelector("script")).toBeNull()
  const editor=(screen.getByLabelText("Document editor") as HTMLElement&{editor:Editor}).editor
  await waitFor(()=>expect(editor.isEditable).toBe(true));act(()=>editor.commands.setTextSelection({from:1,to:7}))
  fireEvent.change(screen.getByLabelText("Link address"),{target:{value:"https://example.com"}});fireEvent.click(screen.getByRole("button",{name:"Apply link"}))
  expect(editor.getHTML()).toContain('href="https://example.com/"');expect(editor.getHTML()).toContain('rel="noopener noreferrer"')
  saved.destroy()
})


it("hides previously loaded server content after access is revoked", async () => {
  const {documentSchema}=await import("@/lib/documents/schema")
  const saved=documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"private"},content:[{type:"text",text:"Private server text"}]}]},3)
  let denied=false
  request.mockImplementation(async()=>denied?{ok:false,status:403,json:async()=>({error:"revoked"})}:{ok:true,json:async()=>({page_id:"page",schema:3,rev:1,epoch:"epoch",can_write:true,snapshot:documentSchema.snapshot(saved)})})
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
  await screen.findByText("Private server text")
  denied=true
  await waitFor(()=>expect(screen.queryByText("Private server text")).toBeNull())
  expect(screen.queryByLabelText("Document editor")).toBeNull()
  saved.destroy()
})

function render(element:ReactNode) {
  const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})
  const wrapped=(value:ReactNode)=>createElement(QueryClientProvider,{client},value)
  const view=baseRender(wrapped(element))
  return {...view,rerender:(value:ReactNode)=>view.rerender(wrapped(value))}
}


it("retains the local draft when availability is disabled after hydration",async()=>{
 const {documentSchema}=await import("@/lib/documents/schema")
 const saved=documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"draft"},content:[{type:"text",text:"Saved text"}]}]},3)
 request.mockImplementation(async()=>({ok:true,json:async()=>({page_id:"page",schema:3,rev:1,epoch:"epoch",can_write:true,snapshot:documentSchema.snapshot(saved)})}))
 const view=render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
 await screen.findByText("Saved text")
 const editor=(screen.getByLabelText("Document editor") as HTMLElement&{editor:Editor}).editor
 await waitFor(()=>expect(editor.isEditable).toBe(true))
 act(()=>editor.commands.insertContent("Local unsaved "))
 availability.enabled=false;view.rerender(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
 expect(screen.getByLabelText("Document editor")).toHaveTextContent("Local unsaved")
 await waitFor(()=>expect(editor.isEditable).toBe(false))
 expect(screen.getByRole("button",{name:"Download unsaved draft"})).toBeEnabled()
 expect(request.mock.calls.some(([,init])=>init?.method==="POST")).toBe(false)
 saved.destroy()
})


it.each([
  {name:"schema2 heading",schema:2,node:{type:"heading",attrs:{id:"last-heading",level:2},content:[{type:"text",text:"Saved final heading"}]}},
  {name:"schema3 code block",schema:3,node:{type:"codeBlock",attrs:{id:"last-code"},content:[{type:"text",text:"saved code"}]}}
])("does not manufacture a trailing block or write when opening $name",async fixture=>{
  const {documentSchema}=await import("@/lib/documents/schema")
  const saved=documentSchema.create({type:"doc",content:[fixture.node]},fixture.schema)
  try {
    request.mockImplementation(async(url:string)=>({ok:true,json:async()=>url.includes("/comments")?{comments:[],can_comment:false}:url.includes("/requests")?{requests:[],can_dispatch:false,eligible_chats:[]}:{page_id:"page",schema:fixture.schema,rev:1,epoch:"initial-epoch",can_write:true,snapshot:documentSchema.snapshot(saved)}}))
    await act(async()=>{render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)})
    const node=await screen.findByLabelText("Document editor")
    const editor=(node as HTMLElement&{editor:Editor}).editor
    await waitFor(()=>expect(editor.isEditable).toBe(true))
    await act(async()=>{await new Promise(resolve=>setTimeout(resolve,1700))})
    expect(request.mock.calls.filter(([,init])=>init?.method==="POST")).toHaveLength(0)
    expect(editor.getJSON().content).toHaveLength(1)
    expect(node.querySelector("p")).toBeNull()
  } finally {cleanup();saved.destroy()}
})


async function historyFixture() {
  const { documentSchema } = await import("@/lib/documents/schema")
  const saved = documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"live-text"},content:[{type:"text",text:"Current live revision"}]}]},3)
  const confirmed = {type:"doc",content:[{type:"paragraph",attrs:{id:"old-text"},content:[{type:"text",text:"Confirmed historical body"}]}]}
  const pin = {id:"confirmed-pin",rev:2,schema:3,actor:"user:document-test-human",created_at:"2026-10-18T00:00:00Z"}
  request.mockImplementation(async(url:string) => ({ok:true,status:200,json:async()=>url.includes("/checkpoints/confirmed-pin")
    ? {page_id:"page",id:pin.id,rev:pin.rev,schema:3,content:confirmed}
    : url.includes("/checkpoints") ? {checkpoints:[pin]}
    : {page_id:"page",schema:3,rev:7,epoch:"current-live-epoch",can_write:true,snapshot:documentSchema.snapshot(saved)}}))
  const dirty=vi.fn(), view=render(<PageDocument workspaceId="w" slug="notes" pageId="page" onDirtyChange={dirty} />)
  const node=await screen.findByLabelText("Document editor"),editor=(node as HTMLElement&{editor:Editor}).editor
  await waitFor(()=>expect(editor.isEditable).toBe(true))
  return {saved,view,node,editor,dirty}
}
async function openHistory() {
  fireEvent.click(screen.getByRole("button",{name:"History"}))
  fireEvent.click(await screen.findByRole("button",{name:/Revision 2/}))
  await screen.findByText("Confirmed historical body")
}
it("browses immutable history and closes without replacing the real editor or unsaved draft",async()=>{
  const f=await historyFixture()
  try {
    expect(request.mock.calls.some(([url])=>url.includes("/checkpoints"))).toBe(false)
    act(()=>f.editor.commands.insertContent("Unsaved local draft "))
    const before=f.editor.getJSON(),dirtyCalls=f.dirty.mock.calls.length
    await openHistory()
    expect(screen.getByLabelText("Document editor")).toBe(f.node)
    expect((f.node as HTMLElement&{editor:Editor}).editor).toBe(f.editor)
    expect(f.editor.getJSON()).toEqual(before);expect(f.editor.getText()).not.toContain("Confirmed historical body")
    fireEvent.click(screen.getByRole("button",{name:"Close history"}))
    expect(screen.queryByText("Confirmed historical body")).toBeNull()
    expect(screen.getByLabelText("Document editor")).toBe(f.node)
    expect(f.editor.getJSON()).toEqual(before);expect(f.dirty.mock.calls.slice(dirtyCalls)).not.toContainEqual([false])
    const historyCalls=request.mock.calls.filter(([url])=>url.includes("/checkpoints"))
    expect(historyCalls.map(([url])=>url)).toEqual(["/api/v1/pages/%40page/document/checkpoints?workspace_id=w","/api/v1/pages/%40page/document/checkpoints/confirmed-pin?workspace_id=w"])
    expect(historyCalls.every(([,init])=>!init?.method&&!init?.body)).toBe(true)
  } finally {f.view.unmount();f.saved.destroy()}
})
it("removes history on unavailable service while retaining the admitted real editor and draft",async()=>{
  const f=await historyFixture()
  try {
    await openHistory();act(()=>f.editor.commands.insertContent("Keep local draft "))
    availability.enabled=false;f.view.rerender(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
    expect(screen.queryByText("Confirmed historical body")).toBeNull()
    expect(screen.getByRole("button",{name:"History"})).toBeDisabled()
    expect(screen.getByLabelText("Document editor")).toBe(f.node)
    expect(f.editor.getText()).toContain("Keep local draft")
    await waitFor(()=>expect(f.editor.isEditable).toBe(false))
    expect(screen.getByRole("button",{name:"Download unsaved draft"})).toBeEnabled()
  } finally {f.view.unmount();f.saved.destroy()}
})
it("clears and disables protected history on auth expiry without remounting the editor",async()=>{
  const f=await historyFixture()
  try {
    await openHistory()
    act(()=>window.dispatchEvent(new Event("auth:session-expired")))
    expect(screen.queryByText("Confirmed historical body")).toBeNull()
    expect(screen.getByRole("button",{name:"History"})).toBeDisabled()
    expect(screen.getByLabelText("Document editor")).toBe(f.node)
    expect((f.node as HTMLElement&{editor:Editor}).editor).toBe(f.editor)
  } finally {f.view.unmount();f.saved.destroy()}
})
it("drops the former human's history immediately on account change without auto-fetching it",async()=>{
  const f=await historyFixture()
  try {
    await openHistory();const count=request.mock.calls.filter(([url])=>url.includes("/checkpoints")).length
    account.id="another-human";f.view.rerender(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
    expect(screen.queryByText("Confirmed historical body")).toBeNull()
    expect(screen.queryByRole("region",{name:"Confirmed revision 2"})).toBeNull()
    expect(request.mock.calls.filter(([url])=>url.includes("/checkpoints"))).toHaveLength(count)
  } finally {f.view.unmount();f.saved.destroy()}
})
