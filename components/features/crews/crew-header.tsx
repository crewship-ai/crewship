"use client"

import { AlertTriangle, Bot, Box, Lock } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { StatusPill } from "@/components/ui/status-pill"

import { HeadChip } from "./cr-card"
import { CrewTile } from "./crew-tile"
import { isCrewPrivileged } from "./crew-privileged-badge"
import type { CrewStatus } from "./crews-panel-model"
import type { AgentSummary, CrewRecord } from "./crew-canvas-tabs/types"

export const AUTONOMY_LABEL: Record<string, { label: string; hint: string }> = {
  strict: { label: "Strict", hint: "Asks before everything. No temporary hires, no memory writes without review." },
  guided: { label: "Guided", hint: "Asks before hiring, adding skills or creating agents. Memory notes go to review." },
  trusted: { label: "Trusted", hint: "Hires and adds skills on its own and tells you in the Inbox." },
  full: { label: "Full", hint: "Acts on its own; only the Activity records it." },
}

const STATE_PILL: Record<CrewStatus["state"], { tone: "blue" | "warn" | "muted"; label: string }> = {
  working: { tone: "blue", label: "Working" },
  needs: { tone: "warn", label: "Needs you" },
  idle: { tone: "muted", label: "Idle" },
}

/** The base image's short name: "Node.js 22" for the devcontainer preset, else its tail. */
export function imageLabel(image: string | null | undefined): string {
  const raw = (image ?? "").trim()
  if (!raw) return "Default image"
  const tail = raw.split("/").pop() ?? raw
  const m = /^javascript-node:(\d+)/.exec(tail)
  if (m) return `Node.js ${m[1]}`
  const py = /^python:(\d+\.\d+)/.exec(tail)
  if (py) return `Python ${py[1]}`
  const go = /^go:(\d+\.\d+)/.exec(tail)
  if (go) return `Go ${go[1]}`
  if (/^base:ubuntu-?(\d+\.\d+)?/.test(tail)) return `Ubuntu ${tail.split("ubuntu-")[1] ?? ""}`.trim()
  if (/^base:bookworm/.test(tail)) return "Debian 12"
  return tail
}

/**
 * C-HEAD: the crew's icon (48), name, purpose and one row of facts — state,
 * lead, size, autonomy, image and container, and a privileged warning.
 * Editing lives in the Environment and Settings tabs; the icon opens the
 * icon picker.
 */
export function CrewHeader({
  crew,
  status,
  lead,
  agentCount,
  autonomy,
  containerState,
  onPickIcon,
}: {
  crew: CrewRecord
  status: CrewStatus
  lead: AgentSummary | null
  agentCount: number
  autonomy: string | null
  /** "running", "stopped", … from GET /crews/{id}/container-status; null while unknown. */
  containerState: string | null
  onPickIcon?: () => void
}) {
  const pill = STATE_PILL[status.state]
  const auto = autonomy ? AUTONOMY_LABEL[autonomy] : null
  return (
    <header className="flex items-start gap-4" data-testid="crew-header">
      <button
        type="button"
        onClick={onPickIcon}
        disabled={!onPickIcon}
        title="Change icon and colour"
        aria-label="Change icon and colour"
        className="shrink-0 rounded-[28%] outline-none transition-opacity hover:opacity-85 focus-visible:ring-2 focus-visible:ring-primary disabled:pointer-events-none"
      >
        <CrewTile icon={crew.icon} color={crew.color} size={48} />
      </button>
      <div className="min-w-0 flex-1">
        <h1 className="type-title truncate">{crew.name}</h1>
        {crew.description && <p className="mt-1 max-w-prose text-body text-muted-foreground">{crew.description}</p>}
        <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
          <StatusPill className="font-sans" tone={pill.tone} label={pill.label} live={status.state === "working"} size="md" />
          {lead && (
            <HeadChip icon={<AgentAvatar seed={lead.avatar_seed || lead.name} style={lead.avatar_style} avatarUrl={lead.avatar_url} className="size-4 rounded-sm" />}>
              Lead · {lead.name}
            </HeadChip>
          )}
          <HeadChip icon={<Bot className="size-3.5" aria-hidden />}>
            {agentCount} {agentCount === 1 ? "agent" : "agents"}
          </HeadChip>
          {auto && (
            <HeadChip icon={<Lock className="size-3.5" aria-hidden />}>
              <span title={auto.hint}>{auto.label}</span>
            </HeadChip>
          )}
          <HeadChip icon={<Box className="size-3.5" aria-hidden />}>
            {imageLabel(crew.runtime_image)}{containerState ? ` · ${containerState.replace(/_/g, " ")}` : ""}
          </HeadChip>
          {isCrewPrivileged(crew.devcontainer_config) && (
            <HeadChip tone="warn" icon={<AlertTriangle className="size-3.5" aria-hidden />}>Privileged container</HeadChip>
          )}
        </div>
      </div>
    </header>
  )
}
