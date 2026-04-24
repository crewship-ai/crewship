"use client"

import { EngineStatusBanner } from "@/components/features/agents/logs/engine-status-banner"
import { LogsViewer } from "@/components/features/agents/logs/logs-viewer"

/**
 * Logs tab. Top banner absorbs the former standalone Debug page
 * (engine health, runtime, providers, last-run stats) as a collapsible summary;
 * main body is the live log stream.
 */
export function LogsPageClient() {
  return (
    <div className="flex flex-col h-full min-h-0">
      <EngineStatusBanner />
      <div className="flex-1 min-h-0 overflow-hidden">
        <LogsViewer />
      </div>
    </div>
  )
}
