import React from "react"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { BRIDGE_HANDSHAKE_TIMEOUT_MS, type TransportResponse, type WorkbookSession, type WorkbookTransport } from "@/lib/workbooks/bridge"
import { WORKBOOK_SESSION_REOPEN_MAX_MS, type WorkbookEditorClient } from "@/lib/workbooks/editor-client"
import { PageWorkbookSurface, WORKBOOK_HEAD_POLL_CONNECTED_MS, WORKBOOK_HEAD_POLL_MS, WORKBOOK_SESSION_HEARTBEAT_MS, WORKBOOK_SESSION_REOPEN_MIN_MS, createEditorFrame, workbookSaveLabel } from "../workbook-editor-host"
import { initialSaveState } from "@/lib/editor-lifecycle/save-state"

const requests = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: requests, broadcastSessionExpired: vi.fn(), tryRefresh: vi.fn(async () => "ok") }))
/** Realtime as the Pages layout provides it: event handlers and channel subscriptions are recorded. */
const realtime = vi.hoisted(() => ({ handlers: new Map<string, Set<(event: { type: string; payload: Record<string, unknown> }) => void>>(), channels: [] as string[], status: null as string | null }))
vi.mock("@/hooks/use-realtime", async () => {
  const React = await import("react")
  return {
    useRealtimeEventSafe: (type: string, callback: (event: { type: string; payload: Record<string, unknown> }) => void) => {
      const ref = React.useRef(callback); ref.current = callback
      React.useEffect(() => {
        const handler = (event: { type: string; payload: Record<string, unknown> }) => ref.current(event)
        const set = realtime.handlers.get(type) ?? new Set()
        realtime.handlers.set(type, set); set.add(handler)
        return () => { set.delete(handler) }
      }, [type])
    },
    useRealtimeChannelSafe: (channel: string | null) => {
      React.useEffect(() => {
        if (!channel) return
        realtime.channels.push(channel)
        return () => { realtime.channels.splice(realtime.channels.indexOf(channel), 1) }
      }, [channel])
    },
    useRealtimeStatusSafe: () => realtime.status,
  }
})
const push = (type: string, payload: Record<string, unknown>) => act(async () => { for (const handler of realtime.handlers.get(type) ?? []) handler({ type, payload }) })

const DIGEST = "b".repeat(64)
/** The session's bundle pin is the same plain hex digest as in the editor URL and the frame's hello. */
const BUNDLE = DIGEST
const EDITOR_URL = `https://editor.test/b/${DIGEST}/index.html`
const SID = "ws_session_secret"
const session: WorkbookSession = { session_id: SID, actor_label: "Owner", rev: 41, generation: 1, protocol: 1, bundle_digest: BUNDLE, editor_url: EDITOR_URL }
const ok = (body: unknown): TransportResponse => ({ status: 200, body })
const receipt = (seq: number, rev: number, durability = "node") => ok({
  operation_key: { artifact_id: "page-1", authority_id: "user:u1", mutation_id: `${SID}:${seq}` }, outcome: "applied", rev, durability,
})
const mutation = (seq: number, base: number, cell: string, input: string) => JSON.stringify({ type: "mutation", protocol: 1, seq, base_rev: base, ops: [{ op: "cell.set", sheet: "Sheet1", cell, input }] })

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}

