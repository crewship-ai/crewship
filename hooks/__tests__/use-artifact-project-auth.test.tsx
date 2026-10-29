import React from "react"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { useArtifactProject } from "../use-artifact-project"
import { _resetRefreshInflightForTesting } from "@/lib/api-fetch"
import { setWorkspaceRequestAvailability, setWorkspaceRequestSession } from "@/lib/workspace-availability"

const auth = vi.hoisted(() => ({ human: "A" }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: auth.human } } }) }))
const snapshot = { page_id: "p", can_write: true, latest_version: 2, manifest: { version: 2, entrypoint: "index.html", files: [] } }
const body = { base_version: 2, entrypoint: "index.html", files: [] }
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
let expired = false
let actor = "A"
let refreshStatus = 200
let saveStatus = 200
let slugReassigned = false
let holdRefresh: (() => Promise<Response>) | undefined
let holdSession: (() => Promise<Response>) | undefined
let transport: ReturnType<typeof vi.fn>
beforeEach(() => {
  delete window.__CREWSHIP_TOKEN__; delete window.__CREWSHIP_SERVER_BASE__
  expired = false; actor = "A"; auth.human = "A"; refreshStatus = 200; saveStatus = 200
  slugReassigned = false
  holdRefresh = undefined; holdSession = undefined
  _resetRefreshInflightForTesting()
  setWorkspaceRequestSession("A"); setWorkspaceRequestAvailability("w", true)
  transport = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url === "/api/auth/token/refresh") {
      if (holdRefresh) return holdRefresh()
      if (refreshStatus === 200) expired = false
      return json({ error: "session_expired" }, refreshStatus)
    }
    if (url === "/api/auth/session") return holdSession ? holdSession() : json({ user: { id: actor } })
    if (init?.method === "POST") {
      if (slugReassigned && url.includes("/pages/demo/")) return json({ error: "draft sent to reassigned slug" }, 409)
      return json({ version: 3, error: "session_expired" }, expired ? 401 : saveStatus)
    }
    return json(snapshot)
  })
  vi.stubGlobal("fetch", transport)
})
afterEach(() => {
  cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); _resetRefreshInflightForTesting()
  setWorkspaceRequestSession(null); setWorkspaceRequestAvailability(null, true)
  delete window.__CREWSHIP_TOKEN__; delete window.__CREWSHIP_SERVER_BASE__
})
function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return renderHook(({ workspace, slug }) => useArtifactProject(workspace, slug), { initialProps: { workspace: "w", slug: "demo" }, wrapper: ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
}
const writes = () => transport.mock.calls.filter(([url, init]) => String(url).includes("/artifact/versions") && init?.method === "POST")
const refreshes = () => transport.mock.calls.filter(([url]) => String(url) === "/api/auth/token/refresh")

it("refreshes an expired cookie before the first write and after five minutes, sending each CAS POST once", async () => {
  let now = Date.now()
  vi.spyOn(Date, "now").mockImplementation(() => now)
  const hook = setup(); await waitFor(() => expect(hook.result.current.query.data).toBeTruthy())
  expired = true
  await act(async () => { await hook.result.current.save.mutateAsync(body) })
  expect(refreshes()).toHaveLength(1); expect(writes()).toHaveLength(1)
  expect(writes()[0][0]).toBe("/api/v1/pages/%40p/artifact/versions?workspace_id=w")
  expect(writes()[0][1]).toMatchObject({ body: JSON.stringify(body), signal: expect.any(AbortSignal) })
  await act(async () => { await hook.result.current.save.mutateAsync(body) })
  expect(refreshes()).toHaveLength(1); expect(writes()).toHaveLength(2)
  now += 6 * 60_000; expired = true
  await act(async () => { await hook.result.current.save.mutateAsync(body) })
  expect(refreshes()).toHaveLength(2); expect(writes()).toHaveLength(3)
})

it.each([401, 503])("does not dispatch a write when preflight refresh returns %s", async status => {
  const hook = setup(); await waitFor(() => expect(hook.result.current.query.data).toBeTruthy())
  refreshStatus = status
  await act(async () => { await expect(hook.result.current.save.mutateAsync(body)).rejects.toThrow() })
  expect(writes()).toHaveLength(0)
})

it.each(["other actor", "missing actor", "session error"])("fails closed on %s from the session read", async reason => {
  const hook = setup(); await waitFor(() => expect(hook.result.current.query.data).toBeTruthy())
  actor = reason === "other actor" ? "B" : ""
  if (reason === "missing actor") holdSession = async () => json({})
  if (reason === "session error") holdSession = async () => json({ error: "unavailable" }, 503)
  await act(async () => { await expect(hook.result.current.save.mutateAsync(body)).rejects.toThrow() })
  expect(writes()).toHaveLength(0)
})

it.each([["refresh", "workspace"], ["session", "workspace"], ["refresh", "session"], ["session", "session"]])("fences the %s preflight await across %s epoch ABA without a React render", async (phase, epoch) => {
  const hook = setup(); await waitFor(() => expect(hook.result.current.query.data).toBeTruthy())
  let resolve!: (response: Response) => void
  const held = vi.fn(() => new Promise<Response>(done => { resolve = done }))
  if (phase === "refresh") holdRefresh = held; else holdSession = held
  let result!: Promise<unknown>
  act(() => { result = hook.result.current.save.mutateAsync(body).catch(error => error) })
  await waitFor(() => expect(held).toHaveBeenCalled())
  if (epoch === "session") { setWorkspaceRequestSession("B"); setWorkspaceRequestSession("A") }
  else setWorkspaceRequestAvailability("other", true)
  setWorkspaceRequestAvailability("w", true)
  await act(async () => { resolve(json({ user: { id: "A" } })); await result })
  expect(writes()).toHaveLength(0)
})

for (const phase of ["refresh", "session"] as const) {
  it.each(["actor", "workspace", "page", "unmount"])(`retires an inflight ${phase} preflight on %s change`, async change => {
    const hook = setup(); await waitFor(() => expect(hook.result.current.query.data).toBeTruthy())
    let resolve!: (response: Response) => void
    const held = vi.fn(() => new Promise<Response>(done => { resolve = done }))
    if (phase === "refresh") holdRefresh = held; else holdSession = held
    const callbacks = vi.fn()
    let result!: Promise<unknown>
    act(() => { result = hook.result.current.save.mutateAsync(body, { onSuccess: callbacks, onError: callbacks }).catch(error => error) })
    await waitFor(() => expect(held).toHaveBeenCalled())
    const sessionSignal = transport.mock.calls.find(([url]) => url === "/api/auth/session")?.[1]?.signal
    if (change === "actor") {
      auth.human = "B"; hook.rerender({ workspace: "w", slug: "demo" })
      auth.human = "A"; hook.rerender({ workspace: "w", slug: "demo" })
    } else if (change === "unmount") hook.unmount()
    else hook.rerender({ workspace: change === "workspace" ? "other" : "w", slug: change === "page" ? "other" : "demo" })
    if (sessionSignal) expect(sessionSignal.aborted).toBe(true)
    await act(async () => { resolve(json({ user: { id: "A" } })); await result })
    expect(writes()).toHaveLength(0)
    expect(callbacks).not.toHaveBeenCalled()
  })
}

it("keeps bearer writes single-attempt without cookie refresh or cookie identity fallback", async () => {
  window.__CREWSHIP_TOKEN__ = "crewship_cli_test"
  const hook = setup(); await waitFor(() => expect(hook.result.current.query.data).toBeTruthy())
  saveStatus = 401
  await act(async () => { await expect(hook.result.current.save.mutateAsync(body)).rejects.toThrow() })
  expect(writes()).toHaveLength(1); expect(refreshes()).toHaveLength(0)
  expect(transport.mock.calls.some(([url]) => url === "/api/auth/session")).toBe(false)
  expect(new Headers(writes()[0][1]?.headers).get("Authorization")).toBe("Bearer crewship_cli_test")
})

it("retires a draft if the shell bearer changes before React renders", async () => {
  window.__CREWSHIP_TOKEN__ = "crewship_cli_original"
  const hook = setup(); await waitFor(() => expect(hook.result.current.query.data).toBeTruthy())
  window.__CREWSHIP_TOKEN__ = "crewship_cli_replacement"
  await act(async () => { await expect(hook.result.current.save.mutateAsync(body)).rejects.toThrow() })
  expect(writes()).toHaveLength(0); expect(refreshes()).toHaveLength(0)
})

it("keeps the original immutable PageID when its slug is reassigned during preflight", async () => {
  const hook = setup(); await waitFor(() => expect(hook.result.current.query.data).toBeTruthy())
  let resolve!: (response: Response) => void
  holdSession = vi.fn(() => new Promise<Response>(done => { resolve = done }))
  let result!: Promise<unknown>
  act(() => { result = hook.result.current.save.mutateAsync(body) })
  await waitFor(() => expect(holdSession).toHaveBeenCalled())
  slugReassigned = true
  await act(async () => { resolve(json({ user: { id: "A" } })); await result })
  expect(writes()).toHaveLength(1)
  expect(writes()[0][0]).toBe("/api/v1/pages/%40p/artifact/versions?workspace_id=w")
})

it.each([401, 403])("never replays a dispatched POST%s or clears its sticky refusal on a later read", async status => {
  const hook = setup(); await waitFor(() => expect(hook.result.current.query.data).toBeTruthy())
  saveStatus = status
  await act(async () => { await expect(hook.result.current.save.mutateAsync(body)).rejects.toThrow() })
  await waitFor(() => expect(hook.result.current.query.isError).toBe(true))
  await act(async () => { await hook.result.current.query.refetch() })
  expect(hook.result.current.query.isError).toBe(true)
  expect(writes()).toHaveLength(1)
  expect(refreshes()).toHaveLength(1)
})
