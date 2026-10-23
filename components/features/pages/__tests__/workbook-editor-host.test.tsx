import React from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { BRIDGE_HANDSHAKE_TIMEOUT_MS, BRIDGE_MAX_PENDING, BRIDGE_MAX_REFUSALS, type TransportResponse, type WorkbookSession, type WorkbookTransport } from "@/lib/workbooks/bridge"
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

function setup(options: { enabled?: boolean; flagError?: boolean; open?: Awaited<ReturnType<WorkbookEditorClient["openSession"]>>; transport?: Partial<WorkbookTransport>; head?: WorkbookEditorClient["head"]; checkSession?: WorkbookEditorClient["checkSession"] } = {}) {
  const transport = {
    snapshot: vi.fn(async () => ok({ schema_version: 1, rev: 41, generation: 1, snapshot: { sheets: [] } })),
    edit: vi.fn(async (): Promise<TransportResponse> => ({ status: 500, body: null })),
    commits: vi.fn(async (since: number) => ok({ head_rev: since, generation: 1, commits: [] })),
    operation: vi.fn(async (): Promise<TransportResponse> => ({ status: 404, body: null })),
    closeSession: vi.fn(async () => {}),
    ...options.transport,
  }
  const client: WorkbookEditorClient = {
    editorEnabled: vi.fn(async () => { if (options.flagError) throw new Error("offline"); return options.enabled ?? true }),
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

it("ends only the targeted workbook when the host revokes its session", async () => {
  const t = setup()
  await t.connect()
  await push("session_closed", { artifact_id: "different-workbook", session_ids: [SID], reason: "access_revoked" })
  expect(t.frame()).not.toBeNull()
  await push("session_closed", { artifact_id: "page-1", session_ids: [SID], reason: "access_revoked" })
  await waitFor(() => expect(t.frame()).toBeNull())
  expect(screen.getByTestId("workbook-editor-notice")).toHaveAttribute("data-reason", "access")
})

it("keeps a newly authorized editor when an older session closure arrives late", async () => {
  const t = setup()
  await t.connect()
  // The current SID was issued after access was restored; this old control
  // belongs to an editor session that the revoke transaction already closed.
  await push("session_closed", { artifact_id: "page-1", session_ids: ["older-editor-session"], reason: "access_revoked" })
  expect(t.frame()).not.toBeNull()
  expect(t.transport.closeSession).not.toHaveBeenCalled()
})

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
    expect(await screen.findByTestId("workbook-editor-notice")).toHaveTextContent("cannot be opened safely here")
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
    expect(await screen.findByTestId("workbook-editor-notice")).toHaveTextContent("not enabled for this workspace")
    await screen.findByText("Workbook unavailable")
    expect(t.client.openSession).not.toHaveBeenCalled()
    expect(t.frame()).toBeNull()
  })

  it("keeps the preview in a browser without credentialless frames", async () => {
    delete (HTMLIFrameElement.prototype as { credentialless?: unknown }).credentialless
    const t = setup()
    expect(await screen.findByTestId("workbook-editor-notice")).toHaveTextContent("Chrome or Edge")
    await waitFor(() => expect(t.client.editorEnabled).toHaveBeenCalled())
    await screen.findByText("Workbook unavailable")
    expect(t.client.openSession).not.toHaveBeenCalled()
  })

  it("distinguishes a failed flag read and lets the person retry", async () => {
    const t = setup({ flagError: true })
    expect(await screen.findByTestId("workbook-editor-notice")).toHaveTextContent("Could not check")
    expect(t.client.openSession).not.toHaveBeenCalled()
    vi.mocked(t.client.editorEnabled).mockResolvedValue(true)
    fireEvent.click(screen.getByRole("button", { name: "Retry editor check" }))
    await waitFor(() => expect(t.frame()).not.toBeNull())
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

  it.each([
    ["a different bundle digest", "e".repeat(64)],
    ["no bundle digest", null],
  ])("says the editor bundle did not match the approved version on a hello naming %s", async (_, bundle) => {
    const t = setup()
    await waitFor(() => expect(t.frame()).not.toBeNull())
    const win = t.frameWindow()
    await t.load()
    await t.message({ type: "gridcraft:hello", protocol: 1, bundle })
    expect(win.postMessage).not.toHaveBeenCalled()
    expect(t.frame()).toBeNull()
    const notice = screen.getByTestId("workbook-editor-notice")
    expect(notice).toHaveAttribute("data-reason", "bundle")
    expect(notice).toHaveTextContent("The editor bundle did not match the approved version. Showing the read-only preview.")
    expect(notice).not.toHaveTextContent("broke the protocol")
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
  it("guards an unsubmitted cell draft and does not call it saved", async () => {
    const t = setup()
    await t.connect()
    await t.send(JSON.stringify({ type: "draft", protocol: 1, cells: [{ sheet: "Sheet1", cell: "B3" }] }))
    await waitFor(() => expect(t.status()).toBe("Not saved · finish editing the cell"))
    expect(t.dirty.at(-1)).toBe(true)
    const leave = new Event("beforeunload", { cancelable: true })
    window.dispatchEvent(leave)
    expect(leave.defaultPrevented).toBe(true)
    expect(t.transport.edit).not.toHaveBeenCalled()
    await t.send(JSON.stringify({ type: "draft", protocol: 1, cells: [] }))
    await waitFor(() => expect(t.dirty.at(-1)).toBe(false))
    expect(t.status()).toBe("No unsaved changes")
  })

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
    // The person's value is held only by the open refusal: leaving would lose it.
    expect(t.dirty.at(-1)).toBe(true)
  })

  it("warns at once when another writer changes the cell of an open draft, without losing the draft", async () => {
    const t = setup({ transport: { commits: vi.fn(async (since: number) => ok({ head_rev: 42, generation: 1, commits: since < 42 ? [{ rev: 42, kind: "cells", actor_label: "Agent Ada", own: false, ops: [{ sheet: "Sheet1", cell: "B3", input: "99" }] }] : [] })) } })
    await t.connect()
    await t.send(JSON.stringify({ type: "draft", protocol: 1, cells: [{ sheet: "Sheet1", cell: "B3" }] }))
    await push("workbook.committed", { page_id: "page-1", rev: 42, generation: 1, kind: "cells", cells: 1 })
    const warning = await screen.findByTestId("workbook-draft-conflict")
    expect(warning).toHaveTextContent("Changed by Agent Ada while you were editing")
    expect(warning).toHaveTextContent("Sheet1!B3")
    // The draft is the editor's: the host never discards or overwrites it, and offers no choice until it is committed.
    expect(screen.queryByRole("button", { name: /^Keep mine/ })).toBeNull()
    expect(t.frame()).not.toBeNull()
    // The draft closed (committed or cancelled): the warning goes.
    await t.send(JSON.stringify({ type: "draft", protocol: 1, cells: [] }))
    await waitFor(() => expect(screen.queryByTestId("workbook-draft-conflict")).toBeNull())
  })

  it("offers keep mine or take theirs when a commit is refused because another writer changed the cell", async () => {
    const edit = vi.fn(async (id: string): Promise<TransportResponse> => id === `${SID}:h1`
      ? ok({ operation_key: { artifact_id: "page-1", authority_id: "user:u1", mutation_id: id }, outcome: "applied", rev: 43, durability: "node" })
      : { status: 409, body: { error: "precondition_failed", head_rev: 42, conflicts: [{ sheet: "Sheet1", cell: "B3", input: "99", rev: 42, actor_label: "Agent Ada" }] } })
    const history = [
      { rev: 42, kind: "cells", actor_label: "Agent Ada", own: false, ops: [{ sheet: "Sheet1", cell: "B3", input: "99" }] },
      { rev: 43, kind: "cells", actor_label: "Owner", own: true, session_id: SID, mutation_id: `${SID}:h1`, ops: [{ sheet: "Sheet1", cell: "B3", input: "777" }] },
    ]
    const commits = vi.fn(async (since: number) => {
      const visible = history.filter(c => c.rev <= (edit.mock.calls.some(([id]) => id === `${SID}:h1`) ? 43 : 42))
      return ok({ head_rev: visible.at(-1)!.rev, generation: 1, commits: visible.filter(c => c.rev > since) })
    })
    const t = setup({ transport: { edit, commits } })
    await t.connect()
    await t.send(mutation(1, 41, "B3", "777"))
    const panel = await screen.findByTestId("workbook-draft-conflict")
    expect(panel).toHaveTextContent("Changed by Agent Ada while you were editing")
    expect(panel).toHaveTextContent("777")
    expect(panel).toHaveTextContent("99")
    await act(async () => { screen.getByRole("button", { name: "Keep mine for Sheet1!B3" }).click() })
    await waitFor(() => expect(edit).toHaveBeenCalledWith(`${SID}:h1`, expect.stringContaining('"input":"777"')))
    await waitFor(() => expect(screen.queryByTestId("workbook-draft-conflict")).toBeNull())

    await waitFor(() => expect(t.fromFrame).toContainEqual(expect.objectContaining({ type: "committed", rev: 43 })))
    await t.send(mutation(2, 43, "B3", "888"))
    await screen.findByTestId("workbook-draft-conflict")
    const edits = edit.mock.calls.length
    await act(async () => { screen.getByRole("button", { name: "Take theirs for Sheet1!B3" }).click() })
    expect(screen.queryByTestId("workbook-draft-conflict")).toBeNull()
    expect(edit.mock.calls.length).toBe(edits)
  })

  const refusedOn = (cells: Record<string, string>) => vi.fn(async (_id: string, body: string): Promise<TransportResponse> => {
    const cell = (JSON.parse(body) as { ops: { cell: string }[] }).ops[0].cell
    return { status: 409, body: { error: "precondition_failed", head_rev: 42, conflicts: [{ sheet: "Sheet1", cell, input: cells[cell], rev: 42, actor_label: "Agent Ada" }] } }
  })
  const agentChanged = (cells: Record<string, string>) => vi.fn(async (since: number) => ok({ head_rev: 42, generation: 1, commits: since < 42 ? [{ rev: 42, kind: "cells", actor_label: "Agent Ada", own: false, ops: Object.entries(cells).map(([cell, input]) => ({ sheet: "Sheet1", cell, input })) }] : [] }))

  it("take theirs on the only refused change returns to no unsaved changes and clears the editor's refusal", async () => {
    const t = setup({ transport: { edit: refusedOn({ B4: "77" }), commits: agentChanged({ B4: "77" }), snapshot: vi.fn(async () => ok({ schema_version: 1, rev: 42, generation: 1, snapshot: { sheets: [] } })) } })
    await t.connect()
    await t.send(mutation(1, 41, "B4", "mine-B4"))
    await waitFor(() => expect(t.status()).toBe("Not saved · a change was refused"))
    const edits = vi.mocked(t.transport.edit).mock.calls.length
    await act(async () => { screen.getByRole("button", { name: /^Take theirs/ }).click() })
    await waitFor(() => expect(t.status()).toBe("No unsaved changes"))
    expect(screen.queryByTestId("workbook-draft-conflict")).toBeNull()
    expect(vi.mocked(t.transport.edit).mock.calls.length).toBe(edits)
    // A fresh snapshot is the only message the editor clears its own refusal notice on.
    await waitFor(() => expect(t.fromFrame.at(-1)).toEqual(expect.objectContaining({ type: "snapshot", rev: 42, discarded: [] })))
  })

  it("take theirs on one of two refused changes keeps the refused status for the other, and moves focus to it", async () => {
    const t = setup({ transport: { edit: refusedOn({ B4: "77", C5: "88" }), commits: agentChanged({ B4: "77", C5: "88" }) } })
    await t.connect()
    await t.send(mutation(1, 41, "B4", "mine-B4"))
    await t.send(mutation(2, 41, "C5", "mine-C5"))
    await waitFor(() => expect(screen.getAllByRole("button", { name: /^Take theirs/ })).toHaveLength(2))
    const snapshots = vi.mocked(t.transport.snapshot).mock.calls.length
    await act(async () => { screen.getByRole("button", { name: "Take theirs for Sheet1!B4" }).click() })
    expect(screen.getAllByRole("button", { name: /^Take theirs/ })).toHaveLength(1)
    expect(t.status()).toBe("Not saved · a change was refused")
    expect(vi.mocked(t.transport.snapshot).mock.calls.length).toBe(snapshots)
    // Focus lands on the next notice itself, not on its Keep mine: a repeated Enter decides nothing.
    const next = screen.getByRole("button", { name: "Keep mine for Sheet1!C5" }).closest<HTMLElement>("[data-kind='refused']")!
    expect(document.activeElement).toBe(next)
    expect(next.tabIndex).toBe(-1)
    expect(document.activeElement?.tagName).not.toBe("BUTTON")
    await act(async () => { screen.getByRole("button", { name: "Take theirs for Sheet1!C5" }).click() })
    await waitFor(() => expect(t.status()).toBe("No unsaved changes"))
    // No notice left: focus goes to the editor, not to the page body.
    expect(document.activeElement).toBe(t.frame())
  })

  it("past the open-refusal cap, counts the refused changes only the editor holds and stays unsaved until the person discards them", async () => {
    const t = setup({ transport: { edit: vi.fn(async (): Promise<TransportResponse> => ({ status: 400, body: { error: "invalid_ops" } })) } })
    await t.connect()
    for (let seq = 1; seq <= BRIDGE_MAX_REFUSALS + 1; seq++) await t.send(mutation(seq, 41, "B2", `x${seq}`))
    await waitFor(() => expect(t.status()).toBe(`Not saved · ${BRIDGE_MAX_REFUSALS + 1} changes were refused`))
    const overflow = screen.getByTestId("workbook-draft-conflict").querySelector<HTMLElement>("[data-kind='overflow']")!
    expect(overflow).toHaveTextContent("1 more change was refused because too many were waiting to be saved")
    expect(t.dirty.at(-1)).toBe(true)
    const snapshots = vi.mocked(t.transport.snapshot).mock.calls.length
    await act(async () => { screen.getByRole("button", { name: "Discard 1 refused change held in the editor" }).click() })
    expect(screen.getByTestId("workbook-draft-conflict").querySelector("[data-kind='overflow']")).toBeNull()
    expect(t.status()).toBe(`Not saved · ${BRIDGE_MAX_REFUSALS} changes were refused`)
    // The held refusals are still open: no reload yet.
    expect(vi.mocked(t.transport.snapshot).mock.calls.length).toBe(snapshots)
  }, 30_000)

  it("keep mine with the save queue full says to try again instead of doing nothing", async () => {
    let calls = 0
    const edit = vi.fn(async (): Promise<TransportResponse> => ++calls === 1
      ? { status: 409, body: { error: "precondition_failed", head_rev: 42, conflicts: [{ sheet: "Sheet1", cell: "B4", input: "77", rev: 42, actor_label: "Agent Ada" }] } }
      : new Promise<TransportResponse>(() => {}))
    const t = setup({ transport: { edit, commits: agentChanged({ B4: "77" }) } })
    await t.connect()
    await t.send(mutation(1, 41, "B4", "mine-B4"))
    const keep = await screen.findByRole("button", { name: "Keep mine for Sheet1!B4" })
    for (let seq = 2; seq <= BRIDGE_MAX_PENDING + 1; seq++) await t.send(mutation(seq, 42, "C9", `v${seq}`))
    await waitFor(() => expect(edit).toHaveBeenCalledTimes(2))
    await act(async () => { keep.click() })
    const row = keep.closest<HTMLElement>("[data-kind='refused']")!
    expect(row).toHaveTextContent("Too many changes are waiting to be saved. Try again once they are saved.")
    expect(row.querySelector("[role='status']")).toHaveTextContent("Try again")
    expect(edit).toHaveBeenCalledTimes(2)
  }, 30_000)

  it("a refused multi-cell edit shows every cell, says which one conflicts, and keep mine sends them all; never Saved before the receipt", async () => {
    const kept = deferred<TransportResponse>()
    const edit = vi.fn(async (id: string): Promise<TransportResponse> => id === `${SID}:h1`
      ? kept.promise
      : { status: 409, body: { error: "precondition_failed", head_rev: 42, conflicts: [{ sheet: "Sheet1", cell: "B2", input: "7", rev: 42, actor_label: "Agent Ada" }] } })
    const t = setup({ transport: { edit, commits: agentChanged({ B2: "7" }) } })
    await t.connect()
    await t.send(JSON.stringify({ type: "mutation", protocol: 1, seq: 1, base_rev: 41, ops: [["B2", "mine"], ["C2", "also mine"], ["D2", "and this"]].map(([cell, input]) => ({ op: "cell.set", sheet: "Sheet1", cell, input })) }))
    const panel = await screen.findByTestId("workbook-draft-conflict")
    const row = panel.querySelector<HTMLElement>("[data-kind='refused']")!
    expect(row).toHaveTextContent("Changed by Agent Ada while you were editing: Sheet1!B2. Yours: mine · Theirs: 7")
    expect(row).toHaveTextContent("Not saved yet: Sheet1!C2. Yours: also mine")
    expect(row).toHaveTextContent("Not saved yet: Sheet1!D2. Yours: and this")
    expect(row.querySelector("[role='status'][aria-live='polite']")).not.toBeNull()
    expect(panel.getAttribute("role")).not.toBe("alert")
    expect(t.status()).toBe("Not saved · a change was refused")
    await act(async () => { screen.getByRole("button", { name: "Keep mine for Sheet1!B2, C2, D2" }).click() })
    await waitFor(() => expect(edit).toHaveBeenCalledWith(`${SID}:h1`, expect.anything()))
    expect((JSON.parse(String((edit.mock.calls.at(-1) as unknown[])[1])) as { ops: { cell: string }[] }).ops.map(op => op.cell)).toEqual(["B2", "C2", "D2"])
    // While the write is out the row stays, with its buttons disabled; the status never claims Saved.
    expect(screen.getByRole("button", { name: "Keep mine for Sheet1!B2, C2, D2" })).toBeDisabled()
    expect(screen.getByRole("button", { name: /^Take theirs/ })).toBeDisabled()
    expect(t.status()).not.toMatch(/Saved|No unsaved changes/)
    await act(async () => { kept.resolve(ok({ operation_key: { artifact_id: "page-1", authority_id: "user:u1", mutation_id: `${SID}:h1` }, outcome: "applied", rev: 43, durability: "node" })) })
    await waitFor(() => expect(t.status()).toBe("Saved"))
    expect(screen.queryByTestId("workbook-draft-conflict")).toBeNull()
  })

  it("take theirs on a multi-cell edit names the cells it still saves and sends only those", async () => {
    const edit = vi.fn(async (id: string): Promise<TransportResponse> => id === `${SID}:h1`
      ? ok({ operation_key: { artifact_id: "page-1", authority_id: "user:u1", mutation_id: id }, outcome: "applied", rev: 43, durability: "node" })
      : { status: 409, body: { error: "precondition_failed", head_rev: 42, conflicts: [{ sheet: "Sheet1", cell: "B2", input: "7", rev: 42, actor_label: "Agent Ada" }] } })
    const t = setup({ transport: { edit, commits: agentChanged({ B2: "7" }) } })
    await t.connect()
    await t.send(JSON.stringify({ type: "mutation", protocol: 1, seq: 1, base_rev: 41, ops: [["B2", "mine"], ["C2", "also mine"]].map(([cell, input]) => ({ op: "cell.set", sheet: "Sheet1", cell, input })) }))
    const take = await screen.findByRole("button", { name: "Take theirs for Sheet1!B2 and save Sheet1!C2" })
    await act(async () => { take.click() })
    await waitFor(() => expect(edit).toHaveBeenCalledWith(`${SID}:h1`, expect.anything()))
    expect((JSON.parse(String((edit.mock.calls.at(-1) as unknown[])[1])) as { ops: { cell: string }[] }).ops.map(op => op.cell)).toEqual(["C2"])
    await waitFor(() => expect(t.status()).toBe("Saved"))
  })

  it("keeps a refused notice open across a reload the person did not cause, and drops only the stale warnings", async () => {
    let pruned = false
    const t = setup({
      transport: {
        edit: refusedOn({ B4: "77" }),
        commits: vi.fn(async (since: number) => pruned ? { status: 410, body: { error: "snapshot_required", head_rev: 50 } } : agentChanged({ B4: "77" })(since)),
        snapshot: vi.fn(async () => ok({ schema_version: 1, rev: pruned ? 50 : 41, generation: 1, snapshot: { sheets: [] } })),
      },
    })
    await t.connect()
    await t.send(mutation(1, 41, "B4", "mine-B4"))
    await screen.findByRole("button", { name: "Keep mine for Sheet1!B4" })
    pruned = true
    await push("workbook.committed", { page_id: "page-1", rev: 50, generation: 1, kind: "cells", cells: 1 })
    await waitFor(() => expect(t.fromFrame.at(-1)).toEqual(expect.objectContaining({ type: "snapshot", rev: 50 })))
    expect(screen.getByRole("button", { name: "Keep mine for Sheet1!B4" })).toBeEnabled()
    expect(t.status()).toBe("Not saved · a change was refused")
  })

  it.each(["workbook.committed", "mooring.committed"])("reads the commits feed on a %s push for its Page, and ignores other Pages", async eventType => {
    const t = setup({ transport: { commits: vi.fn(async (since: number) => ok({ head_rev: 42, generation: 1, commits: since < 42 ? [{ rev: 42, kind: "cells", actor_label: "Agent Ada", own: false, ops: [{ sheet: "Sheet1", cell: "B2", input: "99" }] }] : [] })) } })
    await t.connect()
    expect(realtime.channels).toContain("page:page-1")
    await push(eventType, { page_id: "other", artifact_id: "other", rev: 42, generation: 1, kind: "cells", cells: 1 })
    expect(t.transport.commits).not.toHaveBeenCalled()
    await push(eventType, { page_id: "page-1", artifact_id: "page-1", rev: 42, generation: 1, kind: "cells", cells: 1 })
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
  it("a receipt cannot hide a later open draft", () => {
    const saved = { ...opened, receipt: true }
    expect(workbookSaveLabel(saved, 1)).toBe("Not saved · finish editing the cell")
    expect(workbookSaveLabel({ ...saved, trouble: "offline" }, 1)).toBe("Offline · cell draft not saved")
    expect(workbookSaveLabel(saved, 0)).toBe("Saved")
  })
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
    [{ ...opened, receipt: true, refused: 1 }, "Not saved · a change was refused"],
    [{ ...opened, refused: 2 }, "Not saved · 2 changes were refused"],
    [{ ...opened, pending: 1, refused: 1 }, "Saving… · a change was refused"],
    [{ ...opened, receipt: true, refused: 1, trouble: "unconfirmed" as const }, "Not saved · the server did not confirm a change"],
  ])("%j → %s", (state, label) => {
    expect(workbookSaveLabel(state)).toBe(label)
  })
})
