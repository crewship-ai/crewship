import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react"

let userId: string | null = "user-a"
let authStatus = "authenticated"
let workspaceId: string | null = "ws-1"
vi.mock("@/hooks/use-auth", async (orig) => ({
  ...(await orig<typeof import("@/hooks/use-auth")>()),
  useSessionSafe: () => ({
    data: userId ? { user: { id: userId, name: userId, email: "" } } : null,
    status: authStatus,
  }),
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId, loading: false }),
  useCurrentWorkspaceId: () => workspaceId,
}))
vi.mock("../mention-autocomplete", () => ({ MentionAutocomplete: () => null }))
const toastError = vi.fn()
vi.mock("sonner", () => ({ toast: { error: (m: string, o?: unknown) => toastError(m, o), success: vi.fn(), info: vi.fn() } }))

import { ChatComposer } from "../chat-composer"
import { AttachmentLifecycle } from "../attachment-lifecycle"
import { AttachmentScopeSync } from "../attachment-scope-sync"
import { __resetForTests, currentScopeEpoch, enterScope } from "@/lib/chat-attachment-jobs"
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
    {opts.owner !== false && <AttachmentLifecycle />}
    {composer(variant, opts.sessionId ?? SID, opts.key)}
  </>
)
const file = (name: string) => new File([new Uint8Array(7)], name, { type: "text/plain" })

