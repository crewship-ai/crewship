import * as React from "react"
import {render,screen,fireEvent,waitFor} from "@testing-library/react"
import {describe,it,expect,vi} from "vitest"
import {NativeCraftVersionHistory} from "../native-craft-version-history"
import type {NativeVersionList} from "@/lib/mooring/native-versions"
const list:NativeVersionList={currentVersion:"3",truncated:false,versions:[
 {version:"3",createdAt:"2026-10-31T10:00:00Z",author:{actorId:"user:private-id",kind:"human",displayName:"Pavel"},restoredFrom:"1",current:true},
 {version:"2",createdAt:"2026-10-31T09:00:00Z",author:{actorId:"agent:a",kind:"agent",displayName:"Drafting Agent"},current:false},
 {version:"1",createdAt:"2026-10-31T08:00:00Z",author:{actorId:"user:private-id",kind:"human",displayName:"Pavel"},current:false},
]}
async function opened(props:Partial<React.ComponentProps<typeof NativeCraftVersionHistory>>={}){
 const onRestore=vi.fn()
 render(<NativeCraftVersionHistory load={async()=>list} canRestore={false} onRestore={onRestore} {...props}/>)
 fireEvent.click(await screen.findByRole("button",{name:"Version history (3)"}))
 return onRestore
}
describe("native Craft version history",()=>{
 it("in a drawer tab it is open at once, has no toggle row, and says each entry in plain words",async()=>{
  render(<NativeCraftVersionHistory embedded load={async()=>list} canRestore={false}/>)
  const items=await screen.findAllByRole("listitem")
  expect(screen.queryByRole("button",{name:/^Version history/})).not.toBeInTheDocument()
  expect(items.map(item=>item.querySelector("b")?.textContent)).toEqual(["Pavel restored version 1","Saved by Drafting Agent (agent)","Saved by Pavel"])
  expect(items[0]).toHaveTextContent("v3");expect(items[0]).toHaveTextContent("Latest")
  expect(screen.getAllByText("Latest")).toHaveLength(1)
  expect(screen.getByText("A restore always adds a new version. Nothing is deleted.")).toBeInTheDocument()
  expect(screen.getAllByRole("button",{name:"Restore"})).toHaveLength(2)
  expect(screen.queryByText(/private-id/)).not.toBeInTheDocument()
 })
 it("lists versions newest first with authors and marks only the current one",async()=>{
  await opened()
  const items=screen.getAllByRole("listitem")
  expect(items.map(item=>item.querySelector("b")?.textContent)).toEqual(["Version 3","Version 2","Version 1"])
  expect(items[0]).toHaveAttribute("aria-current","true")
  expect(items[1]).not.toHaveAttribute("aria-current")
  expect(screen.getAllByText("current")).toHaveLength(1)
  expect(items[0]).toHaveTextContent("restored from version 1")
  expect(items[1]).toHaveTextContent("Drafting Agent (agent)")
  expect(screen.queryByText(/private-id/)).not.toBeInTheDocument()
 })
 it("keeps restore disabled for a reader or a non-holder and explains why",async()=>{
  const onRestore=await opened({canRestore:false,restoreHint:"Take over editing to restore a version."})
  expect(screen.getByText("Take over editing to restore a version.")).toBeInTheDocument()
  const buttons=screen.getAllByRole("button",{name:"Restore"})
  expect(buttons).toHaveLength(2)
  for(const button of buttons){expect(button).toBeDisabled();fireEvent.click(button)}
  expect(onRestore).not.toHaveBeenCalled()
 })
 it("lets the holder restore an older version after confirming; the current one has no restore",async()=>{
  const onRestore=await opened({canRestore:true})
  const current=screen.getAllByRole("listitem")[0]
  expect(current.querySelector("button")).toBeNull()
  fireEvent.click(screen.getAllByRole("button",{name:"Restore"})[1])
  expect(screen.getByText(/This adds a new version with the content of version 1\. Nothing is deleted\./)).toBeInTheDocument()
  expect(onRestore).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button",{name:"Restore as a new version"}))
  expect(onRestore).toHaveBeenCalledWith("1")
 })
 it("disables restore while another action is pending",async()=>{
  await opened({canRestore:true,pending:true})
  for(const button of screen.getAllByRole("button",{name:"Restore"}))expect(button).toBeDisabled()
 })
 it("renders nothing while the flag is off",async()=>{
  const load=vi.fn(async()=>null)
  const {container}=render(<NativeCraftVersionHistory load={load} canRestore/>)
  await waitFor(()=>expect(load).toHaveBeenCalled())
  expect(container).toBeEmptyDOMElement()
 })
 it("reloads when the refresh key changes",async()=>{
  const load=vi.fn(async()=>list)
  const {rerender}=render(<NativeCraftVersionHistory load={load} refreshKey="1" canRestore/>)
  await waitFor(()=>expect(load).toHaveBeenCalledTimes(1))
  rerender(<NativeCraftVersionHistory load={load} refreshKey="2" canRestore/>)
  await waitFor(()=>expect(load).toHaveBeenCalledTimes(2))
 })
})

