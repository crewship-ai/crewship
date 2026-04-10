"use client"

import { Fragment, useCallback, useEffect, useMemo, useState } from "react"
import {
  ChevronLeft, ChevronRight, Download, RefreshCw, Search, Shield,
} from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useWorkspace } from "@/hooks/use-workspace"
import { STATUS_BG_LIGHT } from "@/lib/colors"
import { cn } from "@/lib/utils"

interface AuditUser {
  id: string
  email: string
  full_name: string | null
}

interface AuditLog {
  id: string
  action: string
  entity_type: string
  entity_id: string | null
  metadata: Record<string, unknown> | null
  ip_address: string | null
  user_agent: string | null
  user: AuditUser | null
  created_at: string
}

interface AuditResponse {
  data: AuditLog[]
  pagination: {
    page: number
    limit: number
    total: number
    total_pages: number
  }
}

const categories = [
  { label: "All", value: "all" },
  { label: "Agents", value: "Agent" },
  { label: "Credentials", value: "Credential" },
  { label: "Crews", value: "Crew" },
  { label: "Users", value: "WorkspaceMember" },
  { label: "System", value: "Workspace" },
]

const dateRanges = [
  { label: "Last 24 hours", value: "24h" },
  { label: "Last 7 days", value: "7d" },
  { label: "Last 30 days", value: "30d" },
  { label: "All time", value: "all" },
]

const PAGE_SIZE = 25

function getDateFrom(range: string): string | undefined {
  const now = new Date()
  switch (range) {
    case "24h": return new Date(now.getTime() - 24 * 60 * 60 * 1000).toISOString()
    case "7d": return new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000).toISOString()
    case "30d": return new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000).toISOString()
    default: return undefined
  }
}

/**
 * Maps free-form audit action verbs ("user.created", "credential.rotated", …)
 * onto canonical status keys so every action pill can reuse STATUS_BG_LIGHT
 * and stay on the centralized palette.
 */
function actionStatusKey(action: string): string {
  const a = action.toLowerCase()
  if (a.includes("deleted") || a.includes("failed") || a.includes("revoked")) return "FAILED"
  if (a.includes("rotated") || a.includes("blocked")) return "BLOCKED"
  if (a.includes("created") || a.includes("started") || a.includes("completed")) return "COMPLETED"
  if (a.includes("updated") || a.includes("invited") || a.includes("changed")) return "IN_PROGRESS"
  return "PENDING"
}

function getActionClasses(action: string): string {
  return STATUS_BG_LIGHT[actionStatusKey(action)] ?? "bg-muted text-muted-foreground"
}

