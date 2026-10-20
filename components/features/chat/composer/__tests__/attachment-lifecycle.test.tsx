import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react"

let userId: string | null = "user-a"
let workspaceId = "ws-1"
vi.mock("@/hooks/use-auth", async (orig) => ({
  ...(await orig<typeof import("@/hooks/use-auth")>()),
  useSessionSafe: () => ({
    data: userId ? { user: { id: userId, name: userId, email: "" } } : null,
    status: userId ? "authenticated" : "unauthenticated",
  }),
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId, loading: false }),
}))
vi.mock("../mention-autocomplete", () => ({ MentionAutocomplete: () => null }))
const toastError = vi.fn()
vi.mock("sonner", () => ({ toast: { error: (m: string) => toastError(m), success: vi.fn(), info: vi.fn() } }))

import { ChatComposer } from "../chat-composer"
import { AttachmentLifecycle } from "../attachment-lifecycle"
import { useComposerStore } from "@/stores/composer-store"

const SID = "chat-1"
const base = {
  agentId: "agent-1",
  isStreaming: false,
  connectionStatus: "connected",
  stopGeneration: vi.fn(),
  sendMessage: vi.fn(),
}
let ensureSession = vi.fn(async () => true)
const composer = (variant: "desktop" | "mobile", sessionId = SID, key?: string) => (
  <ChatComposer key={key} {...base} sessionId={sessionId} variant={variant} ensureSession={ensureSession} />
)
const tree = (variant: "desktop" | "mobile", opts: { sessionId?: string; key?: string; owner?: boolean } = {}) => (
  <>
    {opts.owner !== false && <AttachmentLifecycle sessionId={opts.sessionId ?? SID} />}
    {composer(variant, opts.sessionId ?? SID, opts.key)}
  </>
)
const file = (name: string) => new File([new Uint8Array(7)], name, { type: "text/plain" })

function gatedFetch() {
  const calls: Array<{ name: string; release: () => void; signal?: AbortSignal | null }> = []
  const impl = vi.fn((_url: string, init?: RequestInit) => {
    const name = ((init?.body as FormData).get("file") as File).name
    return new Promise((resolve, reject) => {
      init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))
      calls.push({
        name,
        signal: init?.signal,
        release: () =>
          resolve({ ok: true, status: 200, json: async () => ({ path: `attachments/${SID}/${name}`, agent_path: `/o/${name}` }) }),
      })
    })
  })
  return { impl, calls }
}
function pick(files: File[]) {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]:not([aria-label])')!
  Object.defineProperty(input, "files", { value: files, configurable: true })
  fireEvent.change(input)
}
const all = () => Object.entries(useComposerStore.getState().attachments).flatMap(([k, l]) => l.map((a) => ({ k, ...a })))
const statuses = () => all().map((a) => a.status)
const tick = (ms = 30) => new Promise((r) => setTimeout(r, ms))