describe("native Craft version preview and comparison",()=>{
 const doc=(version:string,text:string,current=false)=>({version,currentVersion:"3",current,createdAt:"2026-10-31T09:00:00Z",author:{actorId:"agent:a",kind:"agent" as const,displayName:"Drafting Agent"},applicationId:"wordcraft" as const,enginePin:"e".repeat(64),editorBundle:"c".repeat(64),editorURL:"/b/"+"c".repeat(64)+"/index.html",bytes:new TextEncoder().encode(text)})
 const docs:Record<string,string>={"1":'{"title":"Old","body":"a"}',"2":'{"title":"Mid","body":"a"}',"3":'{"title":"New","body":"a"}'}
 const loadVersion=vi.fn(async(version:string)=>doc(version,docs[version],version==="3"))
 function Preview({version,read}:{version:string;read:(signal:AbortSignal)=>Promise<unknown>}){
  const [state,setState]=React.useState("loading")
  React.useEffect(()=>{const a=new AbortController();read(a.signal).then(()=>setState("shown"),()=>setState("refused"));return()=>a.abort()},[read])
  return <p data-testid="preview">preview {version} {state}</p>
 }
 it("previews a chosen version read-only, moves focus to it and returns focus on Escape",async()=>{
  loadVersion.mockClear()
  const onRestore=await opened({loadVersion,Preview,canRestore:true})
  const opener=screen.getByRole("button",{name:"Preview of version 1"})
  opener.focus();fireEvent.click(opener)
  const panel=await screen.findByRole("region",{name:/Preview of version 1 \(read-only\)/})
  expect(screen.getByRole("heading",{name:/Preview of version 1/})).toHaveFocus()
  await waitFor(()=>expect(screen.getByTestId("preview")).toHaveTextContent("preview 1 shown"))
  expect(loadVersion).toHaveBeenCalledWith("1",expect.any(AbortSignal))
  expect(opener).toHaveAttribute("aria-pressed","true")
  fireEvent.keyDown(panel,{key:"Escape"})
  expect(screen.queryByRole("region",{name:/Preview of version/})).not.toBeInTheDocument()
  await waitFor(()=>expect(opener).toHaveFocus())
  expect(onRestore).not.toHaveBeenCalled()
 })
 it("compares an older version with the current one by lines without restoring",async()=>{
  loadVersion.mockClear()
  const onRestore=await opened({loadVersion,Preview,canRestore:true})
  expect(screen.queryByRole("button",{name:"Compare version 3 with the current one"})).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button",{name:"Compare version 1 with the current one"}))
  expect(await screen.findByText(/Compared with version 1, current version 3 has 1 added and 1 removed lines/)).toBeInTheDocument()
  const diff=screen.getByRole("list",{name:"Line differences"})
  expect(diff).toHaveTextContent('removed: "title": "Old",')
  expect(diff).toHaveTextContent('added: "title": "New",')
  expect(screen.getByLabelText("Content of version 1")).toHaveValue('{\n "title": "Old",\n "body": "a"\n}')
  expect(screen.getByLabelText("Content of the current version 3")).toHaveAttribute("readonly")
  expect(loadVersion.mock.calls.map(c=>c[0]).sort()).toEqual(["1","3"])
  fireEvent.click(screen.getByRole("button",{name:"Close"}))
  expect(screen.queryByRole("list",{name:"Line differences"})).not.toBeInTheDocument()
  expect(onRestore).not.toHaveBeenCalled()
 })
 it("reports identical content and a failed version read without leaking anything",async()=>{
  const same=vi.fn(async(version:string)=>doc(version,'{"same":true}',version==="3"))
  await opened({loadVersion:same,Preview,canRestore:false})
  fireEvent.click(screen.getByRole("button",{name:"Compare version 2 with the current one"}))
  expect(await screen.findByText("Version 2 has the same content as the current version 3.")).toBeInTheDocument()
  same.mockRejectedValue(new Error("native_version_unavailable"))
  fireEvent.click(screen.getByRole("button",{name:"Compare version 1 with the current one"}))
  expect(await screen.findByRole("alert")).toHaveTextContent("The version could not be loaded for comparison")
  expect(screen.queryByLabelText(/Content of version/)).not.toBeInTheDocument()
 })
 it("removes an open preview and comparison when a reload fails",async()=>{
  let fail=false
  const load=vi.fn(async()=>{if(fail)throw new Error("revoked");return list})
  const {rerender}=render(<NativeCraftVersionHistory load={load} loadVersion={loadVersion} Preview={Preview} refreshKey="1" canRestore={false}/>)
  fireEvent.click(await screen.findByRole("button",{name:"Version history (3)"}))
  fireEvent.click(screen.getByRole("button",{name:"Compare version 1 with the current one"}))
  await screen.findByLabelText("Content of version 1")
  fail=true
  rerender(<NativeCraftVersionHistory load={load} loadVersion={loadVersion} Preview={Preview} refreshKey="2" canRestore={false}/>)
  await waitFor(()=>expect(screen.queryByLabelText("Content of version 1")).not.toBeInTheDocument())
  expect(screen.queryByText(/Old/)).not.toBeInTheDocument()
  expect(screen.queryByRole("region",{name:/Comparison/})).not.toBeInTheDocument()
 })
 it("offers neither preview nor comparison without a version reader",async()=>{
  await opened({canRestore:true})
  expect(screen.queryByRole("button",{name:/Preview of version/})).not.toBeInTheDocument()
  expect(screen.queryByRole("button",{name:/Compare version/})).not.toBeInTheDocument()
 })
})
