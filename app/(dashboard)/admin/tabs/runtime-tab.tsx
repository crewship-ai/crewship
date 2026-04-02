import React from "react"
import {
  RefreshCw, CheckCircle2, AlertTriangle, Container, ExternalLink,
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
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
    <div className="space-y-5">
      <div className="pb-3 border-b">
        <h3 className="text-sm font-medium">Container Runtime</h3>
        <p className="text-xs text-muted-foreground">
          Manage the container runtime used to run AI agents.
        </p>
      </div>

      <Card>
        <CardContent className="p-5 space-y-4">
          {runtimeChecking && (
            <div className="flex items-center gap-3">
              <RefreshCw className="h-4 w-4 animate-spin text-muted-foreground" />
              <span className="text-sm">Detecting runtime...</span>
            </div>
          )}

          {!runtimeChecking && runtimeAvailable && runtimeInfo && (
            <div className="space-y-4">
              {(allRuntimes.length > 0 ? allRuntimes : [runtimeInfo]).map((rt, i) => (
                <div key={rt.runtime + i} className="flex items-center gap-3">
                  <CheckCircle2 className="h-5 w-5 text-emerald-500" />
                  <div>
                    <div className="text-sm font-medium">
                      {rt.runtime === "apple" ? "Apple Containers" : rt.runtime.charAt(0).toUpperCase() + rt.runtime.slice(1)} {rt.version}
                    </div>
                    {rt.socket && <p className="text-xs text-muted-foreground font-mono">{rt.socket}</p>}
                  </div>
                  <Badge variant="outline" className={cn("ml-auto", i === 0 ? "bg-emerald-50 text-emerald-700 border-emerald-200" : "bg-slate-50 text-slate-600 border-slate-200")}>
                    {i === 0 ? "Active" : "Available"}
                  </Badge>
                </div>
              ))}
            </div>
          )}

          {!runtimeChecking && !runtimeAvailable && (
            <div className="space-y-4">
              <div className="flex items-center gap-3">
                <AlertTriangle className="h-5 w-5 text-amber-500" />
                <div>
                  <div className="text-sm font-medium text-amber-700">No runtime detected</div>
                  <p className="text-xs text-muted-foreground">
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
                    className="flex items-center gap-2 rounded-lg border p-3 hover:bg-accent transition-colors text-sm"
                  >
                    <Container className="h-4 w-4 text-muted-foreground" />
                    <span className="font-medium">{key.charAt(0).toUpperCase() + key.slice(1)}</span>
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
        </CardContent>
      </Card>
    </div>
  )
})
