import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react"

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

describe("review blockers", () => {
  beforeEach(() => {
    sid = `sess-${++n}`
    props.sendMessage.mockClear()
    useComposerStore.setState({ attachments: {}, drafts: {} })
    toastError.mockClear()
  })
  afterEach(() => vi.restoreAllMocks())

  it("a mixed text+file paste attaches the file AND keeps the text", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    render(<ChatComposer {...props} />)
    const ta = document.querySelector("textarea")!
    fireEvent.change(ta, { target: { value: "ab" } })
    ta.setSelectionRange(1, 1)
    const data = {
      files: [file("cell.png")],
      items: [{ kind: "file", type: "image/png", getAsFile: () => file("cell.png") }],
      getData: (t: string) => (t === "text/plain" ? "XY" : ""),
    }
    fireEvent.paste(ta, { clipboardData: data })
    await waitFor(() => expect(g.calls).toHaveLength(1))
    await waitFor(() => expect(ta.value).toBe("aXYb"))
  })

  it("cancel then retry of a waiting file uploads it exactly once", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    render(<ChatComposer {...props} />)
    pick([file("a.txt"), file("b.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    fireEvent.click(screen.getByLabelText("Cancel b.txt"))
    fireEvent.click(screen.getByLabelText("Retry b.txt"))
    g.calls[0].release()
    await waitFor(() => expect(g.calls).toHaveLength(2))
    g.calls[1].release()
    await waitFor(() => expect(list().map((a) => a.status)).toEqual(["ready", "ready"]))
    expect(g.calls.map((c) => c.name)).toEqual(["a.txt", "b.txt"])
  })

  it("cancelling a running upload aborts its request without an error toast", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    render(<ChatComposer {...props} />)
    pick([file("a.txt")])
    await waitFor(() => expect(g.calls).toHaveLength(1))
    fireEvent.click(screen.getByLabelText("Cancel a.txt"))
    expect(g.calls[0].signal?.aborted).toBe(true)
    await waitFor(() => expect(list()[0].status).toBe("cancelled"))
    expect(toastError).not.toHaveBeenCalled()
  })

  it("retry respects the per-message limit", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    const mk = (i: number, status: "ready" | "cancelled") => ({
      id: `x${i}`, name: `x${i}.txt`, size: 5, type: "text/plain", status,
      path: status === "ready" ? `attachments/${sid}/x${i}.txt` : undefined,
      file: status === "cancelled" ? file(`x${i}.txt`) : undefined,
    })
    useComposerStore.setState({
      attachments: { [sid]: [mk(0, "cancelled"), ...Array.from({ length: 10 }, (_, i) => mk(i + 1, "ready"))] },
    })
    render(<ChatComposer {...props} />)
    fireEvent.click(screen.getByLabelText("Retry x0.txt"))
    expect(String(toastError.mock.calls[0]?.[0])).toMatch(/at most 10 files/)
    expect(g.calls).toHaveLength(0)
    expect(list()[0].status).toBe("cancelled")
  })

  it("send is blocked, naming the files, while a failed or cancelled chip is present", async () => {
    useComposerStore.setState({
      attachments: { [sid]: [
        { id: "r", name: "ok.txt", size: 5, type: "text/plain", status: "ready", path: `attachments/${sid}/ok.txt` },
        { id: "c", name: "gone.txt", size: 5, type: "text/plain", status: "cancelled" },
      ] },
    })
    render(<ChatComposer {...props} />)
    fireEvent.change(document.querySelector("textarea")!, { target: { value: "hello" } })
    fireEvent.submit(document.querySelector("form")!)
    await waitFor(() => expect(screen.getAllByRole("status").map((e) => e.textContent).join()).toContain("gone.txt"))
    expect(props.sendMessage).not.toHaveBeenCalled()
  })
})

