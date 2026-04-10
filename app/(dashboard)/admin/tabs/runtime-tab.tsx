import React from "react"
import {
  RefreshCw, CheckCircle2, AlertTriangle, Container, ExternalLink,
} from "lucide-react"
import { SectionCard } from "@/components/ui/section-card"
import { StatusBadge } from "@/components/ui/status-badge"
import { Button } from "@/components/ui/button"
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
    <SectionCard
      title="Container Runtime"
      description="Manage the container runtime used to run AI agents."
    >
      <div className="space-y-4">
        {runtimeChecking && (
          <div className="flex items-center gap-3">
            <RefreshCw className="h-4 w-4 animate-spin text-muted-foreground" />
            <span className="text-body">Detecting runtime...</span>
          </div>
        )}

        {!runtimeChecking && runtimeAvailable && runtimeInfo && (
          <div className="space-y-4">
            {(allRuntimes.length > 0 ? allRuntimes : [runtimeInfo]).map((rt, i) => (
              <div key={rt.runtime + i} className="flex items-center gap-3">
                <CheckCircle2 className="h-5 w-5 text-muted-foreground" />
                <div>
                  <div className="text-body font-medium">
                    {rt.runtime === "apple"
                      ? "Apple Containers"
                      : rt.runtime.charAt(0).toUpperCase() + rt.runtime.slice(1)}{" "}
                    {rt.version}
                  </div>
                  {rt.socket && (
                    <p className="text-label text-muted-foreground font-mono">{rt.socket}</p>
                  )}
                </div>
                <StatusBadge
                  status={i === 0 ? "COMPLETED" : "PENDING"}
                  label={i === 0 ? "Active" : "Available"}
                  className="ml-auto"
                />
              </div>
            ))}
          </div>
        )}

        {!runtimeChecking && !runtimeAvailable && (
          <div className="space-y-4">
            <div className="flex items-center gap-3">
              <AlertTriangle className="h-5 w-5 text-muted-foreground" />
              <div>
                <div className="text-body font-medium">No runtime detected</div>
                <p className="text-label text-muted-foreground">
                  Install a container runtime to enable agent containers.
                </p>
              </div>
            </div>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
              {Object.entries(runtimeInstallLinks).map(([key, url]) => (
                <a
                  key={key}
                  href={url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="flex items-center gap-2 rounded-lg border border-border p-3 hover:bg-accent transition-colors text-body"
                >
                  <Container className="h-4 w-4 text-muted-foreground" />
                  <span className="font-medium">
                    {key.charAt(0).toUpperCase() + key.slice(1)}
                  </span>
                  <ExternalLink className="h-3 w-3 text-muted-foreground ml-auto" />
                </a>
              ))}
            </div>
          </div>
        )}

        <Button variant="outline" size="sm" onClick={onCheckRuntime} disabled={runtimeChecking}>
          <RefreshCw className={cn("mr-2 h-3.5 w-3.5", runtimeChecking && "animate-spin")} />
          Re-detect Runtime
        </Button>
      </div>
    </SectionCard>
  )
})
