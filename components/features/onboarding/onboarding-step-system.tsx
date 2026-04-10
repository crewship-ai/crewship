"use client"

import { useEffect } from "react"
import {
  Loader2,
  Container,
  RefreshCw,
  ExternalLink,
  AlertTriangle,
  CheckCircle2,
} from "lucide-react"
import { Button } from "@/components/ui/button"

const RUNTIME_LABELS: Record<string, string> = {
  docker: "Docker",
  podman: "Podman",
  colima: "Colima",
  orbstack: "OrbStack",
  rancher: "Rancher Desktop",
  apple: "Apple Containers",
  nerdctl: "nerdctl",
}

export interface StepSystemCheckProps {
  available: boolean | null
  info: { runtime: string; version: string } | null
  checking: boolean
  installLinks: Record<string, string>
  onCheck: () => void
}

export function StepSystemCheck({
  available,
  info,
  checking,
  installLinks,
  onCheck,
}: StepSystemCheckProps) {
  useEffect(() => {
    if (available === null) onCheck()
  }, [available, onCheck])

  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h2 className="text-lg font-semibold">System Check</h2>
        <p className="text-sm text-muted-foreground">
          Crewship runs AI agents in isolated containers. A container runtime is required.
        </p>
      </div>

      <div className="rounded-lg border p-4">
        {checking && (
          <div className="flex items-center gap-3">
            <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
            <span className="text-sm">Detecting container runtime...</span>
          </div>
        )}

        {!checking && available === true && info && (
          <div className="flex items-center gap-3">
            <CheckCircle2 className="h-5 w-5 text-emerald-500" />
            <div>
              <div className="text-sm font-medium text-emerald-600 dark:text-emerald-400">
                {RUNTIME_LABELS[info.runtime] ?? info.runtime} {info.version} detected
              </div>
              <p className="text-xs text-muted-foreground">
                Container runtime is ready. Agents will run in isolated containers.
              </p>
            </div>
          </div>
        )}

        {!checking && available === false && (
          <div className="space-y-4">
            <div className="flex items-center gap-3">
              <AlertTriangle className="h-5 w-5 text-amber-500" />
              <div>
                <div className="text-sm font-medium text-amber-600 dark:text-amber-400">
                  No container runtime found
                </div>
                <p className="text-xs text-muted-foreground">
                  Install one of the supported runtimes to run AI agents.
                </p>
              </div>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
              {Object.entries(installLinks).map(([key, url]) => (
                <a
                  key={key}
                  href={url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="flex items-center gap-2 rounded-lg border p-3 hover:bg-accent transition-colors"
                >
                  <Container className="h-4 w-4 text-muted-foreground" />
                  <span className="text-sm font-medium">{RUNTIME_LABELS[key] ?? key}</span>
                  <ExternalLink className="h-3 w-3 text-muted-foreground ml-auto" />
                </a>
              ))}
            </div>
          </div>
        )}
      </div>

      {!checking && (
        <Button variant="outline" size="sm" onClick={onCheck}>
          <RefreshCw className="mr-2 h-3.5 w-3.5" />
          Re-check
        </Button>
      )}

      {!checking && available === false && (
        <p className="text-xs text-muted-foreground">
          You can continue without a runtime, but agents will not be able to run until one is installed.
        </p>
      )}
    </div>
  )
}
