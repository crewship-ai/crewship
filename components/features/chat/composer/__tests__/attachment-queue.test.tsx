import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))
vi.mock("../mention-autocomplete", () => ({ MentionAutocomplete: () => null }))
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { error: (m: string, o?: unknown) => toastError(m, o), success: vi.fn(), info: vi.fn() },
}))

import { ChatComposer } from "../chat-composer"
import { useComposerStore } from "@/stores/composer-store"

let sid = "sess-0"
let n = 0
const props = {
  agentId: "agent-1",
  get sessionId() { return sid },
  isStreaming: false,
  connectionStatus: "connected",
  variant: "desktop" as const,
  stopGeneration: vi.fn(),
  ensureSession: vi.fn(async () => true),
  sendMessage: vi.fn(),
}

const file = (name: string, size = 7) => new File([new Uint8Array(size)], name, { type: "text/plain" })
const list = () => useComposerStore.getState().attachments[sid] ?? []

/** A fetch whose responses the test releases one by one. */
function gatedFetch() {
  const calls: Array<{ name: string; release: () => void; signal?: AbortSignal | null }> = []
  const impl = vi.fn((_url: string, init?: RequestInit) => {
    const name = ((init?.body as FormData).get("file") as File).name
    return new Promise((resolve, reject) => {
      const entry = {
        name,
        signal: init?.signal,
        release: () =>
          resolve({
            ok: true,
            status: 200,
            json: async () => ({ path: `attachments/${sid}/${name}`, agent_path: `/o/${name}` }),
          }),
      }
      init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))
      calls.push(entry)
    })
  })
  return { impl, calls }
}

function pick(files: File[]) {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]:not([aria-label])')!
  Object.defineProperty(input, "files", { value: files, configurable: true })
  fireEvent.change(input)
}

describe("one attachment queue", () => {
  beforeEach(() => {
    sid = `sess-${++n}`
    props.sendMessage.mockClear()
    useComposerStore.setState({ attachments: {}, drafts: {} })
    toastError.mockClear()
  })
  afterEach(() => vi.restoreAllMocks())

  it("uploads one file at a time; the rest wait", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    render(<ChatComposer {...props} />)
    pick([file("a.txt"), file("b.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    expect(list().map((a) => a.status)).toEqual(["uploading", "validating"])
    expect(screen.getByText("Waiting…")).toBeTruthy()
    g.calls[0].release()
    await waitFor(() => expect(g.calls).toHaveLength(2))
    g.calls[1].release()
    await waitFor(() => expect(list().map((a) => a.status)).toEqual(["ready", "ready"]))
  })

  it("cancel keeps the chip as cancelled, skips the queued file, and retry re-queues it", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    render(<ChatComposer {...props} />)
    pick([file("a.txt"), file("b.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    fireEvent.click(screen.getByLabelText("Cancel b.txt"))
    expect(list()[1].status).toBe("cancelled")
    fireEvent.click(screen.getByLabelText("Cancel a.txt"))
    await waitFor(() => expect(list()[0].status).toBe("cancelled"))
    expect(g.calls).toHaveLength(1) // b never started
    expect(screen.getAllByText("Cancelled — not attached")).toHaveLength(2)
    fireEvent.click(screen.getByLabelText("Retry b.txt"))
    await waitFor(() => expect(g.calls).toHaveLength(2))
    expect(g.calls[1].name).toBe("b.txt")
    g.calls[1].release()
    await waitFor(() => expect(list()[1].status).toBe("ready"))
  })

  it("refuses files past the per-message count and says why", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    render(<ChatComposer {...props} />)
    pick(Array.from({ length: 12 }, (_, i) => file(`f${i}.txt`)))
    expect(list()).toHaveLength(10)
    expect(toastError).toHaveBeenCalledTimes(2)
    expect(String(toastError.mock.calls[0][0])).toMatch(/at most 10 files/)
  })

  it("a pasted file joins the queue and the text paste is untouched", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    render(<ChatComposer {...props} />)
    const ta = document.querySelector("textarea")!
    const shot = file("shot.png")
    fireEvent.paste(ta, { clipboardData: { files: [shot], items: [] } })
    await waitFor(() => expect(g.calls).toHaveLength(1))
    expect(list()[0].name).toBe("shot.png")
    const text = fireEvent.paste(ta, { clipboardData: { files: [], items: [] } })
    expect(text).toBe(true) // not default-prevented
    expect(list()).toHaveLength(1)
  })

  it("Send is refused while a file waits in the queue", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    render(<ChatComposer {...props} />)
    pick([file("a.txt"), file("b.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    fireEvent.submit(document.querySelector("form")!)
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(props.sendMessage).not.toHaveBeenCalled()
  })
})
