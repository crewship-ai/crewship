"use client"

import * as React from "react"
import {
  Globe,
  Terminal,
  Search,
  BadgeCheck,
  Plus,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface RegistryServer {
  id: string
  name: string
  display_name: string
  description: string
  icon: string
  transport: string
  command: string
  package_name: string
  endpoint: string
  auth_type: string
  env_vars_json: string
  category: string
  is_verified: boolean
}

interface RegistryResponse {
  servers: RegistryServer[]
  total: number
}

export interface RegistryAddPayload {
  name: string
  display_name: string
  transport: "stdio" | "streamable-http"
  command: string | null
  args: string | null
  url: string | null
  envHint: string | null
}

// ---------------------------------------------------------------------------
// Hook: debounced value
// ---------------------------------------------------------------------------

function useDebouncedValue<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = React.useState(value)
  React.useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delay)
    return () => clearTimeout(timer)
  }, [value, delay])
  return debounced
}

// ---------------------------------------------------------------------------
// Category label helper
// ---------------------------------------------------------------------------

function categoryLabel(raw: string): string {
  return raw
    .replace(/-/g, " ")
    .replace(/\b\w/g, (c) => c.toUpperCase())
}

// ---------------------------------------------------------------------------
// Parse env vars from JSON string
// ---------------------------------------------------------------------------

