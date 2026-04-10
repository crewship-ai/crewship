"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { Shield, ChevronRight, ChevronLeft, Search } from "lucide-react"
import { motion, AnimatePresence } from "motion/react"
import { Skeleton } from "@/components/ui/skeleton"
import { Input } from "@/components/ui/input"
import { StatusBadge } from "@/components/ui/status-badge"
import { Button } from "@/components/ui/button"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"

interface AuditLog {
  id: string
  action: string
  entity_type: string
  entity_id: string | null
  metadata: Record<string, unknown> | null
  ip_address: string | null
  user_agent: string | null
  user: { id: string; email: string; full_name: string | null } | null
  created_at: string
}

interface AuditPagination { page: number; limit: number; total: number; total_pages: number }

const categories = [
  { label: "All", value: "all" },
  { label: "Agents", value: "Agent" },
  { label: "Credentials", value: "Credential" },
  { label: "Crews", value: "Crew" },
  { label: "Users", value: "WorkspaceMember" },
  { label: "System", value: "Workspace" },
]

const dateRanges = [
  { label: "Last 24h", value: "24h" },
  { label: "Last 7d", value: "7d" },
  { label: "Last 30d", value: "30d" },
  { label: "All time", value: "all" },
]

function getDateFrom(range: string): string | undefined {
  const now = new Date()
  switch (range) {
    case "24h": return new Date(now.getTime() - 24 * 60 * 60 * 1000).toISOString()
    case "7d": return new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000).toISOString()
    case "30d": return new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000).toISOString()
    default: return undefined
  }
}

// Map audit action verbs → canonical StatusBadge keys so colors are routed
// through STATUS_BADGE_CLASSES instead of hardcoded shade classes.
const actionStatusKeys: Record<string, string> = {
  created: "COMPLETED",
  started: "COMPLETED",
  completed: "COMPLETED",
  updated: "IN_PROGRESS",
  rotated: "BLOCKED",
  invited: "IN_PROGRESS",
  deleted: "FAILED",
  failed: "FAILED",
}

function getActionStatusKey(action: string): string {
  for (const [key, statusKey] of Object.entries(actionStatusKeys)) {
    if (action.includes(key)) return statusKey
  }
  return "PENDING"
}

const PAGE_SIZE = 50

/** Normalize API response — handles both nested and flat user/metadata shapes */
function normalizeLog(raw: Record<string, unknown>): AuditLog {
  let user: AuditLog["user"] = null
  if (raw.user && typeof raw.user === "object") {
    const u = raw.user as Record<string, unknown>
    user = { id: String(u.id ?? ""), email: String(u.email ?? ""), full_name: (u.full_name as string | null) ?? null }
  } else if (raw.user_email) {
    user = { id: "", email: String(raw.user_email), full_name: (raw.user_name as string | null) ?? null }
  }

  let metadata: Record<string, unknown> | null = null
  if (typeof raw.metadata === "string") {
    try { metadata = JSON.parse(raw.metadata) } catch { metadata = null }
  } else if (raw.metadata && typeof raw.metadata === "object") {
    metadata = raw.metadata as Record<string, unknown>
  }

  return {
    id: String(raw.id ?? ""),
    action: String(raw.action ?? ""),
    entity_type: String(raw.entity_type ?? ""),
    entity_id: (raw.entity_id as string | null) ?? null,
    metadata,
    ip_address: (raw.ip_address as string | null) ?? null,
    user_agent: (raw.user_agent as string | null) ?? null,
    user,
    created_at: String(raw.created_at ?? ""),
  }
}

interface CrewAuditSectionProps {
  workspaceId: string
}

