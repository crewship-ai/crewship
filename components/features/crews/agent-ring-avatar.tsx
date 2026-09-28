"use client"

/**
 * The Harbor agent portrait and card.
 *
 * AgentRingAvatar: the agent's own avatar (style and seed untouched) inside a
 * 3px conic ring in its crew's colour, with the engine as a small app-icon
 * badge at the bottom right. HarborAgentCard: that portrait with name, role,
 * crew, status and model — the card the crew Team grid is made of.
 */

import * as React from "react"
import Link from "next/link"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { StatusPill } from "@/components/ui/status-pill"
import { getBrand } from "@/lib/credential-providers/registry"
import { brandTileColors } from "@/lib/credentials/brand-tile"
import { getCrewDotColor } from "@/lib/crew-icons"
import { getModelLabel } from "@/lib/cli-adapters"
import { cn } from "@/lib/utils"

import type { AgentSummary } from "./crew-canvas-tabs/types"

function ringColor(crewColor: string | null | undefined): string {
  return crewColor ? getCrewDotColor(crewColor) : "var(--primary)"
}

export function EngineBadge({ provider, className }: { provider: string; className?: string }) {
  const brand = getBrand(provider.toUpperCase())
  const Icon = brand.Icon
  const { background, glyph } = brandTileColors(brand.key, brand.hex)
  return (
    <span
      role="img"
      aria-label={`${brand.label} engine`}
      title={brand.label}
      className={cn("flex items-center justify-center rounded-[28%] ring-2 ring-card", className)}
      style={{ background, boxShadow: "0 6px 14px -6px rgba(0,0,0,.45), inset 0 1px 0 rgba(255,255,255,.25)" }}
    >
      <Icon className="h-[60%] w-[60%]" style={{ color: glyph }} aria-hidden="true" />
    </span>
  )
}

export function AgentRingAvatar({
  seed,
  style,
  avatarUrl,
  agentId,
  crewColor,
  engine,
  size = "md",
  className,
}: {
  seed: string
  style?: string | null
  avatarUrl?: string | null
  agentId?: string
  crewColor?: string | null
  /** llm_provider; no badge when the agent has none yet. */
  engine?: string | null
  size?: "md" | "lg"
  className?: string
}) {
  const ring = ringColor(crewColor)
  return (
    <span className={cn("relative inline-flex shrink-0", className)}>
      <span
        data-slot="agent-ring"
        className="rounded-full p-[3px]"
        style={
          {
            "--ring": ring,
            background:
              "conic-gradient(from 210deg, var(--ring), color-mix(in srgb, var(--ring) 30%, transparent), var(--ring))",
          } as React.CSSProperties
        }
      >
        <span className="block rounded-full bg-card p-[2px]">
          <AgentAvatar
            seed={seed}
            style={style}
            avatarUrl={avatarUrl}
            agentId={agentId}
            className={size === "lg" ? "h-16 w-16" : "h-11 w-11"}
          />
        </span>
      </span>
      {engine && (
        <EngineBadge
          provider={engine}
          className={cn("absolute", size === "lg" ? "-bottom-0.5 -right-0.5 h-6 w-6" : "-bottom-1 -right-1 h-[18px] w-[18px]")}
        />
      )}
    </span>
  )
}

export function HarborAgentCard({
  agent,
  crewName,
  crewColor,
  avatarStyle,
  href,
  onSelect,
}: {
  agent: AgentSummary
  crewName?: string
  crewColor?: string | null
  /** The crew's avatar style, when the agent has none of its own. */
  avatarStyle?: string | null
  href: string
  onSelect?: () => void
}) {
  const role = agent.role_title || (agent.agent_role === "LEAD" ? "Lead" : "Agent")
  return (
    <Link
      href={href}
      onClick={(event) => {
        if (onSelect && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && event.button === 0) {
          event.preventDefault()
          onSelect()
        }
      }}
      className="group lift flex h-full flex-col gap-3 rounded-[20px] border border-border bg-card p-4"
    >
      <span className="flex items-start gap-3">
        <AgentRingAvatar
          seed={agent.avatar_seed || agent.slug}
          style={agent.avatar_style || avatarStyle}
          avatarUrl={agent.avatar_url}
          crewColor={crewColor}
          engine={agent.llm_provider}
        />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[17px] font-semibold leading-6 tracking-[-0.01em] text-foreground group-hover:text-primary-hover">
            {agent.name}
          </span>
          <span className="block truncate text-sm text-muted-foreground">{role}</span>
          {crewName && (
            <span className="mt-0.5 block truncate font-mono text-[11px]" style={{ color: ringColor(crewColor) }}>
              {crewName}
            </span>
          )}
        </span>
        <StatusPill status={agent.status} live={agent.status === "RUNNING"} />
      </span>
      {agent.llm_model && (
        <span className="mt-auto flex items-center justify-between gap-2 border-t border-border pt-2.5 text-[11px] text-muted-foreground-soft">
          <span className="truncate">{getModelLabel(agent.llm_model)}</span>
        </span>
      )}
    </Link>
  )
}
