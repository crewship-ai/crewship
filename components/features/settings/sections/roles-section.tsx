"use client"

import { Check, X } from "lucide-react"
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table"
import { cn } from "@/lib/utils"

const permMatrix = [
  { role: "Owner", perms: [true, true, true, "All", true, true] },
  { role: "Admin", perms: [true, true, true, "All", true, true] },
  { role: "Manager", perms: [false, true, "Crew", "Crew", false, false] },
  { role: "Member", perms: [false, false, false, "Own", false, false] },
  { role: "Viewer", perms: [false, false, false, false, false, false] },
]

const permHeaders = ["See all crews", "Create agents", "Manage creds", "Audit access", "Manage members", "Billing"]

const roleColors: Record<string, string> = {
  Owner: "text-amber-400",
  Admin: "text-blue-400",
  Manager: "text-teal-400",
}

export function RolesSection() {
  return (
    <div className="space-y-4">
      <div className="bg-card border border-white/[0.06] rounded-lg">
        <div className="px-6 py-4 border-b border-white/[0.06]">
          <h4 className="text-[14px] font-medium text-foreground">Permission Matrix</h4>
          <p className="text-[12px] text-muted-foreground/50 mt-1">
            Reference of what each role can do. Roles are assigned per member.
          </p>
        </div>
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow className="border-white/[0.06] hover:bg-transparent">
                <TableHead className="w-24 text-[11px] text-muted-foreground/50 uppercase tracking-wider font-semibold">
                  Role
                </TableHead>
                {permHeaders.map((h) => (
                  <TableHead
                    key={h}
                    className="text-center text-[10px] text-muted-foreground/50 uppercase tracking-wider font-semibold"
                  >
                    {h}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {permMatrix.map((row) => (
                <TableRow key={row.role} className="border-white/[0.04] hover:bg-white/[0.02]">
                  <TableCell
                    className={cn(
                      "text-[12px] font-medium",
                      roleColors[row.role] ?? "text-muted-foreground",
                    )}
                  >
                    {row.role}
                  </TableCell>
                  {row.perms.map((v, i) => (
                    <TableCell key={i} className="text-center">
                      {v === true ? (
                        <Check className="h-3.5 w-3.5 text-emerald-400 mx-auto" />
                      ) : v === false ? (
                        <X className="h-3.5 w-3.5 text-muted-foreground/20 mx-auto" />
                      ) : (
                        <span className="text-[10px] text-muted-foreground/60 font-mono">{v}</span>
                      )}
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </div>
    </div>
  )
}
