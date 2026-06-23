import { describe, it, expect } from "vitest"
import {
  parseConfig,
  serializeConfig,
  emptyEntry,
  entryFromTemplate,
} from "@/components/features/mcp/lib/config-parser"
import type { ServerEntry, MCPTemplate } from "@/components/features/mcp/types"

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

describe("parseConfig", () => {
  it("returns [] for empty, whitespace, and invalid JSON input", () => {
    expect(parseConfig("")).toEqual([])
    expect(parseConfig("   \n\t")).toEqual([])
    expect(parseConfig("{not json")).toEqual([])
  })

  it("returns [] when mcpServers is missing", () => {
    expect(parseConfig("{}")).toEqual([])
  })

  it("parses a stdio server with command, args and env", () => {
    const raw = JSON.stringify({
      mcpServers: {
        files: {
          command: "npx",
          args: ["-y", "@modelcontextprotocol/server-filesystem"],
          env: { ROOT: "/tmp", DEBUG: "1" },
        },
      },
    })
    const [e] = parseConfig(raw)
    expect(e.name).toBe("files")
    expect(e.transport).toBe("stdio")
    expect(e.command).toBe("npx")
    expect(e.args).toBe("-y @modelcontextprotocol/server-filesystem")
    expect(e.url).toBe("")
    expect(e.headers).toEqual([])
    expect(e.env).toEqual([
      { key: "ROOT", value: "/tmp" },
      { key: "DEBUG", value: "1" },
    ])
  })

  it("defaults missing stdio command/args/env to empty values", () => {
    const [e] = parseConfig(JSON.stringify({ mcpServers: { bare: {} } }))
    expect(e.transport).toBe("stdio")
    expect(e.command).toBe("")
    expect(e.args).toBe("")
    expect(e.env).toEqual([])
  })

  it("parses an http server with url, headers and env", () => {
    const raw = JSON.stringify({
      mcpServers: {
        remote: {
          type: "http",
          url: "https://mcp.example.com/sse",
          headers: { Authorization: "Bearer x" },
          env: { TOKEN: "t" },
        },
      },
    })
    const [e] = parseConfig(raw)
    expect(e.transport).toBe("http")
    expect(e.url).toBe("https://mcp.example.com/sse")
    expect(e.command).toBe("")
    expect(e.args).toBe("")
    expect(e.headers).toEqual([{ key: "Authorization", value: "Bearer x" }])
    expect(e.env).toEqual([{ key: "TOKEN", value: "t" }])
  })

  it("parses an http server without headers to an empty headers list", () => {
    const raw = JSON.stringify({
      mcpServers: { remote: { type: "http", url: "https://x.dev" } },
    })
    expect(parseConfig(raw)[0].headers).toEqual([])
  })

  it("assigns unique _key values across entries and calls", () => {
    const raw = JSON.stringify({
      mcpServers: { a: { command: "a" }, b: { command: "b" } },
    })
    const first = parseConfig(raw)
    const second = parseConfig(raw)
    const keys = [...first, ...second].map((e) => e._key)
    expect(new Set(keys).size).toBe(4)
  })
})

