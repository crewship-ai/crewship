"use client"

import * as React from "react"
import Link from "next/link"
import { motion, useReducedMotion } from "motion/react"
import { ArrowDown, Bot, Cpu, ExternalLink, Globe, Lock, Package, Plus, User } from "lucide-react"

import { cn } from "@/lib/utils"
import { CrewIcon } from "@/components/ui/crew-icon"
import { AgentAvatar } from "@/components/ui/agent-avatar"

import { agentCount, crewResources, dirKey, isOpenNetwork, shortImage, type Crew, type CrewAgent, type Direction } from "./crew-links-model"

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

function Fact({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn("inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded-lg border border-border bg-surface-subtle px-2.5 text-xs text-muted-foreground", className)}>{children}</span>
}

/**
 * Who the crew is: its identity, what it is for, who works in it and the box
 * it runs in — so a link is decided about a crew, not a name.
 */
export function CrewProfile({ crew, agents }: { crew: Crew; agents: CrewAgent[] | undefined }) {
  const n = agentCount(crew, agents)
  const stopped = (agents ?? []).filter((a) => a.status?.toUpperCase() === "STOPPED").length
  const resources = crewResources(crew)
  const image = shortImage(crew.runtime_image)
  const members = crew._count?.members
  return (
    <section className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-3 rounded-card border border-border bg-card p-4 sm:grid-cols-[auto_minmax(0,1fr)_auto]" aria-label={`${crew.name} profile`} data-slot="crew-profile">
      <CrewTile crew={crew} size="lg" />
      <div className="min-w-0">
        <h2 className="flex flex-wrap items-baseline gap-x-2 text-lg font-semibold tracking-[-0.02em]">
          <span className="truncate">{crew.name}</span>
          <span className="font-mono text-[11px] font-normal text-muted-foreground">{crew.slug}</span>
        </h2>
        {crew.description ? (
          <p className="mt-0.5 max-w-[72ch] text-[13px] text-muted-foreground">{crew.description}</p>
        ) : (
          <p className="mt-0.5 text-[13px] italic text-muted-foreground-soft">No description. Add one in Crews &amp; Agents so people know what this crew is for.</p>
        )}
        <div className="mt-2.5 flex flex-wrap gap-1.5" data-slot="crew-facts">
          {n != null && (
            <Fact>
              {agents && agents.length > 0 ? (
                <span className="flex -space-x-1.5" aria-hidden>
                  {agents.slice(0, 5).map((a) => (
                    <AgentAvatar key={a.id} seed={a.avatar_seed || a.slug} style={a.avatar_style || crew.avatar_style} avatarUrl={a.avatar_url}
                      title={a.name} className={cn("h-5 w-5 rounded-full ring-2 ring-card", a.status?.toUpperCase() === "STOPPED" && "opacity-60 grayscale")} />
                  ))}
                </span>
              ) : <Bot className="h-3.5 w-3.5" aria-hidden />}
              <span><span className="font-medium text-foreground">{n || "No"}</span> {n === 1 ? "agent" : "agents"}{stopped ? ` · ${stopped} stopped` : ""}</span>
            </Fact>
          )}
          {members != null && <Fact><User className="h-3.5 w-3.5" aria-hidden /><span><span className="font-medium text-foreground">{members}</span> {members === 1 ? "member" : "members"}</span></Fact>}
          {crew.network_mode != null && (
            <Fact>{isOpenNetwork(crew) ? <Globe className="h-3.5 w-3.5" aria-hidden /> : <Lock className="h-3.5 w-3.5" aria-hidden />}{isOpenNetwork(crew) ? "Open network" : "Restricted network"}</Fact>
          )}
          {resources && <Fact><Cpu className="h-3.5 w-3.5" aria-hidden /><span className="text-foreground">{resources}</span></Fact>}
          {image && <Fact className="font-mono text-[11px]">{image}</Fact>}
          {!crew.icon && (
            <Link href={crewHref(crew)} className="inline-flex h-7 items-center gap-1.5 rounded-lg border border-dashed border-border px-2.5 text-xs text-primary-hover hover:bg-accent">
              <Plus className="h-3.5 w-3.5" aria-hidden />Set icon and colour
            </Link>
          )}
        </div>
      </div>
      <Link href={crewHref(crew)} className="col-span-2 inline-flex items-center gap-1.5 self-start text-xs font-medium text-primary-hover hover:underline sm:col-span-1">
        Open crew<ExternalLink className="h-3.5 w-3.5" aria-hidden />
      </Link>
    </section>
  )
}

// ── Link map ────────────────────────────────────────────────────────────────

const W = 880
const NODE_W = 210
const NODE_H = 38
const ROW = 50
const CENTRE_W = 190

/**
 * The crew's links at a glance: who hands work to it (left), the crew, and
 * who it hands work to (right). An edge in the ink colour also carries shared
 * files. Edges draw in whenever the crew changes; a node opens that crew.
 * On a phone the three columns stack.
 */