export function CrewAuditSection({ workspaceId }: CrewAuditSectionProps) {
  const [logs, setLogs] = useState<AuditLog[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [category, setCategory] = useState("all")
  const [dateRange, setDateRange] = useState("7d")
  const [searchQuery, setSearchQuery] = useState("")
  const [page, setPage] = useState(1)
  const [pagination, setPagination] = useState<AuditPagination | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const fetchLogs = useCallback(async () => {
    // Abort any in-flight request
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller

    setLoading(true)
    setError(null)
    try {
      const params = new URLSearchParams({ workspace_id: workspaceId, page: String(page), limit: String(PAGE_SIZE) })
      if (category !== "all") params.set("entity_type", category)
      const dateFrom = getDateFrom(dateRange)
      if (dateFrom) params.set("date_from", dateFrom)

      const res = await fetch(`/api/v1/audit?${params}`, { signal: controller.signal })
      if (!res.ok) {
        setError(`Failed to load audit logs (${res.status})`)
        return
      }
      const raw = await res.json()
      const data = Array.isArray(raw.data) ? raw.data.map(normalizeLog) : []
      setLogs(data)
      setPagination(raw.pagination ?? null)
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") return
      setError("Failed to load audit logs")
    } finally {
      setLoading(false)
    }
  }, [workspaceId, category, dateRange, page])

  useEffect(() => { fetchLogs() }, [fetchLogs])

  // Reset page when filters change
  function handleCategoryChange(value: string) {
    setCategory(value)
    setPage(1)
  }
  function handleDateRangeChange(value: string) {
    setDateRange(value)
    setPage(1)
  }

  const filteredLogs = searchQuery
    ? logs.filter(
        (log) =>
          log.action.toLowerCase().includes(searchQuery.toLowerCase()) ||
          log.entity_type.toLowerCase().includes(searchQuery.toLowerCase()) ||
          (log.user?.full_name ?? log.user?.email ?? "").toLowerCase().includes(searchQuery.toLowerCase()),
      )
    : logs

  const total = pagination?.total ?? 0
  const totalPages = pagination?.total_pages ?? 1
  const rangeStart = (page - 1) * PAGE_SIZE + 1
  const rangeEnd = Math.min(page * PAGE_SIZE, total)

  return (
    <div className="space-y-4">
      {/* ── Header ── */}
      <div>
        <h3 className="text-body font-medium text-foreground/80 leading-none">Audit log</h3>
        <p className="text-[11px] text-muted-foreground mt-1 leading-snug">
          Every state-changing action on this workspace, immutably recorded
        </p>
      </div>

      {/* ── Filter bar ── */}
      <div className="flex items-center gap-2 flex-wrap">
        <div
          className="inline-flex items-center gap-0.5 p-0.5 rounded-md bg-white/[0.04] border border-border/60"
          role="group"
          aria-label="Filter by category"
        >
          {categories.map((cat) => (
            <button
              key={cat.value}
              aria-pressed={category === cat.value}
              onClick={() => handleCategoryChange(cat.value)}
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
        <Select value={dateRange} onValueChange={handleDateRangeChange}>
          <SelectTrigger aria-label="Date range" className="w-[120px] h-7 text-xs">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {dateRanges.map((dr) => (
              <SelectItem key={dr.value} value={dr.value} className="text-xs">{dr.label}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="relative flex-1 min-w-[160px] max-w-[260px]">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
          <Input
            aria-label="Filter events on this page"
            placeholder="Filter this page…"
            className="pl-7 h-7 text-xs"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
          />
        </div>
      </div>

      {/* Error with stale data */}
      {error && logs.length > 0 && (
        <div role="alert" className="text-[11px] text-destructive px-3 py-2 rounded-md border border-destructive/30 bg-destructive/5">
          {error}
        </div>
      )}

      {/* Content */}
      {loading ? (
        <div className="rounded-xl border border-border/60 bg-card overflow-hidden">
          {Array.from({ length: 5 }).map((_, i) => (
            <div key={i} className={cn("px-4 py-2.5", i < 4 && "border-b border-border/40")}>
              <Skeleton className="h-3.5 w-full" />
            </div>
          ))}
        </div>
      ) : error ? (
        <div className="rounded-xl border border-destructive/30 bg-destructive/[0.03] p-6 text-center">
          <p role="alert" className="text-xs text-destructive mb-3">{error}</p>
          <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" onClick={fetchLogs}>
            Retry
          </Button>
        </div>
      ) : filteredLogs.length === 0 ? (
        <div className="rounded-xl border border-border/60 bg-card flex flex-col items-center justify-center py-12 text-center">
          <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center mb-3">
            <Shield className="h-4 w-4 text-muted-foreground/60" />
          </div>
          <div className="text-sm font-medium text-foreground/80">
            {searchQuery ? "No matching events" : "No activity yet"}
          </div>
          <div className="text-[11px] text-muted-foreground mt-0.5 max-w-xs">
            {searchQuery ? "Try a different search term" : "All state-changing actions will be logged here."}
          </div>
        </div>
      ) : (
        <>
          <div className="rounded-xl border border-border/60 bg-card overflow-hidden">
            {filteredLogs.map((log, idx) => {
              const isExpanded = expandedId === log.id
              const isLast = idx === filteredLogs.length - 1
              return (
                <div key={log.id}>
                  <button
                    type="button"
                    aria-expanded={isExpanded}
                    aria-controls={`audit-detail-${log.id}`}
                    className={cn(
                      "flex w-full items-center justify-between gap-3 px-4 py-2 cursor-pointer transition-colors text-left",
                      !isLast && !isExpanded && "border-b border-border/40",
                      isExpanded ? "bg-white/[0.03]" : "hover:bg-white/[0.02]",
                    )}
                    onClick={() => setExpandedId(isExpanded ? null : log.id)}
                  >
                    <div className="flex items-center gap-2.5 min-w-0">
                      <ChevronRight
                        className={cn(
                          "h-3 w-3 shrink-0 text-muted-foreground/60 transition-transform duration-150",
                          isExpanded && "rotate-90 text-foreground",
                        )}
                      />
                      <span className="text-[10px] text-muted-foreground font-mono tabular-nums shrink-0">
                        {new Date(log.created_at).toLocaleString()}
                      </span>
                      <span className="text-xs text-foreground/80 truncate">
                        {log.user?.full_name ?? log.user?.email ?? (
                          <span className="text-muted-foreground/60">System</span>
                        )}
                      </span>
                    </div>
                    <div className="flex items-center gap-2 shrink-0">
                      <StatusBadge
                        status={getActionStatusKey(log.action)}
                        label={log.action}
                        className="text-[10px]"
                      />
                      <span className="text-[11px] text-muted-foreground hidden sm:inline">
                        {log.entity_type}
                      </span>
                      {log.entity_id && (
                        <span className="font-mono text-[10px] text-muted-foreground/60 hidden sm:inline">
                          {log.entity_id.slice(0, 8)}
                        </span>
                      )}
                    </div>
                  </button>

                  <AnimatePresence initial={false}>
                    {isExpanded && (
                      <motion.div
                        id={`audit-detail-${log.id}`}
                        role="region"
                        initial={{ height: 0, opacity: 0 }}
                        animate={{ height: "auto", opacity: 1 }}
                        exit={{ height: 0, opacity: 0 }}
                        transition={{ duration: 0.15, ease: "easeInOut" }}
                        className={cn(
                          "overflow-hidden bg-white/[0.02]",
                          !isLast && "border-b border-border/40",
                        )}
                      >
                        <div className="px-4 py-3 pl-11">
                          <div className="grid gap-3 sm:grid-cols-2 max-w-3xl">
                            <div>
                              <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60 mb-0.5">
                                IP address
                              </div>
                              <div className="font-mono text-[11px] text-foreground/80">
                                {log.ip_address ?? "—"}
                              </div>
                            </div>
                            <div>
                              <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60 mb-0.5">
                                User agent
                              </div>
                              <div className="font-mono text-[11px] text-foreground/80 truncate" title={log.user_agent ?? ""}>
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
                            This record is immutable.
                          </div>
                        </div>
                      </motion.div>
                    )}
                  </AnimatePresence>
                </div>
              )
            })}
          </div>

          {/* Pagination */}
          {total > 0 && (
            <div className="flex items-center justify-between gap-2 flex-wrap">
              <span className="text-[11px] text-muted-foreground font-mono tabular-nums">
                Showing {rangeStart}–{rangeEnd} of {total}
              </span>
              <div className="flex items-center gap-1.5">
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  disabled={page <= 1}
                  onClick={() => setPage((p) => Math.max(1, p - 1))}
                >
                  <ChevronLeft className="h-3 w-3 mr-1" />
                  Previous
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  disabled={page >= totalPages}
                  onClick={() => setPage((p) => p + 1)}
                >
                  Next
                  <ChevronRight className="h-3 w-3 ml-1" />
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  )
}
