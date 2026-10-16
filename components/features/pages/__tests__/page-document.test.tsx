import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import type { Editor } from "@tiptap/core"
import { PageDocument } from "../page-document"
import { encodeUpdate } from "@/hooks/use-page-document"
const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
afterEach(() => { cleanup(); request.mockReset() })
it("hydrates saved CRDT content before binding the editor without manufacturing a local write", async () => {
  const saved = new Y.Doc({ gc: false })
  const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", "saved-paragraph")
  const text = new Y.XmlText(); text.insert(0, "Saved collaborative text"); paragraph.insert(0, [text]); saved.getXmlFragment("default").insert(0, [paragraph])
  request.mockImplementation(async (url: string) => ({ ok: true, json: async () => url.includes("/comments") ? { comments: [], can_comment: false } : url.includes("/requests") ? { requests: [], can_dispatch: false, eligible_chats: [] } : { schema: 2, rev: 1, epoch: "go:node:1", can_write: true, snapshot: encodeUpdate(Y.encodeStateAsUpdate(saved)) } }))
  render(<PageDocument workspaceId="w" slug="notes" />)
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
  request.mockImplementation(async (url: string) => ({ ok: !url.includes("/patch"), status: url.includes("/patch") ? 400 : 200, json: async () => url.includes("/patch") ? { error: "unsupported content" } : url.includes("/comments") ? { comments: [], can_comment: false } : url.includes("/requests") ? { requests: [], can_dispatch: false, eligible_chats: [] } : { schema: 2, rev: 1, epoch: "go:node:1", can_write: true, snapshot: encodeUpdate(Y.encodeStateAsUpdate(saved)) } }))
  render(<PageDocument workspaceId="w" slug="notes" />)
  await screen.findByText("Original text")
  const editor = (screen.getByLabelText("Document editor") as HTMLElement & { editor: Editor }).editor
  await waitFor(() => expect(editor.isEditable).toBe(true))
  act(() => { editor.commands.insertContent("Private rejected draft ") })
  expect(await screen.findByRole("button", { name: "Download unsaved draft" })).toBeEnabled()
  await waitFor(() => expect(editor.isEditable).toBe(false))
  expect(editor.getText()).toContain("Private rejected draft")
  saved.destroy()
})

it("freezes editor immediately during restore and retains a copy when the response is ambiguous", async () => {
  const saved = new Y.Doc({ gc: false })
  const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", "restore-paragraph")
  const text = new Y.XmlText(); text.insert(0, "Current saved copy"); paragraph.insert(0, [text]); saved.getXmlFragment("default").insert(0, [paragraph])
  let finishRestore: (value: unknown) => void = () => {}
  const restoreReply = new Promise(resolve => { finishRestore = resolve })
  request.mockImplementation(async (url: string) => {
    if (url.includes("/restore")) return restoreReply
    return { ok: true, json: async () => url.includes("/checkpoints") ? { checkpoints: [{ id: "checkpoint", rev: 1 }] } : url.includes("/comments") ? { comments: [], can_comment: false } : url.includes("/requests") ? { requests: [], can_dispatch: false, eligible_chats: [] } : { schema: 2, rev: 1, epoch: "go:node:1", can_write: true, snapshot: encodeUpdate(Y.encodeStateAsUpdate(saved)) } }
  })
  const dirty = vi.fn()
  render(<PageDocument workspaceId="w" slug="notes" onDirtyChange={dirty} />)
  await screen.findByText("Current saved copy")
  const editor = (screen.getByLabelText("Document editor") as HTMLElement & { editor: Editor }).editor
  await waitFor(() => expect(editor.isEditable).toBe(true))
  fireEvent.click(screen.getByRole("button", { name: "History" }))
  fireEvent.click(await screen.findByRole("button", { name: /^Restore$/ }))
  expect(editor.isEditable).toBe(false)
  expect(screen.getByLabelText("Document editor").getAttribute("contenteditable")).toBe("false")
  expect(dirty).toHaveBeenLastCalledWith(true)
  await act(async () => { finishRestore({ ok: false, status: 503, json: async () => ({ error: "Restore reply unavailable" }) }) })
  expect(await screen.findByRole("button", { name: "Download current copy" })).toBeEnabled()
  expect(editor.isEditable).toBe(false)
  expect(editor.getText()).toBe("Current saved copy")
  expect(dirty).toHaveBeenLastCalledWith(true)
  saved.destroy()
})

