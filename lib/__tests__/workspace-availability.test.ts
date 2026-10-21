import {beforeEach,afterEach,describe,it,expect,vi} from "vitest"
import {assertWorkspaceRequest,captureWorkspaceRequest,setWorkspaceRequestAvailability,setWorkspaceRequestSession,workspaceFetch,WorkspaceUnavailableError} from "../workspace-availability"
const network=vi.fn()
beforeEach(()=>{setWorkspaceRequestSession(null);setWorkspaceRequestSession("actor-a");setWorkspaceRequestAvailability("ws-a",true);network.mockReset();vi.stubGlobal("fetch",network)})
afterEach(()=>{setWorkspaceRequestSession(null);setWorkspaceRequestAvailability(null,true);vi.unstubAllGlobals()})
describe("workspace availability transport",()=>{
 it("blocks domain reads/writes but keeps exact self and directory methods available",async()=>{
  setWorkspaceRequestAvailability("ws-a",false)
  network.mockResolvedValue(new Response("[]"))
  await workspaceFetch("/api/v1/workspaces")
  await workspaceFetch("/api/auth/signout",{method:"POST"})
  await workspaceFetch("/api/v1/auth/sessions")
  for(const [path,method] of [["/api/v1/agents?workspace_id=ws-a","GET"],["/api/v1/pages?workspace_id=ws-a","POST"],["/api/v1/workspaces","POST"],["/api/v1/workspaces/ws-a","PATCH"],["/api/v1/auth/sessions/x/revoke/extra","POST"]]){
   await expect(workspaceFetch(path,{method})).rejects.toBeInstanceOf(WorkspaceUnavailableError)
  }
  expect(network).toHaveBeenCalledTimes(3)
 })
 it("keeps account and instance administration available during a directory outage",async()=>{
  setWorkspaceRequestAvailability("ws-a",false)
  network.mockResolvedValue(new Response("{}"))
  for (const path of ["/api/v1/admin/stats", "/api/v1/admin/instance/people", "/api/v1/users/me/preferences", "/api/v1/users/me/notifications"]) {
   await workspaceFetch(path)
  }
  await expect(workspaceFetch("/api/v1/admin/keeper/requests?workspace_id=ws-a")).rejects.toBeInstanceOf(WorkspaceUnavailableError)
  expect(network).toHaveBeenCalledTimes(4)
 })
 it("aborts in-flight domain requests and rejects late replies",async()=>{
  let resolve!: (response:Response)=>void
  network.mockImplementation(()=>new Promise<Response>(done=>{resolve=done}))
  const request=workspaceFetch("/api/v1/pages?workspace_id=ws-a")
  const signal=network.mock.calls[0][1].signal as AbortSignal
  setWorkspaceRequestAvailability("ws-a",false)
  expect(signal.aborted).toBe(true)
  resolve(new Response("[]"))
  await expect(request).rejects.toBeInstanceOf(WorkspaceUnavailableError)
 })
 it("rejects a body completed after the context changed",async()=>{
  let resolve!: (value:unknown)=>void
  network.mockResolvedValue({json:()=>new Promise(done=>{resolve=done})})
  const response=await workspaceFetch("/api/v1/pages?workspace_id=ws-a")
  const body=response.json()
  setWorkspaceRequestAvailability("ws-b",true)
  resolve({oldWorkspace:"ws-a"})
  await expect(body).rejects.toBeInstanceOf(WorkspaceUnavailableError)
 })
 it("fences cloned response bodies",async()=>{
  network.mockResolvedValue(new Response('{"workspace":"a"}'))
  const clone=(await workspaceFetch("/api/v1/pages")).clone()
  setWorkspaceRequestAvailability("ws-a",false)
  await expect(clone.json()).rejects.toBeInstanceOf(WorkspaceUnavailableError)
 })
 it("aborts and fences an active body stream without delivering late data",async()=>{
  let source!:ReadableStreamDefaultController<Uint8Array>
  network.mockResolvedValue(new Response(new ReadableStream({start(controller){source=controller}})))
  const response=await workspaceFetch("/api/v1/stream")
  const reader=response.body!.getReader()
  const read=reader.read()
  const signal=network.mock.calls[0][1].signal as AbortSignal
  setWorkspaceRequestAvailability("ws-a",false)
  expect(signal.aborted).toBe(true)
  source.enqueue(new TextEncoder().encode("late private frame"))
  await expect(read).rejects.toBeInstanceOf(WorkspaceUnavailableError)
 })
 it("keeps an already completed stream closed",async()=>{
  network.mockResolvedValue(new Response(new ReadableStream({start(controller){controller.close()}})))
  const reader=(await workspaceFetch("/api/v1/stream")).body!.getReader()
  expect((await reader.read()).done).toBe(true)
  setWorkspaceRequestAvailability("ws-a",false)
  expect((await reader.read()).done).toBe(true)
 })
 it("fences captured retries even after same workspace recovers or identity changes",()=>{
  const original=captureWorkspaceRequest("/api/v1/pages?workspace_id=ws-a",{method:"POST"})
  setWorkspaceRequestAvailability("ws-a",false);setWorkspaceRequestAvailability("ws-a",true)
  expect(()=>assertWorkspaceRequest(original)).toThrow(WorkspaceUnavailableError)
  const recovered=captureWorkspaceRequest("/api/v1/pages")
  setWorkspaceRequestSession("actor-b")
  expect(()=>assertWorkspaceRequest(recovered)).toThrow(WorkspaceUnavailableError)
 })
})
