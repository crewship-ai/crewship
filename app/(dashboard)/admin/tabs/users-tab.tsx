"use client"

import React from "react"
import { ChevronRight, Search } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { SettingsCard } from "@/components/features/settings/shared"
import { InviteMemberDialog } from "@/components/features/members/invite-member-dialog"
import { UserAvatar } from "@/components/ui/user-avatar"
import { UserDataActions } from "@/components/features/admin/user-data-actions"
import { cn } from "@/lib/utils"
import type { AdminUser } from "../types"

interface UsersTabProps {
  users: AdminUser[]
  /** The workspace a new member would be added to, and the scope every
   *  per-person action runs in. Null while it is still resolving — there is
   *  nothing to invite INTO or act ON yet, and a control that can only fail
   *  is worse than no control. */
  workspaceId: string | null
  onRefresh: () => void
}

const GRID = "minmax(0,1.6fr) minmax(0,1fr) 80px minmax(0,0.9fr)"

export const UsersTab = React.memo(function UsersTab({ users, workspaceId, onRefresh }: UsersTabProps) {
  const [query, setQuery] = React.useState("")
  // One row open at a time: two open panels invite the wrong one being acted
  // on, and one of these actions is irreversible.
  const [openId, setOpenId] = React.useState<string | null>(null)

  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return users
    return users.filter(
      (u) =>
        u.email.toLowerCase().includes(q) ||
        u.id.toLowerCase().includes(q) ||
        (u.full_name?.toLowerCase().includes(q) ?? false),
    )
  }, [query, users])

  return (
    <SettingsCard
      title="All users"
      description={
        users.length === 0
          ? "No users"
          : `${users.length} user${users.length === 1 ? "" : "s"} across all workspaces`
      }
      actions={
        // The same dialog Settings → Members opens, provisioning included:
        // a second "add a person" form would have to re-derive the invite
        // link and the role rules, and would drift from the first one.
        workspaceId ? (
          <InviteMemberDialog workspaceId={workspaceId} onInvited={onRefresh} />
        ) : undefined
      }
    >
      {/* Search came over with the data actions. Their old home had a picker
          of its own — a second roster of the same people — and this list had
          no way to find anyone at all. */}
      {users.length > 0 && (
        <div className="flex items-center gap-2 border-b border-border/40 px-4 py-2.5">
          <Search className="h-3.5 w-3.5 shrink-0 text-muted-foreground/60" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search by name, email or user id"
            aria-label="Search users"
            className="h-7 border-0 bg-transparent px-0 text-xs shadow-none focus-visible:ring-0"
          />
          {query && (
            <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
              {filtered.length}/{users.length}
            </span>
          )}
        </div>
      )}

      {users.length === 0 ? (
        <div className="flex items-center justify-center py-10 text-[11px] text-muted-foreground">
          No users
        </div>
      ) : filtered.length === 0 ? (
        <div className="flex items-center justify-center py-10 text-[11px] text-muted-foreground">
          No matching users
        </div>
      ) : (
        <>
          {/* Desktop header */}
          <div
            className="hidden items-center gap-3 border-b border-border/60 px-4 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground md:grid"
            style={{ gridTemplateColumns: GRID }}
          >
            <div>User</div>
            <div>Workspace</div>
            <div>Role</div>
            <div>Joined</div>
          </div>

          {filtered.map((u, idx) => {
            const isOpen = openId === u.id
            return (
              <div key={u.id}>
                <button
                  type="button"
                  onClick={() => setOpenId(isOpen ? null : u.id)}
                  aria-expanded={isOpen}
                  aria-label={u.full_name ?? u.email}
                  className={cn(
                    "grid w-full items-center gap-3 px-4 py-2 text-left hover:bg-white/[0.02]",
                    idx < filtered.length - 1 && !isOpen && "border-b border-border/40",
                    isOpen && "bg-accent/40",
                  )}
                  style={{ gridTemplateColumns: GRID }}
                >
                  {/* The same face the top bar draws. UserAvatar exists because
                      three surfaces used to render a person three ways and only
                      one knew about avatar_url; this roster was a fourth. */}
                  <div className="flex min-w-0 items-center gap-2.5">
                    <ChevronRight
                      className={cn(
                        "h-3 w-3 shrink-0 text-muted-foreground transition-transform duration-150",
                        isOpen && "rotate-90 text-foreground",
                      )}
                    />
                    <UserAvatar
                      name={u.full_name}
                      email={u.email}
                      src={u.avatar_url}
                      className="h-7 w-7 shrink-0"
                      textClassName="text-[10px]"
                    />
                    <div className="min-w-0">
                      <div className="truncate text-xs font-medium">{u.full_name ?? "—"}</div>
                      <div className="truncate text-[10px] text-muted-foreground">{u.email}</div>
                    </div>
                  </div>
                  <div className="truncate text-[11px] text-muted-foreground">
                    {u.workspace?.name ?? "—"}
                  </div>
                  <div>
                    {u.role && (
                      <Badge variant="outline" className="text-[10px] font-medium">
                        {u.role}
                      </Badge>
                    )}
                  </div>
                  <div className="font-mono text-[11px] text-muted-foreground">
                    {new Date(u.created_at).toLocaleDateString()}
                  </div>
                </button>

                {isOpen && (
                  <section
                    aria-label={`Data actions for ${u.email}`}
                    className={cn(
                      "bg-muted/20 px-4 py-3 pl-11",
                      idx < filtered.length - 1 && "border-b border-border/40",
                    )}
                  >
                    {workspaceId ? (
                      <UserDataActions
                        userId={u.id}
                        email={u.email}
                        workspaceId={workspaceId}
                        onErased={() => {
                          setOpenId(null)
                          onRefresh()
                        }}
                      />
                    ) : (
                      <p className="text-[11px] text-muted-foreground">
                        Resolving the workspace…
                      </p>
                    )}
                  </section>
                )}
              </div>
            )
          })}
        </>
      )}
    </SettingsCard>
  )
})
