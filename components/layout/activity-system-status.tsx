"use client"

import * as React from "react"
import { ChevronDown, Package } from "lucide-react"

import { cn } from "@/lib/utils"
import { useRealtime } from "@/hooks/use-realtime"
import { useEngineStatus } from "@/hooks/use-engine-status"
import { useCrewsStatus } from "@/hooks/use-crews-status"
import { useProvisioningStatus } from "@/hooks/use-provisioning-status"
import { useTrustedWorkspaceId } from "@/hooks/use-access-mode"
import {
  ProvisioningPanel,
  provisioningNeedingAttention,
  provisioningSummary,
} from "./app-toolbar-provisioning"
import { SystemStatusPanel, describeSystemStatus, type StatusTone } from "./status-pill"

/**
 * The system's status inside the Activity bell's panel (v67: "Now" starts with
 * a summary line, "6 crews · 11 agents online · 3 working · 8 idle").
 *
 * The top bar used to carry this as an amber chip (container builds that need
 * a rebuild) and a Wi-Fi button (connection and fleet). v67 ends the bar with
 * the two bells, so both live here, one click from where they were:
 *
 *  · a summary line — connection and fleet — that opens the full system
 *    status (engine, real-time, agents, the agents in error);
 *  · the container builds that want a person, with their Build / Retry /
 *    Restart actions, shown whenever there are any.
 */
const DOT: Record<StatusTone, string> = {
  success: "bg-success",
  warn: "bg-warn",
  destructive: "bg-destructive",
  muted: "bg-muted-foreground",
}

export function ActivitySystemStatus({ onNavigate }: { onNavigate: () => void }) {
  const workspaceId = useTrustedWorkspaceId()
  const { status: engineStatus } = useEngineStatus(workspaceId)
  const { status: wsStatus } = useRealtime()
  const crews = useCrewsStatus(workspaceId)
  const provisioning = useProvisioningStatus(workspaceId)
  const [open, setOpen] = React.useState(false)
  const panelId = React.useId()

  const description = describeSystemStatus(engineStatus, wsStatus, crews)
  const { connection, fleetKnown } = description
  const builds = provisioning ? provisioningNeedingAttention(provisioning) : []

  return (
    <div data-slot="activity-system-status" className="border-b border-border">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        onClick={() => setOpen((v) => !v)}
        className="kit-tap flex w-full items-center gap-2 px-3 py-2 text-left text-label text-muted-foreground hover:bg-foreground/[0.04]"
      >
        <span className={cn("size-1.5 shrink-0 rounded-full", DOT[connection.tone])} aria-hidden />
        <span
          data-testid="activity-status-summary"
          className="min-w-0 flex-1 truncate"
          aria-label={`System status: ${description.ariaLabel}`}
        >
          <span className="font-medium text-foreground">{connection.label}</span>
          {fleetKnown && crews && (
            <span className="tabular-nums"> · {crews.total} agents · {crews.running} running · {crews.idle} idle</span>
          )}
        </span>
        <span className="sr-only">System status</span>
        <ChevronDown className={cn("size-3.5 shrink-0 transition-transform", open && "rotate-180")} aria-hidden />
      </button>
      {open && (
        <div id={panelId} className="border-t border-border/60">
          <SystemStatusPanel
            description={description}
            engineStatus={engineStatus}
            wsStatus={wsStatus}
            crews={crews}
            workspaceId={workspaceId}
            onNavigate={onNavigate}
          />
        </div>
      )}
      {provisioning && builds.length > 0 && (
        <div className="border-t border-border/60" data-testid="activity-builds">
          <div className="flex items-center gap-2 px-3 pt-2 text-label text-warn">
            <Package className="size-3.5" aria-hidden />
            {provisioningSummary(provisioning)}
          </div>
          <ProvisioningPanel provisioning={provisioning} workspaceId={workspaceId} onNavigate={onNavigate} />
        </div>
      )}
    </div>
  )
}