describe("attachment job lifecycle", () => {
  beforeEach(() => {
    userId = "user-a"
    workspaceId = "ws-1"
    ensureSession = vi.fn(async () => true)
    toastError.mockClear()
    useComposerStore.setState({ attachments: {}, drafts: {} })
  })
  afterEach(() => vi.restoreAllMocks())

  it("a composer remount (variant switch) mid-upload does not cancel it", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const { rerender } = render(tree("desktop", { key: "d" }))
    pick([file("a.txt"), file("b.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    rerender(tree("mobile", { key: "m" })) // breakpoint remount
    expect(g.calls[0].signal?.aborted).toBe(false)
    expect(statuses()).not.toContain("cancelled")
    g.calls[0].release()
    await waitFor(() => expect(g.calls).toHaveLength(2)) // the queued file still runs
    g.calls[1].release()
    await waitFor(() => expect(statuses()).toEqual(["ready", "ready"]))
    expect(screen.queryByText(/Send is off/)).toBeNull()
  })

  it("the whole composer disappearing (phone Artifacts tab) and coming back keeps the upload", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const owner = () => <AttachmentLifecycle sessionId={SID} />
    const { rerender } = render(<>{owner()}{composer("mobile")}</>)
    pick([file("a.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    rerender(<>{owner()}</>) // Artifacts tab: composer unmounted
    expect(g.calls[0].signal?.aborted).toBe(false)
    rerender(<>{owner()}{composer("mobile")}</>) // back to Chat
    g.calls[0].release()
    await waitFor(() => expect(statuses()).toEqual(["ready"]))
    expect(screen.queryByText(/Send is off/)).toBeNull()
  })

  it("a remount while the chat row is being created still uploads", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    let finish!: (ok: boolean) => void
    ensureSession = vi.fn(() => new Promise<boolean>((r) => (finish = r)))
    const { rerender } = render(tree("desktop", { key: "d" }))
    pick([file("a.txt")])
    await waitFor(() => expect(ensureSession).toHaveBeenCalled())
    rerender(tree("mobile", { key: "m" }))
    await act(async () => finish(true))
    await waitFor(() => expect(g.calls).toHaveLength(1))
    g.calls[0].release()
    await waitFor(() => expect(statuses()).toEqual(["ready"]))
  })

  it("A -> B -> A: A's old job does not upload after A signs back in", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    let finish!: (ok: boolean) => void
    ensureSession = vi.fn(() => new Promise<boolean>((r) => (finish = r)))
    const { rerender } = render(tree("desktop"))
    pick([file("a.txt")])
    await waitFor(() => expect(ensureSession).toHaveBeenCalled())
    userId = "user-b"
    rerender(tree("desktop"))
    userId = "user-a"
    rerender(tree("desktop"))
    await act(async () => finish(true))
    await tick()
    expect(g.calls).toHaveLength(0)
    expect(all()).toEqual([])
  })

  it("no signed-in user: upload is refused, no orphaned chip; B can upload afterwards", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    userId = null
    const { rerender } = render(tree("desktop"))
    pick([file("x.txt")])
    await tick()
    expect(g.calls).toHaveLength(0)
    expect(all()).toEqual([])
    expect(toastError).toHaveBeenCalled()
    userId = "user-b"
    rerender(tree("desktop"))
    pick([file("b.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    g.calls[0].release()
    await waitFor(() => expect(statuses()).toEqual(["ready"]))
  })

  it("an abort during chat-row creation stops the upload", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    let finish!: (ok: boolean) => void
    ensureSession = vi.fn(() => new Promise<boolean>((r) => (finish = r)))
    const { rerender } = render(tree("desktop", { sessionId: SID }))
    pick([file("a.txt")])
    await waitFor(() => expect(ensureSession).toHaveBeenCalled())
    rerender(tree("desktop", { sessionId: "chat-2" })) // selected chat changed
    await act(async () => finish(true))
    await tick()
    expect(g.calls).toHaveLength(0)
  })

  it("changing the selected chat cancels pending chips (exact key) and keeps finished ones", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const { rerender } = render(tree("desktop"))
    pick([file("done.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    g.calls[0].release()
    await waitFor(() => expect(statuses()).toEqual(["ready"]))
    pick([file("run.txt"), file("wait.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(2))
    // a sibling chat whose id merely starts with the same text is untouched
    useComposerStore.getState().addAttachments(`user-a::ws-1::${SID}-10`, [
      { id: "s", name: "sib.txt", size: 1, type: "text/plain", status: "validating" },
    ])
    rerender(tree("desktop", { sessionId: "chat-2" }))
    expect(g.calls[1].signal?.aborted).toBe(true)
    const byName = Object.fromEntries(all().map((a) => [a.name, a.status]))
    expect(byName).toMatchObject({ "done.txt": "ready", "run.txt": "cancelled", "wait.txt": "cancelled", "sib.txt": "validating" })
    g.calls[1].release()
    await tick()
    expect(g.calls).toHaveLength(2) // wait.txt never started
  })

  it("the owner unmounting (leaving /chat) cancels pending uploads", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const { unmount } = render(tree("desktop"))
    pick([file("a.txt"), file("b.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    unmount()
    expect(g.calls[0].signal?.aborted).toBe(true)
    await tick()
    expect(g.calls).toHaveLength(1)
    expect(statuses()).toEqual(["cancelled", "cancelled"])
  })

  it("a scope change cancels jobs even though the composer is unmounted", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const owner = () => <AttachmentLifecycle sessionId={SID} />
    const { rerender } = render(<>{owner()}{composer("mobile")}</>)
    pick([file("a.txt"), file("b.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    rerender(<>{owner()}</>)
    userId = "user-b"
    rerender(<>{owner()}</>)
    expect(g.calls[0].signal?.aborted).toBe(true)
    expect(all()).toEqual([])
    g.calls[0].release()
    await tick()
    expect(g.calls).toHaveLength(1)
  })
})
