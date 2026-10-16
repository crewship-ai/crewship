import React from "react"
import { act, cleanup, renderHook } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import fixture from "@/internal/api/testdata/chat-e2/decision-escalation-pending.json"
import answered from "@/internal/api/testdata/chat-e2/decision-escalation-answered.json"
import { DecisionRefSchema } from "@/lib/chat-e2-cards"
import { useChatDecisionAnswer } from "../use-chat-decision-answer"
const { fetch } = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))
const reference = DecisionRefSchema.parse(fixture)
const body = { source_id: reference.entity.id, expected_version: "1", action: "answer" as const, values: { answer: "Tomorrow" } }
function setup() {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
 const wrapper = ({ children }: {children: React.ReactNode}) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
 return renderHook(() => useChatDecisionAnswer({ workspaceId: reference.entity.workspace_id, userId: "viewer", chatId: "chat", reference, onChanged: vi.fn(), onUnauthorized: vi.fn() }), { wrapper })
}
afterEach(() => { cleanup(); sessionStorage.clear(); fetch.mockReset(); vi.restoreAllMocks() })
it("restores and explicitly retries the exact lost response after remount", async () => {
 fetch.mockRejectedValueOnce(new Error("lost"))
 const first = setup()
 await act(async () => expect(await first.result.current.answer(body,"stable-key")).toBe("unknown"))
 first.unmount()
 const next = setup()
 expect(next.result.current.recovery).toMatchObject({ answer: body, key: "stable-key" })
 expect(fetch).toHaveBeenCalledTimes(1)
 fetch.mockResolvedValueOnce(new Response(JSON.stringify(answered), {status:200}))
 await act(async () => expect(await next.result.current.answer(next.result.current.recovery!.answer,next.result.current.recovery!.key)).toBe("saved"))
 expect(fetch.mock.calls[1]).toEqual(fetch.mock.calls[0])
 expect(sessionStorage.length).toBe(0)
})
it("keeps uncertain intent locked after access is revoked", async () => {
 fetch.mockRejectedValueOnce(new Error("lost"))
 const hook = setup()
 await act(async () => { await hook.result.current.answer(body,"key") })
 fetch.mockResolvedValueOnce(new Response("{}",{status:403}))
 await act(async () => expect(await hook.result.current.answer(body,"key")).toBe("unknown"))
 expect(sessionStorage.length).toBe(1)
 await act(async () => expect(await hook.result.current.answer({...body,values:{answer:"Other"}},"new-key")).toBe("conflict"))
 expect(fetch).toHaveBeenCalledTimes(2)
})

it("does not send when a durable retry record cannot be saved", async () => {
 vi.spyOn(Storage.prototype,"setItem").mockImplementation(()=>{throw new Error("quota")})
 const hook=setup()
 await act(async()=>expect(await hook.result.current.answer(body,"no-storage-key")).toBe("invalid"))
 expect(fetch).not.toHaveBeenCalled()
 expect(hook.result.current.message).toContain("No response was sent")
})
