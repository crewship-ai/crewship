"use client"

import { useState, useCallback, useEffect } from "react"
import {
  Plug, Plus, Trash2, Globe, Terminal, ChevronDown,
} from "lucide-react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface StdioServer {
  command: string
  args?: string[]
  env?: Record<string, string>
}

interface HttpServer {
  type: "http"
  url: string
  headers?: Record<string, string>
  env?: Record<string, string>
}

type MCPServer = StdioServer | HttpServer

interface MCPConfig {
  mcpServers: Record<string, MCPServer>
}

interface ServerEntry {
  /** Unique key for React list rendering; NOT the server name. */
  _key: number
  name: string
  transport: "stdio" | "http"
  command: string
  args: string
  url: string
  headers: { key: string; value: string }[]
  env: { key: string; value: string }[]
}

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface MCPConfigEditorProps {
  value: string
  onChange: (json: string) => void
  readOnly?: boolean
  label?: string
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

let nextKey = 1

function parseConfig(raw: string): ServerEntry[] {
  if (!raw || raw.trim() === "") return []
  try {
    const parsed: MCPConfig = JSON.parse(raw)
    const servers = parsed.mcpServers ?? {}
    return Object.entries(servers).map(([name, srv]) => {
      const isHttp = "type" in srv && (srv as HttpServer).type === "http"
      const entry: ServerEntry = {
        _key: nextKey++,
        name,
        transport: isHttp ? "http" : "stdio",
        command: isHttp ? "" : (srv as StdioServer).command ?? "",
        args: isHttp ? "" : ((srv as StdioServer).args ?? []).join(" "),
        url: isHttp ? (srv as HttpServer).url : "",
        headers: isHttp
          ? Object.entries((srv as HttpServer).headers ?? {}).map(([key, value]) => ({ key, value }))
          : [],
        env: Object.entries(srv.env ?? {}).map(([key, value]) => ({ key, value })),
      }
      return entry
    })
  } catch {
    return []
  }
}

function serializeConfig(entries: ServerEntry[]): string {
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

function emptyEntry(): ServerEntry {
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

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function MCPConfigEditor({
  value,
  onChange,
  readOnly = false,
  label,
}: MCPConfigEditorProps) {
  const [entries, setEntries] = useState<ServerEntry[]>(() => parseConfig(value))

  // Sync from parent when value changes externally
  useEffect(() => {
    const parsed = parseConfig(value)
    // Only reset if the serialized form differs (avoids cursor jump)
    if (serializeConfig(parsed) !== serializeConfig(entries)) {
      setEntries(parsed)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value])

  const emit = useCallback(
    (updated: ServerEntry[]) => {
      setEntries(updated)
      onChange(serializeConfig(updated))
    },
    [onChange],
  )

  function updateEntry(index: number, patch: Partial<ServerEntry>) {
    const updated = entries.map((e, i) => (i === index ? { ...e, ...patch } : e))
    emit(updated)
  }

  function removeEntry(index: number) {
    emit(entries.filter((_, i) => i !== index))
  }

  function addEntry() {
    emit([...entries, emptyEntry()])
  }

  // -- Key-value pair helpers -----------------------------------------------

  function addEnvVar(index: number) {
    const updated = [...entries]
    updated[index] = { ...updated[index], env: [...updated[index].env, { key: "", value: "" }] }
    emit(updated)
  }

  function updateEnvVar(serverIdx: number, envIdx: number, field: "key" | "value", val: string) {
    const updated = [...entries]
    const env = [...updated[serverIdx].env]
    env[envIdx] = { ...env[envIdx], [field]: val }
    updated[serverIdx] = { ...updated[serverIdx], env }
    emit(updated)
  }

  function removeEnvVar(serverIdx: number, envIdx: number) {
    const updated = [...entries]
    updated[serverIdx] = {
      ...updated[serverIdx],
      env: updated[serverIdx].env.filter((_, i) => i !== envIdx),
    }
    emit(updated)
  }

  function addHeader(index: number) {
    const updated = [...entries]
    updated[index] = { ...updated[index], headers: [...updated[index].headers, { key: "", value: "" }] }
    emit(updated)
  }

  function updateHeader(serverIdx: number, hdrIdx: number, field: "key" | "value", val: string) {
    const updated = [...entries]
    const headers = [...updated[serverIdx].headers]
    headers[hdrIdx] = { ...headers[hdrIdx], [field]: val }
    updated[serverIdx] = { ...updated[serverIdx], headers }
    emit(updated)
  }

  function removeHeader(serverIdx: number, hdrIdx: number) {
    const updated = [...entries]
    updated[serverIdx] = {
      ...updated[serverIdx],
      headers: updated[serverIdx].headers.filter((_, i) => i !== hdrIdx),
    }
    emit(updated)
  }

  return (
    <div className="space-y-4">
      {label && (
        <div className="flex items-center gap-2">
          <Plug className="h-4 w-4 text-muted-foreground" />
          <span className="text-sm font-medium">{label}</span>
        </div>
      )}

      {entries.length === 0 && (
        <p className="text-sm text-muted-foreground">
          No MCP servers configured.
        </p>
      )}

      {entries.map((entry, idx) => (
        <ServerCard
          key={entry._key}
          entry={entry}
          index={idx}
          readOnly={readOnly}
          onUpdate={updateEntry}
          onRemove={removeEntry}
          onAddEnvVar={addEnvVar}
          onUpdateEnvVar={updateEnvVar}
          onRemoveEnvVar={removeEnvVar}
          onAddHeader={addHeader}
          onUpdateHeader={updateHeader}
          onRemoveHeader={removeHeader}
        />
      ))}

      {!readOnly && (
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={addEntry}
          className="gap-1.5"
        >
          <Plus className="h-3.5 w-3.5" />
          Add MCP Server
        </Button>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Server Card
// ---------------------------------------------------------------------------

interface ServerCardProps {
  entry: ServerEntry
  index: number
  readOnly: boolean
  onUpdate: (index: number, patch: Partial<ServerEntry>) => void
  onRemove: (index: number) => void
  onAddEnvVar: (index: number) => void
  onUpdateEnvVar: (serverIdx: number, envIdx: number, field: "key" | "value", val: string) => void
  onRemoveEnvVar: (serverIdx: number, envIdx: number) => void
  onAddHeader: (index: number) => void
  onUpdateHeader: (serverIdx: number, hdrIdx: number, field: "key" | "value", val: string) => void
  onRemoveHeader: (serverIdx: number, hdrIdx: number) => void
}

function ServerCard({
  entry,
  index,
  readOnly,
  onUpdate,
  onRemove,
  onAddEnvVar,
  onUpdateEnvVar,
  onRemoveEnvVar,
  onAddHeader,
  onUpdateHeader,
  onRemoveHeader,
}: ServerCardProps) {
  const [advancedOpen, setAdvancedOpen] = useState(
    entry.env.length > 0 || entry.headers.length > 0,
  )

  return (
    <Card>
      <CardHeader className="pb-0">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2 flex-1 min-w-0">
            {readOnly ? (
              <CardTitle className="text-sm font-mono truncate">
                {entry.name || "(unnamed)"}
              </CardTitle>
            ) : (
              <Input
                value={entry.name}
                onChange={(e) => onUpdate(index, { name: e.target.value })}
                placeholder="server-name"
                className="h-8 text-sm font-mono max-w-[200px]"
              />
            )}
            <Badge variant="secondary" className="gap-1 shrink-0">
              {entry.transport === "http" ? (
                <>
                  <Globe className="h-3 w-3" />
                  HTTP
                </>
              ) : (
                <>
                  <Terminal className="h-3 w-3" />
                  stdio
                </>
              )}
            </Badge>
          </div>
          {!readOnly && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => onRemove(index)}
              className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          )}
        </div>
      </CardHeader>

      <CardContent className="space-y-3 pt-3">
        {/* Transport selector */}
        {!readOnly && (
          <div className="space-y-1.5">
            <Label className="text-xs">Transport</Label>
            <Select
              value={entry.transport}
              onValueChange={(val: "stdio" | "http") =>
                onUpdate(index, { transport: val })
              }
            >
              <SelectTrigger className="w-full h-8 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="stdio">stdio (local process)</SelectItem>
                <SelectItem value="http">HTTP (remote)</SelectItem>
              </SelectContent>
            </Select>
          </div>
        )}

        {/* Transport-specific fields */}
        {entry.transport === "stdio" ? (
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label className="text-xs">Command</Label>
              <Input
                value={entry.command}
                onChange={(e) => onUpdate(index, { command: e.target.value })}
                placeholder="npx"
                disabled={readOnly}
                className="h-8 text-xs font-mono"
              />
            </div>
            <div className="space-y-1.5">
              <Label className="text-xs">Arguments</Label>
              <Input
                value={entry.args}
                onChange={(e) => onUpdate(index, { args: e.target.value })}
                placeholder="-y @modelcontextprotocol/server-github"
                disabled={readOnly}
                className="h-8 text-xs font-mono"
              />
              <p className="text-xs text-muted-foreground">
                Space-separated arguments passed to the command.
              </p>
            </div>
          </div>
        ) : (
          <div className="space-y-1.5">
            <Label className="text-xs">URL</Label>
            <Input
              value={entry.url}
              onChange={(e) => onUpdate(index, { url: e.target.value })}
              placeholder="https://mcp.example.com/sse"
              disabled={readOnly}
              className="h-8 text-xs font-mono"
            />
          </div>
        )}

        {/* Collapsible advanced section for headers + env */}
        <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
          <CollapsibleTrigger asChild>
            <button
              type="button"
              className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
            >
              <ChevronDown
                className={cn(
                  "h-3.5 w-3.5 transition-transform",
                  advancedOpen && "rotate-180",
                )}
              />
              Advanced ({entry.env.length} env{entry.transport === "http" ? `, ${entry.headers.length} headers` : ""})
            </button>
          </CollapsibleTrigger>

          <CollapsibleContent className="space-y-3 pt-2">
            {/* Headers (HTTP only) */}
            {entry.transport === "http" && (
              <div className="space-y-2">
                <Label className="text-xs">Headers</Label>
                {entry.headers.map((h, hIdx) => (
                  <div key={hIdx} className="flex items-center gap-2">
                    <Input
                      value={h.key}
                      onChange={(e) => onUpdateHeader(index, hIdx, "key", e.target.value)}
                      placeholder="Header-Name"
                      disabled={readOnly}
                      className="h-7 text-xs font-mono flex-1"
                    />
                    <Input
                      value={h.value}
                      onChange={(e) => onUpdateHeader(index, hIdx, "value", e.target.value)}
                      placeholder="Bearer ${TOKEN}"
                      disabled={readOnly}
                      className="h-7 text-xs font-mono flex-1"
                    />
                    {!readOnly && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => onRemoveHeader(index, hIdx)}
                        className="h-7 w-7 p-0 shrink-0 text-muted-foreground hover:text-destructive"
                      >
                        <Trash2 className="h-3 w-3" />
                      </Button>
                    )}
                  </div>
                ))}
                {!readOnly && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => onAddHeader(index)}
                    className="h-7 text-xs gap-1"
                  >
                    <Plus className="h-3 w-3" />
                    Add Header
                  </Button>
                )}
              </div>
            )}

            {/* Environment variables */}
            <div className="space-y-2">
              <Label className="text-xs">Environment Variables</Label>
              {entry.env.map((e, eIdx) => (
                <div key={eIdx} className="flex items-center gap-2">
                  <Input
                    value={e.key}
                    onChange={(ev) => onUpdateEnvVar(index, eIdx, "key", ev.target.value)}
                    placeholder="VAR_NAME"
                    disabled={readOnly}
                    className="h-7 text-xs font-mono flex-1"
                  />
                  <Input
                    value={e.value}
                    onChange={(ev) => onUpdateEnvVar(index, eIdx, "value", ev.target.value)}
                    placeholder="${CREDENTIAL_REF}"
                    disabled={readOnly}
                    className="h-7 text-xs font-mono flex-1"
                  />
                  {!readOnly && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => onRemoveEnvVar(index, eIdx)}
                      className="h-7 w-7 p-0 shrink-0 text-muted-foreground hover:text-destructive"
                    >
                      <Trash2 className="h-3 w-3" />
                    </Button>
                  )}
                </div>
              ))}
              {!readOnly && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => onAddEnvVar(index)}
                  className="h-7 text-xs gap-1"
                >
                  <Plus className="h-3 w-3" />
                  Add Variable
                </Button>
              )}
              <p className="text-xs text-muted-foreground">
                Use {"${VAR_NAME}"} syntax to reference credentials. Claude Code expands environment variables automatically.
              </p>
            </div>
          </CollapsibleContent>
        </Collapsible>
      </CardContent>
    </Card>
  )
}
