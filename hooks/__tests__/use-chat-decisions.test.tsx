import React from "react"
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import reference from "@/internal/api/testdata/chat-e2/decision-escalation-pending.json"
import { DecisionEntityCard } from "@/components/features/chat/decision-entity-card"
import { useChatDecisions } from "../use-chat-decisions"
const { fetch } = vi.hoisted(() => ({fetch:vi.fn()}))
vi.mock("@/lib/api-fetch", () => ({apiFetch:fetch}))
vi.mock("@/hooks/use-realtime", () => ({useRealtimeEventSafe:vi.fn()}))
vi.mock("@/components/features/chat/issue-entity-card",()=>({EntityCardFrame:({children}:{children:React.ReactNode})=><section>{children}</section>}))
afterEach(() => {cleanup();fetch.mockReset();sessionStorage.clear()})
it("evicts denied sources and never restores their existence during a later outage", async () => {
 const page={contract_version:1,reset:true,next_cursor:"",snapshot_id:"snapshot",snapshot_complete:true,has_more:false,refs:[reference]}
 sessionStorage.setItem(`chat-decision:${JSON.stringify([reference.entity.workspace_id,"viewer","chat",reference.decision.source.type,reference.decision.source.id])}`,JSON.stringify({answer:{values:{answer:"Private draft"}}}))
 fetch.mockResolvedValueOnce(new Response(JSON.stringify(page)))
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}})
 const wrapper=({children}:{children:React.ReactNode})=><QueryClientProvider client={client}>{children}</QueryClientProvider>
 const hook=renderHook(()=>useChatDecisions({workspaceId:reference.entity.workspace_id,userId:"viewer",chatId:"chat",enabled:true}),{wrapper})
 await waitFor(()=>expect(hook.result.current.references).toHaveLength(1))
 fetch.mockResolvedValueOnce(new Response("{}",{status:403}))
 act(()=>hook.result.current.reconcile())
 await waitFor(()=>expect(hook.result.current.unavailable).toBe(true))
 expect(hook.result.current.references).toEqual([])
 expect(sessionStorage.length).toBe(0)
 fetch.mockResolvedValueOnce(new Response("{}",{status:500}))
 act(()=>hook.result.current.reconcile())
 await waitFor(()=>expect(fetch).toHaveBeenCalledTimes(3))
 expect(hook.result.current.references).toEqual([])
})

it("reconciles every page into one replacement snapshot before exposing sources", async()=>{
 const first={contract_version:1,reset:true,next_cursor:"page-two",snapshot_id:"snapshot",snapshot_complete:false,has_more:true,refs:[reference]}
 const second={...first,reset:false,next_cursor:"",snapshot_complete:true,has_more:false,refs:[{...reference,event_id:"decision:source-two",entity:{...reference.entity,id:"source-two"},decision:{...reference.decision,source:{...reference.decision.source,id:"source-two"}}}]}
 fetch.mockResolvedValueOnce(new Response(JSON.stringify(first))).mockResolvedValueOnce(new Response(JSON.stringify(second)))
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}})
 const wrapper=({children}:{children:React.ReactNode})=><QueryClientProvider client={client}>{children}</QueryClientProvider>
 const hook=renderHook(()=>useChatDecisions({workspaceId:reference.entity.workspace_id,userId:"viewer",chatId:"chat",enabled:true}),{wrapper})
 await waitFor(()=>expect(hook.result.current.references).toHaveLength(2))
 expect(fetch.mock.calls[1][0]).toContain("cursor=page-two")
})

it("keeps the mounted draft focused across an actual source recheck and 500",async()=>{
 const page={contract_version:1,reset:true,next_cursor:"",snapshot_id:"snapshot",snapshot_complete:true,has_more:false,refs:[reference]}
 fetch.mockResolvedValueOnce(new Response(JSON.stringify(page)))
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}})
 function Mounted(){
  const state=useChatDecisions({workspaceId:reference.entity.workspace_id,userId:"viewer",chatId:"chat",enabled:true})
  return state.references.map(ref=><DecisionEntityCard key={ref.decision.source.id} reference={ref} rechecking={state.rechecking} onAnswer={vi.fn()} onRecheck={state.reconcile}/> )
 }
 render(<QueryClientProvider client={client}><Mounted/></QueryClientProvider>)
 const input=await screen.findByRole("textbox") as HTMLTextAreaElement
 await waitFor(()=>expect(screen.getByRole("button",{name:"Send answer"})).not.toBeDisabled())
 fireEvent.change(input,{target:{value:"Tomorrow morning"}})
 input.focus();input.setSelectionRange(3,8)
 let finish!:(response:Response)=>void
 fetch.mockImplementationOnce(()=>new Promise<Response>(resolve=>{finish=resolve}))
 act(()=>{void client.invalidateQueries()})
 await waitFor(()=>expect(screen.getByRole("button",{name:"Send answer"})).toBeDisabled())
 expect(input).toHaveFocus();expect(input).not.toBeDisabled()
 await act(async()=>finish(new Response("{}",{status:500})))
 await waitFor(()=>expect(fetch).toHaveBeenCalledTimes(2))
 expect(input).toHaveFocus();expect(input).toHaveValue("Tomorrow morning")
 expect([input.selectionStart,input.selectionEnd]).toEqual([3,8])
 expect(screen.getByRole("button",{name:"Send answer"})).toBeDisabled()
})

