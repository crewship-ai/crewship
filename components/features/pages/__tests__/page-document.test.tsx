// Editor lifecycle tests use a protected-store seam (as in the use-page-document
// hook tests); protected-store authorization and encryption are exercised by the
// real-browser and protected-document tests.
vi.mock("@/lib/protected-document-drafts", () => ({ openProtectedDocumentDrafts: async (...args: Parameters<typeof import("@/lib/document-queue").openDocumentDrafts>) => (await import("@/lib/document-queue")).openDocumentDrafts(...args) }))
vi.mock("@/lib/local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
 isSealedLocalDraft: () => false, LocalDraftProtectionError: class extends Error {}, LocalDraftReadRevoked: class extends Error {},
}))
import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render as baseRender, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import type { Editor } from "@tiptap/core"
import { PageDocument } from "../page-document"
import { decodeUpdate, encodeUpdate } from "@/hooks/use-page-document"
const availability=vi.hoisted(()=>({enabled:true}))
const account=vi.hoisted(()=>({id:"document-test-human"}))
const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({data:availability.enabled,isPending:false}) }))
const recoveryJournal=vi.hoisted(()=>({drafts:[] as import("@/lib/document-queue").DocumentDraft[],remove:vi.fn(async()=>{}),exclusive:true}))
vi.mock("@/lib/document-queue", async importOriginal => ({...await importOriginal<typeof import("@/lib/document-queue")>(),openDocumentDrafts:async()=>({load:async()=>recoveryJournal.drafts,put:async()=>{},remove:recoveryJournal.remove,close: () => {}, exclusive: recoveryJournal.exclusive})}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request, broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: account.id } } }) }))
afterEach(() => { cleanup(); request.mockReset();availability.enabled=true;account.id="document-test-human";recoveryJournal.drafts=[];recoveryJournal.remove.mockClear() })
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