export default function AuditPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [logs, setLogs] = useState<AuditLog[]>([])
  const [totalCount, setTotalCount] = useState(0)
  const [totalPages, setTotalPages] = useState(1)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [category, setCategory] = useState("all")
  const [dateRange, setDateRange] = useState("7d")
  const [searchQuery, setSearchQuery] = useState("")

  const fetchLogs = useCallback(
    async (opts?: { silent?: boolean }) => {
      if (!workspaceId) return
      const silent = opts?.silent ?? false
      if (silent) setRefreshing(true)
      else setLoading(true)
      setError(null)
      try {
        const params = new URLSearchParams({
          workspace_id: workspaceId as string,
          page: String(page),
          limit: String(PAGE_SIZE),
        })
        if (category !== "all") params.set("entity_type", category)
        const dateFrom = getDateFrom(dateRange)
        if (dateFrom) params.set("date_from", dateFrom)

        const res = await fetch(`/api/v1/audit?${params}`)
        if (!res.ok) {
          setError("Failed to load audit logs")
          return
        }
        const data = (await res.json()) as AuditResponse
        setLogs(data.data)
        setTotalCount(data.pagination.total)
        setTotalPages(data.pagination.total_pages || 1)
      } catch {
        setError("Failed to load audit logs")
      } finally {
        setLoading(false)
        setRefreshing(false)
      }
    },
    [workspaceId, page, category, dateRange],
  )

  useEffect(() => {
    fetchLogs()
  }, [fetchLogs])

  // Reset page when filters change (otherwise user stays on e.g. page 4 of an empty filtered set)
  useEffect(() => {
    setPage(1)
  }, [category, dateRange])

  const isLoading = wsLoading || loading

  const filteredLogs = useMemo(() => {
    if (!searchQuery) return logs
    const q = searchQuery.toLowerCase()
    return logs.filter(
      (log) =>
        log.action.toLowerCase().includes(q) ||
        log.entity_type.toLowerCase().includes(q) ||
        (log.user?.full_name ?? log.user?.email ?? "").toLowerCase().includes(q),
    )
  }, [logs, searchQuery])

  const handleExport = () => {
    if (filteredLogs.length === 0) return
    const rows = filteredLogs.map((log) => ({
      timestamp: log.created_at,
      action: log.action,
      entity_type: log.entity_type,
      entity_id: log.entity_id,
      user: log.user?.full_name ?? log.user?.email ?? "",
      ip_address: log.ip_address ?? "",
    }))
    const header = Object.keys(rows[0] ?? {}).join(",")
    const csv = [
      header,
      ...rows.map((r) =>
        Object.values(r)
          .map((v) => `"${String(v).replace(/"/g, '""')}"`)
          .join(","),
      ),
    ].join("\n")
    const blob = new Blob([csv], { type: "text/csv" })
    const url = URL.createObjectURL(blob)
    const a = document.createElement("a")
    a.href = url
    a.download = `audit-log-${new Date().toISOString().slice(0, 10)}.csv`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="p-4 md:p-6 pb-10 space-y-4 bg-background min-h-[calc(100vh-48px)]">
      {/* ── Header ─────────────────────────────────────────────── */}
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div className="flex items-center gap-2">
          <Shield className="h-3.5 w-3.5 text-foreground/50" />
          <h1 className="text-body font-medium text-foreground/80">Audit Log</h1>
          <span className="text-[10px] font-mono text-muted-foreground/60">
            {totalCount > 0 ? `${totalCount.toLocaleString()} events` : "no events"}
          </span>
        </div>
        <div className="flex items-center gap-1.5">
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => fetchLogs({ silent: true })}
            disabled={isLoading || refreshing}
          >
            <RefreshCw className={cn("h-3 w-3 mr-1.5", refreshing && "animate-spin")} />
            Refresh
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            disabled={filteredLogs.length === 0}
            onClick={handleExport}
          >
            <Download className="h-3 w-3 mr-1.5" />
            Export CSV
          </Button>
        </div>
      </div>

      {/* ── Filters ────────────────────────────────────────────── */}
      <div className="flex items-center gap-2 flex-wrap">
        {/* Category pills */}
        <div className="inline-flex items-center gap-0.5 p-0.5 rounded-md bg-white/[0.04] border border-border/60">
          {categories.map((cat) => (
            <button
              key={cat.value}
              onClick={() => setCategory(cat.value)}
              className={cn(
                "h-6 px-2.5 rounded text-[11px] font-medium transition-colors",
                category === cat.value
                  ? "bg-white/[0.08] text-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {cat.label}
            </button>
          ))}
        </div>

        {/* Date range */}
        <Select value={dateRange} onValueChange={setDateRange}>
          <SelectTrigger className="h-7 w-[140px] text-xs">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {dateRanges.map((dr) => (
              <SelectItem key={dr.value} value={dr.value} className="text-xs">
                {dr.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        {/* Search */}
        <div className="relative flex-1 min-w-[180px] max-w-[280px]">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
          <Input
            placeholder="Search events…"
            className="h-7 pl-7 text-xs"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
          />
        </div>
      </div>

      {error && (
        <div className="text-xs text-destructive px-3 py-2 rounded-md border border-destructive/30 bg-destructive/5">
          {error}
        </div>
      )}

      {/* ── Content ────────────────────────────────────────────── */}
      {isLoading ? (
        <div className="flex flex-col gap-1.5">
          {Array.from({ length: 8 }).map((_, i) => (
            <Skeleton key={i} className="h-9 rounded-md" />
          ))}
        </div>
      ) : filteredLogs.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center mb-3">
            <Shield className="h-4 w-4 text-muted-foreground/60" />
          </div>
          <div className="text-sm font-medium text-foreground/80">No activity yet</div>
          <div className="text-[11px] text-muted-foreground mt-0.5 max-w-xs">
            All state-changing actions will be logged here with who, what, and when.
          </div>
        </div>
      ) : (
        <div className="rounded-xl border border-border/60 bg-card overflow-hidden">
          {/* Header row */}
          <div
            className="hidden md:grid items-center gap-3 px-4 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60 border-b border-border/60"
            style={{ gridTemplateColumns: "16px 140px minmax(0,1fr) minmax(0,1.4fr) minmax(0,1.2fr)" }}
          >
            <div />
            <div>Time</div>
            <div>User</div>
            <div>Action</div>
            <div>Entity</div>
          </div>

          {/* Rows */}
          <div>
            {filteredLogs.map((log) => {
              const isExpanded = expandedId === log.id
              return (
                <Fragment key={log.id}>
                  <button
                    type="button"
                    onClick={() => setExpandedId(isExpanded ? null : log.id)}
                    className={cn(
                      "grid items-center gap-3 w-full px-4 py-2 text-left transition-colors border-b border-border/40 last:border-b-0",
                      "hover:bg-white/[0.02]",
                      isExpanded && "bg-white/[0.03]",
                    )}
                    style={{
                      gridTemplateColumns: "16px 140px minmax(0,1fr) minmax(0,1.4fr) minmax(0,1.2fr)",
                    }}
                  >
                    <ChevronRight
                      className={cn(
                        "h-3 w-3 text-muted-foreground/60 transition-transform",
                        isExpanded && "rotate-90 text-foreground",
                      )}
                    />
                    <div className="text-[10px] font-mono text-muted-foreground tabular-nums truncate">
                      {new Date(log.created_at).toLocaleString()}
                    </div>
                    <div className="text-xs text-foreground/80 truncate">
                      {log.user?.full_name ?? log.user?.email ?? (
                        <span className="text-muted-foreground/60">System</span>
                      )}
                    </div>
                    <div>
                      <span
                        className={cn(
                          "inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold uppercase tracking-wide",
                          getActionClasses(log.action),
                        )}
                      >
                        {log.action}
                      </span>
                    </div>
                    <div className="text-xs text-muted-foreground truncate">
                      {log.entity_type}
                      {log.entity_id && (
                        <span className="ml-1 font-mono text-[10px] text-muted-foreground/60">
                          {log.entity_id.slice(0, 8)}
                        </span>
                      )}
                    </div>
                  </button>

                  {isExpanded && (
                    <div className="px-4 py-3 bg-white/[0.02] border-b border-border/40">
                      <div className="grid gap-3 text-[11px] sm:grid-cols-2 max-w-3xl">
                        <div>
                          <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60 mb-0.5">
                            IP Address
                          </div>
                          <div className="font-mono text-foreground/80">
                            {log.ip_address ?? "—"}
                          </div>
                        </div>
                        <div>
                          <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60 mb-0.5">
                            User agent
                          </div>
                          <div className="font-mono text-foreground/80 truncate" title={log.user_agent ?? ""}>
                            {log.user_agent ?? "—"}
                          </div>
                        </div>
                        {log.metadata && Object.keys(log.metadata).length > 0 && (
                          <div className="sm:col-span-2">
                            <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60 mb-1">
                              Metadata
                            </div>
                            <pre className="bg-muted/40 border border-border/60 rounded p-2 text-[10px] font-mono text-muted-foreground overflow-auto max-h-32">
                              {JSON.stringify(log.metadata, null, 2)}
                            </pre>
                          </div>
                        )}
                      </div>
                      <div className="flex items-center gap-1.5 mt-3 text-[10px] text-muted-foreground/60">
                        <Shield className="h-3 w-3" />
                        This record is immutable — it cannot be edited or deleted.
                      </div>
                    </div>
                  )}
                </Fragment>
              )
            })}
          </div>
        </div>
      )}

      {/* ── Pagination ─────────────────────────────────────────── */}
      {!isLoading && totalPages > 1 && (
        <div className="flex items-center justify-between gap-2 flex-wrap">
          <div className="text-[11px] text-muted-foreground font-mono tabular-nums">
            Page <span className="text-foreground/80">{page}</span> of{" "}
            <span className="text-foreground/80">{totalPages}</span> ·{" "}
            {totalCount.toLocaleString()} total
          </div>
          <div className="flex items-center gap-1.5">
            <Button
              variant="outline"
              size="sm"
              className="h-7 px-2 text-xs"
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              disabled={page === 1 || isLoading}
            >
              <ChevronLeft className="h-3 w-3 mr-1" />
              Previous
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="h-7 px-2 text-xs"
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              disabled={page >= totalPages || isLoading}
            >
              Next
              <ChevronRight className="h-3 w-3 ml-1" />
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
