import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { describe, expect, it } from "vitest"

import { VALID_REALTIME_TYPES } from "@/hooks/use-realtime"

/**
 * The live workbook push (internal/api/gridcraft_live.go) broadcasts on the
 * Page channel. handleMessage drops every type missing from the allowlist, so
 * an open editor would never hear of a new revision without this entry.
 */
const SOURCE_FILE = "internal/api/gridcraft_live.go"
const BROADCAST_CALL = /broadcast\("page",\s*[^,]+,\s*"([a-zA-Z0-9_.]+)"/g

describe("workbook realtime allowlist", () => {
  it("registers every workbook event the server broadcasts", () => {
    const src = readFileSync(resolve(process.cwd(), SOURCE_FILE), "utf8")
    const types = [...src.matchAll(BROADCAST_CALL)].map(m => m[1])
    expect(types).toEqual(["workbook.committed"])
    for (const type of types) expect(VALID_REALTIME_TYPES.has(type)).toBe(true)
  })
})