describe("blocked send explains itself", () => {
  beforeEach(() => {
    sid = `sess-${++n}`
    props.sendMessage.mockClear()
    useComposerStore.setState({ attachments: {}, drafts: {} })
    toastError.mockClear()
  })
  afterEach(() => vi.restoreAllMocks())

  const failed = (status: "error" | "cancelled" = "error", name = "bad.txt") => ({
    id: `f-${name}`, name, size: 5, type: "text/plain", status, file: file(name),
    error: status === "error" ? "Upload failed" : undefined,
  })
  const typeHello = () =>
    fireEvent.change(document.querySelector("textarea")!, { target: { value: "hello" } })
  const send = () => screen.getByLabelText("Submit") as HTMLButtonElement
  /** The state every blocked test starts from, asserted before acting. */
  const expectBlocked = () => {
    expect(send().getAttribute("aria-disabled")).toBe("true")
    const id = send().getAttribute("aria-describedby")
    expect(id).toBeTruthy()
    const notice = document.getElementById(id!)
    expect(notice).toBeTruthy()
    expect(notice!.textContent).toMatch(/Send is off/i)
    return notice!
  }
  const expectUnblocked = () => {
    expect(send().getAttribute("aria-disabled")).toBeNull()
    expect(send().getAttribute("aria-describedby")).toBeNull()
    expect(screen.queryByText(/Send is off/i)).toBeNull()
  }

  it.each(["error", "cancelled"] as const)(
    "a %s attachment blocks Send and shows a linked inline reason in words",
    (status) => {
      useComposerStore.setState({ attachments: { [sid]: [failed(status)] } })
      render(<ChatComposer {...props} />)
      typeHello()
      const notice = expectBlocked()
      expect(notice.textContent).toContain("bad.txt")
      expect(notice.textContent).toMatch(/Retry or Remove/)
    },
  )

  it("Send stays focusable and a click or Enter does not send but announces the reason", async () => {
    useComposerStore.setState({ attachments: { [sid]: [failed()] } })
    render(<ChatComposer {...props} />)
    typeHello()
    expect(send().disabled).toBe(false) // aria-disabled, so it can take focus
    send().focus()
    expect(document.activeElement).toBe(send())
    const live = () => screen.getAllByRole("status").map((e) => e.textContent).join()
    fireEvent.click(send())
    await waitFor(() => expect(live()).toContain("Send is off"))
    expect(props.sendMessage).not.toHaveBeenCalled()
    fireEvent.keyDown(document.querySelector("textarea")!, { key: "Enter" })
    await waitFor(() => expect(live()).toContain("bad.txt"))
    expect(props.sendMessage).not.toHaveBeenCalled()
  })

  it("the live region is mounted before the block and filled when it starts", () => {
    render(<ChatComposer {...props} />)
    const region = screen.getByRole("status")
    expect(region.textContent).toBe("")
    act(() => {
      useComposerStore.setState({ attachments: { [sid]: [failed()] } })
    })
    expect(screen.getByRole("status")).toBe(region)
    expect(region.textContent).toContain("bad.txt")
  })

  it("names at most two files, then a count", () => {
    useComposerStore.setState({
      attachments: { [sid]: ["a", "b", "c", "d"].map((x) => failed("error", `${x}.txt`)) },
    })
    render(<ChatComposer {...props} />)
    const text = expectBlocked().textContent!
    expect(text).toContain("a.txt, b.txt and 2 more")
    expect(text).not.toContain("c.txt")
  })

  it("two composers get distinct notice ids", () => {
    useComposerStore.setState({ attachments: { [sid]: [failed()] } })
    render(<><ChatComposer {...props} /><ChatComposer {...props} /></>)
    const ids = screen.getAllByLabelText("Submit").map((b) => b.getAttribute("aria-describedby"))
    expect(new Set(ids).size).toBe(2)
  })

  it("Retry is a focusable native button and restarts the upload", async () => {
    const g = gatedFetch()
    vi.spyOn(globalThis, "fetch").mockImplementation(g.impl as never)
    useComposerStore.setState({ attachments: { [sid]: [failed()] } })
    render(<ChatComposer {...props} />)
    typeHello()
    expectBlocked()
    const retry = screen.getByRole("button", { name: "Retry bad.txt" })
    // jsdom has no Tab order or Enter-on-button synthesis (and user-event is not
    // a dependency): assert what makes both work in a browser. A native, enabled,
    // non-negative-tabindex button takes focus and activates on Enter/Space.
    expect(retry.tagName).toBe("BUTTON")
    expect(retry.getAttribute("tabindex")).not.toBe("-1")
    expect((retry as HTMLButtonElement).disabled).toBe(false)
    retry.focus()
    expect(document.activeElement).toBe(retry)
    fireEvent.click(retry)
    await waitFor(() => expect(g.calls).toHaveLength(1))
    expect(g.calls[0].name).toBe("bad.txt")
    g.calls[0].release()
    await waitFor(() => expect(list()[0].status).toBe("ready"))
    expectUnblocked()
  })

  it("Remove is a focusable native button and clears the block", async () => {
    useComposerStore.setState({ attachments: { [sid]: [failed()] } })
    render(<ChatComposer {...props} />)
    typeHello()
    expectBlocked()
    const remove = screen.getByRole("button", { name: "Remove bad.txt" })
    // jsdom has no Tab order or Enter-on-button synthesis (and user-event is not
    // a dependency): assert what makes both work in a browser. A native, enabled,
    // non-negative-tabindex button takes focus and activates on Enter/Space.
    expect(remove.tagName).toBe("BUTTON")
    expect(remove.getAttribute("tabindex")).not.toBe("-1")
    expect((remove as HTMLButtonElement).disabled).toBe(false)
    remove.focus()
    expect(document.activeElement).toBe(remove)
    fireEvent.click(remove)
    expect(list()).toHaveLength(0)
    expectUnblocked()
  })
})