function gatedFetch() {
  const calls: Array<{ name: string; url: string; release: () => void; fail: () => void; signal?: AbortSignal | null }> = []
  const impl = vi.fn((url: string, init?: RequestInit) => {
    const name = ((init?.body as FormData).get("file") as File).name
    return new Promise((resolve, reject) => {
      init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))
      calls.push({
        name,
        url,
        fail: () => resolve({ ok: false, status: 500, json: async () => ({ error: "boom" }) }),
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
    __resetForTests()
    userId = "user-a"
    authStatus = "authenticated"
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
    const owner = () => <AttachmentLifecycle />
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
    const { unmount } = render(tree("desktop"))
    pick([file("a.txt")])
    await waitFor(() => expect(ensureSession).toHaveBeenCalled())
    unmount() // leaving the chat page cancels
    await act(async () => finish(true))
    await tick()
    expect(g.calls).toHaveLength(0)
  })

  it("switching to another chat does not cancel: the upload finishes in the background and is done on return", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const { rerender } = render(tree("desktop"))
    pick([file("run.txt"), file("wait.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    rerender(tree("desktop", { sessionId: "chat-2" }))
    expect(g.calls[0].signal?.aborted).toBe(false)
    expect(statuses()).not.toContain("cancelled")
    g.calls[0].release()
    await waitFor(() => expect(g.calls).toHaveLength(2)) // the queued file still runs
    // ...and it goes to the chat it was added to, not the one now open
    expect(g.calls[1].url).toContain("/chats/chat-1/attachments")
    g.calls[1].release()
    await waitFor(() => expect(statuses()).toEqual(["ready", "ready"]))
    rerender(tree("desktop")) // back to the first chat
    expect(screen.getByText("run.txt")).toBeTruthy()
    expect(screen.getByText("wait.txt")).toBeTruthy()
    expect(screen.queryByText(/Send is off/)).toBeNull()
  })

  it("the owner unmounting (leaving /chat) cancels pending uploads", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const { unmount } = render(tree("desktop"))
    pick([file("a.txt"), file("b.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    const before = currentScopeEpoch()
    unmount()
    expect(g.calls[0].signal?.aborted).toBe(true)
    expect(currentScopeEpoch()).toBeGreaterThan(before) // cancel and epoch move together
    await tick()
    expect(g.calls).toHaveLength(1)
    expect(statuses()).toEqual(["cancelled", "cancelled"])
  })

  it("a scope change cancels jobs even though the composer is unmounted", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const owner = () => <AttachmentScopeSync />
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

  it("A signs out and B signs in on a page with no composer and no owner: A's job never sends", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    let finish!: (ok: boolean) => void
    ensureSession = vi.fn(() => new Promise<boolean>((r) => (finish = r)))
    const sync = () => <AttachmentScopeSync />
    const { rerender } = render(<>{sync()}{composer("desktop")}</>)
    pick([file("a.txt"), file("a2.txt")])
    await waitFor(() => expect(ensureSession).toHaveBeenCalled())
    rerender(<>{sync()}</>) // composer unmounted, nobody consuming the scope
    userId = null
    authStatus = "unauthenticated"
    rerender(<>{sync()}</>)
    userId = "user-b"
    authStatus = "authenticated"
    rerender(<>{sync()}</>)
    await act(async () => finish(true))
    await tick()
    expect(g.calls).toHaveLength(0)
    expect(all()).toEqual([])
  })

  it("a momentary missing workspace or session does not cancel or drop", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const { rerender } = render(tree("desktop"))
    pick([file("a.txt"), file("b.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    const epoch = currentScopeEpoch()
    workspaceId = null // revalidation: workspace briefly unknown
    rerender(tree("desktop"))
    userId = null
    authStatus = "loading" // and the session briefly unread
    rerender(tree("desktop"))
    expect(g.calls[0].signal?.aborted).toBe(false)
    expect(statuses()).not.toContain("cancelled")
    expect(all()).toHaveLength(2)
    userId = "user-a"
    authStatus = "authenticated"
    workspaceId = "ws-1"
    rerender(tree("desktop"))
    expect(currentScopeEpoch()).toBe(epoch)
    g.calls[0].release()
    await waitFor(() => expect(g.calls).toHaveLength(2))
    g.calls[1].release()
    await waitFor(() => expect(statuses()).toEqual(["ready", "ready"]))
  })

  it("a different user behind a momentary missing workspace is still a change", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const { rerender } = render(tree("desktop"))
    pick([file("a.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    userId = "user-b"
    workspaceId = null
    rerender(tree("desktop"))
    expect(g.calls[0].signal?.aborted).toBe(true)
    expect(all()).toEqual([])
  })

  it("runUpload's epoch check alone stops a job (no cancelAll, no abort, chips kept)", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    let finish!: (ok: boolean) => void
    ensureSession = vi.fn(() => new Promise<boolean>((r) => (finish = r)))
    render(composer("desktop"))
    pick([file("a.txt")])
    await waitFor(() => expect(ensureSession).toHaveBeenCalled())
    enterScope("someone-else::ws-9::", "someone-else") // moves the epoch only
    await act(async () => finish(true))
    await tick()
    expect(g.calls).toHaveLength(0)
    expect(all()).toHaveLength(1) // not dropped: only the epoch guard acted
  })

  it("a queued file after a chat switch uploads even when the send-path ensure refuses", async () => {
    // ChatPanel's send-path ensure returns false once the person has moved on
    // (scope-ref check); the upload must not depend on it.
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const refusing = vi.fn(async () => false)
    const withUpload = (sessionId: string) => (
      <>
        <AttachmentLifecycle />
        <ChatComposer {...base} sessionId={sessionId} variant="desktop" ensureSession={refusing} ensureSessionForUpload={async () => true} />
      </>
    )
    const { rerender } = render(withUpload(SID))
    pick([file("a.txt"), file("b.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    rerender(withUpload("chat-2"))
    g.calls[0].release()
    await waitFor(() => expect(g.calls).toHaveLength(2))
    g.calls[1].release()
    await waitFor(() => expect(statuses()).toEqual(["ready", "ready"]))
    expect(refusing).not.toHaveBeenCalled()
  })

  it("a background failure in another chat raises no toast; the chip says Failed on return", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const { rerender } = render(tree("desktop"))
    pick([file("a.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    rerender(tree("desktop", { sessionId: "chat-2" }))
    g.calls[0].fail()
    await waitFor(() => expect(statuses()).toEqual(["error"]))
    expect(toastError).not.toHaveBeenCalled()
    rerender(tree("desktop")) // back
    expect(screen.getByText(/Upload failed/)).toBeTruthy()
    expect(screen.getByRole("note").textContent).toMatch(/Send is off/)
  })

  it("a failure in the open chat raises no toast either: the chip and notice carry it", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    render(tree("desktop"))
    pick([file("a.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    g.calls[0].fail()
    await waitFor(() => expect(statuses()).toEqual(["error"]))
    expect(toastError).not.toHaveBeenCalled()
    expect(screen.getByRole("note").textContent).toContain("a.txt")
  })

  describe("identity unknown (session unread)", () => {
    const unknown = () => {
      userId = null
      authStatus = "loading"
    }
    const confirm = (id: string) => {
      userId = id
      authStatus = "authenticated"
    }

    it("hides the chips and sends nothing while unknown; the same user confirmed resumes", async () => {
      const g = gatedFetch()
      vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
      const { rerender } = render(tree("desktop"))
      pick([file("run.txt"), file("queued.txt")])
      await waitFor(() => expect(g.calls).toHaveLength(1))
      expect(screen.getByText("queued.txt")).toBeTruthy()

      unknown()
      rerender(tree("desktop"))
      expect(screen.queryByText("run.txt")).toBeNull() // previous identity's names are masked
      expect(screen.queryByText("queued.txt")).toBeNull()
      expect(all()).toHaveLength(2) // ...but kept internally, nothing dropped
      expect(statuses()).not.toContain("cancelled")
      g.calls[0].release() // the request already on the wire settles
      await tick(60)
      expect(g.calls).toHaveLength(1) // the queued file does not start

      confirm("user-a")
      rerender(tree("desktop"))
      await waitFor(() => expect(g.calls).toHaveLength(2)) // resumed
      expect(screen.getByText("queued.txt")).toBeTruthy()
      g.calls[1].release()
      await waitFor(() => expect(statuses()).toEqual(["ready", "ready"]))
    })

    it("a different user confirmed afterwards: the paused job never sends and the chips are dropped", async () => {
      const g = gatedFetch()
      vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
      const { rerender } = render(tree("desktop"))
      pick([file("run.txt"), file("queued.txt")])
      await waitFor(() => expect(g.calls).toHaveLength(1))
      unknown()
      rerender(tree("desktop"))
      g.calls[0].release()
      await tick(60)
      confirm("user-b")
      rerender(tree("desktop"))
      await tick(60)
      expect(g.calls).toHaveLength(1)
      expect(all()).toEqual([])
      expect(screen.queryByText("queued.txt")).toBeNull()
    })

    it("a job past chat-row creation does not send while unknown", async () => {
      const g = gatedFetch()
      vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
      let finish!: (ok: boolean) => void
      ensureSession = vi.fn(() => new Promise<boolean>((r) => (finish = r)))
      const { rerender } = render(tree("desktop"))
      pick([file("a.txt")])
      await waitFor(() => expect(ensureSession).toHaveBeenCalled())
      unknown()
      rerender(tree("desktop"))
      await act(async () => finish(true))
      await tick(60)
      expect(g.calls).toHaveLength(0)
      expect(all()).toHaveLength(1) // kept
      confirm("user-a")
      rerender(tree("desktop"))
      await waitFor(() => expect(g.calls).toHaveLength(1))
      g.calls[0].release()
      await waitFor(() => expect(statuses()).toEqual(["ready"]))
    })

    it("uploads are refused while unknown", async () => {
      const g = gatedFetch()
      vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
      unknown()
      render(tree("desktop"))
      pick([file("x.txt")])
      await tick()
      expect(g.calls).toHaveLength(0)
      expect(all()).toEqual([])
    })
  })
})
