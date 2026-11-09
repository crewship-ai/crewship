// UI lifecycle tests use a cipher seam; real browser tests cover encryption/admission.
vi.mock("@/lib/local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({ seal: async (text:string) => text, open: async (text:string) => text, close() {} }),
 forgetLocalDraftPage() {}, isSealedLocalDraft: () => true, LocalDraftReadRevoked: class extends Error {},
}))
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import * as Y from "yjs"
import { DesignEditor } from "../design-editor"
import { designNodes, upsertDesignNode } from "../design-document"
const copying=vi.hoisted(()=>({send:vi.fn(),real:false}))
vi.mock("@/lib/api-fetch",async()=>{
 const actual=await vi.importActual<typeof import("@/lib/api-fetch")>("@/lib/api-fetch")
 return {...actual,apiFetch:(...args:Parameters<typeof actual.apiFetch>)=>copying.real?actual.apiFetch(...args):copying.send(...args)}
})
const state = vi.hoisted(() => ({ comments: [] as unknown[], peerSelection: "" as string, setSelection: vi.fn(), canShare: true, editable: true, doc: null as Y.Doc | null, pageId: "old-private-page", user: false, peerColor: "#2563eb", saveError: null as (Error & { status?: number }) | null, restoreError: null as (Error & { status?: number }) | null, conflicts: [] as unknown[], dismiss: vi.fn(), dirty: false, unsynced: false }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => state.user ? { data: { user: { id: "same-user" } } } : null }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ ready: false, dispatch: vi.fn() }) }))
vi.mock("../use-design", () => ({ useDesign: (_workspaceId: string, _slug: string, selectedVersion?: number) => {
 const mutation = { mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false, error: null }
 return {
  query: { data: { page_id: state.pageId, can_edit: state.editable, can_share: state.canShare, can_comment: state.editable, latest_version: 1, comments: [] }, isError: false },
  comments: { data: { comments: state.comments }, isError: false },
  versions: { data: { versions: [{ version: 1 }], shared_version: 1 }, isError: false },
  doc: state.doc, nodes: designNodes(state.doc!), peers: [{ clientId: 2, name: "Marie", color: state.peerColor, cursor: { x: 12, y: 24 }, ...(state.peerSelection ? { selection: state.peerSelection.split(",") } : {}) }], setSelection: state.setSelection, connection: "connected", editable: state.editable && !selectedVersion, dirty: state.dirty, unsynced: state.unsynced, draftError: false,
  save: { ...mutation, error: state.saveError }, restore: { ...mutation, error: state.restoreError }, share: mutation, comment: mutation, connectAgent: mutation, setCursor: vi.fn(), conflicts: state.conflicts, dismissConflict: state.dismiss,
 }
} }))
beforeEach(() => { state.comments = []; state.peerSelection = ""; state.setSelection.mockReset(); copying.real=false;copying.send.mockReset(); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN", "true"); state.editable = true; state.canShare = true; state.pageId = "old-private-page"; state.user = false; state.peerColor = "#2563eb"; state.conflicts = []; state.dismiss.mockReset(); state.dirty = false; state.unsynced = false; state.saveError = null; state.restoreError = null; state.doc = new Y.Doc() })
afterEach(() => { cleanup(); state.doc?.destroy(); vi.unstubAllEnvs();vi.unstubAllGlobals() })
describe("Design editor controls", () => {
 it("changes layer order from Properties and says when a comment's layer is gone (V05)", () => {
  upsertDesignNode(state.doc!, { id: "s", type: "frame", name: "Screen", x: 0, y: 0, width: 360, height: 640, fill: "#ffffff" })
  upsertDesignNode(state.doc!, { id: "back", type: "rect", name: "Back", parentId: "s", x: 0, y: 0, width: 10, height: 10, fill: "#000000" })
  upsertDesignNode(state.doc!, { id: "front", type: "rect", name: "Front", parentId: "s", x: 0, y: 0, width: 10, height: 10, fill: "#000000" })
  state.comments = [
   { id: "c1", version: 1, path: "design.json", selector: "front", quote: "Front", text: "Fine" },
   { id: "c2", version: 1, path: "design.json", selector: "gone", quote: "Old button", text: "Make it larger" },
  ]
  const { container } = render(<DesignEditor workspaceId="workspace" slug="shop" />)
  const drawn = () => [...container.querySelectorAll("g[data-design-node]")].map(node => node.getAttribute("data-design-node"))
  expect(drawn()).toEqual(["s", "back", "front"])
  fireEvent.click(within(screen.getByLabelText("Layers")).getByRole("button", { name: "Back" }))
  const orderGroup = screen.getByRole("group", { name: "Layer order" })
  fireEvent.click(within(orderGroup).getByRole("button", { name: "Bring to front" }))
  const stored = () => designNodes(state.doc!).map(node => node.id)
  expect(stored()).toEqual(["s", "front", "back"])
  fireEvent.click(within(orderGroup).getByRole("button", { name: "Send backward" }))
  expect(stored()).toEqual(["s", "back", "front"])
  expect(screen.getAllByText(/This layer is no longer on the canvas/)).toHaveLength(1)
  expect(screen.getByText(/This layer is no longer on the canvas/).closest("article")).toHaveTextContent("Make it larger")
 })
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
 it("draws another person's selected layer in their colour and shares my own selection", () => {
  upsertDesignNode(state.doc!, { id: "s", type: "frame", name: "Screen", x: 0, y: 0, width: 360, height: 640, fill: "#ffffff" })
  upsertDesignNode(state.doc!, { id: "cta", type: "rect", name: "Buy", parentId: "s", x: 40, y: 60, width: 120, height: 40, fill: "#2563eb" })
  state.peerSelection = "cta"
  const view = render(<DesignEditor workspaceId="workspace" slug="shop" />)
  const outline = screen.getByLabelText("Marie selected Buy")
  expect(outline).toHaveAttribute("data-remote-selection", "cta")
  expect(outline).toHaveAttribute("transform", "translate(40,60)")
  expect(outline.querySelector("rect")).toHaveAttribute("stroke", state.peerColor)
  expect(within(outline).getByText("Marie")).toBeInTheDocument()
  // My selection goes out through awareness; the document does not change.
  const before = designNodes(state.doc!)
  fireEvent.click(within(screen.getByLabelText("Layers")).getByRole("button", { name: "Buy" }))
  expect(state.setSelection).toHaveBeenLastCalledWith(["cta"])
  expect(designNodes(state.doc!)).toEqual(before)
  view.unmount()
  // A selection of a layer that is gone draws nothing.
  state.peerSelection = "deleted-layer"
  const gone = render(<DesignEditor workspaceId="workspace" slug="shop" />)
  expect(screen.queryByLabelText(/Marie selected/)).toBeNull()
  gone.unmount()
  // A reader shares no selection of its own.
  state.peerSelection = ""
  state.editable = false
  state.setSelection.mockReset()
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  fireEvent.click(within(screen.getByLabelText("Layers")).getByRole("button", { name: "Buy" }))
  expect(state.setSelection).not.toHaveBeenCalledWith(["cta"])
 })
 it("selects several layers with Shift or Ctrl/⌘, shares them, draws a peer's multi-selection and deletes them together", () => {
  upsertDesignNode(state.doc!, { id: "s", type: "frame", name: "Screen", x: 0, y: 0, width: 360, height: 640, fill: "#ffffff" })
  upsertDesignNode(state.doc!, { id: "cta", type: "rect", name: "Buy", parentId: "s", x: 40, y: 60, width: 120, height: 40, fill: "#2563eb" })
  upsertDesignNode(state.doc!, { id: "copy", type: "text", name: "Copy", parentId: "s", x: 40, y: 140, width: 200, height: 40, fill: "#000000", text: "Hi" })
  state.peerSelection = "cta,copy"
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  // Marie's two layers are both outlined; her name shows once.
  expect(screen.getByLabelText("Marie selected Buy")).toHaveAttribute("data-remote-selection", "cta")
  expect(screen.getByLabelText("Marie selected Copy")).toHaveAttribute("data-remote-selection", "copy")
  expect(screen.getAllByText("Marie", { selector: "svg text" })).toHaveLength(2) // her selection label and her cursor
  const layers = within(screen.getByLabelText("Layers"))
  fireEvent.click(layers.getByRole("button", { name: "Buy" }))
  fireEvent.click(layers.getByRole("button", { name: "Copy" }), { shiftKey: true })
  expect(state.setSelection).toHaveBeenLastCalledWith(["cta", "copy"])
  expect(screen.getByText("2 layers selected")).toBeInTheDocument()
  expect(document.querySelector("[data-design-group]")).toHaveAttribute("data-design-group", "2")
  // Ctrl/⌘ click takes one out again.
  fireEvent.click(layers.getByRole("button", { name: "Copy" }), { metaKey: true })
  expect(state.setSelection).toHaveBeenLastCalledWith(["cta"])
  fireEvent.click(layers.getByRole("button", { name: "Copy" }), { ctrlKey: true })
  // Arrows move both by the same offset; Delete removes both, never the screen.
  const viewport = screen.getByLabelText("Design canvas viewport")
  fireEvent.keyDown(viewport, { key: "ArrowRight", shiftKey: true })
  expect(designNodes(state.doc!).filter(node => node.id !== "s").map(node => node.x)).toEqual([50, 50])
  fireEvent.keyDown(viewport, { key: "Escape" })
  expect(state.setSelection).toHaveBeenLastCalledWith([])
  fireEvent.click(layers.getByRole("button", { name: "Buy" }))
  fireEvent.click(layers.getByRole("button", { name: "Copy" }), { shiftKey: true })
  fireEvent.keyDown(viewport, { key: "Delete" })
  expect(designNodes(state.doc!).map(node => node.id)).toEqual(["s"])
 })
 it("draws peer labels on a fill that keeps white text readable", () => {
  state.peerColor = "#059669"
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  expect(screen.getByTitle("Marie")).toHaveStyle({ backgroundColor: "#047857" })
  expect(screen.getByLabelText("Marie cursor").querySelector("rect")).toHaveAttribute("fill", "#047857")
 })
 it.each([false, true])("labels the current History view according to edit authority (%s)", canEdit => {
  state.editable = canEdit
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  fireEvent.click(screen.getByRole("button", { name: /History/ }))
  const history = screen.getByLabelText("Version history")
  expect(within(history).getByRole("button", { name: canEdit ? "Live canvas" : "Current view" })).toBeEnabled()
  if (!canEdit) expect(within(history).queryByRole("button", { name: "Live canvas" })).not.toBeInTheDocument()
 })
 it.each([[true, 1], [false, 0]])("offers Share only when the server says this person may share (%s)", (canShare, buttons) => {
  state.canShare = canShare
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  fireEvent.click(screen.getByRole("button", { name: /History/ }))
  const history = screen.getByLabelText("Version history")
  expect(within(history).getByRole("button", { name: "Restore" })).toBeEnabled()
  expect(within(history).queryAllByRole("button", { name: "Share" })).toHaveLength(buttons)
 })
 it("offers the live canvas to an editor while inspecting a historical snapshot", () => {
  render(<DesignEditor workspaceId="workspace" slug="shop" initialVersion={1} />)
  fireEvent.click(screen.getByRole("button", { name: /History ·/ }))
  expect(within(screen.getByLabelText("Version history")).getByRole("button", { name: "Live canvas" })).toBeEnabled()
 })
 it("keeps the reader canvas and presence usable without edit controls or a live-update promise", () => {
  state.editable = false
  upsertDesignNode(state.doc!, { id: "shared-text", type: "text", text: "Shared snapshot content", x: 10, y: 20, width: 200, height: 40 })
  const original = designNodes(state.doc!)
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  expect(screen.getByText("Read only")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Screen" })).not.toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Save version" })).not.toBeInTheDocument()
  expect(screen.getByLabelText("Marie cursor")).toBeInTheDocument()
  const canvas = screen.getByRole("img", { name: "Design canvas" })
  expect(within(canvas).getByText("Shared snapshot content")).toBeInTheDocument()
  expect(screen.getByRole("status")).toHaveTextContent("Connected · read only")
  expect(within(screen.getByLabelText("Design tools")).getByText("View canvas")).toBeInTheDocument()
  expect(screen.queryByText("Follow changes live")).not.toBeInTheDocument()
  // Read-only selection/pointer interaction must not mutate the displayed model.
  const node = canvas.querySelector('[data-design-node="shared-text"]')!
  fireEvent.pointerDown(node, { clientX: 10, clientY: 20, pointerId: 1 })
  fireEvent.pointerMove(canvas, { clientX: 110, clientY: 120, pointerId: 1 })
  fireEvent.pointerUp(canvas, { pointerId: 1 })
  expect(designNodes(state.doc!)).toEqual(original)
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

describe("Design status after the write right went",()=>{
 it("never says changes are saved over unsynced work",()=>{
  state.editable=false;state.dirty=true;state.unsynced=true
  render(<DesignEditor workspaceId="workspace" slug="shop"/>)
  expect(screen.queryByText(/saved as you work/)).toBeNull()
  expect(screen.getByRole("status")).toHaveTextContent("unsent changes stay in this browser")
 })
 it("does not call a connected canvas up to date while the server has not confirmed the work",()=>{
  state.unsynced=true
  render(<DesignEditor workspaceId="workspace" slug="shop"/>)
  expect(screen.getByRole("status")).toHaveTextContent("Live · sending changes")
  expect(screen.getByRole("status")).not.toHaveTextContent("up to date")
  expect(screen.getByRole("status")).not.toHaveTextContent("saved as you work")
 })
 it("reports synced work as up to date, not as still being saved",()=>{
  state.editable=false;state.dirty=true
  render(<DesignEditor workspaceId="workspace" slug="shop"/>)
  expect(screen.getByRole("status")).toHaveTextContent("Connected · read only")
  expect(screen.getByRole("status")).not.toHaveTextContent("saved as you work")
 })
 it("keeps the live status for an editor",()=>{
  state.dirty=true
  render(<DesignEditor workspaceId="workspace" slug="shop"/>)
  expect(screen.getByRole("status")).toHaveTextContent("Live · changes saved as you work")
 })
})

describe("Design conflict notice",()=>{
 const node={id:"label",type:"text" as const,name:"Nadpis",text:"Základ",x:0,y:0,width:200,height:40,fill:"#111111"}
 const conflict=(kept:"mine"|"theirs")=>({id:"label",name:"Nadpis",mine:"Můj text",theirs:"Cizí text",kept,left:kept==="mine"?"Můj text":"Cizí text",restore:{...node,text:kept==="mine"?"Cizí text":"Můj text"}})
 it("shows the person who lost their text and restores it",()=>{
  upsertDesignNode(state.doc!,{...node,text:"Cizí text"})
  state.conflicts=[conflict("theirs")]
  render(<DesignEditor workspaceId="workspace" slug="shop"/>)
  const alert=screen.getByRole("alert")
  expect(alert).toHaveTextContent("Edit conflict")
  expect(alert).toHaveTextContent("Your text was not saved")
  expect(alert).toHaveTextContent("Můj text")
  fireEvent.click(screen.getByRole("button",{name:"Restore this text"}))
  expect(designNodes(state.doc!)[0].text).toBe("Můj text")
  expect(state.dismiss).toHaveBeenCalledWith(state.conflicts[0])
 })
 it("restores only the text, keeping what changed on the layer since",()=>{
  upsertDesignNode(state.doc!,{...node,text:"Cizí text",x:77,fill:"#ff0000",name:"Přejmenováno"})
  state.conflicts=[conflict("theirs")]
  render(<DesignEditor workspaceId="workspace" slug="shop"/>)
  fireEvent.click(screen.getByRole("button",{name:"Restore this text"}))
  expect(designNodes(state.doc!)).toMatchObject([{text:"Můj text",x:77,fill:"#ff0000",name:"Přejmenováno"}])
 })
 it("says the other person's text was overwritten and may exist nowhere else",()=>{
  state.conflicts=[conflict("mine")]
  render(<DesignEditor workspaceId="workspace" slug="shop"/>)
  const alert=screen.getByRole("alert")
  expect(alert).toHaveTextContent("other person’s text was overwritten")
  expect(alert).toHaveTextContent("stays in History only if it was saved in a version")
  expect(alert).toHaveTextContent("Cizí text")
 })
 it("does not claim a text stayed on a layer that was deleted",()=>{
  state.conflicts=[{...conflict("theirs"),left:undefined,theirs:""}]
  render(<DesignEditor workspaceId="workspace" slug="shop"/>)
  const alert=screen.getByRole("alert")
  expect(alert).toHaveTextContent("The layer was deleted meanwhile")
  expect(alert).not.toHaveTextContent("stayed on the canvas")
  fireEvent.click(screen.getByRole("button",{name:"Restore this text"}))
  expect(designNodes(state.doc!)).toMatchObject([{id:"label",text:"Můj text"}])
 })
 it("copies the text and keeps the notice when a person only dismisses it",async()=>{
  const writeText=vi.fn(async()=>{})
  vi.stubGlobal("navigator",{...navigator,clipboard:{writeText}})
  state.conflicts=[conflict("theirs")]
  render(<DesignEditor workspaceId="workspace" slug="shop"/>)
  fireEvent.click(screen.getByRole("button",{name:"Copy text"}))
  await waitFor(()=>expect(writeText).toHaveBeenCalledWith("Můj text"))
  expect(state.dismiss).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button",{name:"Dismiss"}))
  expect(state.dismiss).toHaveBeenCalledWith(state.conflicts[0])
 })
 it("does not offer a restore to a person who can no longer edit",()=>{
  state.editable=false
  state.conflicts=[conflict("theirs")]
  render(<DesignEditor workspaceId="workspace" slug="shop"/>)
  expect(screen.getByRole("button",{name:"Restore this text"})).toBeDisabled()
 })
})
