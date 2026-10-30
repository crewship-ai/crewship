// UI lifecycle tests use a cipher seam; real browser tests cover encryption/admission.
vi.mock("@/lib/local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({ seal: async (text:string) => text, open: async (text:string) => text, close() {} }),
 forgetLocalDraftPage() {}, isSealedLocalDraft: () => true, LocalDraftReadRevoked: class extends Error {},
}))
import React from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { setArtifactDraftStore } from "@/lib/artifact-drafts"
const state = vi.hoisted(() => ({ user: "owner", token: "", save: vi.fn(), comment: vi.fn(), dispatch: vi.fn(), data: { page_id:"demo", can_write: true, can_comment: false, can_share: true, can_restore: true, can_send_to_agent: true, latest_version: 2, shared_version: 1, manifest: { version: 2, entrypoint: "index.html", files: [{ path: "index.html", content: "<h1>Hello</h1>", media_type: "text/html" }, { path: "details.html", content: "<h1>Details</h1>", media_type: "text/html" }] }, versions: [{ version: 2 }, { version: 1 }], quota: { used_bytes: 10, limit_bytes: 100, warning: false }, comments: [] }, restore: vi.fn(), share: vi.fn() }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ dispatch: state.dispatch, ready: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: state.user } } }) }))
vi.mock("@/hooks/use-artifact-project", () => ({ useArtifactProject: () => ({ query: { data: state.data }, restore: { mutate: state.restore }, share: { mutate: state.share }, comment: { mutate: state.comment }, save: { mutate: state.save } }) }))
vi.mock("@/lib/pages/artifact-project-preview", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/pages/artifact-project-preview")>(), artifactProjectDocument: vi.fn((_files: unknown, _path: string, token: string) => { state.token = token; return "safe document" }) }))
vi.mock("@/components/features/chat/artifact-context/viewer/artifact-slides-preview", () => ({ ArtifactSlidesPreview: ({selectMode,onSelect}:{selectMode:boolean;onSelect:(value:{selector:string;quote:string})=>void}) => <div>Typed Page Slides<button disabled={!selectMode} onClick={() => onSelect({selector:"#typed",quote:"Typed heading"})}>Pick typed element</button></div> }))
vi.mock("@/components/features/chat/artifact-context/viewer/artifact-dashboard-preview", () => ({ ArtifactDashboardPreview: ({selectMode,onSelect}:{selectMode:boolean;onSelect:(value:{selector:string;quote:string})=>void}) => <button disabled={!selectMode} onClick={() => onSelect({selector:"#typed",quote:"Typed heading"})}>Pick typed element</button> }))
import { artifactProjectDocument } from "@/lib/pages/artifact-project-preview"
import { ArtifactProject } from "../artifact-project"
// Durable draft storage: a put lands on "disk" only when released (strict IndexedDB commit).
const disk = new Map<string,string>()
const held: (() => void)[] = []
let hold = false
const release = () => { for (const done of held.splice(0)) done() }
const row = (human:string, kind:string) => JSON.stringify([human, kind, "w", "demo"])
beforeEach(() => {
 disk.clear(); held.length = 0; hold = false
 setArtifactDraftStore({
  capture: async () => [], removeCaptured: async rows => { for(const row of rows) disk.delete(JSON.stringify(row.row)) },
  get: async key => disk.get(JSON.stringify(key)),
  put: (key, text) => new Promise<void>(resolve => { const done = () => { disk.set(JSON.stringify(key), text); resolve() }; if (hold) held.push(done); else done() }),
  delete: async key => { disk.delete(JSON.stringify(key)) },
  purge: async () => { disk.clear() },
 })
})
// Edit source is enabled once the kept source draft has been read.
async function editSource() {
 await waitFor(() => expect(screen.getByRole("button",{name:"Edit source"})).toBeEnabled())
 fireEvent.click(screen.getByRole("button",{name:"Edit source"}))
}
afterEach(() => { cleanup(); vi.clearAllMocks(); state.data.can_write = true; state.user = "owner"; localStorage.clear(); setArtifactDraftStore(undefined) })
it("switches screens, restores with the latest base and shares only the selected version", async () => {
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 fireEvent.click(screen.getByRole("button", { name: "details.html" }))
 expect(screen.getByTitle("Artifact preview")).toHaveAttribute("data-screen", "details.html")
 fireEvent.change(screen.getByLabelText("Version"), { target: { value: "1" } })
 fireEvent.click(screen.getByRole("button", { name: "Restore version 1" }))
 expect(state.restore).toHaveBeenCalledWith({ version: 1, base_version: 2 }, expect.objectContaining({ onSuccess: expect.any(Function) }))
 fireEvent.click(screen.getByRole("button", { name: "Share version 1" }))
 expect(state.share).toHaveBeenCalledWith({ version: 1, base_version: 2 })
})
it("keeps all write controls unavailable to a reader", async () => {
 state.data.can_write = false
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 expect(screen.queryByRole("button", { name: /Restore version/ })).toBeNull()
 expect(screen.queryByRole("button", { name: /Share version/ })).toBeNull()
 expect(screen.queryByRole("button", { name: "Send to agent" })).toBeNull()
 expect(screen.getByText("Read only")).toBeTruthy()
 expect(screen.queryByRole("button", {name:"Select an element"})).toBeNull()
 expect(screen.queryByRole("textbox")).toBeNull()
})

it("never moves an unsent draft into another user browser scope", async () => {
 const view=render(<ArtifactProject workspaceId="w" slug="demo" />)
 await waitFor(()=>expect(screen.getByLabelText("Comment for agent")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment for agent"),{target:{value:"Owner private draft"}})
 await waitFor(() => expect(disk.get(row("owner","artifact-comment"))).toBe("Owner private draft"))
 state.user="reader"
 view.rerender(<ArtifactProject workspaceId="w" slug="demo" />)
 expect(screen.getByLabelText("Comment for agent")).toHaveValue("")
 await act(async () => {})
 expect(screen.getByLabelText("Comment for agent")).toHaveValue("")
 expect(disk.has(row("reader","artifact-comment"))).toBe(false)
 view.unmount()
 state.user="owner"
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await waitFor(() => expect(screen.getByLabelText("Comment for agent")).toHaveValue("Owner private draft"))
})

it("retains 500 Unicode codepoints in a normalized selected quote sent to the agent", async () => {
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 fireEvent.click(screen.getByRole("button", { name: "Select an element" }))
 const frame = screen.getByTitle("Artifact preview") as HTMLIFrameElement
 const quote = "  \ufeffŽluťoučký\u0085  kůň\u00a0pěl\nďábelské\tódy  " + "🙂".repeat(520)
 fireEvent(window, new MessageEvent("message", { origin: "null", source: frame.contentWindow, data: { token: state.token, selector: "#artifact-unicode-quote", quote } }))
 await waitFor(()=>expect(screen.getByLabelText("Comment for agent")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment for agent"), { target: { value: "Uprav nadpis" } })
 fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
 const expected = Array.from("\ufeffŽluťoučký kůň pěl ďábelské ódy " + "🙂".repeat(520)).slice(0,500).join("")
 expect(state.comment.mock.calls[0][0].quote).toBe(expected)
 expect(state.comment.mock.calls[0][0].send_to_agent).toBe(true)
})

it("keeps the Page Project preview when the Dashboard flag is off and dashboard.json is only an asset", async () => {
 vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", "false")
 state.data.manifest.files.push({ path: "dashboard.json", media_type: "application/json", content: '{"schema_version":1,"collections":["sales"]}' })
 try {
  render(<ArtifactProject workspaceId="w" slug="demo" />)
  expect(screen.getByTitle("Artifact preview")).toHaveAttribute("srcdoc", "safe document")
  expect(screen.queryByText("Dashboard preview is unavailable.")).toBeNull()
 } finally { state.data.manifest.files.pop(); vi.unstubAllEnvs() }
})

it.each([["true", "slides", "typed"], ["true", undefined, "unsupported"], ["false", "slides", "legacy"]] as const)("keeps the Pages Slides route safe with flag %s", async (flag, kind, mode) => {
 vi.stubEnv("NEXT_PUBLIC_ARTIFACT_SLIDES", flag)
 const previous = state.data
 state.data = Object.assign({}, previous, { artifact_kind: kind, page_id: "page", manifest: { ...previous.manifest, files: [...previous.manifest.files, { path: "deck.json", media_type: "application/json", content: '{}' }] } })
 try {
  render(<ArtifactProject workspaceId="w" slug="demo" />)
  if (mode === "legacy") expect(screen.getByTitle("Artifact preview")).toHaveAttribute("srcdoc", "safe document")
  else {
   expect(screen.getByText(mode === "typed" ? "Typed Page Slides" : "This Slides format is unsupported.")).toBeInTheDocument()
   expect(screen.queryByTitle("Artifact preview")).toBeNull()
   expect(artifactProjectDocument).not.toHaveBeenCalled()
  }
 } finally { state.data = previous; vi.unstubAllEnvs() }
})

function selectAnchor() {
 fireEvent.click(screen.getByRole("button", { name: "Select an element" }))
 const frame = screen.getByTitle("Artifact preview") as HTMLIFrameElement
 fireEvent(window, new MessageEvent("message", { origin: "null", source: frame.contentWindow, data: { token: state.token, selector: "#shared", quote: "Shared heading" } }))
}
function sharedCommenter() {
 state.data = { ...state.data, can_write: false, can_comment: true, manifest: { ...state.data.manifest, version: 1 }, versions: [{version: 1}] }
}
it("allows a pinned shared commenter to select and post feedback without agent execution", async () => {
 const previous = state.data
 try {
  sharedCommenter()
  state.data = Object.assign({}, state.data, {origin:{chat_id:"owner-chat",agent_id:"owner-agent"}})
  render(<ArtifactProject workspaceId="w" slug="demo" />)
  selectAnchor()
  await waitFor(()=>expect(screen.getByLabelText("Comment")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment"), {target:{value:"Shared feedback"}})
  fireEvent.click(screen.getByRole("button", {name:"Post comment"}))
  expect(state.comment).toHaveBeenCalledWith({version:1,path:"index.html",selector:"#shared",quote:"Shared heading",text:"Shared feedback",send_to_agent:false},expect.anything())
  expect(screen.queryByRole("button",{name:/Restore version|Share version|Send to agent/})).toBeNull()
  await act(async () => { await state.comment.mock.calls[0][1].onSuccess({id:"comment",version:1}) })
  expect(state.dispatch).not.toHaveBeenCalled()
 } finally { state.data = previous }
})
it("rejects commenter selection outside the shared pin", async () => {
 const previous = state.data
 try {
  state.data = {...state.data, can_write:false, can_comment:true}
  render(<ArtifactProject workspaceId="w" slug="demo" />)
  expect(screen.queryByRole("button",{name:"Select an element"})).toBeNull()
  expect(screen.queryByRole("textbox")).toBeNull()
 } finally { state.data = previous }
})
it.each(["revoke","user","workspace","slug"])("retires selected comment anchors on %s while preserving the scoped draft", async change => {
 const previous = state.data
 try {
  sharedCommenter()
  const view=render(<ArtifactProject workspaceId="w" slug="demo" />)
  selectAnchor()
  await waitFor(()=>expect(screen.getByLabelText("Comment")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment"),{target:{value:"Keep this feedback"}})
  if(change==="revoke")state.data={...state.data,can_comment:false}
  if(change==="user")state.user="other"
  view.rerender(<ArtifactProject workspaceId={change==="workspace"?"other":"w"} slug={change==="slug"?"other":"demo"} />)
  const post=screen.queryByRole("button",{name:"Post comment"})
  if(post) { expect(post).toBeDisabled(); fireEvent.click(post) }
  expect(state.comment).not.toHaveBeenCalled()
  if(change==="revoke") {
   state.data={...state.data,can_comment:true}
   view.rerender(<ArtifactProject workspaceId="w" slug="demo" />)
   expect(screen.getByLabelText("Comment")).toHaveValue("Keep this feedback")
   expect(screen.getByRole("button",{name:"Post comment"})).toBeDisabled()
  }
 } finally { state.data=previous }
})

it.each(["slides","dashboard"])("allows shared Comment selection in the typed %s preview", async kind => {
 const previous=state.data
 vi.stubEnv(kind==="slides"?"NEXT_PUBLIC_ARTIFACT_SLIDES":"NEXT_PUBLIC_ARTIFACT_DASHBOARDS","true")
 try {
  sharedCommenter()
  state.data=Object.assign({},state.data,{artifact_kind:kind==="slides"?"slides":undefined,manifest:{...state.data.manifest,files:[...state.data.manifest.files,{path:kind==="slides"?"deck.json":"dashboard.json",media_type:"application/json",content:"{}"}]}})
  render(<ArtifactProject workspaceId="w" slug="demo" />)
  expect(screen.getByRole("button",{name:"Pick typed element"})).toBeDisabled()
  fireEvent.click(screen.getByRole("button",{name:"Select an element"}))
  fireEvent.click(screen.getByRole("button",{name:"Pick typed element"}))
  await waitFor(()=>expect(screen.getByLabelText("Comment")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment"),{target:{value:"Typed feedback"}})
  fireEvent.click(screen.getByRole("button",{name:"Post comment"}))
  expect(state.comment.mock.calls[0][0]).toMatchObject({version:1,selector:"#typed",send_to_agent:false})
 } finally { state.data=previous; vi.unstubAllEnvs() }
})

it.each(["user","workspace","slug","revoke","new-input"])("ignores an old comment success after %s", async change => {
 const previous=state.data
 try {
  sharedCommenter()
  const view=render(<ArtifactProject workspaceId="w" slug="demo" />)
  selectAnchor()
  await waitFor(()=>expect(screen.getByLabelText("Comment")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment"),{target:{value:"Submitted feedback"}})
  fireEvent.click(screen.getByRole("button",{name:"Post comment"}))
  const success=state.comment.mock.calls[0][1].onSuccess
  if(change==="user")state.user="new-user"
  if(change==="revoke") {
   state.data={...state.data,can_comment:false}
   view.rerender(<ArtifactProject workspaceId="w" slug="demo" />)
   state.data={...state.data,can_comment:true}
  }
  view.rerender(<ArtifactProject workspaceId={change==="workspace"?"next":"w"} slug={change==="slug"?"next":"demo"} />)
  await waitFor(()=>expect(screen.getByLabelText("Comment")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment"),{target:{value:"Newer feedback"}})
  await act(async()=>{await success({id:"old",version:1})})
  expect(screen.getByLabelText("Comment")).toHaveValue("Newer feedback")
  if(change==="new-input")expect(screen.getByRole("button",{name:"Post comment"})).toBeEnabled()
  expect(state.dispatch).not.toHaveBeenCalled()
 }finally{state.data=previous}
})
it("retires an owner submission before its success can dispatch after downgrade to Comment", async () => {
 const previous=state.data
 try {
  state.data=Object.assign({},state.data,{origin:{chat_id:"owner-chat",agent_id:"owner-agent"}})
  const view=render(<ArtifactProject workspaceId="w" slug="demo" />)
  selectAnchor()
  await waitFor(()=>expect(screen.getByLabelText("Comment for agent")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment for agent"),{target:{value:"Owner feedback"}})
  fireEvent.click(screen.getByRole("button",{name:"Send to agent"}))
  const success=state.comment.mock.calls[0][1].onSuccess
  sharedCommenter()
  view.rerender(<ArtifactProject workspaceId="w" slug="demo" />)
  await waitFor(()=>expect(screen.getByLabelText("Comment")).not.toBeDisabled()); fireEvent.change(screen.getByLabelText("Comment"),{target:{value:"Commenter feedback"}})
  await act(async()=>{await success({id:"owner-old",version:2})})
  expect(state.dispatch).not.toHaveBeenCalled()
  expect(screen.getByLabelText("Comment")).toHaveValue("Commenter feedback")
 }finally{state.data=previous}
})

it("lets an editor save a whole manifest while withholding owner controls", async () => {
 const previous=state.data
 try {
  state.data={...state.data,can_share:false,can_restore:false,can_send_to_agent:false}
  render(<ArtifactProject workspaceId="w" slug="demo" />)
  expect(screen.queryByRole("button",{name:/Restore version|Share version|Send to agent/})).toBeNull()
  await editSource()
  fireEvent.change(screen.getByLabelText("Source file"),{target:{value:"<h1>Edited by human</h1>"}})
  fireEvent.click(screen.getByRole("button",{name:"Save project"}))
  expect(state.save).toHaveBeenCalledWith({base_version:2,entrypoint:"index.html",files:[{path:"index.html",media_type:"text/html",content:"<h1>Edited by human</h1>"},previous.manifest.files[1]]},expect.anything())
 } finally {state.data=previous}
})
it("keeps the preview and the source draft when history quota is full",async()=>{
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await editSource()
 fireEvent.change(screen.getByLabelText("Source file"),{target:{value:"Work kept at full quota"}})
 fireEvent.click(screen.getByRole("button",{name:"Save project"}))
 await act(async()=>state.save.mock.calls[0][1].onError(Object.assign(Error("Project history quota is full."),{status:413})))
 expect(screen.getByTitle("Artifact preview")).toBeInTheDocument()
 expect(screen.getByLabelText("Source file")).toHaveValue("Work kept at full quota")
 expect(screen.getByRole("alert")).toHaveTextContent("quota is full")
})

it("keeps a conflicting source draft and requires an explicit retry",async()=>{
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await editSource()
 fireEvent.change(screen.getByLabelText("Source file"),{target:{value:"Private source draft"}})
 fireEvent.click(screen.getByRole("button",{name:"Save project"}))
 await act(async()=>state.save.mock.calls[0][1].onError(Object.assign(Error("Changed"),{status:409})))
 expect(screen.getByLabelText("Source file")).toHaveValue("Private source draft")
 expect(state.save).toHaveBeenCalledOnce()
})
it("never carries source drafts across identities or accepts an old save after A to B to A",async()=>{
 const view=render(<ArtifactProject workspaceId="w" slug="demo" />)
 await editSource()
 fireEvent.change(screen.getByLabelText("Source file"),{target:{value:"A private draft"}})
 fireEvent.click(screen.getByRole("button",{name:"Save project"}))
 const success=state.save.mock.calls[0][1].onSuccess
 state.user="B";view.rerender(<ArtifactProject workspaceId="w" slug="demo" />)
 expect(screen.queryByLabelText("Source file")).toBeNull()
 state.user="owner";view.rerender(<ArtifactProject workspaceId="w" slug="demo" />)
 // A's unsaved draft is kept in this browser and offered back, never edited over.
 fireEvent.click(await screen.findByRole("button",{name:"Restore source draft"}))
 fireEvent.change(screen.getByLabelText("Source file"),{target:{value:"New A draft"}})
 await act(async()=>success({version:3}))
 expect(screen.getByLabelText("Source file")).toHaveValue("New A draft")
})
it("keeps the original CAS base when a background refetch finds a newer manifest",async()=>{
 const previous=state.data
 try {
  const view=render(<ArtifactProject workspaceId="w" slug="demo" />)
  await editSource()
  fireEvent.change(screen.getByLabelText("Source file"),{target:{value:"Keep version two draft"}})
  state.data={...state.data,latest_version:3,manifest:{...state.data.manifest,version:3}}
  view.rerender(<ArtifactProject workspaceId="w" slug="demo" />)
  fireEvent.click(screen.getByRole("button",{name:"Save project"}))
  expect(state.save.mock.calls[0][0].base_version).toBe(2)
  expect(state.save.mock.calls[0][0].files[0].content).toBe("Keep version two draft")
 }finally{state.data=previous}
})
it("removes the private iframe after a refused source save",async()=>{
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await editSource()
 fireEvent.change(screen.getByLabelText("Source file"),{target:{value:"Private draft"}})
 fireEvent.click(screen.getByRole("button",{name:"Save project"}))
 await act(async()=>state.save.mock.calls[0][1].onError(Object.assign(Error("Revoked"),{status:403})))
 expect(screen.queryByTitle("Artifact preview")).toBeNull()
 expect(screen.getByLabelText("Source file")).toHaveValue("Private draft")
})

it("offers an unsaved source draft back after the browser was killed, once its strict write committed, and forgets it after save",async()=>{
 hold=true
 const first=render(<ArtifactProject workspaceId="w" slug="demo" />)
 await editSource()
 fireEvent.change(screen.getByLabelText("Source file"),{target:{value:"Typed before the crash"}})
 await waitFor(() => expect(held.length).toBeGreaterThan(0))
 // Nothing is on disk until the durable commit completes; localStorage is not used.
 expect(disk.has(row("owner","artifact-source"))).toBe(false)
 await waitFor(() => { release(); expect(disk.get(row("owner","artifact-source"))).toContain("Typed before the crash") })
 expect(localStorage.setItem).not.toHaveBeenCalled()
 hold=false
 first.unmount()
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 fireEvent.click(await screen.findByRole("button",{name:"Restore source draft"}))
 expect(screen.getByLabelText("Source file")).toHaveValue("Typed before the crash")
 fireEvent.click(screen.getByRole("button",{name:"Save project"}))
 await act(async()=>state.save.mock.calls[0][1].onSuccess({version:3}))
 await waitFor(() => expect(disk.has(row("owner","artifact-source"))).toBe(false))
})

it("offers a source draft kept in localStorage by an older build and moves it into durable storage",async()=>{
 const legacy=JSON.stringify({path:"index.html",body:{base_version:2,entrypoint:"index.html",files:[{path:"index.html",media_type:"text/html",content:"Legacy draft"}]}})
 vi.mocked(localStorage.getItem).mockImplementation(key => key==="artifact-source:owner:w:demo" ? legacy : null)
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 fireEvent.click(await screen.findByRole("button",{name:"Restore source draft"}))
 expect(screen.getByLabelText("Source file")).toHaveValue("Legacy draft")
 await waitFor(() => expect(localStorage.removeItem).toHaveBeenCalledWith("artifact-source:owner:w:demo"))
 expect(JSON.parse(disk.get(row("owner","artifact-source"))!).body.files[0].content).toBe("Legacy draft")
 vi.mocked(localStorage.getItem).mockReset().mockReturnValue(null)
})

const keptSource=(content:string)=>JSON.stringify({path:"index.html",body:{base_version:2,entrypoint:"index.html",files:[{path:"index.html",media_type:"text/html",content}]}})
it("never replaces a crash-recovery source draft when the person starts a fresh edit",async()=>{
 disk.set(row("owner","artifact-source"),keptSource("Recovered after crash"))
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await screen.findByRole("button",{name:"Restore source draft"})
 // Only Restore or Discard are offered while a kept draft waits; Edit source would overwrite it.
 expect(screen.queryByRole("button",{name:"Edit source"})).toBeNull()
 fireEvent.click(screen.getByRole("button",{name:"Discard it"}))
 await editSource()
 expect(screen.getByLabelText("Source file")).toHaveValue("<h1>Hello</h1>")
})
it("does not let Edit source write over a kept draft that is still being read",async()=>{
 disk.set(row("owner","artifact-source"),keptSource("Recovered after crash"))
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 const edit=screen.queryByRole("button",{name:"Edit source"})
 if(edit)fireEvent.click(edit)
 await screen.findByRole("button",{name:"Restore source draft"})
 await act(async()=>{})
 expect(JSON.parse(disk.get(row("owner","artifact-source"))!).body.files[0].content).toBe("Recovered after crash")
})
it("keeps the durable copy of source edits typed after a save was submitted",async()=>{
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await editSource()
 fireEvent.change(screen.getByLabelText("Source file"),{target:{value:"Submitted"}})
 fireEvent.click(screen.getByRole("button",{name:"Save project"}))
 fireEvent.change(screen.getByLabelText("Source file"),{target:{value:"Submitted, then typed more"}})
 await waitFor(()=>expect(disk.get(row("owner","artifact-source"))).toContain("then typed more"))
 await act(async()=>state.save.mock.calls[0][1].onSuccess({version:3}))
 await act(async()=>{})
 expect(screen.getByLabelText("Source file")).toHaveValue("Submitted, then typed more")
 expect(disk.get(row("owner","artifact-source"))).toContain("then typed more")
})
it("never deletes a stored comment draft when reading it fails",async()=>{
 disk.set(row("owner","artifact-comment"),"Unsent private comment")
 setArtifactDraftStore({
  capture: async () => [], removeCaptured: async rows => { for(const row of rows) disk.delete(JSON.stringify(row.row)) },
  get: async () => { throw new Error("IndexedDB open timed out") },
  put: async (key, text) => { disk.set(JSON.stringify(key), text) },
  delete: async key => { disk.delete(JSON.stringify(key)) },
  purge: async () => { disk.clear() },
 })
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await screen.findByText("Browser storage unavailable. Copy your draft before leaving.")
 await act(async()=>{})
 expect(disk.get(row("owner","artifact-comment"))).toBe("Unsent private comment")
})
