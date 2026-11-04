import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { createElement } from "react"
import { renderToString } from "react-dom/server"
import { act, renderHook } from "@testing-library/react"
import { useAppearanceSync } from "../use-appearance-sync"
import { useAppearance } from "../use-appearance"
import { _resetRefreshInflightForTesting } from "@/lib/api-fetch"
import { setWorkspaceRequestAvailability, setWorkspaceRequestSession } from "@/lib/workspace-availability"
import { OUTBOX_STORAGE_KEY } from "@/lib/theme/appearance-outbox"

// Real hook, appearance stores, outbox, API retry and binding boundary.
// Only theme rendering, HTTP/cookie context and observed auth binding are modeled.
vi.mock("next-themes", () => ({ useTheme: () => ({ theme: "dark", resolvedTheme: "dark", setTheme: () => undefined }) }))
function deferred<T>() { let resolve!: (v: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
let human: string
let refresh: ReturnType<typeof deferred<Response>>
let refreshing: boolean
let fail: boolean
let writes: { human: string; body: string }[]
let server: Map<string, unknown>
function bind(id: string) { human = id; setWorkspaceRequestSession(id); setWorkspaceRequestAvailability("same-workspace", true) }
function mount(owner: string) {
 return renderHook(({ account }) => {
  useAppearanceSync(true, undefined, account)
  return useAppearance()
 }, { initialProps: { account: owner } })
}
async function tick(ms = 0) { await act(async () => { await vi.advanceTimersByTimeAsync(ms) }) }
beforeEach(() => {
 vi.useFakeTimers(); const store = new Map<string, string>(); vi.stubGlobal("localStorage", { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v), removeItem: (k: string) => void store.delete(k), clear: () => store.clear() }); delete window.__CREWSHIP_SERVER_BASE__; delete window.__CREWSHIP_TOKEN__
 _resetRefreshInflightForTesting(); bind("Alice"); refreshing = false; fail = true; writes = []; server = new Map(); refresh = deferred<Response>()
 vi.stubGlobal("navigator", { locks: undefined })
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = String(input)
  if (url === "/api/auth/token/refresh") { refreshing = true; return refresh.promise }
  if (init?.method === "PUT") { writes.push({ human, body: String(init.body) }); if (fail) return json(401, { error: "session_expired" }); server.set(human, JSON.parse(String(init.body))); return json(200, {}) }
  return json(200, { appearance: server.get(human) ?? {} })
 }))
})
afterEach(() => { _resetRefreshInflightForTesting(); setWorkspaceRequestSession(null); setWorkspaceRequestAvailability(null, true); vi.useRealTimers(); vi.unstubAllGlobals() })
async function changeDuringAwait() {
 const hook = mount("Alice"); await tick()
 act(() => hook.result.current.setAccent("teal")); await tick(400)
 expect(refreshing).toBe(true); expect(writes).toHaveLength(1)
 act(() => bind("Bob")); fail = false; refresh.resolve(json(200, {})); await tick()
 return hook
}
it("does not re-invoke Alice's retired mutation from an old retry timer", async () => {
 const hook = await changeDuringAwait()
 await tick(5000)
 expect(writes.filter(w => w.human === "Bob")).toEqual([])
 hook.unmount()
})
it("does not adopt Alice's pending body on Bob's new mount; Alice can return without loss", async () => {
 const alice = await changeDuringAwait(); alice.unmount(); await tick()
 const bob = mount("Bob"); await tick(6000)
 expect(writes.filter(w => w.human === "Bob")).toEqual([])
 bob.unmount(); act(() => bind("Alice")); const returned = mount("Alice"); await tick(1000)
 expect(writes.filter(w => w.human === "Alice")).toHaveLength(2)
 expect(JSON.parse(writes[1].body)).toMatchObject({ accent: "teal" })
 returned.unmount()
})
it("keeps an unknown legacy queue byte-for-byte inert", async () => {
 const raw = ' [ { "key":"appearance", "body":"{\\"accent\\":\\"teal\\"}" } ] '
 localStorage.setItem(OUTBOX_STORAGE_KEY, raw); fail = false
 const hook = mount("Alice"); await tick(6000)
 expect(writes).toEqual([]); expect(localStorage.getItem(OUTBOX_STORAGE_KEY)).toBe(raw)
 hook.unmount()
})
it("keeps same-human workspace switches and its own return working", async () => {
 fail = false; const hook = mount("Alice"); await tick()
 act(() => hook.result.current.setAccent("teal")); act(() => setWorkspaceRequestAvailability("other-workspace", true)); await tick(400)
 expect(writes).toHaveLength(1); expect(writes[0].human).toBe("Alice")
 hook.unmount(); const returned = mount("Alice"); await tick(6000)
 expect(writes).toHaveLength(1); returned.unmount()
})

