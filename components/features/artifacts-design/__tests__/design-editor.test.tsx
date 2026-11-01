// UI lifecycle tests use a cipher seam; real browser tests cover encryption/admission.
vi.mock("@/lib/local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({ seal: async (text:string) => text, open: async (text:string) => text, close() {} }),
 forgetLocalDraftPage() {}, isSealedLocalDraft: () => true, LocalDraftReadRevoked: class extends Error {},
}))
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import * as Y from "yjs"
import { DesignEditor } from "../design-editor"
import { designNodes, upsertDesignNode } from "../design-document"
const copying=vi.hoisted(()=>({send:vi.fn(),real:false}))
vi.mock("@/lib/api-fetch",async()=>{
 const actual=await vi.importActual<typeof import("@/lib/api-fetch")>("@/lib/api-fetch")
 return {...actual,apiFetch:(...args:Parameters<typeof actual.apiFetch>)=>copying.real?actual.apiFetch(...args):copying.send(...args)}
})
const state = vi.hoisted(() => ({ editable: true, doc: null as Y.Doc | null, pageId: "old-private-page", user: false, peerColor: "#2563eb", saveError: null as (Error & { status?: number }) | null, restoreError: null as (Error & { status?: number }) | null }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => state.user ? { data: { user: { id: "same-user" } } } : null }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ ready: false, dispatch: vi.fn() }) }))
vi.mock("../use-design", () => ({ useDesign: () => {
 const mutation = { mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false, error: null }
 return {
  query: { data: { page_id: state.pageId, can_edit: state.editable, can_comment: state.editable, latest_version: 1, comments: [] }, isError: false },
  comments: { data: { comments: [] }, isError: false },
  versions: { data: { versions: [{ version: 1 }], shared_version: 1 }, isError: false },
  doc: state.doc, nodes: designNodes(state.doc!), peers: [{ clientId: 2, name: "Marie", color: state.peerColor, cursor: { x: 12, y: 24 } }], connection: "connected", editable: state.editable, dirty: false, draftError: false,
  save: { ...mutation, error: state.saveError }, restore: { ...mutation, error: state.restoreError }, share: mutation, comment: mutation, connectAgent: mutation, setCursor: vi.fn(),
 }
} }))
beforeEach(() => { copying.real=false;copying.send.mockReset(); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN", "true"); state.editable = true; state.pageId = "old-private-page"; state.user = false; state.peerColor = "#2563eb"; state.saveError = null; state.restoreError = null; state.doc = new Y.Doc() })
afterEach(() => { cleanup(); state.doc?.destroy(); vi.unstubAllEnvs();vi.unstubAllGlobals() })
describe("Design editor controls", () => {
 it("offers Export HTML only with its flag, for the saved version, to editors and readers", () => {
  const view = render(<DesignEditor workspaceId="workspace" slug="shop" />)
  expect(screen.queryByRole("button", { name: /Export HTML/ })).not.toBeInTheDocument()
  view.unmount()
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_EXPORT", "true")
  for (const editable of [true, false]) {
   state.editable = editable
   const shown = render(<DesignEditor workspaceId="workspace" slug="shop" />)
   expect(screen.getByRole("button", { name: "Export HTML (version 1)" })).toHaveAttribute("title", expect.stringContaining("Unsaved live changes are not included"))
   shown.unmount()
  }
 })
 it("clears private unsent feedback when the same slug resolves to a new Page", async () => {
  state.user = true
  const view = render(<DesignEditor workspaceId="workspace" slug="same-slug" />)
  await waitFor(()=>expect(screen.getByLabelText("Comment for agent")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment for agent"), { target: { value: "Private feedback about old Page" } })
  expect(screen.getByLabelText("Comment for agent")).toHaveValue("Private feedback about old Page")
  state.pageId = "new-shared-page"
  view.rerender(<DesignEditor workspaceId="workspace" slug="same-slug" />)
  expect(screen.getByLabelText("Comment for agent")).toHaveValue("")
 })
 it("creates a screen and keeps awareness visible", async () => {
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  fireEvent.click(screen.getByRole("button", { name: "Screen" }))
  expect(designNodes(state.doc!)).toMatchObject([{ type: "frame", name: "Screen 1", width: 360, height: 640 }])
  expect(screen.getByLabelText("Marie cursor")).toBeInTheDocument()
 })
 it("draws peer labels on a fill that keeps white text readable", () => {
  state.peerColor = "#059669"
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  expect(screen.getByTitle("Marie")).toHaveStyle({ backgroundColor: "#047857" })
  expect(screen.getByLabelText("Marie cursor").querySelector("rect")).toHaveAttribute("fill", "#047857")
 })
 it("does not offer edit or save controls to readers", async () => {
  state.editable = false
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  expect(screen.getByText("Read only")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Screen" })).not.toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Save version" })).not.toBeInTheDocument()
  expect(screen.getByLabelText("Marie cursor")).toBeInTheDocument()
 })
})


it.each(["save", "restore"] as const)("offers an actual draft download after %s fills history without discarding the canvas or feedback", async operation => {
 state.user = true
 state[operation === "save" ? "saveError" : "restoreError"] = Object.assign(new Error("Design capacity is full; existing work is retained"), { status: 413 })
 upsertDesignNode(state.doc!, { id: "unsaved", type: "text", text: "Keep my local canvas", x: 0, y: 0, width: 240, height: 40 })
 render(<DesignEditor workspaceId="workspace" slug="shop" initialVersion={operation === "restore" ? 1 : undefined} />)
 if (operation === "restore") {
  expect(screen.getByRole("button", { name: "Download draft" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Return to live canvas" }))
 }
 await waitFor(()=>expect(screen.getByLabelText("Comment for agent")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment for agent"), { target: { value: "Keep my pending feedback" } })
 const originalCreate = Object.getOwnPropertyDescriptor(URL, "createObjectURL")
 const originalRevoke = Object.getOwnPropertyDescriptor(URL, "revokeObjectURL")
 const create = vi.fn((_blob: Blob) => "blob:design-draft")
 const revoke = vi.fn()
 const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
 Object.defineProperty(URL, "createObjectURL", { configurable: true, value: create })
 Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revoke })
 try {
  expect(screen.getByRole("button", { name: "Download draft" })).toBeEnabled()
  fireEvent.click(screen.getByRole("button", { name: "Download draft" }))
  expect(create).toHaveBeenCalledOnce()
  expect(click).toHaveBeenCalledOnce()
  const anchor = click.mock.instances[0] as HTMLAnchorElement
  expect(anchor.download).toMatch(/design.*draft.*\.json$/)
  const blob = create.mock.calls[0][0]
  const text = await new Promise<string>((resolve, reject) => {
   const reader = new FileReader()
   reader.onload = () => resolve(String(reader.result))
   reader.onerror = () => reject(reader.error)
   reader.readAsText(blob)
  })
  const downloaded = JSON.parse(text)
  expect(downloaded.nodes).toMatchObject([{ id: "unsaved", text: "Keep my local canvas" }])
  expect(downloaded.feedback_draft.text).toBe("Keep my pending feedback")
  const recovered = new Y.Doc()
  try {
   Y.applyUpdate(recovered, Uint8Array.from(atob(downloaded.yjs_state), char => char.charCodeAt(0)))
   expect(designNodes(recovered)).toEqual(designNodes(state.doc!))
  } finally { recovered.destroy() }
 } finally {
  click.mockRestore()
  if (originalCreate) Object.defineProperty(URL, "createObjectURL", originalCreate); else Reflect.deleteProperty(URL, "createObjectURL")
  if (originalRevoke) Object.defineProperty(URL, "revokeObjectURL", originalRevoke); else Reflect.deleteProperty(URL, "revokeObjectURL")
 }
 expect(screen.getByRole("alert")).toHaveTextContent(/history.*full/i)
 expect(designNodes(state.doc!)).toMatchObject([{ id: "unsaved", text: "Keep my local canvas" }])
 expect(screen.getByLabelText("Comment for agent")).toHaveValue("Keep my pending feedback")
})

it("offers continuation in a new Design without removing the current canvas or feedback",async()=>{
 state.user=true
 state.saveError=Object.assign(new Error("History full"),{status:413})
 upsertDesignNode(state.doc!,{id:"local",type:"text",text:"Only local copy",x:0,y:0,width:100,height:30})
 render(<DesignEditor workspaceId="workspace" slug="shop" />)
 await waitFor(()=>expect(screen.getByLabelText("Comment for agent")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment for agent"),{target:{value:"Unsent feedback stays here"}})
 fireEvent.click(screen.getByRole("button",{name:"Continue in a new Design"}))
 expect(screen.getByRole("dialog",{name:"Continue in a new Design"})).toBeInTheDocument()
 expect(designNodes(state.doc!)).toMatchObject([{text:"Only local copy"}])
 expect(screen.getByLabelText("Comment for agent")).toHaveValue("Unsent feedback stays here")
})

it("creates a confirmed copy in a new tab while retaining the original pending canvas and feedback",async()=>{
 state.user=true;state.saveError=Object.assign(new Error("History full"),{status:413})
 upsertDesignNode(state.doc!,{id:"local",type:"text",text:"Only local copy",x:0,y:0,width:100,height:30})
 copying.send.mockResolvedValue(new Response(JSON.stringify({page_id:"new-copy",slug:"continued",version:1}),{status:201}))
 render(<DesignEditor workspaceId="workspace" slug="shop" />)
 await waitFor(()=>expect(screen.getByLabelText("Comment for agent")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment for agent"),{target:{value:"Unsent feedback stays here"}})
 fireEvent.click(screen.getByRole("button",{name:"Continue in a new Design"}))
 fireEvent.change(screen.getByLabelText("New Design name"),{target:{value:"My continued canvas"}})
 fireEvent.click(screen.getByRole("button",{name:"Create new Design"}))
 const link=await screen.findByRole("link",{name:"Open new Design"})
 expect(link).toHaveAttribute("target","_blank")
 expect(link).toHaveAttribute("href","/artifacts/new-copy?workspace_id=workspace")
 expect(copying.send).toHaveBeenCalledOnce()
 const [url,request]=copying.send.mock.calls[0]
 expect(url).toBe("/api/v1/pages/design?workspace_id=workspace")
 const body=JSON.parse(request.body)
 expect(body).toMatchObject({name:"My continued canvas",source_page_id:state.pageId,expected_human:"same-user"})
 const recovered=new Y.Doc()
 try{Y.applyUpdate(recovered,Uint8Array.from(atob(body.initial_state),c=>c.charCodeAt(0)));expect(designNodes(recovered)).toEqual(designNodes(state.doc!))}finally{recovered.destroy()}
 expect(designNodes(state.doc!)).toMatchObject([{text:"Only local copy"}])
 expect(screen.getByLabelText("Comment for agent")).toHaveValue("Unsent feedback stays here")
})

it("keeps the original and feedback after an unconfirmed copy and does not replay creation",async()=>{
 state.user=true;state.saveError=Object.assign(new Error("History full"),{status:413})
 upsertDesignNode(state.doc!,{id:"local",type:"text",text:"Only local copy",x:0,y:0,width:100,height:30})
 copying.send.mockRejectedValue(new TypeError("connection reset"))
 render(<DesignEditor workspaceId="workspace" slug="shop" />)
 await waitFor(()=>expect(screen.getByLabelText("Comment for agent")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment for agent"),{target:{value:"Unsent feedback stays here"}})
 fireEvent.click(screen.getByRole("button",{name:"Continue in a new Design"}))
 fireEvent.click(screen.getByRole("button",{name:"Create new Design"}))
 await waitFor(()=>expect(screen.getByRole("dialog")).toHaveTextContent("The new Design could not be confirmed"))
 expect(copying.send).toHaveBeenCalledOnce()
 expect(screen.queryByRole("link",{name:"Open new Design"})).not.toBeInTheDocument()
 expect(designNodes(state.doc!)).toMatchObject([{text:"Only local copy"}])
 expect(screen.getByLabelText("Comment for agent")).toHaveValue("Unsent feedback stays here")
})

it("keeps an oversized local copy without starting an oversized upload",async()=>{
 state.user=true;state.saveError=Object.assign(new Error("History full"),{status:413})
 state.doc!.getMap("extra").set("large","x".repeat(4*1024*1024+1))
 render(<DesignEditor workspaceId="workspace" slug="shop" />)
 fireEvent.click(screen.getByRole("button",{name:"Continue in a new Design"}))
 fireEvent.click(screen.getByRole("button",{name:"Create new Design"}))
 await waitFor(()=>expect(screen.getByRole("dialog")).toHaveTextContent("The new Design could not be confirmed"))
 expect(copying.send.mock.calls.length).toBe(0)
 expect(state.doc!.getMap("extra").get("large")).toHaveLength(4*1024*1024+1)
})

it.each([200,202])("does not call status %s a confirmed new Design",async status=>{
 state.user=true;state.saveError=Object.assign(new Error("History full"),{status:413})
 copying.send.mockResolvedValue(new Response(JSON.stringify({page_id:"unconfirmed-copy",version:1}),{status}))
 render(<DesignEditor workspaceId="workspace" slug="shop" />)
 fireEvent.click(screen.getByRole("button",{name:"Continue in a new Design"}))
 fireEvent.click(screen.getByRole("button",{name:"Create new Design"}))
 await waitFor(()=>expect(screen.getByRole("dialog")).toHaveTextContent("The new Design could not be confirmed"))
 expect(screen.queryByRole("link",{name:"Open new Design"})).not.toBeInTheDocument()
})

it("never refreshes and replays new Design creation after 401 through the real apiFetch",async()=>{
 state.user=true;state.saveError=Object.assign(new Error("History full"),{status:413});copying.real=true
 let creates=0,refreshes=0
 vi.stubGlobal("fetch",vi.fn(async(input:RequestInfo|URL)=>{
  const path=typeof input==="string"?input:input instanceof Request?input.url:String(input)
  if(path.includes("/api/auth/token/refresh")){refreshes++;return Response.json({})}
  if(path.includes("/api/v1/pages/design")){creates++;return creates===1?Response.json({error:"expired"},{status:401}):Response.json({page_id:"replayed-copy",version:1},{status:201})}
  throw new Error("unexpected request "+path)
 }))
 render(<DesignEditor workspaceId="workspace" slug="shop" />)
 fireEvent.click(screen.getByRole("button",{name:"Continue in a new Design"}))
 fireEvent.click(screen.getByRole("button",{name:"Create new Design"}))
 await waitFor(()=>expect(creates).toBeGreaterThan(0))
 await waitFor(()=>expect(screen.queryByText("Creating…")).not.toBeInTheDocument())
 expect(creates).toBe(1);expect(refreshes).toBe(0)
 expect(screen.queryByRole("link",{name:"Open new Design"})).not.toBeInTheDocument()
})
