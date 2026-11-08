import {render,screen,fireEvent,act,waitFor} from "@testing-library/react"
import {describe,it,expect,vi,beforeEach} from "vitest"
import type {NativeVersionList} from "@/lib/mooring/native-versions"

const m=vi.hoisted(()=>({apiFetch:vi.fn(),client:{open:vi.fn(),versions:vi.fn(),version:vi.fn(),close:vi.fn()},privacy:vi.fn(),retire:undefined as undefined|(()=>Promise<void>),handlers:new Map<string,Array<(event:{type:string;payload:Record<string,unknown>})=>void>>()}))
vi.mock("@/lib/api-fetch",()=>({apiFetch:m.apiFetch}))
vi.mock("@/lib/mooring/native-document-client",()=>({createNativeDocumentClient:()=>m.client}))
vi.mock("@/lib/document-queue",()=>({registerDocumentDraftWriter:m.privacy}))
vi.mock("@/hooks/use-realtime",()=>({useRealtimeEventSafe:(type:string,cb:(event:{type:string;payload:Record<string,unknown>})=>void)=>{const React=require("react");React.useEffect(()=>{const list=m.handlers.get(type)??[];list.push(cb);m.handlers.set(type,list);return()=>{m.handlers.set(type,(m.handlers.get(type)??[]).filter(h=>h!==cb))}})}}))
import {NativeCraftReaderHistory} from "../native-craft-reader-history"

const list:NativeVersionList={currentVersion:"2",truncated:false,versions:[
 {version:"2",createdAt:"2026-10-31T10:00:00Z",author:{actorId:"user:u",kind:"human",displayName:"Private Pavel"},current:true},
 {version:"1",createdAt:"2026-10-31T09:00:00Z",author:{actorId:"agent:a",kind:"agent",displayName:"Secret Agent"},current:false},
]}
const emit=(type:string,payload:Record<string,unknown>={})=>act(()=>{for(const h of m.handlers.get(type)??[])h({type,payload})})
beforeEach(()=>{
 vi.clearAllMocks();m.handlers.clear();m.retire=undefined
 m.apiFetch.mockImplementation(async()=>new Response(JSON.stringify([{key:"craft_version_history",enabled:true}])))
 m.client.open.mockResolvedValue({sessionId:"s",applicationId:"wordcraft"})
 m.client.versions.mockResolvedValue(list)
 m.privacy.mockImplementation((_human:string,retire:()=>Promise<void>)=>{m.retire=retire;return vi.fn()})
})
async function shown(){
 render(<NativeCraftReaderHistory humanId="reader" workspaceId="ws" pageId="page"/>)
 fireEvent.click(await screen.findByRole("button",{name:"Version history (2)"}))
 expect(screen.getByText(/Private Pavel/)).toBeInTheDocument()
 expect(screen.getByText(/Secret Agent/)).toBeInTheDocument()
}
function wiped(){
 expect(screen.queryByText(/Private Pavel/)).not.toBeInTheDocument()
 expect(screen.queryByText(/Secret Agent/)).not.toBeInTheDocument()
 expect(screen.queryByText(/Version 1/)).not.toBeInTheDocument()
 expect(screen.queryByRole("region",{name:"Version history"})).not.toBeInTheDocument()
}
describe("reader version history access retirement",()=>{
 it("wipes the list and names when the person's read access is retired",async()=>{
  await shown()
  expect(m.privacy).toHaveBeenCalledWith("reader",expect.any(Function))
  await act(async()=>{await m.retire!()})
  wiped()
  expect(m.client.close).toHaveBeenCalled()
 })
 it("wipes the list when an access recheck finds Read revoked",async()=>{
  await shown()
  m.client.versions.mockRejectedValue(new Error("native_versions_unavailable"))
  emit("page.access_recheck",{page_id:"page"})
  await waitFor(wiped)
 })
 it("ignores a recheck for another page and keeps polling the authorized read",async()=>{
  await shown()
  const calls=m.client.versions.mock.calls.length
  emit("page.access_recheck",{page_id:"other"})
  expect(m.client.versions).toHaveBeenCalledTimes(calls)
  emit("realtime.reconnected")
  await waitFor(()=>expect(m.client.versions).toHaveBeenCalledTimes(calls+1))
  expect(screen.getByText(/Private Pavel/)).toBeInTheDocument()
 })
 it("retires everything when a version read for comparison is refused",async()=>{
  await shown()
  m.client.version.mockRejectedValue(new Error("native_version_unavailable"))
  fireEvent.click(screen.getByRole("button",{name:"Compare version 1 with the current one"}))
  await waitFor(wiped)
  expect(m.client.close).toHaveBeenCalled()
 })
 it("compares a version under the same read-only admission",async()=>{
  const bytes=(text:string)=>new TextEncoder().encode(text)
  m.client.version.mockImplementation(async(version:string)=>({version,currentVersion:"2",current:version==="2",createdAt:"x",author:{actorId:"u",kind:"human",displayName:"P"},applicationId:"wordcraft",enginePin:"e".repeat(64),editorBundle:"c".repeat(64),editorURL:"/b/"+"c".repeat(64)+"/index.html",bytes:bytes(version==="1"?'{"t":"old"}':'{"t":"new"}')}))
  await shown()
  fireEvent.click(screen.getByRole("button",{name:"Compare version 1 with the current one"}))
  expect(await screen.findByLabelText("Content of version 1")).toHaveValue('{\n "t": "old"\n}')
  expect(m.client.open).toHaveBeenCalledTimes(1)
  expect(screen.queryByRole("button",{name:/Obnovit jako/})).not.toBeInTheDocument()
 })
})
