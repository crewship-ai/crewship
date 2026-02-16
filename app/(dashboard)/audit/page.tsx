"use client"

import { useEffect, useState } from "react"
import { Shield } from "lucide-react"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
import { Card, CardContent } from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { useOrg } from "@/hooks/use-org"

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

export default function AuditPage() {
  const { orgId, loading: orgLoading } = useOrg()
  const [logs, setLogs] = useState<AuditLog[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!orgId) return

    let cancelled = false

    async function fetchLogs() {
      setLoading(true)
      setError(null)
      try {
        const res = await fetch(`/api/v1/audit?org_id=${orgId}`)
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
    return () => {
      cancelled = true
    }
  }, [orgId])

  const isLoading = orgLoading || loading

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Audit Log" description="Track all actions in your organization" />

      {error && <p className="text-sm text-destructive">{error}</p>}

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="h-12 rounded-md" />
          ))}
        </div>
      ) : logs.length === 0 ? (
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
                  <TableHead>Time</TableHead>
                  <TableHead>User</TableHead>
                  <TableHead>Action</TableHead>
                  <TableHead>Entity</TableHead>
                  <TableHead className="hidden sm:table-cell">Details</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {logs.map((log) => (
                  <TableRow key={log.id}>
                    <TableCell className="text-xs text-muted-foreground">
                      {new Date(log.created_at).toLocaleString()}
                    </TableCell>
                    <TableCell className="text-sm">
                      {log.user?.full_name ?? log.user?.email ?? "System"}
                    </TableCell>
                    <TableCell className="text-sm font-medium">
                      {log.action}
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {log.entity_type}
                      {log.entity_id && (
                        <span className="ml-1 font-mono text-[10px]">
                          ({log.entity_id.slice(0, 8)})
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="hidden sm:table-cell text-xs text-muted-foreground max-w-[200px] truncate">
                      {log.metadata ? JSON.stringify(log.metadata) : "—"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
