import { afterEach, describe, expect, it, vi } from "vitest"

import { randomUUIDv4 } from "../random-id"

const V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

/** Swap globalThis.crypto for one test and put it back afterwards. */
function withCrypto(replacement: unknown) {
  const original = Object.getOwnPropertyDescriptor(globalThis, "crypto")
  Object.defineProperty(globalThis, "crypto", {
    value: replacement,
    configurable: true,
    writable: true,
  })
  return () => {
    if (original) Object.defineProperty(globalThis, "crypto", original)
    else delete (globalThis as { crypto?: unknown }).crypto
  }
}

let restore: (() => void) | null = null
afterEach(() => {
  restore?.()
  restore = null
  vi.restoreAllMocks()
})

describe("randomUUIDv4", () => {
  it("returns a v4 uuid", () => {
    expect(randomUUIDv4()).toMatch(V4)
  })

  it("does not repeat itself", () => {
    const seen = new Set(Array.from({ length: 200 }, () => randomUUIDv4()))
    expect(seen.size).toBe(200)
  })

  it("prefers crypto.randomUUID when the context is secure", () => {
    const randomUUID = vi.fn(() => "11111111-2222-4333-8444-555555555555")
    restore = withCrypto({ randomUUID, getRandomValues: globalThis.crypto.getRandomValues })
    expect(randomUUIDv4()).toBe("11111111-2222-4333-8444-555555555555")
    expect(randomUUID).toHaveBeenCalledTimes(1)
  })

  // The reason this helper exists. crypto.randomUUID is gated to secure
  // contexts and the dev clones are reached over plain HTTP, so the fallback
  // is the path that actually runs there — and it must still be random from
  // the CSPRNG, not from Math.random (CodeQL js/insecure-randomness).
  it("falls back to getRandomValues in an insecure context", () => {
    const getRandomValues = vi.fn((arr: Uint8Array) => {
      for (let i = 0; i < arr.length; i++) arr[i] = (i * 7 + 3) & 0xff
      return arr
    })
    restore = withCrypto({ getRandomValues })

    const id = randomUUIDv4()
    expect(id).toMatch(V4)
    expect(getRandomValues).toHaveBeenCalledTimes(1)
    // Version and variant are forced regardless of what the bytes said.
    expect(id[14]).toBe("4")
    expect(["8", "9", "a", "b"]).toContain(id[19])
  })

  it("keeps every byte of entropy it was given", () => {
    const bytes = new Uint8Array(16).fill(0)
    restore = withCrypto({
      getRandomValues: (arr: Uint8Array) => {
        arr.set(bytes)
        return arr
      },
    })
    // All-zero entropy still has to produce the version and variant nibbles,
    // which is the one place the bytes are overwritten on purpose.
    expect(randomUUIDv4()).toBe("00000000-0000-4000-8000-000000000000")
  })

  it("throws rather than inventing weak entropy when there is no crypto", () => {
    restore = withCrypto(undefined)
    expect(() => randomUUIDv4()).toThrow(/crypto/i)
  })
})
