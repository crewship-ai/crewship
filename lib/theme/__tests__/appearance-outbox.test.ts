import { describe, it, expect, beforeEach } from "vitest"

import { createAppearanceOutbox, OUTBOX_STORAGE_KEY, RETRY_DELAYS_MS } from "../appearance-outbox"

// Server writes for the two appearance keys (#3334, APR-06, APR-10).

type Res = { ok: boolean; status: number }
function memoryStorage() {
  const m = new Map<string, string>()
  return { m, getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k) }
}

let calls: { key: string; body: string; keepalive: boolean }[]
let responder: (key: string) => Res | Error
let timers: { fn: () => Promise<void>; ms: number }[]
function make(storage = memoryStorage()) {
  const statuses: string[] = []
  const ob = createAppearanceOutbox({
    storage,
    send: async (key, body, keepalive) => {
      calls.push({ key, body, keepalive })
      const r = responder(key)
      if (r instanceof Error) throw r
      return r
    },
    schedule: (fn, ms) => void timers.push({ fn, ms }),
    onStatus: (s) => statuses.push(s),
  })
  return { ob, storage, statuses }
}
beforeEach(() => {
  calls = []
  timers = []
  responder = () => ({ ok: true, status: 204 })
})

describe("appearance outbox", () => {
  it("writes appearance before appearance.style, whatever the enqueue order", async () => {
    const { ob } = make()
    ob.enqueue("appearance.style", '{"v":1,"style":"harbor","knobs":{}}')
    ob.enqueue("appearance", '{"accent":"blue"}')
    await ob.flush()
    expect(calls.map((c) => c.key)).toEqual(["appearance", "appearance.style"])
    expect(ob.status()).toBe("synced")
  })

  it("keeps only the latest body per key", async () => {
    const { ob } = make()
    ob.enqueue("appearance", "1")
    ob.enqueue("appearance", "2")
    await ob.flush()
    expect(calls.map((c) => c.body)).toEqual(["2"])
  })

  it("persists pending writes so the next load replays them", async () => {
    const storage = memoryStorage()
    responder = () => new Error("offline")
    const a = make(storage)
    a.ob.enqueue("appearance", "A")
    a.ob.enqueue("appearance.style", "S")
    await a.ob.flush()
    expect(JSON.parse(storage.m.get(OUTBOX_STORAGE_KEY)!)).toHaveLength(2)

    calls = []
    responder = () => ({ ok: true, status: 204 })
    const b = make(storage)
    expect(b.ob.pending("appearance.style")).toBe(true)
    await b.ob.flush()
    expect(calls.map((c) => c.body)).toEqual(["A", "S"])
    expect(storage.m.get(OUTBOX_STORAGE_KEY)).toBe("[]")
  })

  it("retries a transient failure after 5 s, 30 s and 2 min, then waits for a kick", async () => {
    responder = () => ({ ok: false, status: 503 })
    const { ob } = make()
    ob.enqueue("appearance", "A")
    await ob.flush()
    for (let i = 0; i < RETRY_DELAYS_MS.length; i++) {
      expect(timers[i].ms).toBe(RETRY_DELAYS_MS[i])
      await timers[i].fn()
    }
    expect(RETRY_DELAYS_MS).toEqual([5000, 30000, 120000])
    expect(timers).toHaveLength(RETRY_DELAYS_MS.length)
    expect(ob.status()).toBe("pending")
    responder = () => ({ ok: true, status: 204 })
    await ob.kick()
    expect(ob.status()).toBe("synced")
  })

  it("treats 401 and 429 as transient", async () => {
    for (const status of [401, 408, 429]) {
      calls = []
      timers = []
      responder = () => ({ ok: false, status })
      const { ob } = make()
      ob.enqueue("appearance", "A")
      await ob.flush()
      expect(ob.status(), String(status)).toBe("pending")
    }
  })

  it("a second write the server rejects for good leaves the first written and reports Not synced", async () => {
    responder = (key) => (key === "appearance.style" ? { ok: false, status: 400 } : { ok: true, status: 204 })
    const { ob, statuses } = make()
    ob.enqueue("appearance", "A")
    ob.enqueue("appearance.style", "S")
    await ob.flush()
    expect(calls.map((c) => c.key)).toEqual(["appearance", "appearance.style"])
    expect(ob.status()).toBe("failed")
    expect(statuses.at(-1)).toBe("failed")
    // Retry sends it again; the local look was never rolled back by the outbox.
    responder = () => ({ ok: true, status: 204 })
    await ob.retry()
    expect(calls.at(-1)?.key).toBe("appearance.style")
    expect(ob.status()).toBe("synced")
  })

  it("flushKeepalive sends what is left with keepalive", () => {
    responder = () => new Error("unloading")
    const { ob } = make()
    ob.enqueue("appearance", "A")
    ob.flushKeepalive()
    expect(calls).toEqual([{ key: "appearance", body: "A", keepalive: true }])
  })

  it("survives storage that throws", async () => {
    const { ob } = make({ m: new Map(), getItem: () => { throw new Error("blocked") }, setItem: () => { throw new Error("blocked") }, removeItem: () => undefined } as never)
    ob.enqueue("appearance", "A")
    await ob.flush()
    expect(calls).toHaveLength(1)
  })
})

describe("three clients (APR-06)", () => {
  it("new saves a style, old changes the accent, new reloads: both kept", async () => {
    const server = new Map<string, string>()
    const send = async (key: string, body: string) => {
      server.set(key, body)
      return { ok: true, status: 204 }
    }
    const mk = () => createAppearanceOutbox({ storage: memoryStorage(), send, schedule: () => undefined, onStatus: () => undefined })

    const newer = mk()
    newer.enqueue("appearance", JSON.stringify({ theme: "dark", accent: "indigo" }))
    newer.enqueue("appearance.style", JSON.stringify({ v: 1, style: "harbor", knobs: { radius: "round" } }))
    await newer.flush()

    // An older client only knows "appearance" and writes it whole.
    const older = mk()
    older.enqueue("appearance", JSON.stringify({ theme: "dark", accent: "teal" }))
    await older.flush()

    // A third, newer client reading the server sees both writes.
    expect(JSON.parse(server.get("appearance")!).accent).toBe("teal")
    expect(JSON.parse(server.get("appearance.style")!)).toEqual({ v: 1, style: "harbor", knobs: { radius: "round" } })
  })
})
