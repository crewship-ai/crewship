// ---------------------------------------------------------------------------
// Integration API <-> MCPConfigEditor adapter
// ---------------------------------------------------------------------------
//
// Converts between the REST API crew_mcp_servers representation and the
// MCPConfigEditor's internal ServerEntry form-state format.
// ---------------------------------------------------------------------------

import type { ServerEntry } from "../types"

let nextAdapterKey = 100_000

// ---------------------------------------------------------------------------
// API response type (matches crewMCPServerResponse in Go)
// ---------------------------------------------------------------------------

export interface CrewMCPServer {
  id: string
  crew_id: string
  workspace_mcp_server_id?: string | null
  name: string
  display_name: string
  transport: string
  endpoint: string | null
  command: string | null
  args_json: string | null
  env_json: string | null
  config_json: string | null
  icon: string | null
  enabled: boolean
  created_at?: string
  updated_at?: string
  agent_binding_count?: number
}

// ---------------------------------------------------------------------------
// API → Editor
// ---------------------------------------------------------------------------

/** Convert a crew MCP server API response into an editor ServerEntry. */
export function crewServerToEntry(server: CrewMCPServer, key?: number): ServerEntry {
  const isHttp = server.transport === "streamable-http"

  // Parse args_json (JSON array of strings) into space-separated string
  let args = ""
  if (server.args_json) {
    try {
      const parsed: string[] = JSON.parse(server.args_json)
      if (Array.isArray(parsed)) {
        args = parsed.join(" ")
      }
    } catch {
      args = ""
    }
  }

  // Parse env_json (JSON object) into EnvEntry[]
  let env: { key: string; value: string }[] = []
  if (server.env_json) {
    try {
      const parsed: Record<string, string> = JSON.parse(server.env_json)
      env = Object.entries(parsed).map(([k, v]) => ({ key: k, value: v }))
    } catch {
      env = []
    }
  }

  // Parse config_json for headers (used by http transport)
  let headers: { key: string; value: string }[] = []
  if (isHttp && server.config_json) {
    try {
      const parsed = JSON.parse(server.config_json)
      if (parsed && typeof parsed === "object" && parsed.headers) {
        headers = Object.entries(parsed.headers as Record<string, string>).map(
          ([k, v]) => ({ key: k, value: v }),
        )
      }
    } catch {
      headers = []
    }
  }

  return {
    _key: key ?? nextAdapterKey++,
    id: server.id,
    name: server.name,
    transport: isHttp ? "http" : "stdio",
    command: isHttp ? "" : (server.command ?? ""),
    args: isHttp ? "" : args,
    url: isHttp ? (server.endpoint ?? "") : "",
    headers,
    env,
  }
}

// ---------------------------------------------------------------------------
// Editor → API (create payload)
// ---------------------------------------------------------------------------

interface CreatePayload {
  name: string
  display_name: string
  transport: string
  endpoint?: string
  command?: string
  args_json?: string
  env_json?: string
  config_json?: string
}

/** Convert an editor ServerEntry into a create/update API payload. */
export function entryToPayload(entry: ServerEntry): CreatePayload {
  const transport = entry.transport === "http" ? "streamable-http" : "stdio"

  const payload: CreatePayload = {
    name: entry.name.trim(),
    display_name: entry.name.trim(),
    transport,
  }

  if (transport === "streamable-http") {
    payload.endpoint = entry.url
    // Serialize headers into config_json
    const headers: Record<string, string> = {}
    for (const h of entry.headers) {
      if (h.key.trim()) headers[h.key.trim()] = h.value
    }
    if (Object.keys(headers).length > 0) {
      payload.config_json = JSON.stringify({ headers })
    }
  } else {
    payload.command = entry.command
    const argsList = entry.args
      .trim()
      .split(/\s+/)
      .filter(Boolean)
    if (argsList.length > 0) {
      payload.args_json = JSON.stringify(argsList)
    }
  }

  // Serialize env
  const env: Record<string, string> = {}
  for (const e of entry.env) {
    if (e.key.trim()) env[e.key.trim()] = e.value
  }
  if (Object.keys(env).length > 0) {
    payload.env_json = JSON.stringify(env)
  }

  return payload
}

// ---------------------------------------------------------------------------
// Diff engine
// ---------------------------------------------------------------------------

interface EntryDiff {
  create: ServerEntry[]
  update: ServerEntry[]
  remove: string[]
}

/** Compare original vs current entries to produce create/update/remove lists. */
export function diffEntries(original: ServerEntry[], current: ServerEntry[]): EntryDiff {
  const originalById = new Map<string, ServerEntry>()
  for (const entry of original) {
    if (entry.id) {
      originalById.set(entry.id, entry)
    }
  }

  const currentIds = new Set<string>()
  const create: ServerEntry[] = []
  const update: ServerEntry[] = []

  for (const entry of current) {
    if (!entry.id) {
      // New entry — needs creation
      create.push(entry)
    } else {
      currentIds.add(entry.id)
      const orig = originalById.get(entry.id)
      if (orig && hasEntryChanged(orig, entry)) {
        update.push(entry)
      }
    }
  }

  // IDs present in original but missing from current → removed
  const remove: string[] = []
  for (const id of originalById.keys()) {
    if (!currentIds.has(id)) {
      remove.push(id)
    }
  }

  return { create, update, remove }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function hasEntryChanged(a: ServerEntry, b: ServerEntry): boolean {
  if (a.name !== b.name) return true
  if (a.transport !== b.transport) return true
  if (a.command !== b.command) return true
  if (a.args !== b.args) return true
  if (a.url !== b.url) return true
  if (serializeKV(a.env) !== serializeKV(b.env)) return true
  if (serializeKV(a.headers) !== serializeKV(b.headers)) return true
  return false
}

function serializeKV(pairs: { key: string; value: string }[]): string {
  return pairs
    .map((p) => `${p.key}=${p.value}`)
    .sort()
    .join("|")
}
