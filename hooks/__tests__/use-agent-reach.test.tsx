import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { clearStaleCache, DEFAULT_TTL_MS } from "@/lib/stale-cache"
import { useAgentReach } from "@/hooks/use-agent-reach"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const binding = { toolkit: "github" }
const channel = { id: "channel", type: "email", enabled: true }
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
function success() {
  fetchMock.mockImplementation(async url => String(url).includes("/bind?") ? reply({ bindings: [binding] }) : reply({ channels: [channel] }))
}
beforeEach(() => { vi.restoreAllMocks(); fetchMock.mockReset(); clearStaleCache() })

describe("agent tool and notification reach", () => {
  it.each([[null, "agent"], [undefined, "agent"], ["ws", null], ["", "agent"]])("does not request absent scope %s/%s", (ws, agent) => {
    const { result } = renderHook(() => useAgentReach(ws, agent ?? null))
    expect(result.current).toMatchObject({ toolkits: [], channels: [], loading: false, error: null })
    expect(fetchMock).not.toHaveBeenCalled()
  })
  it("encodes both identifiers and returns independently loaded reach", async () => {
    success()
    const { result } = renderHook(() => useAgentReach("ws &/", "agent /"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current).toMatchObject({ toolkits: [binding], channels: [channel], error: null })
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/integrations/composio/agents/agent%20%2F/bind?workspace_id=ws%20%26%2F",
      "/api/v1/agents/agent%20%2F/notification-channels?workspace_id=ws%20%26%2F",
    ])
  })
  it("defaults omitted lists to empty", async () => {
    fetchMock.mockImplementation(async () => reply({}))
    const { result } = renderHook(() => useAgentReach("ws", "agent"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current).toMatchObject({ toolkits: [], channels: [], error: null })
  })
  it.each(["bindings", "channels", "both"])("preserves successful data when %s fail", async failed => {
    fetchMock.mockImplementation(async url => {
      const bind = String(url).includes("/bind?")
      return failed === "both" || (bind ? failed === "bindings" : failed === "channels")
        ? reply({}, 503) : reply(bind ? { bindings: [binding] } : { channels: [channel] })
    })
    const { result } = renderHook(() => useAgentReach("ws", "agent"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe("Some tools or channels could not be loaded.")
    expect(result.current.toolkits).toEqual(failed === "channels" ? [binding] : [])
    expect(result.current.channels).toEqual(failed === "bindings" ? [channel] : [])
  })
  it("serves both cached lists without another request and refresh invalidates them", async () => {
    success()
    const first = renderHook(() => useAgentReach("ws", "agent"))
    await waitFor(() => expect(first.result.current.loading).toBe(false))
    first.unmount()
    const second = renderHook(() => useAgentReach("ws", "agent"))
    expect(second.result.current).toMatchObject({ toolkits: [binding], channels: [channel], loading: false })
    await act(async () => {})
    expect(fetchMock).toHaveBeenCalledTimes(2)
    fetchMock.mockImplementation(async () => reply({}))
    await act(async () => { await second.result.current.refresh() })
    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(second.result.current).toMatchObject({ toolkits: [], channels: [], error: null })
  })
  it("retains stale values when background refresh fails", async () => {
    success()
    const now = Date.now()
    vi.spyOn(Date, "now").mockReturnValue(now)
    const first = renderHook(() => useAgentReach("ws", "agent"))
    await waitFor(() => expect(first.result.current.loading).toBe(false))
    first.unmount()
    vi.mocked(Date.now).mockReturnValue(now + DEFAULT_TTL_MS + 1)
    fetchMock.mockRejectedValue(new Error("offline"))
    const second = renderHook(() => useAgentReach("ws", "agent"))
    await waitFor(() => expect(second.result.current.error).not.toBeNull())
    expect(second.result.current).toMatchObject({ toolkits: [binding], channels: [channel], loading: false })
  })
  it.each(["new", null])("ignores old requests when scope becomes %s", async next => {
    const pending: Array<(value: Response) => void> = []
    fetchMock.mockImplementation(() => new Promise(resolve => pending.push(resolve)))
    const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => useAgentReach(ws, "agent"), { initialProps: { ws: "old" as string | null } })
    success()
    rerender({ ws: next })
    await waitFor(() => expect(result.current.loading).toBe(false))
    await act(async () => { pending.forEach(resolve => resolve(reply({ bindings: [{ toolkit: "old" }], channels: [{ id: "old" }] }))) })
    expect(result.current.toolkits).toEqual(next ? [binding] : [])
    expect(result.current.channels).toEqual(next ? [channel] : [])
  })
})
