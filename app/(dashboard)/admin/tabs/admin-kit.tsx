"use client"

import * as React from "react"
import type { CSSProperties } from "react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusPill } from "@/components/ui/status-pill"
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet"

/**
 * The pieces the Admin lists share (Workspaces, Users): a figures row of icon
 * tiles, filter chips, role pills, "3 h ago", a workspace's tile colour and
 * the right-hand detail drawer. Kept here so the two tabs cannot drift.
 */

/** One figure in the tab's summary row. */
export function Kpi({ icon: Icon, tint, label, value, sub }: { icon: LucideIcon; tint: string; label: string; value?: React.ReactNode; sub?: React.ReactNode }) {
  return (
    <div className="flex min-w-0 items-center gap-3 rounded-card border border-border bg-card px-4 py-3" data-slot="admin-kpi">
      <span className="icon-tile grid h-9 w-9 shrink-0 place-items-center rounded-lg" style={{ "--ic": tint } as CSSProperties} aria-hidden>
        <Icon className="h-4 w-4" />
      </span>
      <div className="min-w-0">
        <div className="eyebrow text-muted-foreground">{label}</div>
        {value === undefined ? <Skeleton className="mt-1 h-5 w-12" /> : <div className="font-mono text-lg font-semibold leading-6 tabular-nums">{value}</div>}
        {sub != null && <div className="truncate text-[11px] text-muted-foreground" title={typeof sub === "string" ? sub : undefined}>{sub}</div>}
      </div>
    </div>
  )
}

/** A filter chip: pressed or not, with how many rows picking it shows. */
export function FilterChip({ pressed, onClick, children, count, dot }: { pressed: boolean; onClick: () => void; children: React.ReactNode; count?: number; dot?: string }) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={onClick}
      className={cn(
        "inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full border px-2.5 text-[12px] transition-colors",
        pressed ? "border-primary/40 bg-primary/10 font-medium text-primary-hover" : "border-border bg-card text-muted-foreground hover:text-foreground",
      )}
    >
      {dot && <span className={cn("h-1.5 w-1.5 rounded-full", dot)} aria-hidden />}
      {children}
      {count != null && <span className="font-mono text-[10.5px] tabular-nums opacity-70">{count}</span>}
    </button>
  )
}

const ROLE_TONE: Record<string, "blue" | "success" | "muted" | "warn"> = { ADMIN: "blue", MANAGER: "success", MEMBER: "muted", VIEWER: "muted" }
export const ROLES = ["OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"] as const
export const roleLabel = (r: string) => r.charAt(0) + r.slice(1).toLowerCase()

export function RolePill({ role }: { role: string | null | undefined }) {
  if (!role) return <span className="text-muted-foreground">—</span>
  if (role === "OWNER") {
    return <span className="inline-flex h-5 items-center rounded-full bg-[color-mix(in_srgb,var(--purple)_16%,transparent)] px-2 font-mono text-[10.5px] font-semibold text-[var(--purple)]">Owner</span>
  }
  return <StatusPill tone={ROLE_TONE[role] ?? "muted"} label={roleLabel(role)} />
}

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

export const daysSince = (iso: string | null | undefined, now = Date.now()) => {
  if (!iso) return Infinity
  const t = Date.parse(iso.includes("T") ? iso : iso.replace(" ", "T") + "Z")
  return Number.isNaN(t) ? Infinity : (now - t) / 86_400_000
}

const TILE = ["#3B82F6", "#8B5CF6", "#14B8A6", "#F59E0B", "#EC4899", "#22C55E", "#EF4444", "#0EA5E9", "#6366F1", "#84CC16"]
/** A workspace's tile colour, stable per id. */
export function workspaceColor(id: string): string {
  let h = 0
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0
  return TILE[h % TILE.length]
}
export const initials = (name: string) => name.split(/\s+/).filter(Boolean).map((w) => w[0]).slice(0, 2).join("").toUpperCase() || "?"

export function WorkspaceTile({ id, name, size = "md" }: { id: string; name: string; size?: "xs" | "md" | "lg" }) {
  return (
    <span
      title={name}
      className={cn(
        "grid shrink-0 place-items-center font-semibold text-white",
        size === "xs" ? "h-5 w-5 rounded-[6px] text-[8.5px]" : size === "lg" ? "h-10 w-10 rounded-xl text-sm" : "h-7 w-7 rounded-lg text-[11px]",
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

/** The right-hand detail drawer, with a header and tabs. */
export function DetailDrawer({ open, onOpenChange, header, title, tabs, tab, onTab, children }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  header: React.ReactNode
  title: string
  tabs: { key: string; label: string }[]
  tab: string
  onTab: (key: string) => void
  children: React.ReactNode
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full gap-0 p-0 sm:max-w-[440px]" data-slot="admin-drawer">
        <SheetTitle className="sr-only">{title}</SheetTitle>
        <SheetDescription className="sr-only">Details and actions for {title}</SheetDescription>
        <div className="flex items-start gap-3 border-b border-border px-5 pb-3 pt-5 pr-12">{header}</div>
        <div className="flex gap-1 border-b border-border px-3" role="tablist">
          {tabs.map((t) => (
            <button key={t.key} type="button" role="tab" aria-selected={tab === t.key} onClick={() => onTab(t.key)}
              className={cn("-mb-px h-9 border-b-2 px-2.5 text-[12.5px] transition-colors", tab === t.key ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground")}>
              {t.label}
            </button>
          ))}
        </div>
        <div key={tab} className="grid flex-1 content-start gap-4 overflow-y-auto px-5 pb-8 pt-4 motion-safe:animate-in motion-safe:fade-in-0 motion-safe:slide-in-from-bottom-1" role="tabpanel">
          {children}
        </div>
      </SheetContent>
    </Sheet>
  )
}

/** A labelled group inside the drawer. */
export function DrawerSection({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <section aria-label={label}>
      <h4 className="mb-1.5 font-mono text-[10.5px] font-medium uppercase tracking-[0.08em] text-muted-foreground-soft">{label}</h4>
      {children}
    </section>
  )
}

/** Label/value pairs inside the drawer. */
export function Facts({ rows }: { rows: [string, React.ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[130px_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[12.5px]">
      {rows.map(([k, v]) => (
        <React.Fragment key={k}>
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0">{v}</dd>
        </React.Fragment>
      ))}
    </dl>
  )
}
