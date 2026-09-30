import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
const { send, request } = vi.hoisted(() => ({send:vi.fn(),request:vi.fn()}))
vi.mock("@/hooks/use-websocket",()=>({useWebSocket:()=>({status:"connected",send,disconnect:vi.fn(),reconnect:vi.fn()})}))
vi.mock("@/lib/api-fetch",()=>({apiFetch:request}))
import { useChat } from "@/hooks/use-chat"

beforeEach(()=>{vi.clearAllMocks();vi.stubGlobal("requestAnimationFrame",()=>1);vi.stubGlobal("cancelAnimationFrame",()=>{})})
describe("restricted chat transport",()=>{
 it("uses authenticated HTTP stream and finishes a classified text reply",async()=>{
  let index=0
  const chunks=[new TextEncoder().encode('data: {"type":"text","text":"private answer"}\n\ndata: {"type":"done","text":""}\n\n')]
  request.mockResolvedValue({ok:true,body:{getReader:()=>({read:async()=>index<chunks.length?{done:false,value:chunks[index++]}:{done:true}})}})
  const {result}=renderHook(()=>useChat({wsUrl:"ws://test",getToken:async()=>"token",sessionId:"private-chat",workspaceId:"workspace",executionProfile:"restricted"}))
  act(()=>{expect(result.current.sendMessage("private question")).toBe(true)})
  await waitFor(()=>expect(result.current.isStreaming).toBe(false))
  expect(request).toHaveBeenCalledWith(expect.stringContaining("/restricted-run?workspace_id=workspace"),expect.objectContaining({method:"POST"}))
  expect(send.mock.calls.some(call=>call[0]?.type==="send_message")).toBe(false)
  expect(result.current.turns.some(turn=>turn.parts.some(part=>part.content==="private answer"))).toBe(true)
 })
 it("drops queued text after local cancellation",async()=>{
  let deliver!: (chunk:{done:boolean;value:Uint8Array}) => void
  const pending = new Promise<{done:boolean;value:Uint8Array}>(resolve=>{deliver=resolve})
  request.mockResolvedValue({ok:true,body:{getReader:()=>({read:()=>pending})}})
  const {result}=renderHook(()=>useChat({wsUrl:"ws://test",getToken:async()=>"token",sessionId:"private",workspaceId:"workspace",executionProfile:"restricted"}))
  act(()=>{result.current.sendMessage("question")})
  await waitFor(()=>expect(request).toHaveBeenCalled())
  act(()=>{result.current.stopGeneration()})
  await act(async()=>{deliver({done:false,value:new TextEncoder().encode('data: {"type":"text","text":"late private text"}\n\n')});await Promise.resolve()})
  expect(result.current.isStreaming).toBe(false)
  expect(result.current.turns.some(turn=>turn.parts.some(part=>part.content.includes("late private text")))).toBe(false)
 })
 it("refuses unclassified Pages metadata and a pending profile",()=>{
  const {result,rerender}=renderHook(({profile})=>useChat({wsUrl:"ws://test",getToken:async()=>"token",sessionId:"private",workspaceId:"workspace",executionProfile:profile}),{initialProps:{profile:"restricted" as "restricted"|"pending"}})
  act(()=>{expect(result.current.sendMessage("question",{page_context:{slug:"foreign"}})).toBe(false)})
  rerender({profile:"pending"})
  act(()=>{expect(result.current.sendMessage("question")).toBe(false)})
  expect(request).not.toHaveBeenCalled()
 })
 it("forwards only explicit selected version IDs to the authenticated native route",async()=>{
  let delivered=false
  request.mockResolvedValue({ok:true,body:{getReader:()=>({read:async()=>delivered?{done:true}:{done:false,value:(delivered=true,new TextEncoder().encode('data: {"type":"done","text":""}\n\n'))}})}})
  const {result}=renderHook(()=>useChat({wsUrl:"ws://test",getToken:async()=>"token",sessionId:"native-chat",workspaceId:"workspace",executionProfile:"restricted"}))
  const ids=["selected-version"]
  act(()=>{expect(result.current.sendMessage("Read this brief",{project_file_versions:ids})).toBe(true)})
  ids.push("later-foreign-version")
  await waitFor(()=>expect(result.current.isStreaming).toBe(false))
  const body=JSON.parse(request.mock.calls[0][1].body)
  expect(body).toEqual({content:"Read this brief",project_file_versions:["selected-version"]})
  expect(send.mock.calls.some(call=>call[0]?.type==="send_message")).toBe(false)
 })
 it("refuses arbitrary metadata, duplicate, path and oversized selections before dispatch",()=>{
  const {result}=renderHook(()=>useChat({wsUrl:"ws://test",getToken:async()=>"token",sessionId:"native-chat",workspaceId:"workspace",executionProfile:"restricted"}))
  for(const metadata of [
   {project_file_versions:["selected"],host_path:"/srv/crew"},
   {project_file_versions:["selected","selected"]},
   {project_file_versions:["../foreign"]},
   {project_file_versions:Array.from({length:17},(_,i)=>`version${i}`)},
   {project_file_versions:"selected"},
  ]) act(()=>{expect(result.current.sendMessage("question",metadata)).toBe(false)})
  expect(request).not.toHaveBeenCalled()
  expect(result.current.turns).toHaveLength(0)
 })

})
