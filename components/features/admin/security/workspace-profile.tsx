"use client"

import * as React from "react"
import { Activity as ActivityIcon, ChevronLeft } from "lucide-react"

import { Button } from "@/components/ui/button"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { KeeperGovernancePanel } from "@/components/features/admin/keeper-governance-panel"
import type { InstanceGovRow } from "./use-instance-keeper"

/**
 * One workspace's security on one page: its watchdog, who hears about a
 * finding and when a second person is needed, how long access lasts, and its
 * judge. The editors are the existing cards, fed this row through the instance
 * route, so an instance admin reaches a workspace they are not a member of.
 */
export function WorkspaceSecurityProfile({ row, requests, serverEnabled, onBack, onActivity, onSaved }: {
  row: InstanceGovRow
  requests: number
  serverEnabled: boolean
  onBack: () => void
  onActivity: () => void
  onSaved: () => void
}) {
  // The editors read the row once and reload only for a new row (after a
  // save): a callback that changes every render must not reset their drafts.
  const saved = React.useRef(onSaved)
  saved.current = onSaved
  const instance = React.useMemo(() => ({ row, onSaved: () => saved.current() }), [row])
  return (
    <>
      <Button type="button" variant="ghost" size="sm" className="-ml-2 self-start" onClick={onBack}><ChevronLeft className="size-3.5" />Workspaces</Button>
      <section data-slot="workspace-security-header" className="flex flex-wrap items-center gap-3.5">
        <WorkspaceTile id={row.workspace_id} name={row.workspace_name} size="lg" logoUrl={row.workspace_logo_url} />
        <div className="min-w-0 flex-[1_1_14rem]">
          <h2 className="text-title font-semibold leading-tight tracking-tight">{row.workspace_name}</h2>
          <p className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-body text-muted-foreground">
            <span className="font-mono text-micro">{row.workspace_slug}</span>
            <span>{requests} {requests === 1 ? "request" : "requests"}</span>
            <span>{row.enabled ? "Watchdog on" : "Watchdog off"}</span>
            {!row.configured && <span>Built-in settings, never changed</span>}
          </p>
        </div>
        <Button type="button" variant="outline" size="sm" onClick={onActivity}><ActivityIcon className="mr-1 size-3.5" />Its activity</Button>
      </section>
      <KeeperGovernancePanel key={`${row.workspace_id}-policy`} workspaceId={row.workspace_id} serverEnabled={serverEnabled} section="policy" instance={instance} />
      <KeeperGovernancePanel key={`${row.workspace_id}-judge`} workspaceId={row.workspace_id} serverEnabled={serverEnabled} section="judge" instance={instance} />
    </>
  )
}
