import React from "react"
import { Badge } from "@/components/ui/badge"
import { SettingsCard } from "@/components/features/settings/shared"
import type { AdminUser } from "../types"

interface UsersTabProps {
  users: AdminUser[]
}

export const UsersTab = React.memo(function UsersTab({ users }: UsersTabProps) {
  return (
    <SettingsCard
      title="All users"
      description={
        users.length === 0
          ? "No users"
          : `${users.length} user${users.length === 1 ? "" : "s"} across all workspaces`
      }
    >
      {users.length === 0 ? (
        <div className="flex items-center justify-center py-10 text-[11px] text-muted-foreground">
          No users
        </div>
      ) : (
        <>
          {/* Desktop header */}
          <div
            className="hidden md:grid items-center gap-3 px-4 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground border-b border-border/60"
            style={{ gridTemplateColumns: "minmax(0,1.6fr) minmax(0,1fr) 80px minmax(0,0.9fr)" }}
          >
            <div>User</div>
            <div>Workspace</div>
            <div>Role</div>
            <div>Joined</div>
          </div>
          {/* Rows */}
          {users.map((u, idx) => (
            <div
              key={u.id}
              className={
                "grid items-center gap-3 px-4 py-2 hover:bg-white/[0.02] " +
                (idx < users.length - 1 ? "border-b border-border/40" : "")
              }
              style={{ gridTemplateColumns: "minmax(0,1.6fr) minmax(0,1fr) 80px minmax(0,0.9fr)" }}
            >
              <div className="min-w-0">
                <div className="text-xs font-medium truncate">{u.full_name ?? "—"}</div>
                <div className="text-[10px] text-muted-foreground truncate">{u.email}</div>
              </div>
              <div className="text-[11px] text-muted-foreground truncate">
                {u.workspace?.name ?? "—"}
              </div>
              <div>
                {u.role && (
                  <Badge variant="outline" className="text-[10px] font-medium">
                    {u.role}
                  </Badge>
                )}
              </div>
              <div className="text-[11px] text-muted-foreground font-mono">
                {new Date(u.created_at).toLocaleDateString()}
              </div>
            </div>
          ))}
        </>
      )}
    </SettingsCard>
  )
})