function parseEnvVars(json: string): { name: string; is_required: boolean; is_secret: boolean }[] {
  if (!json) return []
  try {
    const parsed = JSON.parse(json)
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}

// ---------------------------------------------------------------------------
// Registry Browser Dialog
// ---------------------------------------------------------------------------

export function RegistryBrowser({
  open,
  onOpenChange,
  onAdd,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onAdd: (payload: RegistryAddPayload) => void | Promise<void>
}) {
  const [query, setQuery] = React.useState("")
  const debouncedQuery = useDebouncedValue(query, 300)

  const [servers, setServers] = React.useState<RegistryServer[]>([])
  const [total, setTotal] = React.useState(0)
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState("")
  const [offset, setOffset] = React.useState(0)
  const [addingId, setAddingId] = React.useState<string | null>(null)

  const PAGE_SIZE = 40
  const abortRef = React.useRef<AbortController | null>(null)

  // Fetch servers (initial or search)
  const fetchServers = React.useCallback(
    async (q: string, newOffset: number, append: boolean) => {
      abortRef.current?.abort()
      const controller = new AbortController()
      abortRef.current = controller

      setLoading(true)
      setError("")
      try {
        const url = q.trim()
          ? `/api/v1/mcp-registry/search?q=${encodeURIComponent(q.trim())}&limit=${PAGE_SIZE}&offset=${newOffset}`
          : `/api/v1/mcp-registry?limit=${PAGE_SIZE}&offset=${newOffset}`

        const res = await fetch(url, { signal: controller.signal })
        if (!res.ok) {
          if (res.status === 404) {
            setError("Registry not synced yet. Run the registry sync from settings.")
          } else {
            setError("Failed to load registry")
          }
          if (!append) {
            setServers([])
            setTotal(0)
          }
          return
        }

        const data: RegistryResponse = await res.json()
        setServers((prev) => (append ? [...prev, ...data.servers] : data.servers))
        setTotal(data.total)
        setOffset(newOffset)
      } catch (err) {
        if (err instanceof DOMException && err.name === "AbortError") return
        setError("Network error")
        if (!append) {
          setServers([])
          setTotal(0)
        }
      } finally {
        setLoading(false)
      }
    },
    [],
  )

  // Reset and fetch when query changes
  React.useEffect(() => {
    if (!open) return
    setOffset(0)
    fetchServers(debouncedQuery, 0, false)
    return () => abortRef.current?.abort()
  }, [debouncedQuery, open, fetchServers])

  // Reset state when dialog closes
  React.useEffect(() => {
    if (!open) {
      setQuery("")
      setServers([])
      setTotal(0)
      setOffset(0)
      setError("")
      setAddingId(null)
    }
  }, [open])

  function handleLoadMore() {
    const nextOffset = offset + PAGE_SIZE
    fetchServers(debouncedQuery, nextOffset, true)
  }

  async function handleAdd(server: RegistryServer) {
    setAddingId(server.id)

    const envVars = parseEnvVars(server.env_vars_json)
    const requiredVars = envVars.filter((v) => v.is_required)
    const envHint = requiredVars.length > 0
      ? requiredVars.map((v) => v.name).join(",")
      : null

    const isStdio = server.transport === "stdio"

    let command: string | null = null
    let args: string | null = null

    if (isStdio) {
      if (server.command) {
        // Use command from registry as-is (e.g., "uvx mcp-server-fetch")
        const parts = server.command.split(" ")
        command = parts[0]
        args = parts.slice(1).join(" ") || null
      } else if (server.package_name) {
        command = "npx"
        args = `-y ${server.package_name}`
      }
    }

    const payload: RegistryAddPayload = {
      name: server.name,
      display_name: server.display_name || server.name,
      transport: isStdio ? "stdio" : "streamable-http",
      command,
      args,
      url: !isStdio ? server.endpoint : null,
      envHint,
    }

    try {
      await onAdd(payload)
      onOpenChange(false)
    } catch {
      // Keep dialog open on error
    } finally {
      setAddingId(null)
    }
  }

  const hasMore = servers.length < total

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl max-h-[80vh] flex flex-col gap-0 p-0">
        <DialogHeader className="px-6 pt-6 pb-4 space-y-4 shrink-0">
          <DialogTitle>Browse MCP Registry</DialogTitle>
          <div className="relative">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
            <Input
              className="pl-9 h-9"
              placeholder="Search servers..."
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              autoFocus
            />
          </div>
          {!error && total > 0 && (
            <p className="text-xs text-muted-foreground">
              {total.toLocaleString()} servers available
            </p>
          )}
        </DialogHeader>

        <div className="flex-1 overflow-y-auto px-6 pb-6 min-h-0">
          {/* Error state */}
          {error && !loading && (
            <div className="flex items-center justify-center py-12">
              <p className="text-sm text-muted-foreground">{error}</p>
            </div>
          )}

          {/* Empty state */}
          {!error && !loading && servers.length === 0 && (
            <div className="flex items-center justify-center py-12">
              <p className="text-sm text-muted-foreground">
                {debouncedQuery ? "No servers found" : "Registry not synced yet"}
              </p>
            </div>
          )}

          {/* Results grid */}
          {servers.length > 0 && (
            <div className="grid grid-cols-1 gap-2">
              {servers.map((server) => (
                <div
                  key={server.id}
                  className="flex items-start gap-3 rounded-md border p-3 hover:bg-muted/40 transition-colors"
                >
                  <div className="flex-1 min-w-0 space-y-1">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="text-sm font-medium truncate">
                        {server.display_name || server.name}
                      </span>
                      {server.is_verified && (
                        <BadgeCheck className="h-3.5 w-3.5 text-blue-500 shrink-0" />
                      )}
                      <Badge
                        variant="outline"
                        className="text-[10px] px-1.5 py-0 shrink-0"
                      >
                        {server.transport === "stdio" ? (
                          <Terminal className="mr-0.5 h-2.5 w-2.5" />
                        ) : (
                          <Globe className="mr-0.5 h-2.5 w-2.5" />
                        )}
                        {server.transport === "stdio" ? "stdio" : "HTTP"}
                      </Badge>
                      {server.category && (
                        <Badge
                          variant="secondary"
                          className="text-[10px] px-1.5 py-0 shrink-0"
                        >
                          {categoryLabel(server.category)}
                        </Badge>
                      )}
                    </div>
                    {server.description && (
                      <p className="text-xs text-muted-foreground line-clamp-2">
                        {server.description}
                      </p>
                    )}
                    {server.package_name && (
                      <p className="text-[10px] text-muted-foreground/70 font-mono truncate">
                        {server.package_name}
                      </p>
                    )}
                  </div>

                  <Button
                    variant="outline"
                    size="sm"
                    className="h-7 text-xs shrink-0"
                    disabled={addingId === server.id}
                    onClick={() => handleAdd(server)}
                  >
                    {addingId === server.id ? (
                      <Spinner className="h-3 w-3" />
                    ) : (
                      <>
                        <Plus className="mr-1 h-3 w-3" />
                        Add
                      </>
                    )}
                  </Button>
                </div>
              ))}
            </div>
          )}

          {/* Load more */}
          {hasMore && !loading && (
            <div className="flex justify-center pt-4">
              <Button
                variant="outline"
                size="sm"
                onClick={handleLoadMore}
              >
                Load more
              </Button>
            </div>
          )}

          {/* Loading spinner */}
          {loading && (
            <div className="flex items-center justify-center py-8">
              <Spinner className="h-5 w-5 text-muted-foreground" />
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}
