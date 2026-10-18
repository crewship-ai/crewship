"use client"

import * as React from "react"
import Link from "next/link"
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
          <span className="font-mono text-micro font-normal text-muted-foreground">{crew.slug}</span>
        </h2>
        {crew.description ? (
          <p className="mt-0.5 max-w-[72ch] text-control text-muted-foreground">{crew.description}</p>
        ) : (
          <p className="mt-0.5 text-control italic text-muted-foreground-soft">No description. Add one in Crews &amp; Agents so people know what this crew is for.</p>
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
          {image && <Fact className="font-mono text-micro">{image}</Fact>}
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

const MAP_W = 640
const ROW_GAP = 36
const NAME_MAX = 24

const short = (name: string) => (name.length > NAME_MAX ? `${name.slice(0, NAME_MAX - 1)}…` : name)

/**
 * The crew's links at a glance (v67 `.cl-map`): who hands work to it on the
 * left, the crew as a circle in its own colour, who it hands work to on the
 * right. A line in green also carries shared files to read, in amber files to
 * deliver; a name opens that crew. On a phone the three columns stack.
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
  const node = (c: Crew) => (
    <button type="button" onClick={() => onSelect(c)} aria-label={`Open ${c.name}`} title={`${c.name} · ${c.slug}`} data-slot="link-map-node"
      className="flex h-full w-full min-w-0 items-center gap-2 rounded-lg border border-border bg-surface-subtle px-1.5 text-left transition-colors hover:border-foreground/25 hover:bg-accent">
      <CrewTile crew={c} />
      <span className="min-w-0">
        <span className="block truncate text-label font-medium leading-tight">{c.name}</span>
        <span className="block truncate text-micro leading-tight text-muted-foreground">{agentsLine(c, agentsByCrew?.get(c.id)) ?? c.slug}</span>
      </span>
    </button>
  )
  const empty = (text: string) => <p className="text-label text-muted-foreground">{text}</p>

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
  const span = Math.max(120, (rows - 1) * ROW_GAP)
  const cy = 40 + span / 2
  const H = cy + 100
  // Each side's crews are spread over the same height, around the circle.
  const y = (i: number, n: number) => (n <= 1 ? cy : cy - span / 2 + (i * span) / (n - 1))
  const files = (from: string, to: string) => dirs.get(dirKey(from, to))?.files ?? null
  const stroke = (f: ReturnType<typeof files>) => (f === "read" ? "stroke-success" : f === "read_write" ? "stroke-warn" : "stroke-primary")
  const label = (c: Crew, x: number, ly: number, anchor: "start" | "end") => (
    <g key={c.id} role="button" tabIndex={0} aria-label={`Open ${c.name}`} data-slot="link-map-node" className="cursor-pointer outline-none [&:focus-visible>text]:underline [&:hover>text]:fill-foreground"
      onClick={() => onSelect(c)} onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onSelect(c) } }}>
      <title>{`${c.name} · ${c.slug}`}</title>
      <text x={x} y={ly + 4} textAnchor={anchor} className="fill-muted-foreground text-control">{short(c.name)}</text>
    </g>
  )
  return (
    <section aria-label="Link map" className="rounded-card border border-border bg-card px-3 py-2" data-slot="link-map">
      <svg viewBox={`0 0 ${MAP_W} ${H}`} className="mx-auto block h-auto w-full" role="group" aria-label={`Links of ${crew.name}`}>
        {ins.map((c, i) => {
          const f = files(c.id, crew.id)
          return <line key={`in-${c.id}`} x1={150} y1={y(i, ins.length)} x2={290} y2={cy} strokeWidth={2} opacity={0.6} data-files={f ?? undefined} className={stroke(f)} />
        })}
        {outs.map((c, i) => {
          const f = files(crew.id, c.id)
          return <line key={`out-${c.id}`} x1={350} y1={cy} x2={490} y2={y(i, outs.length)} strokeWidth={2} opacity={0.6} data-files={f ?? undefined} className={stroke(f)} />
        })}
        {ins.map((c, i) => label(c, 140, y(i, ins.length), "end"))}
        {outs.map((c, i) => label(c, 500, y(i, outs.length), "start"))}
        <circle data-slot="link-map-centre" cx={320} cy={cy} r={30} fill={crew.color || "var(--primary)"} />
        <text x={320} y={cy + 50} textAnchor="middle" className="fill-foreground text-control font-semibold">{short(crew.name)}</text>
        <text x={70} y={H - 10} textAnchor="middle" className="fill-muted-foreground-soft text-micro uppercase tracking-[0.04em]">Receives work from</text>
        <text x={570} y={H - 10} textAnchor="middle" className="fill-muted-foreground-soft text-micro uppercase tracking-[0.04em]">Hands work to</text>
        {!ins.length && <text x={20} y={cy + 4} className="fill-muted-foreground-soft text-control italic">Nobody hands work here yet</text>}
        {!outs.length && <text x={620} y={cy + 4} textAnchor="end" className="fill-muted-foreground-soft text-control italic">Hands work to nobody yet</text>}
      </svg>
    </section>
  )
}
