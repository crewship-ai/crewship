import React from "react"
import { Card } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { AdminUser } from "../types"

interface UsersTabProps {
  users: AdminUser[]
}

export const UsersTab = React.memo(function UsersTab({ users }: UsersTabProps) {
  if (users.length === 0) {
    return (
      <div className="flex items-center justify-center py-12 text-[11px] text-muted-foreground/60">
        No users
      </div>
    )
  }
  return (
    <div className="space-y-3">
      <div className="text-[11px] text-muted-foreground font-mono tabular-nums">
        {users.length} user{users.length === 1 ? "" : "s"} across all workspaces
      </div>
      <Card className="overflow-hidden p-0 rounded-xl border-border/60">
        <Table>
          <TableHeader>
            <TableRow className="border-border/60">
              <TableHead className="text-[10px] uppercase tracking-wider h-8">User</TableHead>
              <TableHead className="text-[10px] uppercase tracking-wider h-8">Workspace</TableHead>
              <TableHead className="text-[10px] uppercase tracking-wider h-8">Role</TableHead>
              <TableHead className="text-[10px] uppercase tracking-wider h-8">Joined</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {users.map((u) => (
              <TableRow key={u.id} className="border-border/40 hover:bg-white/[0.02]">
                <TableCell className="py-2">
                  <div className="min-w-0">
                    <div className="text-xs font-medium truncate">{u.full_name ?? "—"}</div>
                    <div className="text-[10px] text-muted-foreground/60 truncate">{u.email}</div>
                  </div>
                </TableCell>
                <TableCell className="text-[11px] text-muted-foreground py-2">
                  {u.workspace?.name ?? "—"}
                </TableCell>
                <TableCell className="py-2">
                  {u.role && (
                    <Badge variant="outline" className="text-[10px] font-medium">
                      {u.role}
                    </Badge>
                  )}
                </TableCell>
                <TableCell className="text-[11px] text-muted-foreground py-2">
                  {new Date(u.created_at).toLocaleDateString()}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>
    </div>
  )
})