// happy-dom has no credentialless iframes (Chromium reflects the attribute as a boolean property)
// and loads no frame pages, so a connected frame gets a stand-in window whose postMessage is recorded.
const credentiallessDescriptor = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, "credentialless")
const contentWindowDescriptor = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, "contentWindow")
type FakeWindow = { postMessage: ReturnType<typeof vi.fn> }
let frameWindows = new WeakMap<HTMLIFrameElement, FakeWindow>()
beforeEach(() => {
  frameWindows = new WeakMap()
  Object.defineProperty(HTMLIFrameElement.prototype, "credentialless", { configurable: true, get() { return (this as HTMLIFrameElement).hasAttribute("credentialless") } })
  Object.defineProperty(HTMLIFrameElement.prototype, "contentWindow", { configurable: true, get() {
    const frame = this as HTMLIFrameElement
    if (!frame.isConnected) return null
    let win = frameWindows.get(frame)
    if (!win) frameWindows.set(frame, win = { postMessage: vi.fn() })
    return win
  } })
  requests.mockImplementation(async () => new Response(JSON.stringify({ error: "Workbook not found" }), { status: 404 }))
})
const ports: MessagePort[] = []
afterEach(() => {
  for (const port of ports.splice(0)) port.close()
  cleanup()
  requests.mockReset()
  if (contentWindowDescriptor) Object.defineProperty(HTMLIFrameElement.prototype, "contentWindow", contentWindowDescriptor)
  if (credentiallessDescriptor) Object.defineProperty(HTMLIFrameElement.prototype, "credentialless", credentiallessDescriptor)
  else delete (HTMLIFrameElement.prototype as { credentialless?: unknown }).credentialless
})

function setup(options: { enabled?: boolean; open?: Awaited<ReturnType<WorkbookEditorClient["openSession"]>>; transport?: Partial<WorkbookTransport>; head?: WorkbookEditorClient["head"]; checkSession?: WorkbookEditorClient["checkSession"] } = {}) {
  const transport = {
    snapshot: vi.fn(async () => ok({ schema_version: 1, rev: 41, generation: 1, snapshot: { sheets: [] } })),
    edit: vi.fn(async (): Promise<TransportResponse> => ({ status: 500, body: null })),
    commits: vi.fn(async (since: number) => ok({ head_rev: since, generation: 1, commits: [] })),
    operation: vi.fn(async (): Promise<TransportResponse> => ({ status: 404, body: null })),
    closeSession: vi.fn(async () => {}),
    ...options.transport,
  }
  const client: WorkbookEditorClient = {
    editorEnabled: vi.fn(async () => options.enabled ?? true),
    openSession: vi.fn(async () => options.open ?? { ok: true as const, session }),
    transport: vi.fn(() => transport),
    head: vi.fn(options.head ?? (async () => ({ ok: true as const, head: { rev: 41, generation: 1 } }))),
    checkSession: vi.fn(options.checkSession ?? (async () => "live" as const)),
  }
  const dirty: boolean[] = []
  const view = render(<PageWorkbookSurface workspaceId="ws" pageId="page-1" name="Budget" client={client} onDirtyChange={value => dirty.push(value)} />)
  const frame = () => document.querySelector<HTMLIFrameElement>("iframe")
  /** The frame's window, with `postMessage` captured so the transferred port can be taken. */
  const frameWindow = () => frame()!.contentWindow as unknown as FakeWindow
  const load = () => act(async () => { frame()!.dispatchEvent(new Event("load")) })
  const message = (data: unknown, { origin = "null", source = frameWindow() as unknown as MessageEventSource }: { origin?: string; source?: MessageEventSource } = {}) =>
    act(async () => { window.dispatchEvent(new MessageEvent("message", { data, origin, source })) })
  const hello = (bundle = BUNDLE) => message({ type: "gridcraft:hello", protocol: 1, bundle })
  const fromFrame: unknown[] = []
  let port: MessagePort | undefined
  /** Load, hello, and the port the host transferred in connect. */
  const connect = async () => {
    await waitFor(() => expect(frame()).not.toBeNull())
    const win = frameWindow()
    await load()
    await hello()
    const calls = win.postMessage.mock.calls
    expect(calls).toHaveLength(1)
    port = (calls[0][2] as unknown as MessagePort[])[0]
    ports.push(port)
    port.onmessage = event => fromFrame.push(JSON.parse(event.data as string))
    await waitFor(() => expect(fromFrame).toContainEqual({ type: "online", protocol: 1 }))
    return { win, port }
  }
  const send = (data: string) => act(async () => { port!.postMessage(data) })
  const status = () => screen.getByTestId("workbook-save-state").textContent
  return { view, client, transport, frame, frameWindow, load, message, hello, connect, send, status, dirty, fromFrame }
}

