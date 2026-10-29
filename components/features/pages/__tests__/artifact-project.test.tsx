import React from "react"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
const state = vi.hoisted(() => ({ user: "owner", token: "", comment: vi.fn(), dispatch: vi.fn(), data: { can_write: true, can_comment: false, latest_version: 2, shared_version: 1, manifest: { version: 2, entrypoint: "index.html", files: [{ path: "index.html", content: "<h1>Hello</h1>", media_type: "text/html" }, { path: "details.html", content: "<h1>Details</h1>", media_type: "text/html" }] }, versions: [{ version: 2 }, { version: 1 }], quota: { used_bytes: 10, limit_bytes: 100, warning: false }, comments: [] }, restore: vi.fn(), share: vi.fn() }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ dispatch: state.dispatch, ready: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: state.user } } }) }))
vi.mock("@/hooks/use-artifact-project", () => ({ useArtifactProject: () => ({ query: { data: state.data }, restore: { mutate: state.restore }, share: { mutate: state.share }, comment: { mutate: state.comment } }) }))
vi.mock("@/lib/pages/artifact-project-preview", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/pages/artifact-project-preview")>(), artifactProjectDocument: vi.fn((_files: unknown, _path: string, token: string) => { state.token = token; return "safe document" }) }))
vi.mock("@/components/features/chat/artifact-context/viewer/artifact-slides-preview", () => ({ ArtifactSlidesPreview: ({selectMode,onSelect}:{selectMode:boolean;onSelect:(value:{selector:string;quote:string})=>void}) => <div>Typed Page Slides<button disabled={!selectMode} onClick={() => onSelect({selector:"#typed",quote:"Typed heading"})}>Pick typed element</button></div> }))
vi.mock("@/components/features/chat/artifact-context/viewer/artifact-dashboard-preview", () => ({ ArtifactDashboardPreview: ({selectMode,onSelect}:{selectMode:boolean;onSelect:(value:{selector:string;quote:string})=>void}) => <button disabled={!selectMode} onClick={() => onSelect({selector:"#typed",quote:"Typed heading"})}>Pick typed element</button> }))
import { artifactProjectDocument } from "@/lib/pages/artifact-project-preview"
import { ArtifactProject } from "../artifact-project"
afterEach(() => { cleanup(); vi.clearAllMocks(); state.data.can_write = true; state.user = "owner"; localStorage.clear() })
it("switches screens, restores with the latest base and shares only the selected version", () => {
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 fireEvent.click(screen.getByRole("button", { name: "details.html" }))
 expect(screen.getByTitle("Artifact preview")).toHaveAttribute("data-screen", "details.html")
 fireEvent.change(screen.getByLabelText("Version"), { target: { value: "1" } })
 fireEvent.click(screen.getByRole("button", { name: "Restore version 1" }))
 expect(state.restore).toHaveBeenCalledWith({ version: 1, base_version: 2 }, expect.objectContaining({ onSuccess: expect.any(Function) }))
 fireEvent.click(screen.getByRole("button", { name: "Share version 1" }))
 expect(state.share).toHaveBeenCalledWith({ version: 1, base_version: 2 })
})
it("keeps all write controls unavailable to a reader", () => {
 state.data.can_write = false
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 expect(screen.queryByRole("button", { name: /Restore version/ })).toBeNull()
 expect(screen.queryByRole("button", { name: /Share version/ })).toBeNull()
 expect(screen.queryByRole("button", { name: "Send to agent" })).toBeNull()
 expect(screen.getByText("Read only")).toBeTruthy()
 expect(screen.queryByRole("button", {name:"Select an element"})).toBeNull()
 expect(screen.queryByRole("textbox")).toBeNull()
})

it("never moves an unsent draft into another user browser scope", () => {
 const storage = new Map<string,string>()
 vi.mocked(localStorage.getItem).mockImplementation(key => storage.get(key) ?? null)
 vi.mocked(localStorage.setItem).mockImplementation((key,value) => { storage.set(key,value) })
 vi.mocked(localStorage.removeItem).mockImplementation(key => { storage.delete(key) })
 const view=render(<ArtifactProject workspaceId="w" slug="demo" />)
 fireEvent.change(screen.getByLabelText("Comment for agent"),{target:{value:"Owner private draft"}})
 expect(localStorage.getItem("artifact-comment:owner:w:demo")).toBe("Owner private draft")
 state.user="reader"
 view.rerender(<ArtifactProject workspaceId="w" slug="demo" />)
 expect(screen.getByLabelText("Comment for agent")).toHaveValue("")
 expect(localStorage.getItem("artifact-comment:reader:w:demo")).toBeNull()
})

it("retains 500 Unicode codepoints in a normalized selected quote sent to the agent", () => {
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 fireEvent.click(screen.getByRole("button", { name: "Select an element" }))
 const frame = screen.getByTitle("Artifact preview") as HTMLIFrameElement
 const quote = "  \ufeffŽluťoučký\u0085  kůň\u00a0pěl\nďábelské\tódy  " + "🙂".repeat(520)
 fireEvent(window, new MessageEvent("message", { origin: "null", source: frame.contentWindow, data: { token: state.token, selector: "#artifact-unicode-quote", quote } }))
 fireEvent.change(screen.getByLabelText("Comment for agent"), { target: { value: "Uprav nadpis" } })
 fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
 const expected = Array.from("\ufeffŽluťoučký kůň pěl ďábelské ódy " + "🙂".repeat(520)).slice(0,500).join("")
 expect(state.comment.mock.calls[0][0].quote).toBe(expected)
 expect(state.comment.mock.calls[0][0].send_to_agent).toBe(true)
})

it("keeps the Page Project preview when the Dashboard flag is off and dashboard.json is only an asset", () => {
 vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", "false")
 state.data.manifest.files.push({ path: "dashboard.json", media_type: "application/json", content: '{"schema_version":1,"collections":["sales"]}' })
 try {
  render(<ArtifactProject workspaceId="w" slug="demo" />)
  expect(screen.getByTitle("Artifact preview")).toHaveAttribute("srcdoc", "safe document")
  expect(screen.queryByText("Dashboard preview is unavailable.")).toBeNull()
 } finally { state.data.manifest.files.pop(); vi.unstubAllEnvs() }
})

it.each([["true", "slides", "typed"], ["true", undefined, "unsupported"], ["false", "slides", "legacy"]] as const)("keeps the Pages Slides route safe with flag %s", (flag, kind, mode) => {
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
  fireEvent.change(screen.getByLabelText("Comment"), {target:{value:"Shared feedback"}})
  fireEvent.click(screen.getByRole("button", {name:"Post comment"}))
  expect(state.comment).toHaveBeenCalledWith({version:1,path:"index.html",selector:"#shared",quote:"Shared heading",text:"Shared feedback",send_to_agent:false},expect.anything())
  expect(screen.queryByRole("button",{name:/Restore version|Share version|Send to agent/})).toBeNull()
  await act(async () => { await state.comment.mock.calls[0][1].onSuccess({id:"comment",version:1}) })
  expect(state.dispatch).not.toHaveBeenCalled()
 } finally { state.data = previous }
})
it("rejects commenter selection outside the shared pin", () => {
 const previous = state.data
 try {
  state.data = {...state.data, can_write:false, can_comment:true}
  render(<ArtifactProject workspaceId="w" slug="demo" />)
  expect(screen.queryByRole("button",{name:"Select an element"})).toBeNull()
  expect(screen.queryByRole("textbox")).toBeNull()
 } finally { state.data = previous }
})
it.each(["revoke","user","workspace","slug"])("retires selected comment anchors on %s while preserving the scoped draft", change => {
 const previous = state.data
 try {
  sharedCommenter()
  const view=render(<ArtifactProject workspaceId="w" slug="demo" />)
  selectAnchor()
  fireEvent.change(screen.getByLabelText("Comment"),{target:{value:"Keep this feedback"}})
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

it.each(["slides","dashboard"])("allows shared Comment selection in the typed %s preview", kind => {
 const previous=state.data
 vi.stubEnv(kind==="slides"?"NEXT_PUBLIC_ARTIFACT_SLIDES":"NEXT_PUBLIC_ARTIFACT_DASHBOARDS","true")
 try {
  sharedCommenter()
  state.data=Object.assign({},state.data,{artifact_kind:kind==="slides"?"slides":undefined,manifest:{...state.data.manifest,files:[...state.data.manifest.files,{path:kind==="slides"?"deck.json":"dashboard.json",media_type:"application/json",content:"{}"}]}})
  render(<ArtifactProject workspaceId="w" slug="demo" />)
  expect(screen.getByRole("button",{name:"Pick typed element"})).toBeDisabled()
  fireEvent.click(screen.getByRole("button",{name:"Select an element"}))
  fireEvent.click(screen.getByRole("button",{name:"Pick typed element"}))
  fireEvent.change(screen.getByLabelText("Comment"),{target:{value:"Typed feedback"}})
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
  fireEvent.change(screen.getByLabelText("Comment"),{target:{value:"Submitted feedback"}})
  fireEvent.click(screen.getByRole("button",{name:"Post comment"}))
  const success=state.comment.mock.calls[0][1].onSuccess
  if(change==="user")state.user="new-user"
  if(change==="revoke") {
   state.data={...state.data,can_comment:false}
   view.rerender(<ArtifactProject workspaceId="w" slug="demo" />)
   state.data={...state.data,can_comment:true}
  }
  view.rerender(<ArtifactProject workspaceId={change==="workspace"?"next":"w"} slug={change==="slug"?"next":"demo"} />)
  fireEvent.change(screen.getByLabelText("Comment"),{target:{value:"Newer feedback"}})
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
  fireEvent.change(screen.getByLabelText("Comment for agent"),{target:{value:"Owner feedback"}})
  fireEvent.click(screen.getByRole("button",{name:"Send to agent"}))
  const success=state.comment.mock.calls[0][1].onSuccess
  sharedCommenter()
  view.rerender(<ArtifactProject workspaceId="w" slug="demo" />)
  fireEvent.change(screen.getByLabelText("Comment"),{target:{value:"Commenter feedback"}})
  await act(async()=>{await success({id:"owner-old",version:2})})
  expect(state.dispatch).not.toHaveBeenCalled()
  expect(screen.getByLabelText("Comment")).toHaveValue("Commenter feedback")
 }finally{state.data=previous}
})