it("Bob can make a fresh change after activation, without overwriting Alice's pending intent", async () => {
 const alice = await changeDuringAwait(); alice.unmount(); await tick()
 const bob = mount("Bob"); await tick(600)
 expect(writes.filter(w => w.human === "Bob")).toEqual([])
 act(() => bob.result.current.setAccent("violet")); await tick(400)
 expect(writes.filter(w => w.human === "Bob")).toHaveLength(1)
 expect(JSON.parse(writes.at(-1)!.body)).toMatchObject({ accent: "violet" })
 bob.unmount(); act(() => bind("Alice")); const returned = mount("Alice"); await tick(600)
 expect(returned.result.current.accent).toBe("teal")
 expect(JSON.parse(writes.at(-1)!.body)).toMatchObject({ accent: "teal" })
 expect(writes.at(-1)!.human).toBe("Alice"); returned.unmount()
})
it("retires the captured box when the same hook instance changes accounts", async () => {
 const hook = await changeDuringAwait()
 hook.rerender({ account: "Bob" }); await tick(6000)
 expect(writes.filter(w => w.human === "Bob")).toEqual([])
 act(() => hook.result.current.setAccent("violet")); await tick(400)
 expect(writes.filter(w => w.human === "Bob")).toHaveLength(1)
 hook.unmount(); act(() => bind("Alice")); const returned = mount("Alice"); await tick(600)
 expect(returned.result.current.accent).toBe("teal")
 expect(writes.at(-1)!.human).toBe("Alice"); returned.unmount()
})
it("does not activate cloud writes without an owning human", async () => {
 fail = false
 const hook = renderHook(() => { useAppearanceSync(true); return useAppearance() })
 act(() => hook.result.current.setAccent("teal")); await tick(6000)
 expect(fetch).not.toHaveBeenCalled(); hook.unmount()
})
it("does not adopt another server's queue for the same human", async () => {
 const alice = await changeDuringAwait(); alice.unmount(); await tick()
 act(() => bind("Alice")); window.__CREWSHIP_SERVER_BASE__ = "https://other.example.test"
 const other = mount("Alice"); await tick(6000)
 expect(writes.filter(w => w.human === "Alice")).toHaveLength(1)
 other.unmount(); delete window.__CREWSHIP_SERVER_BASE__
 const returned = mount("Alice"); await tick(600)
 expect(writes.filter(w => w.human === "Alice")).toHaveLength(2); returned.unmount()
})

it("preserves a debounced write when sync is turned off without sending it", async () => {
 fail = false; const hook = mount("Alice"); await tick()
 act(() => hook.result.current.setAccent("teal"))
 act(() => hook.result.current.setSyncEnabled(false)); await tick(6000)
 expect(writes).toEqual([])
 act(() => hook.result.current.setSyncEnabled(true)); await tick(600)
 expect(writes).toHaveLength(1); expect(JSON.parse(writes[0].body)).toMatchObject({ accent: "teal" })
 hook.unmount()
})
it("can render the client hook on the server before a browser exists", () => {
 const browser = window
 vi.stubGlobal("window", undefined)
 function Probe() { useAppearanceSync(true, undefined, "Alice"); return null }
 try { expect(() => renderToString(createElement(Probe))).not.toThrow() } finally { vi.stubGlobal("window", browser) }
})