describe("createEditorFrame", () => {
  it("sets every attribute before src, and src last", () => {
    const seen: Record<string, string | null>[] = []
    const real = document.createElement("iframe")
    Object.defineProperty(real, "src", { set() { seen.push({ sandbox: real.getAttribute("sandbox"), credentialless: real.getAttribute("credentialless"), referrerpolicy: real.getAttribute("referrerpolicy") }) } })
    const doc = { createElement: () => real } as unknown as Document
    expect(createEditorFrame(doc, EDITOR_URL, () => {})).toBe(real)
    expect(seen).toEqual([{ sandbox: "allow-scripts", credentialless: "", referrerpolicy: "no-referrer" }])
  })

  it("shows the configuration message, not a protocol failure, when the frame attributes do not stick", async () => {
    Object.defineProperty(HTMLIFrameElement.prototype, "credentialless", { configurable: true, get: () => false })
    const t = setup()
    expect(await screen.findByTestId("workbook-editor-notice")).toHaveTextContent("cannot be opened safely in this browser")
    expect(screen.getByTestId("workbook-editor-notice")).toHaveAttribute("data-reason", "config")
    expect(screen.queryByRole("button", { name: "Open editor again" })).toBeNull()
    expect(t.frame()).toBeNull()
    await waitFor(() => expect(t.transport.closeSession).toHaveBeenCalledTimes(1))
  })

  it("refuses to assign src when the browser does not keep credentialless", () => {
    Object.defineProperty(HTMLIFrameElement.prototype, "credentialless", { configurable: true, get: () => false })
    const real = document.createElement("iframe")
    const src = vi.fn()
    Object.defineProperty(real, "src", { set: src })
    expect(createEditorFrame({ createElement: () => real } as unknown as Document, EDITOR_URL, () => {})).toBeNull()
    expect(src).not.toHaveBeenCalled()
  })
})

describe("mounting", () => {
  it("keeps the read-only preview while the flag is off", async () => {
    const t = setup({ enabled: false })
    await screen.findByText("Workbook unavailable")
    expect(t.client.openSession).not.toHaveBeenCalled()
    expect(t.frame()).toBeNull()
  })

  it("keeps the preview in a browser without credentialless frames", async () => {
    delete (HTMLIFrameElement.prototype as { credentialless?: unknown }).credentialless
    const t = setup()
    await waitFor(() => expect(t.client.editorEnabled).toHaveBeenCalled())
    await screen.findByText("Workbook unavailable")
    expect(t.client.openSession).not.toHaveBeenCalled()
  })

  it("falls back to the preview when the server will not open a session", async () => {
    const t = setup({ open: { ok: false, status: 503 } })
    expect(await screen.findByTestId("workbook-editor-notice")).toHaveTextContent("The workbook editor is turned off. Showing the read-only preview.")
    expect(t.frame()).toBeNull()
  })

  it.each([
    ["the Studio origin outside the same-host editor path", `${window.location.origin}/b/${DIGEST}/index.html`],
    ["another path of the Studio origin", `${window.location.origin}/_editor/b/${DIGEST}/../../../api/index.html`],
    ["plain HTTP", `http://editor.test/b/${DIGEST}/index.html`],
    ["no editor URL", undefined],
    ["another bundle than the session's pin", `https://editor.test/b/${"e".repeat(64)}/index.html`],
  ])("never frames an editor at %s, and closes the session", async (_, editorUrl) => {
    const t = setup({ open: { ok: true, session: { ...session, editor_url: editorUrl } } })
    expect(await screen.findByText(/cannot be opened safely/)).toBeInTheDocument()
    expect(t.frame()).toBeNull()
    expect(t.transport.closeSession).toHaveBeenCalledTimes(1)
  })

  it("renders exactly the required frame attributes, with no session data in the URL", async () => {
    const t = setup()
    await waitFor(() => expect(t.frame()).not.toBeNull())
    const frame = t.frame()!
    expect(frame.getAttribute("sandbox")).toBe("allow-scripts")
    expect(frame.hasAttribute("credentialless")).toBe(true)
    expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer")
    expect(frame.getAttribute("src")).toBe(`${EDITOR_URL}?host=authority&parent_origin=${encodeURIComponent(window.location.origin)}&v=1`)
    expect(frame.getAttribute("src")).not.toContain(SID)
    expect(t.status()).toBe("Connecting…")
  })
})