describe("serializeConfig", () => {
  it("returns empty string for no entries", () => {
    expect(serializeConfig([])).toBe("")
  })

  it("skips entries with a blank name and returns empty string if all are skipped", () => {
    expect(serializeConfig([entry({ name: "  " })])).toBe("")
  })

  it("serializes a stdio entry, splitting args on whitespace and dropping blanks", () => {
    const out = serializeConfig([
      entry({ name: " files ", command: "npx", args: "  -y   pkg  " }),
    ])
    expect(JSON.parse(out)).toEqual({
      mcpServers: { files: { command: "npx", args: ["-y", "pkg"] } },
    })
  })

  it("omits args/env when empty and filters env rows with blank keys", () => {
    const out = serializeConfig([
      entry({
        name: "s",
        command: "run",
        args: "   ",
        env: [
          { key: "  ", value: "ignored" },
          { key: " KEY ", value: "v" },
        ],
      }),
    ])
    expect(JSON.parse(out)).toEqual({
      mcpServers: { s: { command: "run", env: { KEY: "v" } } },
    })
  })

  it("serializes an http entry with headers and env", () => {
    const out = serializeConfig([
      entry({
        name: "remote",
        transport: "http",
        url: "https://x.dev",
        headers: [
          { key: "Authorization", value: "Bearer y" },
          { key: "", value: "dropped" },
        ],
        env: [{ key: "TOKEN", value: "t" }],
      }),
    ])
    expect(JSON.parse(out)).toEqual({
      mcpServers: {
        remote: {
          type: "http",
          url: "https://x.dev",
          headers: { Authorization: "Bearer y" },
          env: { TOKEN: "t" },
        },
      },
    })
  })

  it("omits headers/env on http entries when none survive filtering", () => {
    const out = serializeConfig([
      entry({ name: "remote", transport: "http", url: "https://x.dev" }),
    ])
    expect(JSON.parse(out)).toEqual({
      mcpServers: { remote: { type: "http", url: "https://x.dev" } },
    })
  })

  it("round-trips through parseConfig (ignoring _key)", () => {
    const original = [
      entry({ name: "files", command: "npx", args: "-y pkg", env: [{ key: "A", value: "1" }] }),
      entry({
        name: "remote",
        transport: "http",
        command: "", // command is stdio-only; serialization drops it for http
        url: "https://x.dev",
        headers: [{ key: "H", value: "v" }],
      }),
    ]
    const reparsed = parseConfig(serializeConfig(original))
    expect(reparsed.map(({ _key, ...rest }) => rest)).toEqual(
      original.map(({ _key, ...rest }) => rest),
    )
  })
})

describe("emptyEntry", () => {
  it("returns a blank stdio entry with a fresh _key each call", () => {
    const a = emptyEntry()
    const b = emptyEntry()
    expect(a).toMatchObject({
      name: "",
      transport: "stdio",
      command: "",
      args: "",
      url: "",
      headers: [],
      env: [],
    })
    expect(b._key).not.toBe(a._key)
  })
})

describe("entryFromTemplate", () => {
  const base: MCPTemplate = {
    name: "github",
    label: "GitHub",
    icon: "gh",
    transport: "stdio",
  }

  it("fills stdio fields and defaults missing command/args/url to empty strings", () => {
    const e = entryFromTemplate(base)
    expect(e).toMatchObject({
      name: "github",
      transport: "stdio",
      command: "",
      args: "",
      url: "",
      headers: [],
      env: [],
    })
  })

  it("maps streamable-http transport to http", () => {
    const e = entryFromTemplate({
      ...base,
      transport: "streamable-http",
      url: "https://api.example.com/mcp",
    })
    expect(e.transport).toBe("http")
    expect(e.url).toBe("https://api.example.com/mcp")
  })

  it("copies command and args for stdio templates", () => {
    const e = entryFromTemplate({ ...base, command: "npx", args: "-y srv" })
    expect(e.command).toBe("npx")
    expect(e.args).toBe("-y srv")
  })

  it("expands envHint into empty-valued env rows, trimming and dropping blanks", () => {
    const e = entryFromTemplate({ ...base, envHint: " GITHUB_TOKEN , ORG ,, " })
    expect(e.env).toEqual([
      { key: "GITHUB_TOKEN", value: "" },
      { key: "ORG", value: "" },
    ])
  })

  it("splits headerHint on the first colon", () => {
    const e = entryFromTemplate({
      ...base,
      transport: "streamable-http",
      headerHint: "Authorization: Bearer <token>:extra",
    })
    expect(e.headers).toEqual([
      { key: "Authorization", value: "Bearer <token>:extra" },
    ])
  })

  it("ignores headerHint without a usable key (no colon or leading colon)", () => {
    expect(entryFromTemplate({ ...base, headerHint: "no-colon-here" }).headers).toEqual([])
    expect(entryFromTemplate({ ...base, headerHint: ":only-value" }).headers).toEqual([])
  })
})
