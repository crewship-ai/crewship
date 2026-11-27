// @vitest-environment node
import { describe, it, expect } from "vitest"
import { base64Parts, compactNativeJson, gzipBlob, gzipBytes } from "./native-compact"

const enc = (s: string) => new TextEncoder().encode(s)
const dec = (b: Uint8Array) => new TextDecoder().decode(b)

describe("compactNativeJson", () => {
  it("equals JSON.stringify of the parsed document, strings untouched", async () => {
    const doc = { "a b": "x  \" y\\ \n z", n: [1, 2.5, -3e10, null, true], deep: { "ž ě": "řeřicha  \u0000", empty: {}, e2: [] } }
    const pretty = JSON.stringify(doc, null, 2)
    expect(dec(await compactNativeJson(enc(pretty)))).toBe(JSON.stringify(doc))
    expect(dec(await compactNativeJson(enc(pretty.replace(/\n/g, "\r\n"))))).toBe(JSON.stringify(doc))
  })
  it("yields between slices and still gives the same bytes", async () => {
    const doc = { cells: Array.from({ length: 5000 }, (_, i) => ({ value: { t: "Number", v: i }, formula: null, style: i % 7 })) }
    const pretty = JSON.stringify(doc, null, 2)
    let ticks = 0
    const timer = setInterval(() => ticks++, 0)
    const out = await compactNativeJson(enc(pretty), undefined, 4096)
    clearInterval(timer)
    expect(dec(out)).toBe(JSON.stringify(doc))
    expect(ticks).toBeGreaterThan(0)
  })
  it("stops when its signal aborts", async () => {
    const c = new AbortController()
    const big = enc(JSON.stringify({ s: "x".repeat(20000) }, null, 2))
    const run = compactNativeJson(big, c.signal, 1024)
    c.abort()
    await expect(run).rejects.toBeTruthy()
  })
})

describe("gzipBytes", () => {
  it("round-trips through gunzip", async () => {
    const data = enc("{\"a\":1}".repeat(50000))
    const gz = await gzipBytes(data)
    expect(gz).toBeDefined()
    expect(gz!.length).toBeLessThan(data.length / 10)
    const back = new Uint8Array(await new Response(new Blob([gz! as BlobPart]).stream().pipeThrough(new DecompressionStream("gzip"))).arrayBuffer())
    expect(dec(back)).toBe(dec(data))
  })
})

describe("base64Parts and gzipBlob", () => {
  it("parts concatenate to the plain base64 of the bytes, none above ~4 MiB", () => {
    const bytes = new Uint8Array(7_000_001).map((_, i) => (i * 31) & 0xff)
    const parts = base64Parts(bytes)
    expect(parts.length).toBe(3)
    expect(Math.max(...parts.map(p => p.length))).toBeLessThanOrEqual(4 << 20)
    expect(Buffer.from(parts.join(""), "base64").equals(Buffer.from(bytes))).toBe(true)
  })
  it("compresses a Blob and returns undefined only without CompressionStream", async () => {
    const blob = new Blob(["{\"a\":1}".repeat(100000)])
    const gz = await gzipBlob(blob)
    expect(gz!.length).toBeLessThan(blob.size / 10)
  })
})
