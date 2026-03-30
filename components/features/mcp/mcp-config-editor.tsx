"use client"

import { useState, useCallback, useEffect, useRef } from "react"
import {
  Plug, Plus, Trash2, Globe, Terminal, ChevronDown,
  Check, KeyRound, Loader2, Type,
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
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"
import { toast } from "sonner"

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

interface Credential {
  id: string
  name: string
  type: string
  provider?: string
  status?: string
}

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface MCPConfigEditorProps {
  value: string
  onChange: (json: string) => void
  readOnly?: boolean
  label?: string
  workspaceId?: string
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

/** Check whether a value looks like a credential reference: ${SOME_VAR} */
function isCredentialRef(value: string): boolean {
  return /^\$\{[A-Z_][A-Z0-9_]*\}$/.test(value)
}

/** Derive a credential name from an env var key. E.g. GITHUB_TOKEN -> github-token */
function deriveCredentialName(envKey: string): string {
  return envKey.toLowerCase().replace(/_/g, "-")
}

// ---------------------------------------------------------------------------
// Credential hooks
// ---------------------------------------------------------------------------

function useCredentials(workspaceId: string | undefined) {
  const [credentials, setCredentials] = useState<Credential[]>([])
  const [loading, setLoading] = useState(false)
  const fetchedRef = useRef(false)

  const fetchCredentials = useCallback(async () => {
    if (!workspaceId) return
    setLoading(true)
    try {
      const res = await fetch(`/api/v1/credentials?workspace_id=${workspaceId}`)
      if (res.ok) {
        const data: Credential[] = await res.json()
        setCredentials(data)
      }
    } catch {
      // Silently fail — credentials are enhancement, not critical
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    if (workspaceId && !fetchedRef.current) {
      fetchedRef.current = true
      fetchCredentials()
    }
  }, [workspaceId, fetchCredentials])

  const addCredential = useCallback((cred: Credential) => {
    setCredentials((prev) => [...prev, cred])
  }, [])

  return { credentials, loading, fetchCredentials, addCredential }
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function MCPConfigEditor({
  value,
  onChange,
  readOnly = false,
  label,
  workspaceId,
}: MCPConfigEditorProps) {
  const [entries, setEntries] = useState<ServerEntry[]>(() => parseConfig(value))
  const { credentials, loading: credLoading, addCredential } = useCredentials(
    readOnly ? undefined : workspaceId,
  )

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

  const hasCredentialSupport = Boolean(workspaceId) && !readOnly

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
          credentials={credentials}
          credLoading={credLoading}
          hasCredentialSupport={hasCredentialSupport}
          workspaceId={workspaceId}
          onAddCredential={addCredential}
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
  credentials: Credential[]
  credLoading: boolean
  hasCredentialSupport: boolean
  workspaceId?: string
  onAddCredential: (cred: Credential) => void
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
  credentials,
  credLoading,
  hasCredentialSupport,
  workspaceId,
  onAddCredential,
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
                  {hasCredentialSupport ? (
                    <div className="flex-1">
                      <CredentialPicker
                        envKey={e.key}
                        envValue={e.value}
                        credentials={credentials}
                        credLoading={credLoading}
                        workspaceId={workspaceId!}
                        onAddCredential={onAddCredential}
                        onChangeValue={(val) => onUpdateEnvVar(index, eIdx, "value", val)}
                      />
                    </div>
                  ) : (
                    <Input
                      value={e.value}
                      onChange={(ev) => onUpdateEnvVar(index, eIdx, "value", ev.target.value)}
                      placeholder="${CREDENTIAL_REF}"
                      disabled={readOnly}
                      className="h-7 text-xs font-mono flex-1"
                    />
                  )}
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
              {!hasCredentialSupport && (
                <p className="text-xs text-muted-foreground">
                  Use {"${VAR_NAME}"} syntax to reference credentials. Claude Code expands environment variables automatically.
                </p>
              )}
            </div>
          </CollapsibleContent>
        </Collapsible>
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// Credential Picker
// ---------------------------------------------------------------------------

interface CredentialPickerProps {
  envKey: string
  envValue: string
  credentials: Credential[]
  credLoading: boolean
  workspaceId: string
  onAddCredential: (cred: Credential) => void
  onChangeValue: (value: string) => void
}

type PickerMode = "credential" | "manual" | "create"

function CredentialPicker({
  envKey,
  envValue,
  credentials,
  credLoading,
  workspaceId,
  onAddCredential,
  onChangeValue,
}: CredentialPickerProps) {
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<PickerMode>(() => {
    if (!envValue) return "credential"
    if (isCredentialRef(envValue)) return "credential"
    return "manual"
  })
  const [createName, setCreateName] = useState("")
  const [createValue, setCreateValue] = useState("")
  const [creating, setCreating] = useState(false)

  // Derive current credential ref key (e.g. "GITHUB_TOKEN" from "${GITHUB_TOKEN}")
  const currentRefKey = isCredentialRef(envValue)
    ? envValue.slice(2, -1)
    : null

  // Find matching credential by checking if any credential name matches the ref
  const selectedCredential = currentRefKey
    ? credentials.find(
        (c) =>
          c.name === deriveCredentialName(currentRefKey) ||
          c.name === currentRefKey ||
          c.name.toLowerCase() === currentRefKey.toLowerCase(),
      )
    : null

  function handleSelectCredential(credName: string) {
    // When selecting a credential, the env value becomes ${ENV_KEY}
    // The env key IS the var name the MCP server expects, so value = ${ENV_KEY}
    const refKey = envKey.trim() || credName.toUpperCase().replace(/-/g, "_")
    onChangeValue(`\${${refKey}}`)
    setMode("credential")
    setOpen(false)
  }

  function handleSwitchToManual() {
    setMode("manual")
    // Clear the credential ref if one was set
    if (isCredentialRef(envValue)) {
      onChangeValue("")
    }
    setOpen(false)
  }

  function handleSwitchToCreate() {
    setMode("create")
    setCreateName(envKey ? deriveCredentialName(envKey) : "")
    setCreateValue("")
  }

  async function handleCreate() {
    if (!createName.trim() || !createValue.trim()) {
      toast.error("Name and value are required")
      return
    }

    setCreating(true)
    try {
      const res = await fetch(`/api/v1/credentials?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: createName.trim(),
          type: "SECRET",
          value: createValue.trim(),
          scope: "WORKSPACE",
        }),
      })

      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed to create credential" }))
        toast.error(typeof data.error === "string" ? data.error : "Failed to create credential")
        return
      }

      const created: Credential = await res.json()
      onAddCredential(created)
      toast.success(`Credential "${createName.trim()}" created`)

      // Auto-select the new credential
      handleSelectCredential(createName.trim())
      setMode("credential")
      setCreateName("")
      setCreateValue("")
    } catch {
      toast.error("Network error creating credential")
    } finally {
      setCreating(false)
    }
  }

  // Manual mode — show plain text input
  if (mode === "manual" && !open) {
    return (
      <div className="flex items-center gap-1">
        <Input
          value={isCredentialRef(envValue) ? "" : envValue}
          onChange={(ev) => onChangeValue(ev.target.value)}
          placeholder="plain value"
          className="h-7 text-xs font-mono flex-1"
        />
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => {
            setMode("credential")
            setOpen(true)
          }}
          className="h-7 w-7 p-0 shrink-0 text-muted-foreground hover:text-foreground"
          title="Switch to credential"
        >
          <KeyRound className="h-3 w-3" />
        </Button>
      </div>
    )
  }

  // Credential / create mode — show picker trigger
  const triggerLabel = selectedCredential
    ? selectedCredential.name
    : currentRefKey
      ? currentRefKey
      : "Select credential..."

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className={cn(
            "flex items-center gap-1.5 w-full h-7 px-2 text-xs rounded-md border",
            "bg-transparent hover:bg-accent/50 transition-colors text-left",
            "border-input",
            !envValue && "text-muted-foreground",
          )}
        >
          {selectedCredential || currentRefKey ? (
            <>
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 shrink-0" />
              <span className="truncate font-mono">{triggerLabel}</span>
              {selectedCredential && (
                <Badge variant="outline" className="ml-auto h-4 text-[10px] px-1 shrink-0">
                  {selectedCredential.type}
                </Badge>
              )}
            </>
          ) : (
            <>
              <KeyRound className="h-3 w-3 shrink-0" />
              <span className="truncate">Select credential...</span>
            </>
          )}
        </button>
      </PopoverTrigger>

      <PopoverContent align="start" className="w-72 p-0">
        {credLoading ? (
          <div className="flex items-center justify-center py-6">
            <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
          </div>
        ) : mode === "create" ? (
          /* ---- Inline create form ---- */
          <div className="p-3 space-y-3">
            <div className="text-xs font-medium">Create new credential</div>
            <div className="space-y-2">
              <div className="space-y-1">
                <Label className="text-xs text-muted-foreground">Name</Label>
                <Input
                  value={createName}
                  onChange={(ev) => setCreateName(ev.target.value)}
                  placeholder="github-token"
                  className="h-7 text-xs"
                  autoFocus
                />
              </div>
              <div className="space-y-1">
                <Label className="text-xs text-muted-foreground">Secret value</Label>
                <Input
                  type="password"
                  value={createValue}
                  onChange={(ev) => setCreateValue(ev.target.value)}
                  placeholder="ghp_xxxxxxxxxxxx"
                  className="h-7 text-xs font-mono"
                />
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Button
                type="button"
                size="sm"
                className="h-7 text-xs gap-1 flex-1"
                disabled={creating || !createName.trim() || !createValue.trim()}
                onClick={handleCreate}
              >
                {creating && <Loader2 className="h-3 w-3 animate-spin" />}
                Save
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 text-xs"
                onClick={() => setMode("credential")}
                disabled={creating}
              >
                Cancel
              </Button>
            </div>
          </div>
        ) : (
          /* ---- Credential list ---- */
          <div>
            {credentials.length > 0 && (
              <div className="max-h-48 overflow-y-auto p-1">
                {credentials.map((cred) => {
                  const isSelected =
                    selectedCredential?.id === cred.id ||
                    (currentRefKey &&
                      (cred.name === deriveCredentialName(currentRefKey) ||
                        cred.name === currentRefKey))
                  return (
                    <button
                      key={cred.id}
                      type="button"
                      className={cn(
                        "flex items-center gap-2 w-full px-2 py-1.5 text-xs rounded-sm",
                        "hover:bg-accent hover:text-accent-foreground transition-colors text-left",
                        isSelected && "bg-accent/50",
                      )}
                      onClick={() => handleSelectCredential(cred.name)}
                    >
                      {isSelected ? (
                        <Check className="h-3 w-3 text-emerald-500 shrink-0" />
                      ) : (
                        <KeyRound className="h-3 w-3 text-muted-foreground shrink-0" />
                      )}
                      <span className="truncate flex-1">{cred.name}</span>
                      <Badge variant="outline" className="h-4 text-[10px] px-1 shrink-0">
                        {cred.type}
                      </Badge>
                    </button>
                  )
                })}
              </div>
            )}

            {credentials.length === 0 && (
              <div className="px-3 py-4 text-xs text-muted-foreground text-center">
                No credentials found
              </div>
            )}

            <div className="border-t p-1 space-y-0.5">
              <button
                type="button"
                className="flex items-center gap-2 w-full px-2 py-1.5 text-xs rounded-sm hover:bg-accent hover:text-accent-foreground transition-colors text-left"
                onClick={handleSwitchToCreate}
              >
                <Plus className="h-3 w-3 shrink-0" />
                <span>Create new credential</span>
              </button>
              <button
                type="button"
                className="flex items-center gap-2 w-full px-2 py-1.5 text-xs rounded-sm hover:bg-accent hover:text-accent-foreground transition-colors text-left"
                onClick={handleSwitchToManual}
              >
                <Type className="h-3 w-3 shrink-0" />
                <span>Manual value</span>
              </button>
            </div>
          </div>
        )}
      </PopoverContent>
    </Popover>
  )
}