it.each(["confirmed-read-failure", "uncertain-write"])("keeps checkpoint acknowledgment distinct from refresh failure: %s", async scenario => {
  const saved = new Y.Doc({ gc: false })
  const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", "checkpoint-paragraph")
  const text = new Y.XmlText(); text.insert(0, "Checkpoint text"); paragraph.insert(0, [text]); saved.getXmlFragment("default").insert(0, [paragraph])
  const submissions: Record<string, unknown>[] = []
  request.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST" && url.includes("/checkpoint")) {
      submissions.push(JSON.parse(init.body as string))
      if (scenario === "uncertain-write" && submissions.length === 1) return { ok: false, status: 503, json: async () => ({ error: "Reply lost" }) }
      return { ok: true, json: async () => ({ rev: 1 }) }
    }
    if (url.includes("/checkpoints")) return scenario === "confirmed-read-failure" ? { ok: false, status: 503, json: async () => ({ error: "History unavailable" }) } : { ok: true, json: async () => ({ checkpoints: [{ id: "checkpoint", rev: 1 }] }) }
    return { ok: true, json: async () => url.includes("/comments") ? { comments: [], can_comment: false } : url.includes("/requests") ? { requests: [], can_dispatch: false, eligible_chats: [] } : { schema: 2, rev: 1, epoch: "go:node:1", can_write: true, snapshot: encodeUpdate(Y.encodeStateAsUpdate(saved)) } }
  })
  render(<PageDocument workspaceId="w" slug="notes" />)
  await screen.findByText("Checkpoint text")
  const save = screen.getByRole("button", { name: "Save checkpoint" })
  await waitFor(() => expect(save).toBeEnabled())
  fireEvent.click(save)
  if (scenario === "uncertain-write") {
    fireEvent.click(await screen.findByRole("button", { name: "Retry checkpoint" }))
    await waitFor(() => expect(submissions).toHaveLength(2))
    expect(submissions[1]).toEqual(submissions[0])
  }
  expect(await screen.findByText("Checkpoint saved at revision 1.")).toBeInTheDocument()
  if (scenario === "confirmed-read-failure") {
    expect(screen.getByText(/Checkpoint saved, but history could not be refreshed/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Retry checkpoint" })).toBeNull()
    expect(submissions).toHaveLength(1)
  }
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
it("freezes editing and retries the exact original Page/revision upgrade after uncertain acknowledgment",async()=>{
  const {documentSchema}=await import("@/lib/documents/schema");const saved=documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"upgrade-p"},content:[{type:"text",text:"Original plain document"}]}]},2);const submissions:unknown[]=[]
  request.mockImplementation(async(url:string,init?:RequestInit)=>{if(url.includes("/upgrade")){submissions.push(JSON.parse(init!.body as string));return{ok:false,status:503,json:async()=>({error:"Lost upgrade acknowledgment"})}};return{ok:true,json:async()=>url.includes("/comments")?{comments:[],can_comment:false}:url.includes("/requests")?{requests:[],can_dispatch:false,eligible_chats:[]}:{page_id:"page",schema:2,rev:7,epoch:"go:node:1",can_write:true,snapshot:documentSchema.snapshot(saved)}}})
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />);const start=await screen.findByRole("button",{name:"Enable links and code"});await waitFor(()=>expect(start).toBeEnabled());fireEvent.click(start)
  const retry=await screen.findByRole("button",{name:"Retry upgrade"});await waitFor(()=>expect(retry).toBeEnabled());expect(screen.getByLabelText("Document editor")).toHaveAttribute("contenteditable","false");fireEvent.click(retry);await waitFor(()=>expect(submissions).toHaveLength(2));expect(submissions[1]).toEqual(submissions[0]);expect(submissions[0]).toMatchObject({schema:2,target_schema:3,page_id:"page",ifRev:7,epoch:"go:node:1"});expect(screen.getByText("Original plain document")).toBeInTheDocument();saved.destroy()
})