it.each([false, true])("does not promote Alice's browser accent through Bob's density choice (remote=%s)", async (existing) => {
 const alice = await changeDuringAwait(); alice.unmount(); await tick()
 if (existing) server.set("Bob", { motion: "on" })
 const bob = mount("Bob"); await tick(600)
 if (existing) expect(bob.result.current.motion).toBe("on")
 act(() => bob.result.current.setDensity("compact")); await tick(400)
 expect(JSON.parse(writes.at(-1)!.body)).toEqual(existing ? { motion: "on", density: "compact" } : { density: "compact" })
 expect(writes.at(-1)!.human).toBe("Bob")
 bob.unmount(); act(() => bind("Alice")); const returned = mount("Alice"); await tick(600)
 expect(returned.result.current.accent).toBe("teal")
 expect(JSON.parse(writes.at(-1)!.body)).toEqual({ accent: "teal" })
 act(() => returned.result.current.setTextSize("large")); await tick(400)
 expect(JSON.parse(writes.at(-1)!.body)).toEqual({ accent: "teal", textSize: "large" })
 returned.unmount()
})
it("preserves a pending pick through sync off/on when storage is blocked", async () => {
 fail = false
 // Only the outbox key is denied; the appearance store itself still works.
 const original = localStorage
 vi.stubGlobal("localStorage", { getItem: (k: string) => k.startsWith(OUTBOX_STORAGE_KEY) ? null : original.getItem(k), setItem: (k: string, v: string) => { if (k.startsWith(OUTBOX_STORAGE_KEY)) throw new Error("blocked"); original.setItem(k, v) } })
 const hook = mount("Alice"); await tick()
 act(() => hook.result.current.setAccent("teal"))
 act(() => hook.result.current.setSyncEnabled(false)); await tick(1000)
 expect(writes).toEqual([])
 act(() => hook.result.current.setSyncEnabled(true)); await tick(600)
 expect(writes.map(w => JSON.parse(w.body))).toEqual([{ accent: "teal" }])
 hook.unmount()
})
it("ignores Alice's late pull after the same instance binds Bob", async () => {
 fail = false; const late = deferred<Response>(); const fetcher = fetch
 vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => !init?.method && human === "Alice" ? late.promise : fetcher(input, init)))
 const hook = mount("Alice"); await tick()
 act(() => bind("Bob")); hook.rerender({ account: "Bob" }); await tick()
 late.resolve(json(200, { appearance: { accent: "teal" } })); await tick(600)
 expect(hook.result.current.accent).not.toBe("teal"); expect(writes).toEqual([])
 act(() => hook.result.current.setDensity("compact")); await tick(400)
 expect(writes.map(w => ({ human: w.human, body: JSON.parse(w.body) }))).toEqual([{ human: "Bob", body: { density: "compact" } }])
 hook.unmount()
})
it("keeps Alice's uncertain late successful PUT in her queue after Bob binds", async () => {
 fail = false; const late = deferred<Response>(); const fetcher = fetch
 vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
  if (init?.method === "PUT" && human === "Alice") { writes.push({ human, body: String(init.body) }); return late.promise }
  return fetcher(input, init)
 }))
 const hook = mount("Alice"); await tick()
 act(() => hook.result.current.setAccent("teal")); await tick(400)
 act(() => bind("Bob")); hook.rerender({ account: "Bob" }); await tick()
 late.resolve(json(200, {})); await tick(6000)
 expect(writes.map(w => w.human)).toEqual(["Alice"])
 vi.stubGlobal("fetch", fetcher)
 act(() => bind("Alice")); hook.rerender({ account: "Alice" }); await tick(600)
 expect(writes.map(w => w.human)).toEqual(["Alice", "Alice"])
 expect(JSON.parse(writes.at(-1)!.body)).toEqual({ accent: "teal" })
 hook.unmount()
})
