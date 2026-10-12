import { describe, it, expect } from "vitest"
import { crewServerToEntry, diffEntries, entryToPayload, type CrewMCPServer } from "@/components/features/mcp/lib/integration-adapter"
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

function server(overrides: Partial<CrewMCPServer> = {}): CrewMCPServer {
  return { id: "id", crew_id: "crew", name: "srv", display_name: "Server", transport: "stdio", endpoint: null, command: null, args_json: null, env_json: null, config_json: null, icon: null, enabled: true, ...overrides }
}

describe("crewServerToEntry", () => {
  it("maps a stdio server and preserves a supplied React key", () => {
    expect(crewServerToEntry(server({ command: "node", args_json: '["-y","pkg"]', env_json: '{"MODE":"test"}' }), 0)).toEqual({
      _key: 0, id: "id", name: "srv", transport: "stdio", command: "node", args: "-y pkg", url: "", env: [{ key: "MODE", value: "test" }], headers: [],
    })
  })
  it("maps HTTP endpoint and headers without stdio fields", () => {
    expect(crewServerToEntry(server({ transport: "streamable-http", endpoint: "https://example.test/mcp", command: "ignored", args_json: '["ignored"]', config_json: '{"headers":{"X-Test":"value"}}' }), 42)).toMatchObject({
      _key: 42, transport: "http", url: "https://example.test/mcp", command: "", args: "", headers: [{ key: "X-Test", value: "value" }],
    })
  })
  it("allocates distinct keys and handles absent endpoints and commands", () => {
    const a = crewServerToEntry(server())
    const b = crewServerToEntry(server({ transport: "streamable-http" }))
    expect(a._key).not.toBe(b._key)
    expect(a.command).toBe("")
    expect(b.url).toBe("")
  })
  it.each(["{broken", "{}", "null"])("tolerates non-array arguments %s", args_json => {
    expect(crewServerToEntry(server({ args_json })).args).toBe("")
  })
  it.each(["{broken", "null"])("tolerates unreadable environment %s", env_json => {
    expect(crewServerToEntry(server({ env_json })).env).toEqual([])
  })
  it.each(["{broken", "null", '"scalar"', "{}"])("tolerates configuration without headers %s", config_json => {
    expect(crewServerToEntry(server({ transport: "streamable-http", config_json })).headers).toEqual([])
  })
  it("does not expose HTTP headers for stdio transports", () => {
    expect(crewServerToEntry(server({ config_json: '{"headers":{"X-Test":"secret"}}' })).headers).toEqual([])
  })
})

describe("payload transport boundaries", () => {
  it("trims names and keys while preserving values and omitting blank keys", () => {
    const payload = entryToPayload(entry({ name: " srv ", transport: "http", url: "https://example.test/mcp", headers: [{ key: " X-Test ", value: " value " }, { key: " ", value: "ignore" }], env: [{ key: " MODE ", value: " test " }, { key: "", value: "ignore" }] }))
    expect(payload).toEqual({ name: "srv", display_name: "srv", transport: "streamable-http", endpoint: "https://example.test/mcp", config_json: '{"headers":{"X-Test":" value "}}', env_json: '{"MODE":" test "}' })
  })
  it("omits empty optional configuration", () => {
    expect(entryToPayload(entry({ transport: "http" }))).toEqual({ name: "srv", display_name: "srv", transport: "streamable-http", endpoint: "" })
  })
})

describe("editor changes", () => {
  it("separates creation, edits and removal and ignores unpersisted originals", () => {
    const removed = entry({ id: "removed" })
    const edited = entry({ id: "edited", name: "changed" })
    const created = entry({ name: "new" })
    expect(diffEntries([removed, entry({ id: "edited" }), entry()], [edited, created, entry({ id: "unknown" })])).toEqual({ create: [created], update: [edited], remove: ["removed"] })
  })
  it.each<Partial<ServerEntry>>([
    { name: "other" }, { transport: "http" }, { command: "python" }, { args: "--flag" }, { url: "https://example.test" },
    { env: [{ key: "MODE", value: "test" }] }, { headers: [{ key: "X-Test", value: "value" }] },
  ])("detects an edit to %j", patch => {
    const original = entry({ id: "id" })
    const changed = { ...original, ...patch }
    expect(diffEntries([original], [changed])).toEqual({ create: [], update: [changed], remove: [] })
  })
  it("ignores key/value ordering and editor-only keys", () => {
    const pairs = [{ key: "A", value: "1" }, { key: "B", value: "2" }]
    const original = entry({ id: "id", env: pairs, headers: pairs })
    expect(diffEntries([original], [{ ...original, _key: 9, env: [...pairs].reverse(), headers: [...pairs].reverse() }])).toEqual({ create: [], update: [], remove: [] })
  })
})
