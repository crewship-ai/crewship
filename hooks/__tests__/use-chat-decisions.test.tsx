import React from "react"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import reference from "@/internal/api/testdata/chat-e2/decision-escalation-pending.json"
import { useChatDecisions } from "../use-chat-decisions"
const { fetch } = vi.hoisted(() => ({fetch:vi.fn()}))
vi.mock("@/lib/api-fetch", () => ({apiFetch:fetch}))
vi.mock("@/hooks/use-realtime", () => ({useRealtimeEventSafe:vi.fn()}))
afterEach(() => {cleanup();fetch.mockReset()})
it("evicts denied sources and never restores their existence during a later outage", async () => {
 const page={contract_version:1,reset:true,next_cursor:"",snapshot_id:"snapshot",snapshot_complete:true,has_more:false,refs:[reference]}
 fetch.mockResolvedValueOnce(new Response(JSON.stringify(page)))
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}})
 const wrapper=({children}:{children:React.ReactNode})=><QueryClientProvider client={client}>{children}</QueryClientProvider>
 const hook=renderHook(()=>useChatDecisions({workspaceId:reference.entity.workspace_id,userId:"viewer",chatId:"chat",enabled:true}),{wrapper})
 await waitFor(()=>expect(hook.result.current.references).toHaveLength(1))
 fetch.mockResolvedValueOnce(new Response("{}",{status:403}))
 act(()=>hook.result.current.reconcile())
 await waitFor(()=>expect(hook.result.current.unavailable).toBe(true))
 expect(hook.result.current.references).toEqual([])
 fetch.mockResolvedValueOnce(new Response("{}",{status:500}))
 act(()=>hook.result.current.reconcile())
 await waitFor(()=>expect(fetch).toHaveBeenCalledTimes(3))
 expect(hook.result.current.references).toEqual([])
})