export function LinkMap({ crew, ins, outs, dirs, agentsByCrew, onSelect, compact = false }: {
  crew: Crew
  ins: Crew[]
  outs: Crew[]
  dirs: Map<string, Direction>
  agentsByCrew: Map<string, CrewAgent[]> | null
  onSelect: (c: Crew) => void
  compact?: boolean
}) {
  const reduce = useReducedMotion()
  const node = (c: Crew) => (
    <button type="button" onClick={() => onSelect(c)} aria-label={`Open ${c.name}`} title={`${c.name} · ${c.slug}`} data-slot="link-map-node"
      className="flex h-full w-full min-w-0 items-center gap-2 rounded-lg border border-border bg-surface-subtle px-1.5 text-left transition-colors hover:border-foreground/25 hover:bg-accent">
      <CrewTile crew={c} />
      <span className="min-w-0">
        <span className="block truncate text-[12.5px] font-medium leading-tight">{c.name}</span>
        <span className="block truncate text-[10.5px] leading-tight text-muted-foreground">{agentsLine(c, agentsByCrew?.get(c.id)) ?? c.slug}</span>
      </span>
    </button>
  )
  const empty = (text: string) => <p className="text-[11.5px] text-muted-foreground">{text}</p>

  if (compact) {
    return (
      <section aria-label="Link map" className="flex flex-col items-stretch gap-2 rounded-card border border-border bg-card p-3" data-slot="link-map">
        <span className="eyebrow text-muted-foreground">Receives work from</span>
        {ins.length ? ins.map((c) => <div key={c.id} className="h-11">{node(c)}</div>) : empty("Nobody hands work here yet")}
        <ArrowDown className="mx-auto h-4 w-4 text-muted-foreground" aria-hidden />
        <div className="flex items-center gap-2 rounded-lg border border-primary/40 bg-primary/10 px-2 py-2 text-sm font-semibold"><CrewTile crew={crew} />{crew.name}</div>
        <ArrowDown className="mx-auto h-4 w-4 text-muted-foreground" aria-hidden />
        <span className="eyebrow text-muted-foreground">Hands work to</span>
        {outs.length ? outs.map((c) => <div key={c.id} className="h-11">{node(c)}</div>) : empty("Hands work to nobody yet")}
      </section>
    )
  }

  const rows = Math.max(ins.length, outs.length, 1)
  const H = Math.max(120, rows * ROW + 40)
  const y = (i: number, n: number) => 34 + ((H - 34) / (n + 1)) * (i + 1)
  const cx = (W - CENTRE_W) / 2
  const cy = 34 + (H - 34) / 2
  const edge = (key: string, x1: number, y1: number, x2: number, y2: number, files: boolean, i: number) => (
    <motion.path
      key={key}
      d={`M${x1},${y1} C${(x1 + x2) / 2},${y1} ${(x1 + x2) / 2},${y2} ${x2},${y2}`}
      fill="none"
      strokeWidth={1.5}
      className={files ? "stroke-primary-hover" : "stroke-muted-foreground/50"}
      markerEnd={files ? "url(#link-map-arrow-ink)" : "url(#link-map-arrow)"}
      data-files={files || undefined}
      initial={reduce ? false : { pathLength: 0, opacity: 0 }}
      animate={{ pathLength: 1, opacity: 1 }}
      transition={{ duration: 0.55, delay: 0.08 + i * 0.06, ease: [0.2, 0.7, 0.2, 1] }}
    />
  )
  const carriesFiles = (from: string, to: string) => {
    const f = dirs.get(dirKey(from, to))?.files
    return f === "read" || f === "read_write"
  }
  return (
    <section aria-label="Link map" className="rounded-card border border-border bg-card px-3 py-2.5" data-slot="link-map">
      <svg viewBox={`0 0 ${W} ${H}`} className="mx-auto block h-auto w-full max-w-[880px]" role="group" aria-label={`Links of ${crew.name}`}>
        <defs>
          {[["link-map-arrow", "fill-muted-foreground/60"], ["link-map-arrow-ink", "fill-primary-hover"]].map(([id, cls]) => (
            <marker key={id} id={id} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
              <path d="M0,0 L10,5 L0,10 z" className={cls} />
            </marker>
          ))}
        </defs>
        <text x={0} y={14} className="fill-muted-foreground font-mono text-[10px] uppercase tracking-[0.08em]">Receives work from</text>
        <text x={W} y={14} textAnchor="end" className="fill-muted-foreground font-mono text-[10px] uppercase tracking-[0.08em]">Hands work to</text>
        <g key={crew.id}>
          {ins.map((c, i) => edge(`in-${c.id}`, NODE_W, y(i, ins.length), cx - 4, cy, carriesFiles(c.id, crew.id), i))}
          {outs.map((c, i) => edge(`out-${c.id}`, cx + CENTRE_W, cy, W - NODE_W - 4, y(i, outs.length), carriesFiles(crew.id, c.id), i + ins.length))}
        </g>
        {ins.map((c, i) => <foreignObject key={c.id} x={0} y={y(i, ins.length) - NODE_H / 2} width={NODE_W} height={NODE_H}>{node(c)}</foreignObject>)}
        {outs.map((c, i) => <foreignObject key={c.id} x={W - NODE_W} y={y(i, outs.length) - NODE_H / 2} width={NODE_W} height={NODE_H}>{node(c)}</foreignObject>)}
        {!ins.length && <text x={0} y={cy + 4} className="fill-muted-foreground text-[11.5px]">Nobody hands work here yet</text>}
        {!outs.length && <text x={W} y={cy + 4} textAnchor="end" className="fill-muted-foreground text-[11.5px]">Hands work to nobody yet</text>}
        <foreignObject x={cx} y={cy - 24} width={CENTRE_W} height={48}>
          <div className="flex h-full items-center gap-2 rounded-xl border border-primary/40 bg-primary/10 px-2 text-sm font-semibold" data-slot="link-map-centre">
            <CrewTile crew={crew} />
            <span className="truncate">{crew.name}</span>
          </div>
        </foreignObject>
      </svg>
    </section>
  )
}
