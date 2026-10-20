import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, act, cleanup } from "@testing-library/react"

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
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))

import { ChatComposer } from "../chat-composer"
import { useComposerStore } from "@/stores/composer-store"

const SID = "group-chat-1" // the same authorized group chat for both humans
const props = {
  agentId: "agent-1",
  sessionId: SID,
  isStreaming: false,
  connectionStatus: "connected",
  variant: "desktop" as const,
  stopGeneration: vi.fn(),
  ensureSession: vi.fn(async () => true),
  sendMessage: vi.fn(),
}
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
const names = () => Object.values(useComposerStore.getState().attachments).flat().map((a) => a.name)

describe("attachments are scoped to the human, workspace and chat", () => {
  beforeEach(() => {
    userId = "user-a"
    workspaceId = "ws-1"
    useComposerStore.setState({ attachments: {}, drafts: {} })
  })
  afterEach(() => vi.restoreAllMocks())

  it("A's pending files are invisible to B in the same chat and A's queued job never uploads", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const { rerender } = render(<ChatComposer {...props} />)
    pick([file("a-running.txt"), file("a-queued.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    expect(screen.getByText("a-running.txt")).toBeTruthy()
    expect(screen.getByText("a-queued.txt")).toBeTruthy()

    userId = "user-b"
    rerender(<ChatComposer {...props} />)
    expect(screen.queryByText("a-running.txt")).toBeNull()
    expect(screen.queryByText("a-queued.txt")).toBeNull()
    expect(screen.queryByText(/Send is off/)).toBeNull()
    expect(g.calls[0].signal?.aborted).toBe(true) // A's running request is aborted
    await waitFor(() => expect(names()).toEqual([])) // and A's chips are dropped

    // Even when the server answers late, nothing of A's lands anywhere.
    g.calls[0].release()
    await new Promise((r) => setTimeout(r, 30))
    expect(g.calls).toHaveLength(1) // the queued file never started
    expect(names()).toEqual([])

    // B's own upload still works.
    pick([file("b.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(2))
    expect(g.calls[1].name).toBe("b.txt")
    g.calls[1].release()
    await waitFor(() => expect(names()).toEqual(["b.txt"]))
  })

  it("logout drops pending jobs and chips", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const { rerender } = render(<ChatComposer {...props} />)
    pick([file("a1.txt"), file("a2.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    userId = null
    rerender(<ChatComposer {...props} />)
    expect(g.calls[0].signal?.aborted).toBe(true)
    expect(screen.queryByText("a1.txt")).toBeNull()
    g.calls[0].release()
    await new Promise((r) => setTimeout(r, 30))
    expect(g.calls).toHaveLength(1)
  })

  it("a workspace switch is an identity change too", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const { rerender } = render(<ChatComposer {...props} />)
    pick([file("w1.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    workspaceId = "ws-2"
    rerender(<ChatComposer {...props} />)
    expect(screen.queryByText("w1.txt")).toBeNull()
    expect(g.calls[0].signal?.aborted).toBe(true)
  })

  it("unmounting the composer cancels queued and running uploads", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    render(<ChatComposer {...props} />)
    pick([file("u1.txt"), file("u2.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    cleanup()
    expect(g.calls[0].signal?.aborted).toBe(true)
    g.calls[0].release()
    await new Promise((r) => setTimeout(r, 30))
    expect(g.calls).toHaveLength(1) // u2 never started
    expect(Object.values(useComposerStore.getState().attachments).flat().map((a) => a.status)).not.toContain("uploading")
  })

  it("a job still creating the chat row when the human changes never sends the file", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    let finish!: (ok: boolean) => void
    const ensureSession = vi.fn(() => new Promise<boolean>((r) => (finish = r)))
    const { rerender } = render(<ChatComposer {...props} ensureSession={ensureSession} />)
    pick([file("late.txt")])
    await waitFor(() => expect(ensureSession).toHaveBeenCalled())
    userId = "user-b"
    rerender(<ChatComposer {...props} ensureSession={ensureSession} />)
    await act(async () => finish(true))
    await new Promise((r) => setTimeout(r, 30))
    expect(g.calls).toHaveLength(0) // nothing went out under B's cookies
    expect(names()).toEqual([])
  })
})
