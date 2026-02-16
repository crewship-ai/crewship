"use client"

import { useEffect, useState } from "react"
import { Shield, ChevronRight, Download, Search } from "lucide-react"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
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
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { useOrg } from "@/hooks/use-org"
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
  { label: "Teams", value: "Team" },
  { label: "Users", value: "OrganizationMember" },
  { label: "System", value: "Organization" },
]

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

const actionColors: Record<string, string> = {
  created: "bg-emerald-50 text-emerald-700",
  started: "bg-emerald-50 text-emerald-700",
  completed: "bg-emerald-50 text-emerald-700",
  updated: "bg-blue-50 text-blue-700",
  rotated: "bg-amber-50 text-amber-700",
  invited: "bg-blue-50 text-blue-700",
  deleted: "bg-red-50 text-red-700",
  failed: "bg-red-50 text-red-700",
}

function getActionColor(action: string): string {
  for (const [key, cls] of Object.entries(actionColors)) {
    if (action.includes(key)) return cls
  }
  return "bg-muted text-muted-foreground"
}

export default function AuditPage() {
  const { orgId, loading: orgLoading } = useOrg()
  const [logs, setLogs] = useState<AuditLog[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [category, setCategory] = useState("all")
  const [dateRange, setDateRange] = useState("7d")
  const [searchQuery, setSearchQuery] = useState("")

  useEffect(() => {
    if (!orgId) return

    let cancelled = false

    async function fetchLogs() {
      setLoading(true)
      setError(null)
      try {
        const params = new URLSearchParams({ org_id: orgId as string })
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
  }, [orgId, category, dateRange])

  const isLoading = orgLoading || loading

  const filteredLogs = searchQuery
    ? logs.filter(
        (log) =>
          log.action.toLowerCase().includes(searchQuery.toLowerCase()) ||
          log.entity_type.toLowerCase().includes(searchQuery.toLowerCase()) ||
          (log.user?.full_name ?? log.user?.email ?? "").toLowerCase().includes(searchQuery.toLowerCase())
      )
    : logs

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Audit Log" description="Track all actions in your organization" />

      {error && <p className="text-sm text-destructive">{error}</p>}

      {/* Filters */}
      <div className="flex items-center justify-between flex-wrap gap-3">
        <div className="flex items-center gap-3">
          {/* Category tabs */}
          <div className="flex items-center gap-1">
            {categories.map((cat) => (
              <Button
                key={cat.value}
                variant={category === cat.value ? "default" : "ghost"}
                size="sm"
                className="text-xs h-7 px-2.5"
                onClick={() => setCategory(cat.value)}
              >
                {cat.label}
              </Button>
            ))}
          </div>
          <Select value={dateRange} onValueChange={setDateRange}>
            <SelectTrigger className="w-[150px] h-7 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {dateRanges.map((dr) => (
                <SelectItem key={dr.value} value={dr.value}>{dr.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex items-center gap-2">
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
            <Input
              placeholder="Search events..."
              className="pl-8 h-7 text-xs w-48"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
            />
          </div>
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="outline" size="sm" className="h-7 text-xs" disabled>
                  <Download className="mr-1.5 h-3.5 w-3.5" />
                  Export
                </Button>
              </TooltipTrigger>
              <TooltipContent>Available in Phase 2</TooltipContent>
            </Tooltip>
          </TooltipProvider>
        </div>
      </div>

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
                    <>
                      <TableRow
                        key={log.id}
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
                        <TableCell className="text-xs text-muted-foreground">
                          {new Date(log.created_at).toLocaleString()}
                        </TableCell>
                        <TableCell className="text-sm">
                          {log.user?.full_name ?? log.user?.email ?? "System"}
                        </TableCell>
                        <TableCell>
                          <Badge variant="secondary" className={cn("text-[10px]", getActionColor(log.action))}>
                            {log.action}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground">
                          {log.entity_type}
                          {log.entity_id && (
                            <span className="ml-1 font-mono text-[10px]">
                              ({log.entity_id.slice(0, 8)})
                            </span>
                          )}
                        </TableCell>
                      </TableRow>
                      {isExpanded && (
                        <TableRow key={`${log.id}-detail`} className="bg-primary/5">
                          <TableCell />
                          <TableCell colSpan={4} className="pb-4 pt-1">
                            <div className="bg-background rounded-md border p-4 max-w-2xl">
                              <div className="grid grid-cols-2 gap-4 text-xs mb-3">
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
                                  <div className="text-[10px] font-medium text-muted-foreground uppercase tracking-wider mb-1.5">
                                    Metadata
                                  </div>
                                  <pre className="bg-muted border rounded p-2.5 text-[11px] font-mono text-muted-foreground overflow-auto max-h-28">
                                    {JSON.stringify(log.metadata, null, 2)}
                                  </pre>
                                </>
                              )}
                              <div className="flex items-center gap-1.5 mt-3 text-[10px] text-muted-foreground">
                                <Shield className="h-3 w-3" />
                                This record is immutable — it cannot be edited or deleted.
                              </div>
                            </div>
                          </TableCell>
                        </TableRow>
                      )}
                    </>
                  )
                })}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