it.each([
  ["text", (editor: Editor) => editor.commands.insertContent("First line")],
  ["a table", (editor: Editor) => editor.commands.insertTable({ rows: 2, cols: 2, withHeaderRow: true })],
  ["a heading", (editor: Editor) => editor.commands.toggleHeading({ level: 1 })],
] as const)("prompts in an empty document and drops the prompt once it holds %s", async (_kind, add) => {
  const saved = new Y.Doc({ gc: false })
  const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", "empty-paragraph")
  saved.getXmlFragment("default").insert(0, [paragraph])
  request.mockImplementation(async (url: string) => ({ ok: true, json: async () => url.includes("/comments") ? { comments: [], can_comment: false } : url.includes("/requests") ? { requests: [], can_dispatch: false, eligible_chats: [] } : { page_id:"page", schema: 2, rev: 1, epoch: "go:node:1", can_write: true, snapshot: encodeUpdate(Y.encodeStateAsUpdate(saved)) } }))
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
  const prompt = "This document is empty. Start writing below, or ask an agent in a chat to draft it."
  expect(await screen.findByText(prompt)).toBeInTheDocument()
  const editor = (screen.getByLabelText("Document editor") as HTMLElement & { editor: Editor }).editor
  await waitFor(() => expect(editor.isEditable).toBe(true))
  act(() => { expect(add(editor)).toBe(true) })
  // Neither variant may stay: a save in flight swaps the long prompt for the short one.
  await waitFor(() => expect(screen.queryByText(/This document is empty/)).toBeNull())
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 1700)) })
  expect(screen.queryByText(/This document is empty/)).toBeNull()
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
it("restores only after confirmation, locks the real editor and reopens confirmed content in a new epoch",async()=>{
  const f=await historyFixture(),{documentSchema}=await import("@/lib/documents/schema")
  const restored=documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"old-text"},content:[{type:"text",text:"Confirmed historical body"}]}]},3)
  const read=request.getMockImplementation()!
  let committed=false,resolve!: (value:unknown)=>void
  const receipt=new Promise(done=>{resolve=done})
  request.mockImplementation((url:string,init?:RequestInit)=>{
    if(url.includes("/restore")) { committed=true;const packet=JSON.parse(String(init?.body));return receipt.then(()=>({ok:true,status:200,json:async()=>({page_id:"page",id:packet.id,rev:8,epoch:"restored-epoch",kind:"restore"})})) }
    if(committed&&!url.includes("/checkpoints")) return Promise.resolve({ok:true,status:200,json:async()=>({page_id:"page",schema:3,rev:8,epoch:"restored-epoch",can_write:true,snapshot:documentSchema.snapshot(restored)})})
    return read(url,init)
  })
  try {
    await openHistory()
    fireEvent.click(screen.getByRole("button",{name:"Restore selected checkpoint"}))
    expect(request.mock.calls.some(([url])=>url.includes("/restore"))).toBe(false)
    fireEvent.click(screen.getByRole("button",{name:"Confirm restore"}))
    expect(f.editor.isEditable).toBe(false)
    expect(screen.getByRole("button",{name:"Close history"})).toBeDisabled()
    await act(async()=>resolve(undefined))
    await waitFor(()=>expect((screen.getByLabelText("Document editor")as HTMLElement&{editor:Editor}).editor).not.toBe(f.editor))
    expect(screen.getByLabelText("Document editor")).toHaveTextContent("Confirmed historical body")
    expect(request.mock.calls.filter(([url])=>url.includes("/restore"))).toHaveLength(1)
  } finally {f.view.unmount();f.saved.destroy();restored.destroy()}
})
it("never offers restore over a real pending local draft",async()=>{
  const f=await historyFixture()
  try {
    act(()=>f.editor.commands.insertContent("Local unsaved text "))
    const before=f.editor.getJSON()
    await openHistory()
    expect(screen.queryByRole("button",{name:"Restore selected checkpoint"})).toBeNull()
    expect(f.editor.getJSON()).toEqual(before)
    expect(request.mock.calls.some(([url])=>url.includes("/restore"))).toBe(false)
  } finally {f.view.unmount();f.saved.destroy()}
})
it("retains exact restore retry through service unavailability without unlocking the real editor",async()=>{
  const f=await historyFixture(),read=request.getMockImplementation()!
  request.mockImplementation((url:string,init?:RequestInit)=>url.includes("/restore")
    ?Promise.resolve({ok:false,status:503,json:async()=>({error:"unconfirmed"})}):read(url,init))
  try {
    await openHistory()
    fireEvent.click(screen.getByRole("button",{name:"Restore selected checkpoint"}))
    fireEvent.click(screen.getByRole("button",{name:"Confirm restore"}))
    await screen.findByRole("button",{name:"Retry exact restore"})
    const packet=request.mock.calls.find(([url])=>url.includes("/restore"))![1].body
    availability.enabled=false;f.view.rerender(<PageDocument workspaceId="w" slug="notes" pageId="page" onDirtyChange={f.dirty}/>)
    expect(screen.queryByRole("region",{name:"Document history"})).toBeNull()
    expect(f.editor.isEditable).toBe(false)
    availability.enabled=true;f.view.rerender(<PageDocument workspaceId="w" slug="notes" pageId="page" onDirtyChange={f.dirty}/>)
    fireEvent.click(await screen.findByRole("button",{name:"Retry exact restore"}))
    await waitFor(()=>expect(request.mock.calls.filter(([url])=>url.includes("/restore"))).toHaveLength(2))
    expect(request.mock.calls.filter(([url])=>url.includes("/restore"))[1][1].body).toBe(packet)
    expect(f.editor.isEditable).toBe(false)
  } finally {f.view.unmount();f.saved.destroy()}
})
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

