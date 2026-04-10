"use client"

import { Fragment, useEffect, useState } from "react"
import { ChevronRight, Download, Search, Shield } from "lucide-react"
import { PageShell } from "@/components/layout/page-shell"
import { EmptyState } from "@/components/layout/empty-state"
import { FilterBar } from "@/components/layout/filter-bar"
import { Skeleton } from "@/components/ui/skeleton"
import { Card, CardContent } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
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

const categoryLabels = categories.map((c) => c.label)

const dateRanges = [
  { label: "Last 24 hours", value: "24h" },
  { label: "Last 7 days", value: "7d" },
  { label: "Last 30 days", value: "30d" },
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

/**
 * Maps free-form audit action verbs ("user.created", "credential.rotated", …)
 * onto canonical status keys so every action pill can reuse STATUS_BG_LIGHT
 * and stay on the centralized palette (no local bg-{color}-50 maps).
 */
function actionStatusKey(action: string): string {
  const a = action.toLowerCase()
  if (a.includes("deleted") || a.includes("failed") || a.includes("revoked")) {
    return "FAILED"
  }
  if (a.includes("rotated") || a.includes("blocked")) {
    return "BLOCKED"
  }
  if (a.includes("created") || a.includes("started") || a.includes("completed")) {
    return "COMPLETED"
  }
  if (a.includes("updated") || a.includes("invited") || a.includes("changed")) {
    return "IN_PROGRESS"
  }
  return "PENDING"
}

function getActionClasses(action: string): string {
  return STATUS_BG_LIGHT[actionStatusKey(action)] ?? "bg-muted text-muted-foreground"
}

export default function AuditPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [logs, setLogs] = useState<AuditLog[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [category, setCategory] = useState("all")
  const [dateRange, setDateRange] = useState("7d")
  const [searchQuery, setSearchQuery] = useState("")

  useEffect(() => {
    if (!workspaceId) return

    let cancelled = false

    async function fetchLogs() {
      setLoading(true)
      setError(null)
      try {
        const params = new URLSearchParams({ workspace_id: workspaceId as string })
        if (category !== "all") params.set("entity_type", category)
        const dateFrom = getDateFrom(dateRange)
        if (dateFrom) params.set("date_from", dateFrom)

        const res = await fetch(`/api/v1/audit?${params}`)
        if (!res.ok) {
          setError("Failed to load audit logs")
          return
        }
        const data = (await res.json()) as AuditResponse
        if (!cancelled) setLogs(data.data)
      } catch {
        if (!cancelled) setError("Failed to load audit logs")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchLogs()
    return () => { cancelled = true }
  }, [workspaceId, category, dateRange])

  const isLoading = wsLoading || loading

  const filteredLogs = searchQuery
    ? logs.filter(
        (log) =>
          log.action.toLowerCase().includes(searchQuery.toLowerCase()) ||
          log.entity_type.toLowerCase().includes(searchQuery.toLowerCase()) ||
          (log.user?.full_name ?? log.user?.email ?? "").toLowerCase().includes(searchQuery.toLowerCase())
      )
    : logs

  const activeCategoryLabel =
    categories.find((c) => c.value === category)?.label ?? "All"

  const handleFilter = (label: string) => {
    const match = categories.find((c) => c.label === label)
    if (match) setCategory(match.value)
  }

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

  const actions = (
    <div className="flex items-center gap-2">
      <div className="relative">
        <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
        <Input
          placeholder="Search events..."
          className="pl-8 w-48"
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
        />
      </div>
      <Button
        variant="outline"
        size="sm"
        disabled={filteredLogs.length === 0}
        onClick={handleExport}
      >
        <Download className="mr-1.5 h-3.5 w-3.5" />
        Export CSV
      </Button>
    </div>
  )

  const toolbar = (
    <div className="flex items-center justify-between flex-wrap gap-3">
      <div className="flex items-center gap-3">
        <FilterBar
          filters={categoryLabels}
          active={activeCategoryLabel}
          onFilter={handleFilter}
        />
        <Select value={dateRange} onValueChange={setDateRange}>
          <SelectTrigger className="w-[150px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {dateRanges.map((dr) => (
              <SelectItem key={dr.value} value={dr.value}>
                {dr.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </div>
  )

  return (
    <PageShell
      title="Audit Log"
      description="Track all actions in your workspace"
      actions={actions}
      toolbar={toolbar}
    >
      {error && <p className="text-body text-destructive">{error}</p>}

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-12 rounded-md" />
          ))}
        </div>
      ) : filteredLogs.length === 0 ? (
        <EmptyState
          icon={Shield}
          title="No activity yet"
          description="All state-changing actions will be logged here with who, what, and when."
        />
      ) : (
        <Card>
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-8" />
                  <TableHead>Time</TableHead>
                  <TableHead>User</TableHead>
                  <TableHead>Action</TableHead>
                  <TableHead>Entity</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filteredLogs.map((log) => {
                  const isExpanded = expandedId === log.id
                  return (
                    <Fragment key={log.id}>
                      <TableRow
                        className={cn("cursor-pointer", isExpanded && "bg-primary/5")}
                        onClick={() => setExpandedId(isExpanded ? null : log.id)}
                      >
                        <TableCell className="text-muted-foreground">
                          <ChevronRight
                            className={cn(
                              "h-3.5 w-3.5 transition-transform",
                              isExpanded && "rotate-90"
                            )}
                          />
                        </TableCell>
                        <TableCell className="text-label text-muted-foreground">
                          {new Date(log.created_at).toLocaleString()}
                        </TableCell>
                        <TableCell className="text-body">
                          {log.user?.full_name ?? log.user?.email ?? "System"}
                        </TableCell>
                        <TableCell>
                          <Badge
                            variant="outline"
                            className={cn(
                              "border-transparent text-micro",
                              getActionClasses(log.action),
                            )}
                          >
                            {log.action}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-body text-muted-foreground">
                          {log.entity_type}
                          {log.entity_id && (
                            <span className="ml-1 font-mono text-micro">
                              ({log.entity_id.slice(0, 8)})
                            </span>
                          )}
                        </TableCell>
                      </TableRow>
                      {isExpanded && (
                        <TableRow key={`${log.id}-detail`} className="bg-primary/5">
                          <TableCell />
                          <TableCell colSpan={4} className="pb-4 pt-1">
                            <div className="bg-background rounded-md border border-border p-4 max-w-2xl">
                              <div className="grid grid-cols-2 gap-4 text-label mb-3">
                                <div>
                                  <span className="text-muted-foreground">IP Address</span>
                                  <div className="font-mono text-foreground mt-0.5">
                                    {log.ip_address ?? "—"}
                                  </div>
                                </div>
                                <div>
                                  <span className="text-muted-foreground">User Agent</span>
                                  <div className="font-mono text-foreground mt-0.5 truncate">
                                    {log.user_agent ?? "—"}
                                  </div>
                                </div>
                              </div>
                              {log.metadata && Object.keys(log.metadata).length > 0 && (
                                <>
                                  <div className="text-micro font-medium text-muted-foreground uppercase tracking-wider mb-1.5">
                                    Metadata
                                  </div>
                                  <pre className="bg-muted border border-border rounded p-2.5 text-micro font-mono text-muted-foreground overflow-auto max-h-28">
                                    {JSON.stringify(log.metadata, null, 2)}
                                  </pre>
                                </>
                              )}
                              <div className="flex items-center gap-1.5 mt-3 text-micro text-muted-foreground">
                                <Shield className="h-3 w-3" />
                                This record is immutable — it cannot be edited or deleted.
                              </div>
                            </div>
                          </TableCell>
                        </TableRow>
                      )}
                    </Fragment>
                  )
                })}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
    </PageShell>
  )
}
