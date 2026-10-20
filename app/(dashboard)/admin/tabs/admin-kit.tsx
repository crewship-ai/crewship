"use client"

import * as React from "react"

import { cn } from "@/lib/utils"

/**
 * Small pieces the Admin console shares: "3 h ago", a workspace's tile and
 * colour, a week of runs as a line, and a labelled group. The People &
 * workspaces page (components/features/admin/people) builds on them.
 */

/** "just now", "12 min ago", "3 h ago", "yesterday", "5 days ago", else a date. */
export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "never"
  const t = Date.parse(iso.includes("T") ? iso : iso.replace(" ", "T") + "Z")
  if (Number.isNaN(t)) return "—"
  const s = Math.max(0, (now - t) / 1000)
  if (s < 90) return "just now"
  if (s < 3600) return `${Math.round(s / 60)} min ago`
  if (s < 86400) return `${Math.round(s / 3600)} h ago`
  const d = Math.floor(s / 86400)
  if (d === 1) return "yesterday"
  if (d < 30) return `${d} days ago`
  return new Date(t).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })
}

const TILE = ["#3B82F6", "#8B5CF6", "#14B8A6", "#F59E0B", "#EC4899", "#22C55E", "#EF4444", "#0EA5E9", "#6366F1", "#84CC16"]
/** A workspace's tile colour, stable per id. */
export function workspaceColor(id: string): string {
  let h = 0
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0
  return TILE[h % TILE.length]
}
export const initials = (name: string) => name.split(/\s+/).filter(Boolean).map((w) => w[0]).slice(0, 2).join("").toUpperCase() || "?"

export function WorkspaceTile({ id, name, size = "md", logoUrl }: { id: string; name: string; size?: "xs" | "md" | "lg"; logoUrl?: string | null }) {
  const box = size === "xs" ? "h-5 w-5 rounded-[6px]" : size === "lg" ? "h-10 w-10 rounded-xl" : "h-7 w-7 rounded-lg"
  if (logoUrl) {
    // The workspace's own logo (#3005) when it has one.
    return <img src={logoUrl} alt="" title={name} aria-hidden className={cn("shrink-0 object-cover", box)} />
  }
  return (
    <span
      title={name}
      className={cn(
        "grid shrink-0 place-items-center font-semibold text-white",
        size === "xs" ? "h-5 w-5 rounded-[6px] text-micro" : size === "lg" ? "h-10 w-10 rounded-xl text-sm" : "h-7 w-7 rounded-lg text-label",
      )}
      style={{ background: workspaceColor(id) }}
      aria-hidden
    >
      {initials(name)}
    </span>
  )
}

/** A week of counts as a small line, with the total beside it. */
export function WeekLine({ values, muted }: { values: number[]; muted?: boolean }) {
  const w = 64, h = 18, max = Math.max(1, ...values)
  const pts = values.map((v, i) => `${((i / Math.max(1, values.length - 1)) * w).toFixed(1)},${(h - 2 - (v / max) * (h - 4)).toFixed(1)}`).join(" ")
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} aria-hidden className="shrink-0">
      <polyline points={pts} fill="none" stroke={muted ? "var(--muted-foreground)" : "var(--primary)"} strokeOpacity={muted ? 0.5 : 1} strokeWidth="1.6" strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  )
}

/** A labelled group: a small mono heading over its content. */
export function DrawerSection({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <section aria-label={label}>
      <h4 className="mb-1.5 text-micro font-medium text-muted-foreground-soft">{label}</h4>
      {children}
    </section>
  )
}

/** One figure in an AdminStrip: a 30 px grey tile, a small label, the value. */
export interface StripItem {
  icon: React.ReactNode
  label: string
  value: React.ReactNode
  /** Stable hook for tests and the e2e spec ("runtime", "daemon"). */
  slot?: string
}

/**
 * The row of figures above a section's cards (Harbor 2 strip): Runtime,
 * Daemon, Agents and Log level on Runtime; services, channels and email on
 * Notifications. The boxes wrap on a phone.
 */
export function AdminStrip({ items, label, className }: { items: StripItem[]; label: string; className?: string }) {
  return (
    <ul aria-label={label} data-slot="admin-strip" className={cn("flex flex-wrap gap-2", className)}>
      {items.map((it) => (
        <li key={it.label} data-strip={it.slot} className="flex min-w-0 flex-[1_1_9rem] items-center gap-2.5 rounded-card border border-border bg-card px-3 py-2.5">
          <span aria-hidden className="grid size-[var(--control-md)] shrink-0 place-items-center rounded-lg bg-surface-subtle text-muted-foreground [&>svg]:size-4">
            {it.icon}
          </span>
          <span className="min-w-0">
            <small className="block text-micro text-muted-foreground">{it.label}</small>
            <b className="block truncate text-control font-semibold text-foreground">{it.value}</b>
          </span>
        </li>
      ))}
    </ul>
  )
}

/** The 24 px tile a panel row's icon sits in; the open section's is in the accent. */
export function AdminNavTile({ icon: Icon, active }: { icon: React.ElementType; active: boolean }) {
  return (
    <span
      aria-hidden
      className={cn(
        "grid h-6 w-6 shrink-0 place-items-center rounded-md border",
        active ? "icon-tile" : "border-border bg-surface-subtle text-muted-foreground",
      )}
    >
      <Icon className="h-3.5 w-3.5" />
    </span>
  )
}