async function openReviewHistory(canWrite:boolean,flag=true) {
  const {documentSchema}=await import("@/lib/documents/schema")
  vi.stubEnv("NEXT_PUBLIC_DOCUMENT_REVIEW_ACTIONS",flag?"true":"false")
  const content={type:"doc",content:[{type:"paragraph",attrs:{id:"p1"},content:[{type:"text",text:"Old"}]}]}
  const saved=documentSchema.create(content,3)
  request.mockImplementation(async(url:string)=>{
    const json=url.includes("/proposals")?{proposals:[]}:url.includes("/checkpoints/cp1")?{page_id:"page",id:"cp1",rev:1,schema:3,content}
      :url.includes("/checkpoints")?{checkpoints:[{id:"cp1",rev:1,schema:3,actor:"Ada",created_at:"2026-10-19T10:00:00Z"}]}
      :{page_id:"page",schema:3,rev:2,epoch:"epoch",generation:1,incarnation:"i",content,can_write:canWrite,can_comment:false,can_resolve:false,snapshot:documentSchema.snapshot(saved)}
    return {ok:true,status:200,json:async()=>json}
  })
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
  fireEvent.click(await screen.findByRole("button",{name:"History"}))
  fireEvent.click(await screen.findByRole("button",{name:/Revision 1/}))
  await screen.findByRole("button",{name:"Compare with saved version"})
  saved.destroy()
}
it("offers Revert to a writer",async()=>{
  await openReviewHistory(true)
  expect(screen.getByRole("button",{name:"Revert this change"})).toBeInTheDocument()
  vi.unstubAllEnvs()
})
it("never offers Revert to a reader",async()=>{
  await openReviewHistory(false)
  expect(screen.queryByRole("button",{name:"Revert this change"})).toBeNull()
  vi.unstubAllEnvs()
})
it("offers Compare without the review flag, but neither Revert nor proposals",async()=>{
  await openReviewHistory(true,false)
  expect(screen.getByRole("button",{name:"Compare with saved version"})).toBeInTheDocument()
  expect(screen.queryByRole("button",{name:"Revert this change"})).toBeNull()
  expect(screen.queryByRole("region",{name:"Document proposals"})).toBeNull()
  expect(request.mock.calls.some(([url])=>String(url).includes("/proposals"))).toBe(false)
  vi.unstubAllEnvs()
})

it("downloads the persisted recovery content and explicitly discards it before reopening",async()=>{
 const {documentSchema}=await import("@/lib/documents/schema")
 const confirmed=documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"p"},content:[{type:"text",text:"Confirmed head"}]}]},3)
 const draft=documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"p"},content:[{type:"text",text:"Pending browser draft"}]}]},3)
 recoveryJournal.drafts=[{id:"pending",schema:3,epoch:"previous-epoch",attempted:true,update:"bytes",snapshot:documentSchema.snapshot(draft)}]
 request.mockImplementation(async(url:string)=>({ok:true,json:async()=>url.includes("/comments")?{comments:[],can_comment:false}:url.includes("/requests")?{requests:[],can_dispatch:false,eligible_chats:[]}:{page_id:"page",schema:3,rev:1,epoch:"current-epoch",can_write:true,snapshot:documentSchema.snapshot(confirmed)}}))
 const blobs:Blob[]=[]
 const create=vi.spyOn(URL,"createObjectURL").mockImplementation(blob=>{blobs.push(blob as Blob);return "blob:recovery"})
 const revoke=vi.spyOn(URL,"revokeObjectURL").mockImplementation(()=>{})
 const click=vi.spyOn(HTMLAnchorElement.prototype,"click").mockImplementation(()=>{})
 try{
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
  fireEvent.click(await screen.findByRole("button",{name:"Download unsaved draft"}))
  expect(await blobs[0].text()).toContain("Pending browser draft")
  fireEvent.click(screen.getByRole("button",{name:"Discard recovered drafts and reopen"}))
  await waitFor(()=>expect(recoveryJournal.remove).toHaveBeenCalledWith("pending"))
  await waitFor(()=>expect(screen.queryByRole("button",{name:"Download unsaved draft"})).toBeNull())
  expect(request.mock.calls.some(([,init])=>init?.method==="POST")).toBe(false)
 }finally{create.mockRestore();revoke.mockRestore();click.mockRestore();confirmed.destroy();draft.destroy()}
})

