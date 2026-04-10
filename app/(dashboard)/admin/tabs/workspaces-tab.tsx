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
  if (orgs.length === 0) {
    return (
      <div className="flex items-center justify-center py-12 text-[11px] text-muted-foreground/60">
        No workspaces
      </div>
    )
  }
  return (
    <div className="space-y-3">
      <div className="text-[11px] text-muted-foreground font-mono tabular-nums">
        {orgs.length} workspace{orgs.length === 1 ? "" : "s"} on this instance
      </div>
      <Card className="overflow-hidden p-0 rounded-xl border-border/60">
        <Table>
          <TableHeader>
            <TableRow className="border-border/60">
              <TableHead className="text-[10px] uppercase tracking-wider h-8">Workspace</TableHead>
              <TableHead className="text-[10px] uppercase tracking-wider h-8 text-center">Members</TableHead>
              <TableHead className="text-[10px] uppercase tracking-wider h-8 text-center">Agents</TableHead>
              <TableHead className="text-[10px] uppercase tracking-wider h-8 text-center">Crews</TableHead>
              <TableHead className="text-[10px] uppercase tracking-wider h-8">Created</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {orgs.map((o) => (
              <TableRow key={o.id} className="border-border/40 hover:bg-white/[0.02]">
                <TableCell className="py-2">
                  <div className="flex items-center gap-2.5">
                    <div className="h-6 w-6 rounded-md bg-primary flex items-center justify-center text-primary-foreground text-[11px] font-semibold">
                      {o.name[0]?.toUpperCase()}
                    </div>
                    <div className="min-w-0">
                      <div className="text-xs font-medium truncate">{o.name}</div>
                      <div className="text-[10px] text-muted-foreground/60 font-mono truncate">{o.slug}</div>
                    </div>
                  </div>
                </TableCell>
                <TableCell className="text-center text-xs tabular-nums py-2">
                  {o._count_members ?? 0}
                </TableCell>
                <TableCell className="text-center text-xs tabular-nums py-2">
                  {o._count_agents ?? 0}
                </TableCell>
                <TableCell className="text-center text-xs tabular-nums py-2">
                  {o._count_crews ?? 0}
                </TableCell>
                <TableCell className="text-[11px] text-muted-foreground py-2">
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
