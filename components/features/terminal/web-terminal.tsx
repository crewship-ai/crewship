"use client"

import { useRef, useState } from "react"
import { useTerminal, type TerminalStatus } from "@/hooks/use-terminal"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { TerminalSquare, Maximize2, Minimize2, X, Loader2, WifiOff } from "lucide-react"
import "@xterm/xterm/css/xterm.css"

interface Agent {
  id: string
  slug: string
  name: string
}

interface WebTerminalProps {
  crewId: string
  crewSlug: string
  agents?: Agent[]
  defaultAgentSlug?: string
  onClose?: () => void
}

function StatusIndicator({ status }: { status: TerminalStatus }) {
  switch (status) {
    case "connecting":
      return <Loader2 className="h-3.5 w-3.5 animate-spin text-yellow-400" />
    case "connected":
      return <span className="h-2 w-2 rounded-full bg-emerald-400 inline-block" />
    case "error":
      return <WifiOff className="h-3.5 w-3.5 text-red-400" />
    default:
      return <span className="h-2 w-2 rounded-full bg-neutral-500 inline-block" />
  }
}

export function WebTerminal({
  crewId,
  crewSlug,
  agents = [],
  defaultAgentSlug,
  onClose,
}: WebTerminalProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [agentSlug, setAgentSlug] = useState(defaultAgentSlug || "__crew_shared__")
  const [isFullscreen, setIsFullscreen] = useState(false)
  const [isConnected, setIsConnected] = useState(true)

  const { status, disconnect } = useTerminal({
    containerRef,
    crewId,
    crewSlug,
    mode: "shell",
    agentSlug: agentSlug === "__crew_shared__" ? undefined : agentSlug,
    enabled: isConnected,
  })

  const handleClose = () => {
    disconnect()
    setIsConnected(false)
    onClose?.()
  }

  const handleReconnect = () => {
    setIsConnected(false)
    // Force re-mount by toggling enabled.
    setTimeout(() => setIsConnected(true), 100)
  }

  const effectiveSlug = agentSlug === "__crew_shared__" ? "" : agentSlug
  const targetLabel = effectiveSlug
    ? agents.find((a) => a.slug === effectiveSlug)?.name || effectiveSlug
    : "Crew Shared"

  return (
    <div
      className={`flex flex-col bg-[#0a0a0a] border border-neutral-800 rounded-lg overflow-hidden ${
        isFullscreen ? "fixed inset-0 z-50" : "h-[400px]"
      }`}
    >
      {/* Toolbar */}
      <div className="flex items-center justify-between px-3 py-1.5 bg-neutral-900 border-b border-neutral-800 shrink-0">
        <div className="flex items-center gap-2">
          <TerminalSquare className="h-4 w-4 text-neutral-400" />
          <StatusIndicator status={status} />
          <span className="text-xs text-neutral-400">{targetLabel}</span>
        </div>

        <div className="flex items-center gap-1.5">
          {/* Agent selector */}
          <Select value={agentSlug} onValueChange={(val) => { setAgentSlug(val); handleReconnect() }}>
            <SelectTrigger className="h-6 w-[140px] text-xs bg-neutral-800 border-neutral-700">
              <SelectValue placeholder="Crew Shared" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__crew_shared__">Crew Shared</SelectItem>
              {agents.map((agent) => (
                <SelectItem key={agent.id} value={agent.slug}>
                  {agent.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          {status === "disconnected" && (
            <Button variant="ghost" size="icon" className="h-6 w-6" onClick={handleReconnect}>
              <TerminalSquare className="h-3.5 w-3.5 text-neutral-400" />
            </Button>
          )}

          <Button
            variant="ghost"
            size="icon"
            className="h-6 w-6"
            onClick={() => setIsFullscreen(!isFullscreen)}
          >
            {isFullscreen ? (
              <Minimize2 className="h-3.5 w-3.5 text-neutral-400" />
            ) : (
              <Maximize2 className="h-3.5 w-3.5 text-neutral-400" />
            )}
          </Button>

          {onClose && (
            <Button variant="ghost" size="icon" className="h-6 w-6" onClick={handleClose}>
              <X className="h-3.5 w-3.5 text-neutral-400" />
            </Button>
          )}
        </div>
      </div>

      {/* Terminal container */}
      <div ref={containerRef} className="flex-1 min-h-0" />
    </div>
  )
}
