import { describe, it, expect } from "vitest"
import { bodyIsReplayable, inputIsReplayable } from "@/lib/fetch-with-retry"

describe("bodyIsReplayable", () => {
  it("treats null/undefined as replayable", () => {
    expect(bodyIsReplayable(null)).toBe(true)
    expect(bodyIsReplayable(undefined)).toBe(true)
  })

  it("treats strings as replayable", () => {
    expect(bodyIsReplayable("hello")).toBe(true)
  })

  it("treats Blob/ArrayBuffer/Uint8Array as replayable", () => {
    const blob = new Blob(["x"])
    expect(bodyIsReplayable(blob)).toBe(true)
    const ab = new ArrayBuffer(4)
    expect(bodyIsReplayable(ab)).toBe(true)
    const u8 = new Uint8Array([1, 2, 3])
    expect(bodyIsReplayable(u8)).toBe(true)
  })

  it("treats FormData / URLSearchParams as replayable", () => {
    const fd = new FormData()
    fd.append("k", "v")
    expect(bodyIsReplayable(fd)).toBe(true)
    const sp = new URLSearchParams("k=v")
    expect(bodyIsReplayable(sp)).toBe(true)
  })

  it("treats ReadableStream as NOT replayable — once consumed, retry sends garbage", () => {
    const stream = new ReadableStream({
      start(c) {
        c.enqueue(new TextEncoder().encode("x"))
        c.close()
      },
    })
    expect(bodyIsReplayable(stream)).toBe(false)
  })
})

describe("inputIsReplayable", () => {
  it("treats string URLs as replayable", () => {
    expect(inputIsReplayable("https://example.test/x")).toBe(true)
  })

  it("treats URL objects as replayable", () => {
    expect(inputIsReplayable(new URL("https://example.test/x"))).toBe(true)
  })

  it("treats Request without body as replayable", () => {
    expect(inputIsReplayable(new Request("https://example.test/x"))).toBe(true)
  })

  it("treats Request carrying a body as NOT replayable", () => {
    // A body attached to a Request lives on the Request, not init.body —
    // so the retry guard would otherwise miss it and re-fetch a consumed
    // body on the second attempt.
    const req = new Request("https://example.test/x", {
      method: "POST",
      body: "payload",
    })
    expect(inputIsReplayable(req)).toBe(false)
  })
})