it("immediately evicts only a denied source and its saved reply without waiting for another GET",async()=>{
 const second={...reference,event_id:"decision:second",entity:{...reference.entity,id:"second"},decision:{...reference.decision,source:{...reference.decision.source,id:"second"}}}
 const page={contract_version:1,reset:true,next_cursor:"",snapshot_id:"snapshot",snapshot_complete:true,has_more:false,refs:[reference,second]}
 fetch.mockResolvedValueOnce(new Response(JSON.stringify(page)))
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}})
 const wrapper=({children}:{children:React.ReactNode})=><QueryClientProvider client={client}>{children}</QueryClientProvider>
 const hook=renderHook(()=>useChatDecisions({workspaceId:reference.entity.workspace_id,userId:"viewer",chatId:"chat",enabled:true}),{wrapper})
 await waitFor(()=>expect(hook.result.current.references).toHaveLength(2))
 const sourceKey=(id:string)=>`chat-decision:${JSON.stringify([reference.entity.workspace_id,"viewer","chat",reference.decision.source.type,id])}`
 sessionStorage.setItem(sourceKey(reference.decision.source.id),"private denied reply")
 sessionStorage.setItem(sourceKey("second"),"other uncertain reply")
 act(()=>hook.result.current.evictSource(reference.decision.source.type,reference.decision.source.id))
 await waitFor(()=>expect(hook.result.current.references.map(ref=>ref.decision.source.id)).toEqual(["second"]))
 expect(sessionStorage.getItem(sourceKey(reference.decision.source.id))).toBeNull()
 expect(sessionStorage.getItem(sourceKey("second"))).toBe("other uncertain reply")
 expect(fetch).toHaveBeenCalledTimes(1)
})

it("ignores an old viewer denial arriving after a new viewer has verified access",async()=>{
 let finishOld!:(response:Response)=>void
 fetch.mockImplementationOnce(()=>new Promise<Response>(resolve=>{finishOld=resolve}))
 const page={contract_version:1,reset:true,next_cursor:"",snapshot_id:"snapshot",snapshot_complete:true,has_more:false,refs:[reference]}
 fetch.mockResolvedValueOnce(new Response(JSON.stringify(page)))
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}})
 const wrapper=({children}:{children:React.ReactNode})=><QueryClientProvider client={client}>{children}</QueryClientProvider>
 const hook=renderHook(({viewer})=>useChatDecisions({workspaceId:reference.entity.workspace_id,userId:viewer,chatId:"chat",enabled:true}),{wrapper,initialProps:{viewer:"old"}})
 await waitFor(()=>expect(fetch).toHaveBeenCalledTimes(1))
 hook.rerender({viewer:"new"})
 await waitFor(()=>expect(hook.result.current.references).toHaveLength(1))
 const recoveryKey=`chat-decision:${JSON.stringify([reference.entity.workspace_id,"new","chat",reference.decision.source.type,reference.decision.source.id])}`
 sessionStorage.setItem(recoveryKey,"new viewer private draft")
 await act(async()=>finishOld(new Response("{}",{status:401})))
 expect(hook.result.current.unavailable).toBe(false)
 expect(hook.result.current.references).toHaveLength(1)
 expect(sessionStorage.getItem(recoveryKey)).toBe("new viewer private draft")
})

it("rejects a foreign workspace reference without publishing any page",async()=>{
 const foreign={...reference,entity:{...reference.entity,workspace_id:"foreign"}}
 fetch.mockResolvedValueOnce(new Response(JSON.stringify({contract_version:1,reset:true,next_cursor:"",snapshot_id:"snapshot",snapshot_complete:true,has_more:false,refs:[reference,foreign]})))
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}})
 const wrapper=({children}:{children:React.ReactNode})=><QueryClientProvider client={client}>{children}</QueryClientProvider>
 const hook=renderHook(()=>useChatDecisions({workspaceId:reference.entity.workspace_id,userId:"viewer",chatId:"chat",enabled:true}),{wrapper})
 await waitFor(()=>expect(client.getQueryState(["chat-decisions",reference.entity.workspace_id,"viewer","chat"])?.status).toBe("error"))
 expect(hook.result.current.references).toEqual([])
 expect(hook.result.current.rechecking).toBe(true)
})
