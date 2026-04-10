import React from "react"
import { Card } from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { AdminOrg } from "../types"

interface WorkspacesTabProps {
  orgs: AdminOrg[]
}

export const WorkspacesTab = React.memo(function WorkspacesTab({ orgs }: WorkspacesTabProps) {
  return (
    <div className="space-y-4">
      <div>
        <h3 className="text-body font-medium">All Workspaces</h3>
        <p className="text-label text-muted-foreground">
          {orgs.length} workspaces on this instance
        </p>
      </div>
      <Card className="overflow-hidden p-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Workspace</TableHead>
              <TableHead className="text-center">Members</TableHead>
              <TableHead className="text-center">Agents</TableHead>
              <TableHead className="text-center">Teams</TableHead>
              <TableHead>Created</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {orgs.map((o) => (
              <TableRow key={o.id}>
                <TableCell>
                  <div className="flex items-center gap-3">
                    <div className="h-8 w-8 rounded-lg bg-primary flex items-center justify-center text-primary-foreground text-label font-bold">
                      {o.name[0]?.toUpperCase()}
                    </div>
                    <div>
                      <div className="text-body font-medium">{o.name}</div>
                      <div className="text-micro text-muted-foreground font-mono">{o.slug}</div>
                    </div>
                  </div>
                </TableCell>
                <TableCell className="text-center text-label">
                  {o._count_members ?? 0}
                </TableCell>
                <TableCell className="text-center text-label">
                  {o._count_agents ?? 0}
                </TableCell>
                <TableCell className="text-center text-label">
                  {o._count_crews ?? 0}
                </TableCell>
                <TableCell className="text-label text-muted-foreground">
                  {new Date(o.created_at).toLocaleDateString()}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>
    </div>
  )
})