it("protects unsent comments and agent instructions before an upgrade reload",async()=>{
 const {documentSchema}=await import("@/lib/documents/schema");const saved=documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"draft-p"},content:[{type:"text",text:"Saved document"}]}]},2)
 request.mockImplementation(async(url:string)=>({ok:true,json:async()=>url.includes("/comments")?{comments:[],can_comment:true,can_resolve:true}:url.includes("/requests")?{requests:[],can_dispatch:true,eligible_chats:[{chat_id:"chat",agent_name:"Agent"}]}:{page_id:"page",schema:2,rev:1,epoch:"go:node:1",can_write:true,snapshot:documentSchema.snapshot(saved)}}))
 const dirty=vi.fn();render(<PageDocument workspaceId="w" slug="notes" pageId="page" onDirtyChange={dirty} />);const upgrade=await screen.findByRole("button",{name:"Enable links and code"});await waitFor(()=>expect(upgrade).toBeEnabled());fireEvent.change(await screen.findByLabelText("Comment text"),{target:{value:"Unsent comment"}});fireEvent.change(await screen.findByLabelText("Agent instructions"),{target:{value:"Unsent task"}});await waitFor(()=>expect(upgrade).toBeDisabled());expect(dirty).toHaveBeenLastCalledWith(true);const warning=new Event("beforeunload",{cancelable:true});window.dispatchEvent(warning);expect(warning.defaultPrevented).toBe(true);fireEvent.click(screen.getByRole("button",{name:"Discard local comment draft"}));expect(upgrade).toBeDisabled();expect(screen.getByLabelText("Agent instructions")).toHaveValue("Unsent task");fireEvent.click(screen.getByRole("button",{name:"Discard draft"}));await waitFor(()=>expect(upgrade).toBeEnabled());const cleared=new Event("beforeunload",{cancelable:true});window.dispatchEvent(cleared);expect(cleared.defaultPrevented).toBe(false);saved.destroy()
})

it('retains local comment and agent drafts through epoch recovery and clears server content on access loss',async()=>{
 const {documentSchema}=await import("@/lib/documents/schema");const saved=documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"keep-p"},content:[{type:"text",text:"Private saved document"}]}]},2);let epoch='go:node:1',denied=false;
 request.mockImplementation(async(url:string)=>denied?{ok:false,status:403,json:async()=>({error:'revoked'})}:{ok:true,json:async()=>url.includes('/comments')?{comments:[],can_comment:true,can_resolve:true}:url.includes('/requests')?{requests:[],can_dispatch:true,eligible_chats:[]}:{page_id:'page',schema:2,rev:1,epoch,can_write:true,snapshot:documentSchema.snapshot(saved)}});render(<PageDocument workspaceId="w" slug="notes" pageId="page"/>);fireEvent.change(await screen.findByLabelText('Comment text'),{target:{value:'Keep my local comment'}});fireEvent.change(await screen.findByLabelText('Agent instructions'),{target:{value:'Keep my local task'}});epoch='go:new:2';await screen.findByText(/Document history changed/);expect(screen.getByLabelText('Comment text')).toHaveValue('Keep my local comment');expect(screen.getByLabelText('Agent instructions')).toHaveValue('Keep my local task');
 // A child-channel denial must clear the Page's server data, while own text stays recoverable.
 denied=true;await waitFor(()=>expect(screen.getByLabelText('Retained local comment draft')).toHaveValue('Keep my local comment'),{timeout:6500});expect(screen.getByLabelText('Retained local agent instructions')).toHaveValue('Keep my local task');expect(screen.queryByText('Private saved document')).toBeNull();saved.destroy()
});
it('disables every agent input while a version upgrade is pending',async()=>{
 const {documentSchema}=await import("@/lib/documents/schema");const saved=documentSchema.create({type:'doc',content:[{type:'paragraph',attrs:{id:'pending-p'},content:[{type:'text',text:'Saved'}]}]},2);request.mockImplementation(async(url:string)=>url.includes('/upgrade')?new Promise(()=>{}):{ok:true,json:async()=>url.includes('/comments')?{comments:[],can_comment:false}:url.includes('/requests')?{requests:[],can_dispatch:true,eligible_chats:[]}:{page_id:'page',schema:2,rev:1,epoch:'go:node:1',can_write:true,snapshot:documentSchema.snapshot(saved)}});render(<PageDocument workspaceId="w" slug="notes" pageId="page"/>);const button=await screen.findByRole('button',{name:'Enable links and code'});await screen.findByLabelText('Agent instructions');await waitFor(()=>expect(button).toBeEnabled());fireEvent.click(button);expect(screen.getByLabelText('Agent instructions')).toBeDisabled();expect(screen.getByLabelText('Private agent chat')).toBeDisabled();saved.destroy()
});
