import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"

// A refresh rotates the refresh token; the server answers a second use of the
// old token as a replay and revokes the session (nextauth.go RefreshToken).
// Every tab of the origin shares one cookie jar, but `refreshInflight` in
// api-fetch single-flights only within one document (#4902).
//
// The model below is that server and jar: a refresh presents whatever token
// the jar holds when it is SENT, and the jar learns the successor only when
// the response ARRIVES. Each "document" is a fresh instance of the module.

type Api = typeof import("../api-fetch")

const server = { current: "t0", jar: "t0", revoked: false, sent: [] as string[], delayMs: 50, rotations: 0 }

function respond(status: number, body: unknown): Response {
  const text = JSON.stringify(body)
  return { ok: status === 200, status, json: () => Promise.resolve(JSON.parse(text)), text: () => Promise.resolve(text), clone() { return this } } as unknown as Response
}

const fetchMock = vi.fn((_url: string, init?: RequestInit) => {
  expect(init?.credentials).toBe("include")
  const presented = server.jar
  server.sent.push(presented)
  return new Promise<Response>((resolve) => setTimeout(() => {
    if (server.revoked) return resolve(respond(401, { error: "session_revoked" }))
    if (presented !== server.current) {
      server.revoked = true
      return resolve(respond(401, { error: "session_revoked" }))
    }
    server.current = `t${++server.rotations}`
    server.jar = server.current
    resolve(respond(200, { ok: true }))
  }, server.delayMs))
})

/** In-memory exclusive Web Locks: FIFO per name, `signal` cancels a waiter. */
function fakeLocks() {
  let tail: Promise<void> = Promise.resolve()
  return {
    held: 0,
    request<T>(_name: string, options: { signal?: AbortSignal }, run: () => Promise<T>): Promise<T> {
      const previous = tail
      let release!: () => void
      tail = new Promise<void>((resolve) => { release = resolve })
      const aborted = new Promise<never>((_, reject) => options.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError"))))
      return Promise.race([previous.then(() => "granted" as const), aborted]).then(
        async () => {
          this.held++
          try { return await run() } finally { this.held--; release() }
        },
        (error) => { void previous.then(release); throw error },
      )
    },
  }
}

async function documents(count: number): Promise<Api[]> {
  const out: Api[] = []
  for (let i = 0; i < count; i++) {
    vi.resetModules()
    out.push(await import("../api-fetch"))
  }
  return out
}

function installLocks(locks: unknown) {
  Object.defineProperty(navigator, "locks", { value: locks, configurable: true })
}

beforeEach(() => {
  Object.assign(server, { current: "t0", jar: "t0", revoked: false, sent: [], delayMs: 50, rotations: 0 })
  fetchMock.mockClear()
  vi.stubGlobal("fetch", fetchMock)
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  installLocks(undefined)
})

describe("token refresh across documents (#4902)", () => {
  it("two tabs refreshing at once take turns and the session survives", async () => {
    installLocks(fakeLocks())
    const [a, b] = await documents(2)
    const both = Promise.all([a.tryRefresh(), b.tryRefresh()])
    await vi.advanceTimersByTimeAsync(500)
    await expect(both).resolves.toEqual(["ok", "ok"])
    expect(server.sent).toEqual(["t0", "t1"])
    expect(server.revoked).toBe(false)
  })

  it("waits for another tab's slow refresh however long it takes, within that refresh's own timeout", async () => {
    installLocks(fakeLocks())
    server.delayMs = 4_000
    const [a, b] = await documents(2)
    const first = a.tryRefresh()
    await vi.advanceTimersByTimeAsync(10)
    const second = b.tryRefresh()
    await vi.advanceTimersByTimeAsync(3_500)
    // A three-second guess would have sent the unchanged token here.
    expect(server.sent).toEqual(["t0"])
    await vi.advanceTimersByTimeAsync(5_000)
    await expect(Promise.all([first, second])).resolves.toEqual(["ok", "ok"])
    expect(server.sent).toEqual(["t0", "t1"])
    expect(server.revoked).toBe(false)
  })

  it("sends the refresh as keepalive, so a page replaced mid-refresh still stores the rotated cookies", async () => {
    const [a] = await documents(1)
    const result = a.tryRefresh()
    await vi.advanceTimersByTimeAsync(100)
    await expect(result).resolves.toBe("ok")
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: "POST", keepalive: true })
  })

  it("without the Locks API it sends at once, exactly as before", async () => {
    installLocks(undefined)
    const [a] = await documents(1)
    void a.tryRefresh()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(100)
  })

  it("gives up waiting for a lock that is never released without sending anything", async () => {
    installLocks({ request: (_n: string, o: { signal?: AbortSignal }) => new Promise((_, reject) => o.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))) })
    const [a] = await documents(1)
    const result = a.tryRefresh()
    await vi.advanceTimersByTimeAsync(20_000)
    await expect(result).resolves.toBe("retryable_failed")
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