it("keeps editing when the journal cannot be locked; earlier rows are download-only with no delete",async()=>{
 const {documentSchema}=await import("@/lib/documents/schema")
 const confirmed=documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"p"},content:[{type:"text",text:"Confirmed head"}]}]},3)
 recoveryJournal.exclusive=false
 recoveryJournal.remove.mockClear()
 recoveryJournal.drafts=[{id:"earlier",schema:3,epoch:"previous-epoch",attempted:false,update:"bytes",snapshot:documentSchema.snapshot(confirmed)}]
 request.mockImplementation(async(url:string)=>({ok:true,json:async()=>url.includes("/comments")?{comments:[],can_comment:false}:url.includes("/requests")?{requests:[],can_dispatch:false,eligible_chats:[]}:{page_id:"page",schema:3,rev:1,epoch:"current-epoch",can_write:true,snapshot:documentSchema.snapshot(confirmed)}}))
 try{
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
  expect(await screen.findByRole("button",{name:"Download earlier draft"})).toBeTruthy()
  const node=await screen.findByLabelText("Document editor")
  await waitFor(()=>expect((node as HTMLElement&{editor:Editor}).editor.isEditable).toBe(true))
  expect(screen.queryByRole("button",{name:/Discard/})).toBeNull()
  expect(document.body.textContent).toContain("could not lock this document's local drafts and an earlier draft exists")
  expect(document.body.textContent).not.toContain("retained privately")
  expect(recoveryJournal.remove).not.toHaveBeenCalled()
 }finally{recoveryJournal.exclusive=true;confirmed.destroy()}
})

it("undo takes back only this editor's own edit and keeps another person's confirmed edit", async () => {
  const server = new Y.Doc({ gc: false })
  const base = new Y.XmlElement("paragraph"); base.setAttribute("id", "base-paragraph")
  const baseText = new Y.XmlText(); baseText.insert(0, "Base"); base.insert(0, [baseText]); server.getXmlFragment("default").insert(0, [base])
  let rev = 1, otherConfirmed = false
  const posted: string[] = []
  request.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      const body = JSON.parse(init.body as string)
      posted.push(body.update); Y.applyUpdate(server, decodeUpdate(body.update))
      if (!otherConfirmed) {
        // A second identity, its own Yjs client, confirms a paragraph in the same head.
        otherConfirmed = true
        const other = new Y.Doc({ gc: false }); Y.applyUpdate(other, Y.encodeStateAsUpdate(server))
        const theirs = new Y.XmlElement("paragraph"); theirs.setAttribute("id", "other-paragraph")
        const theirsText = new Y.XmlText(); theirsText.insert(0, "Theirs confirmed"); theirs.insert(0, [theirsText])
        other.getXmlFragment("default").push([theirs])
        Y.applyUpdate(server, Y.encodeStateAsUpdate(other, Y.encodeStateVector(server))); other.destroy()
      }
      rev++
      return { ok: true, status: 200, json: async () => ({ id: body.id, epoch: body.epoch, rev }) }
    }
    return { ok: true, json: async () => url.includes("/comments") ? { comments: [], can_comment: false } : url.includes("/requests") ? { requests: [], can_dispatch: false, eligible_chats: [] } : { page_id: "page", schema: 2, rev, epoch: "go:node:1", can_write: true, snapshot: encodeUpdate(Y.encodeStateAsUpdate(server)) } }
  })
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
  await screen.findByText("Base")
  const editor = (screen.getByLabelText("Document editor") as HTMLElement & { editor: Editor }).editor
  await waitFor(() => expect(editor.isEditable).toBe(true))
  act(() => { editor.commands.insertContent("Mine ") })
  await waitFor(() => expect(editor.getText()).toContain("Theirs confirmed"), { timeout: 5000 })
  expect(editor.getText()).toContain("Mine Base")
  // The toolbar offers the same undo, for touch screens without a keyboard shortcut.
  const undo = screen.getByRole("button", { name: "Undo" })
  await waitFor(() => expect(undo).toBeEnabled())
  fireEvent.click(undo)
  expect(editor.getText()).not.toContain("Mine")
  expect(editor.getText()).toContain("Base")
  expect(editor.getText()).toContain("Theirs confirmed")
  // The undo is an ordinary local write: the server loses this editor's text, never the other person's.
  await waitFor(() => expect(posted.length).toBeGreaterThanOrEqual(2), { timeout: 5000 })
  const serverText = server.getXmlFragment("default").toString()
  expect(serverText).not.toContain("Mine")
  expect(serverText).toContain("Theirs confirmed")
  // Nothing remains to undo: the other person's paragraph is not in this editor's history.
  await waitFor(() => expect(undo).toBeDisabled())
  act(() => { expect(editor.commands.undo()).toBe(false) })
  expect(editor.getText()).toContain("Theirs confirmed")
  fireEvent.click(screen.getByRole("button", { name: "Redo" }))
  expect(editor.getText()).toContain("Mine Base")
  server.destroy()
})
