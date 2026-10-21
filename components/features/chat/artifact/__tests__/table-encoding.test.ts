import { describe, expect, it } from "vitest"
import { decodeTableBytes, parseDelimited } from "../table-data"

describe("local CSV encoding", () => {
  const decode = (bytes: number[]) => decodeTableBytes(Uint8Array.from(bytes).buffer)
  it("decodes Central European Windows-1250 CSV", () => {
    expect(parseDelimited(decode([0x8e, 111, 102, 105, 101, 44, 0xe8, 97, 115]), ",").rows).toEqual([["Žofie", "čas"]])
  })
  it("uses BOMs for both UTF-16 byte orders and prefers UTF-8", () => {
    expect(decode([0xff, 0xfe, 0x7d, 1])).toBe("Ž")
    expect(decode([0xfe, 0xff, 1, 0x7d])).toBe("Ž")
    expect(decodeTableBytes(new TextEncoder().encode("Žofie").buffer)).toBe("Žofie")
  })
  it("offers a download for broken UTF-16 and binary input", () => {
    expect(() => decode([0xff, 0xfe, 49])).toThrow(/encoding.*Download/)
    expect(() => decode([0, 1, 2])).toThrow(/encoding.*Download/)
  })
})
