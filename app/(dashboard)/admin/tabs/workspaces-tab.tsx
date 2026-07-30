import React from "react"
import { Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import { SettingsCard } from "@/components/features/settings/shared"
import { CreateWorkspaceDialog } from "@/components/layout/workspace-switcher"
import type { AdminOrg } from "../types"

interface WorkspacesTabProps {
  orgs: AdminOrg[]
  /** Re-read the admin data after a create, so the list the operator is
   *  looking at catches up on its own. A created workspace that does not
   *  appear reads as a failed create. */
  onRefresh: () => void
}

export const WorkspacesTab = React.memo(function WorkspacesTab({ orgs, onRefresh }: WorkspacesTabProps) {
  const [createOpen, setCreateOpen] = React.useState(false)

  return (
    <>
    <SettingsCard
      title="All workspaces"
      description={
        orgs.length === 0
          ? "No workspaces"
          : `${orgs.length} workspace${orgs.length === 1 ? "" : "s"} on this instance`
      }
      actions={
        // The SAME dialog the workspace switcher opens, not a second
        // implementation of it: two create forms for one object drift, and
        // the slug rule is the kind of thing that drifts silently.
        <Button
          variant="outline"
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={() => setCreateOpen(true)}
        >
          <Plus className="mr-1.5 h-3 w-3" />
          Create workspace
        </Button>
      }
    >
      {orgs.length === 0 ? (
        <div className="flex items-center justify-center py-10 text-[11px] text-muted-foreground">
          No workspaces
        </div>
      ) : (
        <>
          {/* Desktop header */}
          <div
            className="hidden md:grid items-center gap-3 px-4 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground border-b border-border/60"
            style={{ gridTemplateColumns: "minmax(0,1.8fr) 90px 90px 90px minmax(0,1fr)" }}
          >
            <div>Workspace</div>
            <div className="text-center">Members</div>
            <div className="text-center">Agents</div>
            <div className="text-center">Crews</div>
            <div>Created</div>
          </div>
          {/* Rows */}
          {orgs.map((o, idx) => (
            <div
              key={o.id}
              className={
                "grid items-center gap-3 px-4 py-2 hover:bg-white/[0.02] " +
                (idx < orgs.length - 1 ? "border-b border-border/40" : "")
              }
              style={{ gridTemplateColumns: "minmax(0,1.8fr) 90px 90px 90px minmax(0,1fr)" }}
            >
              <div className="flex items-center gap-2.5 min-w-0">
                <div className="h-6 w-6 rounded-md bg-primary flex items-center justify-center text-primary-foreground text-[11px] font-semibold shrink-0">
                  {o.name[0]?.toUpperCase()}
                </div>
                <div className="min-w-0">
                  <div className="text-xs font-medium truncate">{o.name}</div>
                  <div className="text-[10px] text-muted-foreground font-mono truncate">{o.slug}</div>
                </div>
              </div>
              <div className="text-center text-xs tabular-nums">{o._count_members ?? 0}</div>
              <div className="text-center text-xs tabular-nums">{o._count_agents ?? 0}</div>
              <div className="text-center text-xs tabular-nums">{o._count_crews ?? 0}</div>
              <div className="text-[11px] text-muted-foreground font-mono">
                {new Date(o.created_at).toLocaleDateString()}
              </div>
            </div>
          ))}
        </>
      )}
    </SettingsCard>

    <CreateWorkspaceDialog
      open={createOpen}
      onOpenChange={setCreateOpen}
      // The switcher's version switches you INTO the new workspace; from the
      // admin console you are auditing the instance, not moving house, so the
      // list refreshes and the operator stays where they were.
      onCreated={() => onRefresh()}
    />
    </>
  )
})
