import React from "react"
import {
  RefreshCw, CheckCircle2, AlertTriangle, Container, ExternalLink,
} from "lucide-react"
import { StatusBadge } from "@/components/ui/status-badge"
import { Button } from "@/components/ui/button"
import { SettingsCard } from "@/components/features/settings/shared"
import { cn } from "@/lib/utils"

interface RuntimeTabProps {
  runtimeChecking: boolean
  runtimeAvailable: boolean | null
  runtimeInfo: { runtime: string; version: string; socket: string } | null
  allRuntimes: { runtime: string; version: string; socket: string }[]
  runtimeInstallLinks: Record<string, string>
  onCheckRuntime: () => void
}

export const RuntimeTab = React.memo(function RuntimeTab({
  runtimeChecking,
  runtimeAvailable,
  runtimeInfo,
  allRuntimes,
  runtimeInstallLinks,
  onCheckRuntime,
}: RuntimeTabProps) {
  return (
    <SettingsCard
      title="Container runtime"
      description="Detected container runtime(s) for running agent containers"
      actions={
        <Button
          variant="outline"
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={onCheckRuntime}
          disabled={runtimeChecking}
        >
          <RefreshCw className={cn("mr-1.5 h-3 w-3", runtimeChecking && "animate-spin")} />
          Re-detect
        </Button>
      }
      padded
    >
      <div className="space-y-3">
        {runtimeChecking && (
          <div className="flex items-center gap-2">
            <RefreshCw className="h-3 w-3 animate-spin text-muted-foreground" />
            <span className="text-xs text-muted-foreground">Detecting runtime…</span>
          </div>
        )}

        {!runtimeChecking && runtimeAvailable && runtimeInfo && (
          <div className="flex flex-col gap-2">
            {(allRuntimes.length > 0 ? allRuntimes : [runtimeInfo]).map((rt, i) => (
              <div
                key={rt.runtime + i}
                className="flex items-center gap-3 px-3 py-2 rounded-lg border border-border/60 bg-white/[0.02]"
              >
                <CheckCircle2 className="h-3.5 w-3.5 text-emerald-400 shrink-0" />
                <div className="min-w-0 flex-1">
                  <div className="text-xs font-medium">
                    {rt.runtime === "apple"
                      ? "Apple Containers"
                      : rt.runtime.charAt(0).toUpperCase() + rt.runtime.slice(1)}{" "}
                    <span className="font-mono text-muted-foreground">{rt.version}</span>
                  </div>
                  {rt.socket && (
                    <p className="text-[10px] text-muted-foreground/70 font-mono truncate mt-0.5">
                      {rt.socket}
                    </p>
                  )}
                </div>
                <StatusBadge
                  status={i === 0 ? "COMPLETED" : "PENDING"}
                  label={i === 0 ? "Active" : "Available"}
                  className="text-[10px]"
                />
              </div>
            ))}
          </div>
        )}

        {!runtimeChecking && !runtimeAvailable && (
          <div className="space-y-3">
            <div className="flex items-center gap-3">
              <AlertTriangle className="h-4 w-4 text-amber-400 shrink-0" />
              <div className="min-w-0">
                <div className="text-xs font-medium">No runtime detected</div>
                <p className="text-[11px] text-muted-foreground">
                  Install a container runtime to enable agent containers.
                </p>
              </div>
            </div>
            {Object.keys(runtimeInstallLinks).length > 0 && (
              <div className="grid gap-2 grid-cols-1 sm:grid-cols-2">
                {Object.entries(runtimeInstallLinks).map(([key, url]) => (
                  <a
                    key={key}
                    href={url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="flex items-center gap-2 rounded-lg border border-border/60 bg-white/[0.02] px-3 py-2 hover:bg-white/[0.04] hover:border-border transition-colors text-xs"
                  >
                    <Container className="h-3 w-3 text-muted-foreground" />
                    <span className="font-medium">
                      {key.charAt(0).toUpperCase() + key.slice(1)}
                    </span>
                    <ExternalLink className="h-2.5 w-2.5 text-muted-foreground/60 ml-auto" />
                  </a>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </SettingsCard>
  )
})
