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

  describe("waiting for sign-in", () => {
    const unknown = () => { userId = null; authStatus = "loading" }
    const confirm = (id: string) => { userId = id; authStatus = "authenticated" }
    const send = () => screen.getByLabelText("Submit") as HTMLButtonElement
    const typeText = (v: string) => fireEvent.change(document.querySelector("textarea")!, { target: { value: v } })

    it("keeps Send blocked with a visible, announced status; the text is never sent; same user resumes", async () => {
      const g = gatedFetch()
      vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
      const { rerender } = render(tree("desktop"))
      pick([file("a.txt")])
      await waitFor(() => expect(g.calls).toHaveLength(1))
      g.calls[0].fail() // a failed chip is held under A
      await waitFor(() => expect(statuses()).toEqual(["error"]))
      typeText("hello")

      unknown() // brief null session for the same person
      rerender(tree("desktop"))
      expect(screen.queryByText("a.txt")).toBeNull() // chips masked...
      expect(all()).toHaveLength(1) // ...not dropped
      expect(send().getAttribute("aria-disabled")).toBe("true")
      expect(send().getAttribute("aria-describedby")).toBeTruthy()
      expect(screen.getByRole("note").textContent).toMatch(/Waiting for sign-in/)
      fireEvent.click(send())
      fireEvent.keyDown(document.querySelector("textarea")!, { key: "Enter" })
      await tick(80)
      expect(base.sendMessage).not.toHaveBeenCalled()
      // (the composer's text box is per-person too, so it is empty for the
      // unknown identity; the draft is kept under A and comes back below)
      expect(screen.getAllByRole("status").map((e) => e.textContent).join()).toMatch(/Waiting for sign-in/)

      confirm("user-a") // same person confirmed
      rerender(tree("desktop"))
      expect(screen.getByText("a.txt")).toBeTruthy() // chips back, still failed
      expect(screen.getByRole("note").textContent).toMatch(/a\.txt is not attached/)
      expect(screen.getByRole("note").textContent).not.toMatch(/Waiting for sign-in/)
      expect((document.querySelector("textarea") as HTMLTextAreaElement).value).toBe("hello")
    })

    it("blocks Send for typed text alone while waiting, and unblocks when confirmed", async () => {
      const { rerender } = render(tree("desktop"))
      typeText("just text")
      expect(send().getAttribute("aria-disabled")).toBeNull()
      unknown()
      rerender(tree("desktop"))
      expect(send().getAttribute("aria-disabled")).toBe("true")
      confirm("user-a")
      rerender(tree("desktop"))
      expect(send().getAttribute("aria-disabled")).toBeNull()
    })

    it("a failed file with no caption keeps Send focusable (aria-disabled) with the reason linked", async () => {
      useComposerStore.setState({
        attachments: { [`user-a::ws-1::${SID}`]: [
          { id: "f", name: "bad.txt", size: 5, type: "text/plain", status: "error", file: file("bad.txt"), failureLabel: "server refused" },
        ] },
      })
      render(tree("desktop"))
      expect(send().disabled).toBe(false) // not a native disabled: it can take focus
      expect(send().getAttribute("aria-disabled")).toBe("true")
      expect(document.getElementById(send().getAttribute("aria-describedby")!)?.textContent).toMatch(/bad\.txt/)
      send().focus()
      expect(document.activeElement).toBe(send())
      fireEvent.click(send())
      await waitFor(() => expect(screen.getAllByRole("status").map((e) => e.textContent).join()).toMatch(/bad\.txt/))
      expect(base.sendMessage).not.toHaveBeenCalled()
    })

    it("a workspace switch says it is loading, in neutral copy, not a sign-in error", async () => {
      const { rerender } = render(tree("desktop"))
      workspaceId = null // person known, workspace changing
      rerender(tree("desktop"))
      const note = screen.getByRole("note")
      expect(note.textContent).toMatch(/Loading this chat/)
      expect(note.textContent).not.toMatch(/sign-in/i)
      expect(note.className).not.toMatch(/text-destructive/)
    })

    it("an unknown person says waiting for sign-in", async () => {
      const { rerender } = render(tree("desktop"))
      unknown()
      rerender(tree("desktop"))
      expect(screen.getByRole("note").textContent).toMatch(/Waiting for sign-in/)
    })

    it("a brief blink is not announced; a lasting wait is announced once", async () => {
      const { rerender } = render(tree("desktop"))
      const live = () => screen.getByRole("status").textContent
      workspaceId = null
      rerender(tree("desktop"))
      workspaceId = "ws-1"
      rerender(tree("desktop")) // gone again before anything was announced
      await tick(1100)
      expect(live()).toBe("")
      workspaceId = null
      rerender(tree("desktop"))
      expect(live()).toBe("") // not on the first frame
      await tick(1100)
      expect(live()).toMatch(/Loading this chat/)
      workspaceId = "ws-1"
      rerender(tree("desktop"))
      expect(live()).toBe("") // cleared when the wait ends
    })

    it("a request in flight when identity goes unknown is aborted when a different user is confirmed", async () => {
      const g = gatedFetch()
      vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
      const { rerender } = render(tree("desktop"))
      pick([file("a.txt")])
      await waitFor(() => expect(g.calls).toHaveLength(1))
      unknown()
      rerender(tree("desktop"))
      expect(g.calls[0].signal?.aborted).toBe(false) // unknown alone does not abort
      confirm("user-b")
      rerender(tree("desktop"))
      expect(g.calls[0].signal?.aborted).toBe(true)
      expect(g.calls).toHaveLength(1)
      expect(all()).toEqual([])
      expect(screen.queryByText("a.txt")).toBeNull()
    })

    it("no sending after the chat row is created if identity became unknown in between", async () => {
      const g = gatedFetch()
      vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
      let finish!: (ok: boolean) => void
      ensureSession = vi.fn(() => new Promise<boolean>((r) => (finish = r)))
      const { rerender } = render(tree("desktop"))
      pick([file("a.txt")])
      await waitFor(() => expect(ensureSession).toHaveBeenCalled())
      unknown()
      rerender(tree("desktop"))
      await act(async () => { finish(true) })
      await tick(60)
      expect(g.calls).toHaveLength(0)
    })
  })

  describe("failure reasons are visible and announced", () => {
    it.each([
      [500, /server refused/i],
      [413, /too large/i],
      [415, /type not allowed/i],
    ])("status %i shows a short reason class on the chip, in the notice and in the live region", async (status, label) => {
      const g = gatedFetch()
      vi.spyOn(globalThis, "fetch").mockImplementation(((url: string, init?: RequestInit) => {
        g.impl(url, init)
        return Promise.resolve({ ok: false, status, json: async () => ({ error: "/srv/secret/path: permission denied" }) })
      }) as never)
      render(tree("desktop"))
      pick([file("a.txt")])
      await waitFor(() => expect(statuses()).toEqual(["error"]))
      const chip = document.querySelector("[data-status='error']") as HTMLElement
      expect(chip.textContent).toMatch(label)
      expect(screen.getByRole("note").textContent).toMatch(label)
      await waitFor(() =>
        expect(screen.getAllByRole("status").map((e) => e.textContent).join()).toMatch(label),
      )
      // never the server's own words
      expect(document.body.textContent).not.toContain("/srv/secret")
    })

    it("a network failure says so", async () => {
      vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("Failed to fetch"))
      render(tree("desktop"))
      pick([file("a.txt")])
      await waitFor(() => expect(statuses()).toEqual(["error"]))
      expect((document.querySelector("[data-status='error']") as HTMLElement).textContent).toMatch(/network/i)
    })
  })
})
