import type {
  StdioServer, HttpServer, MCPConfig, MCPServer,
  ServerEntry, MCPTemplate,
} from "../types"

// ---------------------------------------------------------------------------
// Key counter — shared across all calls for unique React keys
// ---------------------------------------------------------------------------

let nextKey = 1

// ---------------------------------------------------------------------------
// Quote-aware arg tokenizer
// ---------------------------------------------------------------------------

/**
 * Splits a user-typed args string into fields, honoring single and double
 * quotes so a quoted argument with spaces (or a bare path at a spaced
 * location) survives intact — unlike a naive `.split(/\s+/)`, which shreds
 * both.
 *
 * Mirrors the backslash semantics of `internal/shlex` (Go): a backslash is a
 * literal character except when it escapes something meaningful, so a
 * Windows path like `C:\Program Files\nodejs\npx.exe` still parses correctly
 * when quoted.
 *   - Unquoted: `\` escapes only space, tab, `"`, `'`, or `\` itself;
 *     otherwise literal.
 *   - Inside double quotes: `\` escapes only `"` or `\`; otherwise literal.
 *   - Inside single quotes: everything is literal, including `\`.
 *
 *   splitArgs(`-y "@scope/pkg with space"`) => ["-y", "@scope/pkg with space"]
 *   splitArgs(`--root "/opt/my app"`)       => ["--root", "/opt/my app"]
 */
export function splitArgs(raw: string): string[] {
  const fields: string[] = []
  let cur = ""
  let inField = false
  let quote: "" | "'" | '"' = ""

  const isEscapable = (c: string) => c === " " || c === "\t" || c === "'" || c === '"' || c === "\\"

  for (let i = 0; i < raw.length; i++) {
    const c = raw[i]
    const next = i + 1 < raw.length ? raw[i + 1] : undefined

    if (c === "\\" && quote === "") {
      if (next !== undefined && isEscapable(next)) {
        cur += next
        i++
      } else {
        cur += c
      }
      inField = true
      continue
    }
    if (c === "\\" && quote === '"') {
      if (next === '"' || next === "\\") {
        cur += next
        i++
      } else {
        cur += c
      }
      continue
    }
    if (quote !== "") {
      if (c === quote) {
        quote = ""
      } else {
        cur += c
      }
      continue
    }
    if (c === "'" || c === '"') {
      quote = c
      inField = true
      continue
    }
    if (c === " " || c === "\t" || c === "\n" || c === "\r") {
      if (inField) {
        fields.push(cur)
        cur = ""
        inField = false
      }
      continue
    }
    cur += c
    inField = true
  }
  if (inField) fields.push(cur)
  return fields
}

// ---------------------------------------------------------------------------
// Parse / Serialize
// ---------------------------------------------------------------------------

/** Parse a JSON MCP config string into editable ServerEntry[]. */
export function parseConfig(raw: string): ServerEntry[] {
  if (!raw || raw.trim() === "") return []
  try {
    const parsed: MCPConfig = JSON.parse(raw)
    const servers = parsed.mcpServers ?? {}
    return Object.entries(servers).map(([name, srv]) => {
      const isHttp = "type" in srv && (srv as HttpServer).type === "http"
      return {
        _key: nextKey++,
        name,
        transport: isHttp ? "http" as const : "stdio" as const,
        command: isHttp ? "" : (srv as StdioServer).command ?? "",
        args: isHttp ? "" : ((srv as StdioServer).args ?? []).join(" "),
        url: isHttp ? (srv as HttpServer).url : "",
        headers: isHttp
          ? Object.entries((srv as HttpServer).headers ?? {}).map(([key, value]) => ({ key, value }))
          : [],
        env: Object.entries(srv.env ?? {}).map(([key, value]) => ({ key, value })),
      }
    })
  } catch {
    return []
  }
}

/** Serialize ServerEntry[] back to JSON MCP config string. */
export function serializeConfig(entries: ServerEntry[]): string {
  const mcpServers: Record<string, MCPServer> = {}

  for (const entry of entries) {
    const name = entry.name.trim()
    if (!name) continue

    const env: Record<string, string> = {}
    for (const e of entry.env) {
      if (e.key.trim()) env[e.key.trim()] = e.value
    }

    if (entry.transport === "http") {
      const headers: Record<string, string> = {}
      for (const h of entry.headers) {
        if (h.key.trim()) headers[h.key.trim()] = h.value
      }
      const server: HttpServer = { type: "http", url: entry.url }
      if (Object.keys(headers).length > 0) server.headers = headers
      if (Object.keys(env).length > 0) server.env = env
      mcpServers[name] = server
    } else {
      const server: StdioServer = { command: entry.command }
      const args = splitArgs(entry.args.trim()).filter(Boolean)
      if (args.length > 0) server.args = args
      if (Object.keys(env).length > 0) server.env = env
      mcpServers[name] = server
    }
  }

  if (Object.keys(mcpServers).length === 0) return ""
  return JSON.stringify({ mcpServers }, null, 2)
}

// ---------------------------------------------------------------------------
// Entry constructors
// ---------------------------------------------------------------------------

/** Create a blank server entry for the "Custom server" action. */
export function emptyEntry(): ServerEntry {
  return {
    _key: nextKey++,
    name: "",
    transport: "stdio",
    command: "",
    args: "",
    url: "",
    headers: [],
    env: [],
  }
}

/** Create a server entry pre-filled from a template. */
export function entryFromTemplate(template: MCPTemplate): ServerEntry {
  const entry: ServerEntry = {
    _key: nextKey++,
    name: template.name,
    transport: template.transport === "streamable-http" ? "http" : template.transport,
    command: template.command ?? "",
    args: template.args ?? "",
    url: template.url ?? "",
    headers: [],
    env: [],
  }

  if (template.envHint) {
    for (const key of template.envHint.split(",").map((s) => s.trim()).filter(Boolean)) {
      entry.env.push({ key, value: "" })
    }
  }

  if (template.headerHint) {
    const colonIdx = template.headerHint.indexOf(":")
    if (colonIdx > 0) {
      entry.headers.push({
        key: template.headerHint.slice(0, colonIdx).trim(),
        value: template.headerHint.slice(colonIdx + 1).trim(),
      })
    }
  }

  return entry
}