describe("handshake", () => {
  it("connects our frame after its load and sends the port once, never the session id", async () => {
    const t = setup()
    const { win } = await t.connect()
    const [connect, target] = win.postMessage.mock.calls[0]
    expect(target).toBe("*")
    expect(connect).toMatchObject({ type: "gridcraft:connect", protocol: 1, page_label: "Budget", rev: 41, generation: 1 })
    expect(JSON.stringify(connect)).not.toContain(SID)
    await waitFor(() => expect(t.status()).toBe("No unsaved changes"))
  })

  it("ignores a hello from another window", async () => {
    const t = setup()
    await waitFor(() => expect(t.frame()).not.toBeNull())
    const win = t.frameWindow()
    await t.load()
    await t.message({ type: "gridcraft:hello", protocol: 1, bundle: BUNDLE }, { source: window as unknown as MessageEventSource })
    expect(win.postMessage).not.toHaveBeenCalled()
    expect(t.frame()).not.toBeNull()
  })

  it.each([
    ["a real (non-sandboxed) origin", { origin: "https://editor.test" }, BUNDLE],
    ["the host origin", { origin: window.location.origin }, BUNDLE],
    ["a wrong bundle digest", {}, "sha256:other"],
  ])("removes the frame on a hello from %s", async (_, from, bundle) => {
    const t = setup()
    await waitFor(() => expect(t.frame()).not.toBeNull())
    const win = t.frameWindow()
    await t.load()
    await t.message({ type: "gridcraft:hello", protocol: 1, bundle }, from)
    expect(win.postMessage).not.toHaveBeenCalled()
    expect(t.frame()).toBeNull()
    expect(screen.getByTestId("workbook-editor-notice")).toHaveTextContent("The editor was closed because its frame broke the protocol")
    expect(t.transport.closeSession).toHaveBeenCalled()
  })

  it("removes the frame on a hello before its first load", async () => {
    const t = setup()
    await waitFor(() => expect(t.frame()).not.toBeNull())
    const win = t.frameWindow()
    await t.hello()
    expect(win.postMessage).not.toHaveBeenCalled()
    expect(t.frame()).toBeNull()
  })

  it("removes the frame on a second load before the hello", async () => {
    const t = setup()
    await waitFor(() => expect(t.frame()).not.toBeNull())
    const win = t.frameWindow()
    await t.load()
    await t.load()
    expect(t.frame()).toBeNull()
    expect(win.postMessage).not.toHaveBeenCalled()
  })

  it("removes the frame when it loads another document after the handshake, and says what was lost", async () => {
    const pending = deferred<TransportResponse>()
    const t = setup({ transport: { edit: vi.fn(() => pending.promise) } })
    const { win } = await t.connect()
    await t.send(mutation(1, 41, "B3", "21"))
    await waitFor(() => expect(t.status()).toBe("Saving…"))
    const frame = t.frame()!
    await act(async () => { frame.dispatchEvent(new Event("load")) })
    expect(t.frame()).toBeNull()
    expect(screen.getByTestId("workbook-editor-notice")).toHaveTextContent("1 change not saved")
    // The port is never offered again.
    expect(win.postMessage).toHaveBeenCalledTimes(1)
    expect(screen.getByRole("button", { name: "Open editor again" })).toBeInTheDocument()
  })
})

