import { act, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useTerminal } from "@/hooks/use-terminal"
import { resolveWsBase } from "@/lib/server-base"

type TerminalDouble = {
  rows: number
  cols: number
  write: ReturnType<typeof vi.fn>
  writeln: ReturnType<typeof vi.fn>
  dispose: ReturnType<typeof vi.fn>
  data: (data: string) => void
  resize: (size: { rows: number; cols: number }) => void
}
type FitDouble = { fit: ReturnType<typeof vi.fn> }
const doubles = vi.hoisted(() => ({ terminals: [] as TerminalDouble[], fits: [] as FitDouble[] }))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/server-base", () => ({ resolveWsBase: vi.fn(() => "wss://crewship.test") }))
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    rows = 25
    cols = 90
    write = vi.fn()
    writeln = vi.fn()
    dispose = vi.fn()
    data = (_data: string) => {}
    resize = (_size: { rows: number; cols: number }) => {}
    constructor() { doubles.terminals.push(this) }
    loadAddon = vi.fn()
    open = vi.fn()
    onData(handler: (data: string) => void) { this.data = handler }
    onResize(handler: (size: { rows: number; cols: number }) => void) { this.resize = handler }
  },
}))
vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit = vi.fn()
    constructor() { doubles.fits.push(this) }
  },
}))
vi.mock("@xterm/addon-web-links", () => ({ WebLinksAddon: class {} }))

