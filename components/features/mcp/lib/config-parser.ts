import type {
  StdioServer, HttpServer, MCPConfig, MCPServer,
  ServerEntry, MCPTemplate,
} from "../types"

// ---------------------------------------------------------------------------
// Key counter — shared across all calls for unique React keys
// ---------------------------------------------------------------------------

let nextKey = 1

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
      const args = entry.args
        .trim()
        .split(/\s+/)
        .filter(Boolean)
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