describe("failures fall back to the preview", () => {
  it("shows the preview with a notice and a reopen button when the snapshot cannot be read at connect", async () => {
    const t = setup({ transport: { snapshot: vi.fn(async () => ({ status: 500, body: { error: "boom" } })) } })
    await waitFor(() => expect(t.frame()).not.toBeNull())
    await t.load()
    await t.hello()
    const notice = await screen.findByTestId("workbook-editor-notice")
    expect(notice).toHaveAttribute("data-reason", "load")
    expect(notice).toHaveTextContent("could not load the workbook")
    expect(screen.queryByText("Connecting…")).toBeNull()
    expect(t.frame()).toBeNull()
    expect(t.transport.closeSession).toHaveBeenCalledTimes(1)
    expect(await screen.findByText("Workbook unavailable")).toBeInTheDocument() // the preview, here without data
    await act(async () => { screen.getByRole("button", { name: "Open editor again" }).click() })
    await waitFor(() => expect(t.client.openSession).toHaveBeenCalledTimes(2))
  })

  it("offers to open the editor again after 429 too_many_editor_sessions, and opens it by itself after Retry-After", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const t = setup({ open: { ok: false, status: 429, retryAfterMs: 120_000 } })
      const notice = await screen.findByTestId("workbook-editor-notice")
      expect(notice).toHaveAttribute("data-reason", "unavailable")
      expect(notice).toHaveTextContent("Too many editor windows are open for this workbook")
      expect(notice).toHaveTextContent("in about 2 minutes")
      expect(screen.getByRole("button", { name: "Open editor again" })).toBeInTheDocument()
      expect(t.client.openSession).toHaveBeenCalledTimes(1)
      await act(async () => { await vi.advanceTimersByTimeAsync(119_000) })
      expect(t.client.openSession).toHaveBeenCalledTimes(1)
      vi.mocked(t.client.openSession).mockResolvedValueOnce({ ok: true, session })
      await act(async () => { await vi.advanceTimersByTimeAsync(1_000) })
      await waitFor(() => expect(t.client.openSession).toHaveBeenCalledTimes(2))
      await waitFor(() => expect(t.frame()).not.toBeNull())
    } finally {
      vi.useRealTimers()
    }
  })

  it("bounds the scheduled reopen after 429 between a floor and 15 minutes", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const t = setup({ open: { ok: false, status: 429, retryAfterMs: 4 * 60 * 60_000 } })
      expect(await screen.findByTestId("workbook-editor-notice")).toHaveTextContent("in about 15 minutes")
      await act(async () => { await vi.advanceTimersByTimeAsync(WORKBOOK_SESSION_REOPEN_MAX_MS - 1000) })
      expect(t.client.openSession).toHaveBeenCalledTimes(1)
      await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
      await waitFor(() => expect(t.client.openSession).toHaveBeenCalledTimes(2))
      cleanup()

      const soon = setup({ open: { ok: false, status: 429, retryAfterMs: 1000 } })
      await screen.findByTestId("workbook-editor-notice")
      await act(async () => { await vi.advanceTimersByTimeAsync(WORKBOOK_SESSION_REOPEN_MIN_MS - 1000) })
      expect(soon.client.openSession).toHaveBeenCalledTimes(1)
      await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
      await waitFor(() => expect(soon.client.openSession).toHaveBeenCalledTimes(2))
    } finally {
      vi.useRealTimers()
    }
  })

  it("schedules no reopen for other refusals to open", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const t = setup({ open: { ok: false, status: 404, refusal: "access" } })
      await screen.findByTestId("workbook-editor-notice")
      expect(screen.queryByRole("button", { name: "Open editor again" })).toBeNull()
      await act(async () => { await vi.advanceTimersByTimeAsync(WORKBOOK_SESSION_REOPEN_MAX_MS) })
      expect(t.client.openSession).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it("ends a session whose editor never says hello, and shows the preview", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const t = setup()
      await waitFor(() => expect(t.frame()).not.toBeNull())
      await t.load()
      await act(async () => { await vi.advanceTimersByTimeAsync(BRIDGE_HANDSHAKE_TIMEOUT_MS) })
      const notice = await screen.findByTestId("workbook-editor-notice")
      expect(notice).toHaveAttribute("data-reason", "timeout")
      expect(notice).toHaveTextContent("did not start")
      expect(t.frame()).toBeNull()
      expect(t.transport.closeSession).toHaveBeenCalledTimes(1)
      expect(screen.getByRole("button", { name: "Open editor again" })).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it("removes the frame when an edit finds the editor turned off, without retrying", async () => {
    const t = setup({ transport: { edit: vi.fn(async () => ({ status: 503, body: { error: "gridcraft_editor_disabled" } })) } })
    await t.connect()
    await t.send(mutation(1, 41, "B3", "21"))
    await waitFor(() => expect(t.frame()).toBeNull())
    const notice = screen.getByTestId("workbook-editor-notice")
    expect(notice).toHaveAttribute("data-reason", "disabled")
    expect(notice).toHaveTextContent("The workbook editor is turned off · 1 change not saved. Showing the read-only preview.")
    expect(screen.queryByRole("button", { name: "Open editor again" })).toBeNull()
    expect(t.transport.edit).toHaveBeenCalledTimes(1)
  })

  it("removes the frame when the head poll finds the editor turned off", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const t = setup({ head: async () => ({ ok: false as const, status: 503, refusal: "disabled" as const }) })
      await t.connect()
      await act(async () => { await vi.advanceTimersByTimeAsync(2100) })
      await waitFor(() => expect(t.frame()).toBeNull())
      expect(screen.getByTestId("workbook-editor-notice")).toHaveAttribute("data-reason", "disabled")
    } finally {
      vi.useRealTimers()
    }
  })

  it.each([
    ["revoked", "disabled"],
    ["disabled", "disabled"],
    ["access", "access"],
  ] as const)("rechecks the session on focus: %s ends the editor (%s)", async (check, reason) => {
    const t = setup({ checkSession: async () => check })
    await t.connect()
    expect(t.client.checkSession).not.toHaveBeenCalled()
    await act(async () => { window.dispatchEvent(new Event("focus")) })
    await waitFor(() => expect(t.frame()).toBeNull())
    expect(t.client.checkSession).toHaveBeenCalledWith(session)
    expect(screen.getByTestId("workbook-editor-notice")).toHaveAttribute("data-reason", reason)
  })

  it("opens a new session when the recheck finds the session expired, and keeps the editor", async () => {
    const t = setup({ checkSession: async () => "expired" })
    await t.connect()
    const renewed = { ...session, session_id: "ws_session_next" }
    vi.mocked(t.client.openSession).mockResolvedValueOnce({ ok: true, session: renewed })
    await act(async () => { window.dispatchEvent(new Event("focus")) })
    await waitFor(() => expect(t.client.openSession).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(t.client.transport).toHaveBeenLastCalledWith(renewed))
    expect(t.frame()).not.toBeNull()
    expect(screen.queryByTestId("workbook-editor-notice")).toBeNull()
  })

  it("sends a pending edit again under a new session when the server says the session expired", async () => {
    const edits: string[] = []
    const t = setup({ transport: { edit: vi.fn(async (id: string): Promise<TransportResponse> => {
      edits.push(id)
      return id.startsWith(`${SID}:`) ? { status: 401, body: { error: "session_expired" } } : ok({ operation_key: { artifact_id: "page-1", authority_id: "user:u1", mutation_id: id }, outcome: "applied", rev: 42, durability: "node" })
    }) } })
    await t.connect()
    vi.mocked(t.client.openSession).mockResolvedValueOnce({ ok: true, session: { ...session, session_id: "ws_session_next" } })
    await t.send(mutation(1, 41, "B3", "21"))
    await waitFor(() => expect(t.status()).toBe("Saved"))
    expect(edits).toEqual([`${SID}:1`, "ws_session_next:1"])
    expect(t.fromFrame).toContainEqual({ type: "ack", protocol: 1, seq: 1, rev: 42 })
    expect(t.frame()).not.toBeNull()
  })

  it("renews the session on a timer while the editor is open and visible, not only on focus", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const t = setup()
      await t.connect()
      expect(t.client.checkSession).not.toHaveBeenCalled()
      await act(async () => { await vi.advanceTimersByTimeAsync(WORKBOOK_SESSION_HEARTBEAT_MS + 100) })
      await waitFor(() => expect(t.client.checkSession).toHaveBeenCalledTimes(1))
      await act(async () => { await vi.advanceTimersByTimeAsync(WORKBOOK_SESSION_HEARTBEAT_MS) })
      await waitFor(() => expect(t.client.checkSession).toHaveBeenCalledTimes(2))
    } finally {
      vi.useRealTimers()
    }
  })

  it("keeps the editor when the recheck cannot reach the server", async () => {
    const t = setup({ checkSession: async () => "unknown" })
    await t.connect()
    await act(async () => { window.dispatchEvent(new Event("focus")) })
    await act(async () => { window.dispatchEvent(new Event("online")) })
    await waitFor(() => expect(t.client.checkSession).toHaveBeenCalled())
    expect(t.frame()).not.toBeNull()
  })
})

