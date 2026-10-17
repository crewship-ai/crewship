import { describe, it, expect } from "vitest"
import { createAppearanceOutbox } from "../appearance-outbox"

// Regression scenarios from the independent review of PR #3353 (2026-10-17),
// plus the pause/resume contract APR-05 needs: switching sync off must stop
// every later request, including a retry that was already scheduled.

function storage() {
  const data = new Map<string, string>()
  return { getItem: (key: string) => data.get(key) ?? null, setItem: (key: string, value: string) => void data.set(key, value) }
}

describe("appearance outbox: review regressions", () => {
  it("sends the latest choice made while the first request is in flight", async () => {
    const sent: string[] = []
    let release!: () => void
    const held = new Promise<void>((resolve) => { release = resolve })
    const outbox = createAppearanceOutbox({
      storage: storage(),
      send: async (_key, body) => { sent.push(body); if (body === "first") await held; return { ok: true, status: 204 } },
      schedule: () => () => {}, onStatus: () => {},
    })
    outbox.enqueue("appearance", "first")
    const first = outbox.flush()
    outbox.enqueue("appearance", "latest")
    const next = outbox.flush()
    release()
    await Promise.all([first, next])
    expect(sent).toEqual(["first", "latest"])
    expect(outbox.status()).toBe("synced")
  })

  it("does not send style if the required appearance write is rejected", async () => {
    const sent: string[] = []
    const outbox = createAppearanceOutbox({
      storage: storage(),
      send: async (key) => { sent.push(key); return key === "appearance" ? { ok: false, status: 400 } : { ok: true, status: 204 } },
      schedule: () => () => {}, onStatus: () => {},
    })
    outbox.enqueue("appearance", "new theme and accent")
    outbox.enqueue("appearance.style", "new style")
    await outbox.flush()
    expect(sent).toEqual(["appearance"])
    expect(outbox.pending("appearance.style")).toBe(true)
    expect(outbox.status()).toBe("failed")
  })

  it("pause cancels a scheduled retry; resume and kick send what is still owed", async () => {
    const sent: string[] = []
    const timers: { fn: () => Promise<void>; cancelled: boolean }[] = []
    let fail = true
    const outbox = createAppearanceOutbox({
      storage: storage(),
      send: async (key) => { sent.push(key); return fail ? { ok: false, status: 503 } : { ok: true, status: 204 } },
      schedule: (fn) => { const t = { fn, cancelled: false }; timers.push(t); return () => { t.cancelled = true } },
      onStatus: () => {},
    })
    outbox.enqueue("appearance", "x")
    await outbox.flush()
    expect(sent).toHaveLength(1)
    expect(timers).toHaveLength(1)
    outbox.pause()
    expect(timers[0].cancelled).toBe(true)
    // Even a timer that fires anyway (already queued by the runtime) sends nothing.
    await timers[0].fn()
    expect(sent).toHaveLength(1)
    // Nothing goes out while paused, whatever triggers a flush.
    await outbox.kick()
    await outbox.flush()
    outbox.flushKeepalive()
    expect(sent).toHaveLength(1)
    fail = false
    outbox.resume()
    await outbox.kick()
    expect(sent).toHaveLength(2)
    expect(outbox.status()).toBe("synced")
  })

  it("a request already in flight when paused finishes, but nothing follows it", async () => {
    const sent: string[] = []
    let release!: () => void
    const held = new Promise<void>((resolve) => { release = resolve })
    const outbox = createAppearanceOutbox({
      storage: storage(),
      send: async (key) => { sent.push(key); if (key === "appearance") await held; return { ok: true, status: 204 } },
      schedule: () => () => {}, onStatus: () => {},
    })
    outbox.enqueue("appearance", "a")
    outbox.enqueue("appearance.style", "s")
    const run = outbox.flush()
    outbox.pause()
    release()
    await run
    expect(sent).toEqual(["appearance"])
    expect(outbox.pending("appearance.style")).toBe(true)
    outbox.resume()
    await outbox.kick()
    expect(sent).toEqual(["appearance", "appearance.style"])
  })
})
