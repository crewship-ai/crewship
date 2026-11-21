"use client"

import * as React from "react"
import Link from "next/link"
import { ExternalLink, Package, Plus } from "lucide-react"

import { cn } from "@/lib/utils"
import { CrewIcon } from "@/components/ui/crew-icon"

import { agentCount, crewResources, isOpenNetwork, shortImage, type Crew, type CrewAgent } from "./crew-links-model"

/** Where a crew is opened and edited. */
export const crewHref = (c: Crew) => `/crews?crew=${encodeURIComponent(c.slug)}`

const TILE: Record<"sm" | "lg", { box: string; icon: string }> = {
  sm: { box: "h-7 w-7 rounded-lg", icon: "h-3.5 w-3.5" },
  lg: { box: "h-12 w-12 rounded-xl", icon: "h-6 w-6" },
}

/**
 * The crew's own icon and colour, as Crews & Agents shows it. A crew nobody
 * gave an icon gets a dashed neutral tile, so "not set" never passes for a
 * choice.
 */
export function CrewTile({ crew, size = "sm", className }: { crew: Crew; size?: "sm" | "lg"; className?: string }) {
  if (crew.icon) return <CrewIcon icon={crew.icon} color={crew.color} size={size} className={className} />
  return (
    <span title="No icon set" data-slot="crew-tile-unset"
      className={cn("inline-flex shrink-0 items-center justify-center border border-dashed border-border text-muted-foreground", TILE[size].box, className)}>
      <Package className={TILE[size].icon} aria-hidden />
    </span>
  )
}

/** "5 agents · 1 stopped", "no agents · collector", or null when unknown. */
export function agentsLine(c: Crew, agents: CrewAgent[] | undefined): string | null {
  const n = agentCount(c, agents)
  if (n == null) return null
  if (n === 0) return "no agents · collector"
  const stopped = (agents ?? []).filter((a) => a.status?.toUpperCase() === "STOPPED").length
  return `${n} ${n === 1 ? "agent" : "agents"}${stopped ? ` · ${stopped} stopped` : ""}`
}

/**
 * "5 agents · 1 stopped · Restricted network", or null when the agent count is
 * unknown. The panel's second line and the crew page's head both read it.
 */
export function crewSummary(c: Crew, agents: CrewAgent[] | undefined): string | null {
  const n = agentCount(c, agents)
  const net = c.network_mode == null ? null : isOpenNetwork(c) ? "Open network" : "Restricted network"
  const stopped = (agents ?? []).filter((a) => a.status?.toUpperCase() === "STOPPED").length
  const parts = [n == null ? null : `${n === 0 ? "No" : n} ${n === 1 ? "agent" : "agents"}`, stopped ? `${stopped} stopped` : null, net]
  const line = parts.filter(Boolean).join(" · ")
  return line || null
}

/**
 * The crew the page is about: its icon, one line of facts and what it is for,
 * then what it runs on. Opening the crew is one link, on the right.
 */
export function CrewHead({ crew, agents, showSlug = false }: { crew: Crew; agents: CrewAgent[] | undefined; showSlug?: boolean }) {
  const members = crew._count?.members
  const resources = crewResources(crew)
  const image = shortImage(crew.runtime_image)
  const box = [members != null ? `${members} ${members === 1 ? "member" : "members"}` : null, resources, image].filter(Boolean).join(" · ")
  const summary = crewSummary(crew, agents)
  return (
    <header className="flex flex-wrap items-start gap-x-4 gap-y-2" aria-label={`${crew.name} profile`} data-slot="crew-profile">
      <CrewTile crew={crew} size="lg" />
      <div className="min-w-0 flex-1 basis-60">
        <h2 className="flex flex-wrap items-baseline gap-x-2 text-lg font-semibold tracking-[-0.02em]">
          <span className="truncate">{crew.name}</span>
          {showSlug && <span className="font-mono text-micro font-normal text-muted-foreground">{crew.slug}</span>}
        </h2>
        <p className="mt-0.5 max-w-[72ch] text-control text-muted-foreground">
          {summary}
          {summary && " · "}
          {crew.description ? crew.description : <span className="italic text-muted-foreground-soft">No description. Add one in Crews &amp; Agents so people know what this crew is for.</span>}
        </p>
        {(box || !crew.icon) && (
          <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-label text-muted-foreground" data-slot="crew-facts">
            {box && <span>{box}</span>}
            {!crew.icon && (
              <Link href={crewHref(crew)} className="inline-flex items-center gap-1 text-primary-hover hover:underline">
                <Plus className="h-3.5 w-3.5" aria-hidden />Set icon and colour
              </Link>
            )}
          </p>
        )}
      </div>
      <Link href={crewHref(crew)} className="inline-flex items-center gap-1.5 self-start text-xs font-medium text-primary-hover hover:underline coarse:min-h-[44px]">
        Open crew<ExternalLink className="h-3.5 w-3.5" aria-hidden />
      </Link>
    </header>
  )
}
