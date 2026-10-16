import React from "react"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import reference from "@/internal/api/testdata/chat-e2/decision-escalation-pending.json"
import { useChatDecisions } from "../use-chat-decisions"
const { fetch } = vi.hoisted(() => ({fetch:vi.fn()}))
vi.mock("@/lib/api-fetch", () => ({apiFetch:fetch}))
vi.mock("@/hooks/use-realtime", () => ({useRealtimeEventSafe:vi.fn()}))
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
