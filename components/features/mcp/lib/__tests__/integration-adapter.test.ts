import { describe, it, expect } from "vitest"
import { entryToPayload } from "@/components/features/mcp/lib/integration-adapter"
import type { ServerEntry } from "@/components/features/mcp/types"

function entry(overrides: Partial<ServerEntry> = {}): ServerEntry {
  return {
    _key: 0,
    name: "srv",
    transport: "stdio",
    command: "npx",
    args: "",
    url: "",
    headers: [],
    env: [],
    ...overrides,
  }
}

describe("entryToPayload", () => {
  it("keeps a quoted arg with embedded spaces intact instead of shredding it", () => {
    const payload = entryToPayload(
      entry({ command: "npx", args: `--flag "hello world"` }),
    )
    expect(JSON.parse(payload.args_json ?? "[]")).toEqual(["--flag", "hello world"])
  })

  it("splits unquoted args on whitespace and drops blanks", () => {
    const payload = entryToPayload(entry({ command: "npx", args: "  -y   pkg  " }))
    expect(JSON.parse(payload.args_json ?? "[]")).toEqual(["-y", "pkg"])
  })

  it("omits args_json when the args string is empty after tokenizing", () => {
    const payload = entryToPayload(entry({ command: "npx", args: "   " }))
    expect(payload.args_json).toBeUndefined()
  })
})
