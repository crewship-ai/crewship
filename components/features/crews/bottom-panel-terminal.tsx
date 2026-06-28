"use client"

/**
 * Terminal tab body for the Crews & Agents bottom panel.
 *
 * Loaded lazily by `bottom-panel.tsx` via `next/dynamic({ ssr: false })`
 * so xterm + its CSS only arrive when the tab is actually opened.
 *
 * Backend protocol: shared `useTerminal` hook. Two text frames first
 * (`{type:"auth",token}` then `{mode,crew_id,crew_slug,agent_slug,…}`),
 * then binary stdin/stdout, with `{type:"resize",rows,cols}` text frames
 * as control messages. See `internal/terminal/handler.go`.
 */
import { useEffect, useRef, useState } from "react"
import { RefreshCw, TerminalSquare, WifiOff } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { useTerminal, type TerminalStatus } from "@/hooks/use-terminal"
import { cn } from "@/lib/utils"
import "@xterm/xterm/css/xterm.css"

interface BottomPanelTerminalProps {
  agentName: string
  agentSlug: string
  crewId: string
  crewSlug: string
}

function StatusDot({ status }: { status: TerminalStatus }) {
  if (status === "connecting") return <Spinner className="h-3 w-3 text-amber-400" />
  if (status === "connected") return <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 inline-block" />
  if (status === "error") return <WifiOff className="h-3 w-3 text-red-400" />
  return <span className="h-1.5 w-1.5 rounded-full bg-zinc-500 inline-block" />
}

export function BottomPanelTerminal({
  agentName, agentSlug, crewId, crewSlug,
}: BottomPanelTerminalProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  // Bumping `key` forces useTerminal to re-run its effect (reconnect).
  const [reconnectNonce, setReconnectNonce] = useState(0)
  const { status, disconnect } = useTerminal({
    containerRef,
    crewId,
    crewSlug,
    agentSlug,
    mode: "shell",
    enabled: true,
    key: reconnectNonce,
  })

  // When the agent changes, the parent re-mounts via React `key`, so
  // disconnect-on-unmount in the hook is sufficient cleanup. We still
  // tear down explicitly on tab navigation away.
  useEffect(() => () => disconnect(), [disconnect])

  const reconnect = () => setReconnectNonce((n) => n + 1)
  const offline = status === "disconnected" || status === "error"

  return (
    <div className="h-full flex flex-col bg-[#0a0a0a]">
      <div className="flex items-center justify-between px-3 py-1.5 border-b border-white/5 shrink-0 text-xs">
        <div className="flex items-center gap-2 text-muted-foreground">
          <TerminalSquare className="h-3.5 w-3.5" />
          <StatusDot status={status} />
          <span>{agentName}</span>
          <span className="text-muted-foreground">· /crew/agents/{agentSlug}</span>
        </div>
        {offline && (
          <button
            type="button"
            onClick={reconnect}
            className="flex items-center gap-1.5 px-2 py-0.5 rounded bg-white/5 hover:bg-white/10 text-foreground/85"
            title="Reconnect"
          >
            <RefreshCw className="h-3 w-3" />
            Reconnect
          </button>
        )}
      </div>
      <div
        ref={containerRef}
        className={cn("flex-1 min-h-0", offline && "opacity-60")}
      />
    </div>
  )
}
