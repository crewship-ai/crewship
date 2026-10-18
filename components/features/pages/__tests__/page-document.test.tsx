import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render as baseRender, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import type { Editor } from "@tiptap/core"
import { PageDocument } from "../page-document"
import { encodeUpdate } from "@/hooks/use-page-document"
const availability=vi.hoisted(()=>({enabled:true}))
const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({data:availability.enabled,isPending:false}) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "document-test-human" } } }) }))
afterEach(() => { cleanup(); request.mockReset();availability.enabled=true })
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
