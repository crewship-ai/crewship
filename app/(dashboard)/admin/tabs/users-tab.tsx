import React from "react"
import { Card, CardContent } from "@/components/ui/card"
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
  return (
    <div className="space-y-4">
      <div>
        <h3 className="text-sm font-medium">All Users</h3>
        <p className="text-xs text-muted-foreground">{users.length} users across all workspaces</p>
      </div>
      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>User</TableHead>
                <TableHead>Workspace</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Joined</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.map((u) => (
                <TableRow key={u.id}>
                  <TableCell>
                    <div>
                      <div className="text-sm font-medium">{u.full_name ?? "—"}</div>
                      <div className="text-micro text-muted-foreground">{u.email}</div>
                    </div>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {u.workspace?.name ?? "—"}
                  </TableCell>
                  <TableCell>
                    {u.role && <Badge variant="outline" className="text-micro">{u.role}</Badge>}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {new Date(u.created_at).toLocaleDateString()}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  )
})
