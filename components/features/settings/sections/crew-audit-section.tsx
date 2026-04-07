"use client"

import { useCallback, useEffect, useState } from "react"
import { Shield, ChevronRight, ChevronLeft, Search } from "lucide-react"
import { motion, AnimatePresence } from "motion/react"
import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
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

interface AuditResponse {
  data: AuditLog[]
  pagination: { page: number; limit: number; total: number; total_pages: number }
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

const actionColors: Record<string, string> = {
  created: "bg-emerald-500/20 text-emerald-400 border-emerald-500/30",
  started: "bg-emerald-500/20 text-emerald-400 border-emerald-500/30",
  completed: "bg-emerald-500/20 text-emerald-400 border-emerald-500/30",
  updated: "bg-blue-500/20 text-blue-400 border-blue-500/30",
  rotated: "bg-amber-500/20 text-amber-400 border-amber-500/30",
  invited: "bg-blue-500/20 text-blue-400 border-blue-500/30",
  deleted: "bg-red-500/20 text-red-400 border-red-500/30",
  failed: "bg-red-500/20 text-red-400 border-red-500/30",
}

function getActionColor(action: string): string {
  for (const [key, cls] of Object.entries(actionColors)) {
    if (action.includes(key)) return cls
  }
  return "bg-white/[0.06] text-muted-foreground border-white/[0.08]"
}

const PAGE_SIZE = 50

function Row({ label, description, children, border = true }: {
  label?: string; description?: string; children: React.ReactNode; border?: boolean
}) {
  return (
    <div className={cn("flex items-center justify-between gap-4 px-5 py-3.5 min-h-[48px]", border && "border-b border-white/[0.04] last:border-b-0")}>
      {label ? (
        <div className="shrink-0">
          <div className="text-[13px] text-foreground">{label}</div>
          {description && <div className="text-[11px] text-muted-foreground/30 mt-0.5">{description}</div>}
        </div>
      ) : <div />}
      <div className="flex items-center gap-2 min-w-0 justify-end">{children}</div>
    </div>
  )
}

interface CrewAuditSectionProps {
  workspaceId: string
}

export function CrewAuditSection({ workspaceId }: CrewAuditSectionProps) {
  const [logs, setLogs] = useState<AuditLog[]>([])
  const [loading, setLoading] = useState(true)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [category, setCategory] = useState("all")
  const [dateRange, setDateRange] = useState("7d")
  const [searchQuery, setSearchQuery] = useState("")
  const [page, setPage] = useState(1)
  const [pagination, setPagination] = useState<AuditResponse["pagination"] | null>(null)

  const fetchLogs = useCallback(async () => {
    setLoading(true)
    try {
      const params = new URLSearchParams({ workspace_id: workspaceId, page: String(page), limit: String(PAGE_SIZE) })
      if (category !== "all") params.set("entity_type", category)
      const dateFrom = getDateFrom(dateRange)
      if (dateFrom) params.set("date_from", dateFrom)

      const res = await fetch(`/api/v1/audit?${params}`)
      if (res.ok) {
        const data = (await res.json()) as AuditResponse
        setLogs(data.data)
        setPagination(data.pagination)
      }
    } catch { /* ignore */ }
    finally { setLoading(false) }
  }, [workspaceId, category, dateRange, page])

  useEffect(() => { fetchLogs() }, [fetchLogs])

  useEffect(() => { setPage(1) }, [category, dateRange])

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
      {/* Filter bar */}
      <div className="flex items-center justify-between flex-wrap gap-3">
        <div className="flex items-center gap-0.5">
          {categories.map((cat) => (
            <button
              key={cat.value}
              onClick={() => setCategory(cat.value)}
              className={cn(
                "h-7 px-2.5 rounded-[4px] text-[11px] font-medium transition-colors",
                category === cat.value
                  ? "bg-white/[0.08] text-foreground"
                  : "text-muted-foreground/50 hover:text-foreground/80 hover:bg-white/[0.03]",
              )}
            >
              {cat.label}
            </button>
          ))}
        </div>
        <div className="flex items-center gap-2">
          <Select value={dateRange} onValueChange={setDateRange}>
            <SelectTrigger className="w-[120px] h-7 text-[11px] bg-white/[0.03] border-white/[0.08]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {dateRanges.map((dr) => (
                <SelectItem key={dr.value} value={dr.value}>{dr.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground/40" />
            <Input
              placeholder="Search events..."
              className="pl-8 h-7 text-[11px] w-44 bg-white/[0.03] border-white/[0.08]"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
            />
          </div>
        </div>
      </div>

      {/* Content */}
      {loading ? (
        <Card className="border-white/[0.06]">
          <CardContent className="p-0">
            {Array.from({ length: 5 }).map((_, i) => (
              <Row key={i} border={i < 4}>
                <Skeleton className="h-4 w-full" />
              </Row>
            ))}
          </CardContent>
        </Card>
      ) : filteredLogs.length === 0 ? (
        <Card className="border-white/[0.06]">
          <CardContent className="p-8">
            <EmptyState
              icon={Shield}
              title="No activity yet"
              description="All state-changing actions will be logged here."
            />
          </CardContent>
        </Card>
      ) : (
        <>
          <Card className="border-white/[0.06] overflow-hidden">
            <CardContent className="p-0">
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
                        "flex w-full items-center justify-between gap-4 px-5 py-3.5 min-h-[48px] cursor-pointer transition-colors text-left",
                        !isLast && !isExpanded && "border-b border-white/[0.04]",
                        isExpanded ? "bg-white/[0.03]" : "hover:bg-white/[0.02]",
                      )}
                      onClick={() => setExpandedId(isExpanded ? null : log.id)}
                    >
                      <div className="flex items-center gap-3 min-w-0">
                        <ChevronRight
                          className={cn(
                            "h-3 w-3 shrink-0 text-muted-foreground/40 transition-transform duration-150",
                            isExpanded && "rotate-90",
                          )}
                        />
                        <span className="text-[11px] text-muted-foreground/50 font-mono tabular-nums shrink-0">
                          {new Date(log.created_at).toLocaleString()}
                        </span>
                        <span className="text-[12px] text-foreground truncate">
                          {log.user?.full_name ?? log.user?.email ?? "System"}
                        </span>
                      </div>
                      <div className="flex items-center gap-2.5 shrink-0">
                        <Badge
                          variant="outline"
                          className={cn("text-[9px] font-medium", getActionColor(log.action))}
                        >
                          {log.action}
                        </Badge>
                        <span className="text-[12px] text-muted-foreground/60">
                          {log.entity_type}
                        </span>
                        {log.entity_id && (
                          <span className="font-mono text-[10px] text-muted-foreground/30">
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
                            !isLast && "border-b border-white/[0.04]",
                          )}
                        >
                          <div className="px-5 py-4 pl-11">
                            <div className="bg-white/[0.02] border border-white/[0.06] rounded-md p-4 max-w-2xl">
                              <div className="grid grid-cols-2 gap-4 text-[11px] mb-3">
                                <div>
                                  <span className="text-muted-foreground/50 uppercase tracking-wider text-[10px]">
                                    IP Address
                                  </span>
                                  <div className="font-mono text-foreground mt-0.5">
                                    {log.ip_address ?? "\u2014"}
                                  </div>
                                </div>
                                <div>
                                  <span className="text-muted-foreground/50 uppercase tracking-wider text-[10px]">
                                    User Agent
                                  </span>
                                  <div className="font-mono text-foreground mt-0.5 truncate">
                                    {log.user_agent ?? "\u2014"}
                                  </div>
                                </div>
                              </div>
                              {log.metadata && Object.keys(log.metadata).length > 0 && (
                                <>
                                  <div className="text-[10px] font-semibold text-muted-foreground/50 uppercase tracking-wider mb-1.5">
                                    Metadata
                                  </div>
                                  <pre className="bg-white/[0.02] border border-white/[0.06] rounded p-2.5 text-[11px] font-mono text-muted-foreground overflow-auto max-h-28">
                                    {JSON.stringify(log.metadata, null, 2)}
                                  </pre>
                                </>
                              )}
                              <div className="flex items-center gap-1.5 mt-3 text-[10px] text-muted-foreground/40">
                                <Shield className="h-3 w-3" />
                                This record is immutable.
                              </div>
                            </div>
                          </div>
                        </motion.div>
                      )}
                    </AnimatePresence>
                  </div>
                )
              })}
            </CardContent>
          </Card>

          {/* Pagination */}
          {total > 0 && (
            <div className="flex items-center justify-between">
              <span className="text-[11px] text-muted-foreground/40">
                Showing {rangeStart}-{rangeEnd} of {total}
              </span>
              <div className="flex items-center gap-1.5">
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 text-[11px] px-2.5 border-white/[0.08] bg-white/[0.03]"
                  disabled={page <= 1}
                  onClick={() => setPage((p) => Math.max(1, p - 1))}
                >
                  <ChevronLeft className="h-3 w-3 mr-1" />
                  Previous
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 text-[11px] px-2.5 border-white/[0.08] bg-white/[0.03]"
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