describe("save state and leave guard", () => {
  it("says Saved only after a durable server receipt, and guards leaving until then", async () => {
    const pending = deferred<TransportResponse>()
    const t = setup({ transport: { edit: vi.fn(() => pending.promise) } })
    await t.connect()
    await t.send(mutation(1, 41, "B3", "21"))
    await waitFor(() => expect(t.status()).toBe("Saving…"))
    expect(t.dirty.at(-1)).toBe(true)
    const leave = new Event("beforeunload", { cancelable: true })
    window.dispatchEvent(leave)
    expect(leave.defaultPrevented).toBe(true)

    await act(async () => { pending.resolve(receipt(1, 42)) })
    await waitFor(() => expect(t.status()).toBe("Saved"))
    expect(t.dirty.at(-1)).toBe(false)
    const after = new Event("beforeunload", { cancelable: true })
    window.dispatchEvent(after)
    expect(after.defaultPrevented).toBe(false)
    await waitFor(() => expect(t.fromFrame).toContainEqual({ type: "ack", protocol: 1, seq: 1, rev: 42 }))
    expect(screen.getByText("Revision 42")).toBeInTheDocument()
  })

  it("never says Saved for a receipt that is not durable", async () => {
    const t = setup({ transport: { edit: vi.fn(async () => receipt(1, 42, "memory")) } })
    await t.connect()
    await t.send(mutation(1, 41, "B3", "21"))
    await waitFor(() => expect(t.transport.edit).toHaveBeenCalled())
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 20)) })
    expect(t.status()).not.toBe("Saved")
    expect(t.dirty.at(-1)).toBe(true)
  })

  it("says Not saved with the server's value when the cell was changed by someone else", async () => {
    const t = setup({
      transport: {
        edit: vi.fn(async () => ({ status: 409, body: { error: "precondition_failed", head_rev: 42, conflicts: [{ cell: "B2", input: "7", rev: 42, actor_label: "Agent Ada" }] } })),
        commits: vi.fn(async () => ok({ head_rev: 42, generation: 1, commits: [{ rev: 42, kind: "cells", actor_label: "Agent Ada", own: false, ops: [{ sheet: "Sheet1", cell: "B2", input: "7" }] }] })),
      },
    })
    await t.connect()
    await t.send(mutation(1, 41, "B2", "mine"))
    await waitFor(() => expect(t.status()).toBe("Not saved · a change was refused"))
    expect(t.fromFrame).toContainEqual(expect.objectContaining({ type: "committed", rev: 42, ops: [{ op: "cell.set", sheet: "Sheet1", cell: "B2", input: "7" }] }))
    expect(t.fromFrame).toContainEqual(expect.objectContaining({ type: "rejected", seq: 1, code: "precondition_failed", cells: [{ cell: "B2", input: "7", rev: 42, actor_label: "Agent Ada" }] }))
    expect(t.dirty.at(-1)).toBe(false)
  })

  it("reads the commits feed on a workbook.committed push for its Page, and ignores other Pages", async () => {
    const t = setup({ transport: { commits: vi.fn(async (since: number) => ok({ head_rev: 42, generation: 1, commits: since < 42 ? [{ rev: 42, kind: "cells", actor_label: "Agent Ada", own: false, ops: [{ sheet: "Sheet1", cell: "B2", input: "99" }] }] : [] })) } })
    await t.connect()
    expect(realtime.channels).toContain("page:page-1")
    await push("workbook.committed", { page_id: "other", rev: 42, generation: 1, kind: "cells", cells: 1 })
    expect(t.transport.commits).not.toHaveBeenCalled()
    await push("workbook.committed", { page_id: "page-1", rev: 42, generation: 1, kind: "cells", cells: 1 })
    await waitFor(() => expect(t.fromFrame).toContainEqual({ type: "committed", protocol: 1, rev: 42, actor_label: "Agent Ada", ops: [{ op: "cell.set", sheet: "Sheet1", cell: "B2", input: "99" }] }))
  })

  it("polls the head only as a slow fallback while the realtime connection is up", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    realtime.status = "connected"
    try {
      const t = setup()
      await t.connect()
      await act(async () => { await vi.advanceTimersByTimeAsync(WORKBOOK_HEAD_POLL_MS * 3) })
      expect(t.client.head).not.toHaveBeenCalled()
      await act(async () => { await vi.advanceTimersByTimeAsync(WORKBOOK_HEAD_POLL_CONNECTED_MS) })
      await waitFor(() => expect(t.client.head).toHaveBeenCalled())
    } finally {
      realtime.status = null
      vi.useRealTimers()
    }
  })

  it("delivers a commit the head poll finds", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      let head = 41
      const t = setup({
        head: async () => ({ ok: true, head: { rev: head, generation: 1 } }),
        transport: { commits: vi.fn(async (since: number) => ok({ head_rev: head, generation: 1, commits: since < 42 && head >= 42 ? [{ rev: 42, kind: "cells", actor_label: "Agent Ada", own: false, ops: [{ sheet: "Sheet1", cell: "B2", input: "99" }] }] : [] })) },
      })
      await t.connect()
      head = 42
      await act(async () => { await vi.advanceTimersByTimeAsync(2100) })
      await waitFor(() => expect(t.fromFrame).toContainEqual({ type: "committed", protocol: 1, rev: 42, actor_label: "Agent Ada", ops: [{ op: "cell.set", sheet: "Sheet1", cell: "B2", input: "99" }] }))
    } finally {
      vi.useRealTimers()
    }
  })

  it("removes the frame when access is lost", async () => {
    const t = setup({ transport: { edit: vi.fn(async () => ({ status: 404, body: { error: "Workbook not found" } })) } })
    await t.connect()
    await t.send(mutation(1, 41, "B3", "21"))
    await waitFor(() => expect(t.frame()).toBeNull())
    expect(screen.getByTestId("workbook-editor-notice")).toHaveTextContent("Access lost · 1 change not saved")
    expect(screen.queryByRole("button", { name: "Open editor again" })).toBeNull()
  })

  it("closes the session and removes the frame on unmount", async () => {
    const t = setup()
    await t.connect()
    t.view.unmount()
    expect(document.querySelector("iframe")).toBeNull()
    await waitFor(() => expect(t.transport.closeSession).toHaveBeenCalledTimes(1))
  })
})

describe("workbookSaveLabel", () => {
  const opened = { ...initialSaveState, opened: true, canWrite: true }
  it.each([
    [initialSaveState, "Connecting…"],
    [opened, "No unsaved changes"],
    [{ ...opened, pending: 2 }, "Saving…"],
    [{ ...opened, pending: 1, trouble: "offline" as const }, "Offline · 1 change not saved"],
    [{ ...opened, pending: 1, trouble: "retrying" as const }, "Saving… · retrying"],
    [{ ...opened, receipt: true }, "Saved"],
    [{ ...opened, receipt: true, pending: 1 }, "Saving…"],
    [{ ...opened, canWrite: false, trouble: "denied" as const }, "Access lost"],
    [{ ...opened, pending: 1, trouble: "unconfirmed" as const }, "Not saved · 1 change not saved"],
    [{ ...opened, trouble: "unconfirmed" as const }, "Not saved · the server did not confirm a change"],
  ])("%j → %s", (state, label) => {
    expect(workbookSaveLabel(state)).toBe(label)
  })
})