class SocketDouble {
  static OPEN = 1
  readyState = 0
  binaryType = "blob"
  onopen: (() => void) | null = null
  onmessage: ((event: { data: unknown }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null
  send = vi.fn<(data: string | Uint8Array) => void>()
  close = vi.fn(() => { this.readyState = 3 })
  constructor(readonly url: string) { sockets.push(this) }
  open() { this.readyState = SocketDouble.OPEN; this.onopen?.() }
  message(data: unknown) { this.onmessage?.({ data }) }
}
class ObserverDouble {
  observe = vi.fn()
  disconnect = vi.fn()
  constructor(readonly callback: () => void) { observers.push(this) }
}
let sockets: SocketDouble[] = []
let observers: ObserverDouble[] = []
type Options = Parameters<typeof useTerminal>[0]
function renderTerminal(overrides: Partial<Options> = {}) {
  const initialProps: Options = {
    crewId: "crew-one", crewSlug: "one", containerRef: { current: document.createElement("div") }, ...overrides,
  }
  return renderHook((props: Options) => useTerminal(props), { initialProps })
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}
const tokenResponse = () => new Response(JSON.stringify({ token: "private-auth-token" }))

beforeEach(() => {
  sockets = []
  observers = []
  doubles.terminals = []
  doubles.fits = []
  vi.mocked(resolveWsBase).mockReturnValue("wss://crewship.test")
  vi.mocked(apiFetch).mockReset().mockImplementation(async () => tokenResponse())
  vi.stubGlobal("WebSocket", SocketDouble)
  vi.stubGlobal("ResizeObserver", ObserverDouble)
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe("useTerminal", () => {
  it.each([{ enabled: false }, { containerRef: { current: null } }])("does not allocate a session when unavailable: %o", (options) => {
    const { result } = renderTerminal(options)
    expect(result.current.status).toBe("disconnected")
    expect(apiFetch).not.toHaveBeenCalled()
    expect(sockets).toHaveLength(0)
    expect(doubles.terminals).toHaveLength(0)
  })

  it.each([{}, { mode: "attach" as const, agentSlug: "agent-two" }])("authenticates before initiating the selected session: %o", async (options) => {
    const { result } = renderTerminal(options)
    expect(result.current.status).toBe("connecting")
    await waitFor(() => expect(sockets).toHaveLength(1))
    const socket = sockets[0]
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/ws-token")
    expect(socket.url).toBe("wss://crewship.test/ws/terminal")
    expect(socket.url).not.toContain("private-auth-token")
    expect(socket.binaryType).toBe("arraybuffer")
    expect(socket.send).not.toHaveBeenCalled()
    act(() => socket.open())
    expect(socket.send.mock.calls.map(([frame]) => JSON.parse(String(frame)))).toEqual([
      { type: "auth", token: "private-auth-token" },
      { mode: options.mode ?? "shell", crew_id: "crew-one", crew_slug: "one", agent_slug: options.agentSlug ?? "", rows: 25, cols: 90 },
    ])
    expect(result.current.status).toBe("connected")
  })

  it.each([
    { port: "3001", protocol: "http:", expected: "ws://localhost:8080" },
    { port: "3011", protocol: "https:", expected: "wss://localhost:8090" },
    { port: "3019", protocol: "http:", expected: "ws://localhost:8098" },
    { port: "3020", protocol: "http:", expected: "ws://localhost:3020" },
  ])("maps same-origin development connections: $protocol$port", async ({ port, protocol, expected }) => {
    vi.spyOn(window.location, "port", "get").mockReturnValue(port)
    vi.spyOn(window.location, "host", "get").mockReturnValue(`localhost:${port}`)
    vi.spyOn(window.location, "hostname", "get").mockReturnValue("localhost")
    vi.spyOn(window.location, "protocol", "get").mockReturnValue(protocol)
    vi.mocked(resolveWsBase).mockReturnValue(`${protocol === "https:" ? "wss:" : "ws:"}//localhost:${port}`)
    renderTerminal()
    await waitFor(() => expect(sockets).toHaveLength(1))
    expect(sockets[0].url).toBe(`${expected}/ws/terminal`)
  })

  it("streams terminal bytes, plain output and control messages", async () => {
    const { result } = renderTerminal()
    await waitFor(() => expect(sockets).toHaveLength(1))
    const socket = sockets[0], terminal = doubles.terminals[0]
    const binary = new Uint8Array([0, 255, 10]).buffer
    act(() => {
      socket.open()
      socket.message(binary)
      socket.message("plain terminal output")
      socket.message(JSON.stringify({ type: "info", message: "session ready" }))
      socket.message(JSON.stringify({ type: "unknown" }))
      socket.message({ ignored: true })
    })
    expect(terminal.write.mock.calls).toEqual([[new Uint8Array(binary)], ["plain terminal output"]])
    expect(terminal.writeln).toHaveBeenCalledWith(expect.stringContaining("session ready"))
    expect(result.current.status).toBe("connected")
    act(() => socket.message(JSON.stringify({ type: "error", message: "container stopped" })))
    expect(terminal.writeln).toHaveBeenCalledWith(expect.stringContaining("Error: container stopped"))
    expect(result.current.status).toBe("error")
  })

  it("sends UTF-8 input and resize controls only through an open socket", async () => {
    renderTerminal()
    await waitFor(() => expect(sockets).toHaveLength(1))
    const socket = sockets[0], terminal = doubles.terminals[0]
    act(() => { terminal.data("ignored"); terminal.resize({ rows: 12, cols: 40 }) })
    expect(socket.send).not.toHaveBeenCalled()
    act(() => socket.open())
    socket.send.mockClear()
    act(() => { terminal.data("echo příliš\r"); terminal.resize({ rows: 32, cols: 110 }) })
    expect(socket.send.mock.calls).toEqual([
      [new TextEncoder().encode("echo příliš\r")],
      [JSON.stringify({ type: "resize", rows: 32, cols: 110 })],
    ])
    expect(doubles.fits[0].fit).toHaveBeenCalledTimes(1)
    act(() => observers[0].callback())
    expect(doubles.fits[0].fit).toHaveBeenCalledTimes(2)
    socket.close()
    act(() => { terminal.data("ignored"); terminal.resize({ rows: 40, cols: 120 }) })
    expect(socket.send).toHaveBeenCalledTimes(2)
  })

  it("reports transport errors and connection close", async () => {
    const { result } = renderTerminal()
    await waitFor(() => expect(sockets).toHaveLength(1))
    act(() => { sockets[0].open(); sockets[0].onerror?.() })
    expect(result.current.status).toBe("error")
    act(() => sockets[0].onclose?.())
    expect(result.current.status).toBe("disconnected")
    expect(doubles.terminals[0].writeln).toHaveBeenCalledWith(expect.stringContaining("Connection closed"))
  })

  it.each(["refused", "network", "invalid JSON"])("reports token acquisition failure: %s", async (kind) => {
    if (kind === "refused") vi.mocked(apiFetch).mockResolvedValue(new Response("denied", { status: 403 }))
    else if (kind === "network") vi.mocked(apiFetch).mockRejectedValue(new Error("offline"))
    else vi.mocked(apiFetch).mockResolvedValue(new Response("not-json"))
    const { result } = renderTerminal()
    await waitFor(() => expect(result.current.status).toBe("error"))
    expect(sockets).toHaveLength(0)
    expect(doubles.terminals).toHaveLength(0)
  })

  it.each([{}, { token: "" }, { token: 42 }, { token: { invalid: true } }])("reports invalid successful token responses without opening a session: %o", async (body) => {
    vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify(body)))
    const { result } = renderTerminal()
    await waitFor(() => expect(result.current.status).toBe("error"))
    expect(sockets).toHaveLength(0)
    expect(doubles.terminals).toHaveLength(0)
  })

  it("does not create a terminal after unmount during token acquisition", async () => {
    const pending = deferred<Response>()
    vi.mocked(apiFetch).mockReturnValue(pending.promise)
    const { unmount } = renderTerminal()
    unmount()
    await act(async () => pending.resolve(tokenResponse()))
    expect(sockets).toHaveLength(0)
    expect(doubles.terminals).toHaveLength(0)
  })

  it("disconnects every resource and makes explicit disconnect idempotent", async () => {
    const { result, unmount } = renderTerminal()
    await waitFor(() => expect(sockets).toHaveLength(1))
    act(() => { sockets[0].open(); result.current.disconnect(); result.current.disconnect() })
    expect(result.current.status).toBe("disconnected")
    expect(sockets[0].close).toHaveBeenCalledTimes(1)
    expect(doubles.terminals[0].dispose).toHaveBeenCalledTimes(1)
    expect(observers[0].disconnect).toHaveBeenCalledTimes(1)
    unmount()
    expect(sockets[0].close).toHaveBeenCalledTimes(1)
    expect(observers[0].disconnect).toHaveBeenCalledTimes(1)
  })

  it("explicit disconnect cancels an outstanding token request", async () => {
    const pending = deferred<Response>()
    vi.mocked(apiFetch).mockReturnValue(pending.promise)
    const { result } = renderTerminal()
    act(() => result.current.disconnect())
    await act(async () => pending.resolve(tokenResponse()))
    expect(result.current.status).toBe("disconnected")
    expect(sockets).toHaveLength(0)
    expect(doubles.terminals).toHaveLength(0)
  })

  it("does not reconnect when disconnect races token JSON decoding", async () => {
    const pending = deferred<{ token: string }>()
    const response = tokenResponse()
    const json = vi.spyOn(response, "json").mockReturnValue(pending.promise)
    vi.mocked(apiFetch).mockResolvedValue(response)
    const { result } = renderTerminal()
    await waitFor(() => expect(json).toHaveBeenCalledTimes(1))
    act(() => result.current.disconnect())
    await act(async () => pending.resolve({ token: "late-token" }))
    expect(result.current.status).toBe("disconnected")
    expect(sockets).toHaveLength(0)
    expect(doubles.terminals).toHaveLength(0)
  })

  it("reconnects on key changes and refuses a late open from the replaced socket", async () => {
    const containerRef = { current: document.createElement("div") }
    const { result, rerender } = renderTerminal({ containerRef })
    await waitFor(() => expect(sockets).toHaveLength(1))
    const previous = sockets[0], oldTerminal = doubles.terminals[0]
    rerender({ containerRef, crewId: "crew-one", crewSlug: "one", key: 1 })
    await waitFor(() => expect(sockets).toHaveLength(2))
    act(() => { sockets[1].open(); previous.open(); oldTerminal.data("old input"); oldTerminal.resize({ rows: 30, cols: 100 }) })
    expect(previous.send).not.toHaveBeenCalled()
    expect(previous.close).toHaveBeenCalled()
    expect(sockets[1].send).toHaveBeenCalledTimes(2)
    expect(result.current.status).toBe("connected")
  })

  it("ignores old socket events after switching crews", async () => {
    const containerRef = { current: document.createElement("div") }
    const { result, rerender } = renderTerminal({ containerRef })
    await waitFor(() => expect(sockets).toHaveLength(1))
    const oldSocket = sockets[0], oldTerminal = doubles.terminals[0], oldObserver = observers[0]
    rerender({ containerRef, crewId: "crew-two", crewSlug: "two" })
    await waitFor(() => expect(sockets).toHaveLength(2))
    act(() => sockets[1].open())
    expect(oldTerminal.dispose).toHaveBeenCalledTimes(1)
    expect(oldObserver.disconnect).toHaveBeenCalledTimes(1)
    const newFitCount = doubles.fits[1].fit.mock.calls.length
    act(() => {
      oldSocket.message("late output")
      oldSocket.message(JSON.stringify({ type: "error", message: "old session failure" }))
      oldSocket.onerror?.()
      oldSocket.onclose?.()
      oldObserver.callback()
    })
    expect(result.current.status).toBe("connected")
    expect(oldTerminal.write).not.toHaveBeenCalled()
    expect(oldTerminal.writeln).not.toHaveBeenCalled()
    expect(doubles.fits[1].fit).toHaveBeenCalledTimes(newFitCount)
  })

  it("ignores an obsolete token failure after a newer crew connects", async () => {
    const pending = deferred<Response>()
    vi.mocked(apiFetch).mockReturnValueOnce(pending.promise)
    const containerRef = { current: document.createElement("div") }
    const { result, rerender } = renderTerminal({ containerRef })
    rerender({ containerRef, crewId: "crew-two", crewSlug: "two" })
    await waitFor(() => expect(sockets).toHaveLength(1))
    act(() => sockets[0].open())
    await act(async () => pending.resolve(new Response("denied", { status: 403 })))
    expect(result.current.status).toBe("connected")
  })
})
