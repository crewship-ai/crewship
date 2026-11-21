"use client"

import * as React from "react"
import { AlertTriangle, ChevronRight, CloudOff } from "lucide-react"

import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusPill } from "@/components/ui/status-pill"
import type { StatusTone } from "@/lib/format-status"
import type { Tone } from "./backups-model"
import type { Resource } from "./use-backups-data"
import { settingsTable, settingsTh, settingsTd, settingsTableRowLink } from "@/components/features/settings/shared"

/**
 * The few pieces every Backups section draws the same way, on top of the
 * Harbor primitives (SettingsCard, StatusPill, SettingsSegmented): a label /
 * value row with a sub-line, the warn bar, a small button, the workspace
 * avatar and the quiet "not on this server yet" state.
 */

export const TONE_PILL: Record<Tone, StatusTone> = { ok: "success", warn: "warn", bad: "danger", muted: "muted" }
export const TONE_TEXT: Record<Tone, string> = { ok: "text-success", warn: "text-warn", bad: "text-destructive", muted: "text-muted-foreground" }

/** A result chip: a dot and a word (never colour alone). */
export function Chip({ tone, children, className }: { tone: Tone; children: React.ReactNode; className?: string }) {
  return <StatusPill tone={TONE_PILL[tone]} label={children} className={className} />
}

/** Label on the left (170px), value and an optional sub-line on the right. */
export function FieldRow({ label, hint, children, detail, detailTone, className }: {
  label: React.ReactNode
  hint?: React.ReactNode
  children: React.ReactNode
  detail?: React.ReactNode
  detailTone?: Tone | null
  className?: string
}) {
  return (
    <div data-slot="field-row" className={cn("grid grid-cols-1 items-center gap-x-3 gap-y-1 border-b border-border px-4 py-2.5 last:border-b-0 md:grid-cols-[170px_minmax(0,1fr)]", className)}>
      <div className="text-control text-muted-foreground">
        {label}
        {hint && <small className="block text-label text-muted-foreground-soft">{hint}</small>}
      </div>
      <div className="min-w-0 text-control text-foreground">
        <div>{children}</div>
        {detail && <div className={cn("mt-0.5 text-label", detailTone ? TONE_TEXT[detailTone] : "text-muted-foreground")}>{detail}</div>}
      </div>
    </div>
  )
}

/** The warn bar: something the admin should know before anything else. */
export function WarnBar({ title, children, action, tone = "warn" }: { title: React.ReactNode; children?: React.ReactNode; action?: React.ReactNode; tone?: "warn" | "bad" }) {
  return (
    <div role="status" data-slot="warn-bar"
      className={cn("flex items-start gap-2.5 rounded-lg border px-3 py-2.5 text-label",
        tone === "warn" ? "border-warn/45 bg-warn/10" : "border-destructive/40 bg-destructive/[0.07]")}>
      <AlertTriangle className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", tone === "warn" ? "text-warn" : "text-destructive")} aria-hidden />
      <div className="min-w-0 flex-1">
        <b className={cn("font-semibold", tone === "warn" ? "text-warn" : "text-destructive")}>{title}</b>{children && <> {children}</>}
        {action && <> {action}</>}
      </div>
    </div>
  )
}

/** Shown whenever no off-site destination is verified. */
export function LocalOnlyBar({ onStorage }: { onStorage?: () => void }) {
  return (
    <WarnBar
      title="Local copy only. Losing this server is not covered."
      action={onStorage && <SmallButton onClick={onStorage}>Storage</SmallButton>}
    >
      Every backup sits on the same disk as the data it protects.
    </WarnBar>
  )
}

/** The compact button the sections use; 44px under a coarse pointer. */
export function SmallButton({ primary, danger, className, ...props }: React.ComponentProps<"button"> & { primary?: boolean; danger?: boolean; asChild?: boolean }) {
  return (
    <Button type={props.asChild ? undefined : "button"} size="sm" variant={danger ? "destructive" : primary ? "default" : "outline"}
      className={cn("h-7 px-2.5 text-xs coarse:h-[44px]", className)} {...props} />
  )
}

const AVATAR_TINT = ["var(--primary)", "var(--purple)", "var(--warn)", "var(--success)", "var(--muted-foreground)"]

/** A workspace's initial on a tinted tile; the instance has its own tint. */
export function WsAvatar({ name, instance, size = 20 }: { name: string; instance?: boolean; size?: number }) {
  const i = [...name].reduce((a, c) => a + c.charCodeAt(0), 0) % AVATAR_TINT.length
  return (
    <span aria-hidden className="grid shrink-0 place-items-center rounded-[6px] text-micro font-bold text-white"
      style={{ width: size, height: size, background: instance ? "var(--purple)" : AVATAR_TINT[i] }}>
      {(name.trim()[0] ?? "·").toUpperCase()}
    </span>
  )
}

export function WsName({ name, instance, here }: { name: string; instance?: boolean; here?: boolean }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <WsAvatar name={name} instance={instance} />
      {name}
      {here && <span className="rounded border border-dashed border-control-border px-1 font-mono text-micro text-muted-foreground-soft">here</span>}
    </span>
  )
}

/** A number field inline in a sentence ("wait up to [2 h]"). */
export function InlineInput({ className, ...props }: React.ComponentProps<"input">) {
  return (
    <input {...props}
      className={cn("mx-1 h-7 w-[5.5rem] rounded-md border border-control-border bg-surface-subtle px-2 text-control text-foreground outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 coarse:h-[44px]", className)} />
  )
}

/** A dot for a legend. */
export function Dot({ className, style }: { className?: string; style?: React.CSSProperties }) {
  return <span aria-hidden className={cn("mr-1.5 inline-block h-[7px] w-[7px] rounded-full", className)} style={style} />
}

/** Quiet: the endpoint behind this part is not on this server yet. */
export function Unavailable({ what, className }: { what?: string; className?: string }) {
  return (
    <div data-slot="backups-unavailable" className={cn("flex items-center gap-2 rounded-lg border border-dashed border-border px-4 py-3 text-label text-muted-foreground", className)}>
      <CloudOff className="h-3.5 w-3.5 shrink-0" aria-hidden />
      <span>Not available on this server yet{what ? ` · ${what}` : ""}</span>
    </div>
  )
}

/** Loading, not-yet-available and failed states for one resource. */
export function Gate<T>({ resource, what, children, skeleton = "h-[160px]" }: {
  resource: Resource<T>
  what?: string
  children: (data: T) => React.ReactNode
  skeleton?: string
}) {
  if (resource.status === "loading") return <Skeleton className={cn("rounded-card", skeleton)} />
  if (resource.status === "unavailable") return <Unavailable what={what} />
  if (resource.status === "error" || resource.data === null) {
    return (
      <p role="alert" className="rounded-lg border border-border px-4 py-3 text-label text-destructive">
        {what ? `${what} could not be read` : "Could not be read"}: {resource.error ?? "no answer"}{" "}
        <button type="button" className="underline" onClick={resource.reload}>Retry</button>
      </p>
    )
  }
  return <>{children(resource.data)}</>
}

/** A card row: text on the left, an optional chip before and action after. */
export function ItemRow({ lead, title, detail, action, className }: {
  lead?: React.ReactNode
  title: React.ReactNode
  detail?: React.ReactNode
  action?: React.ReactNode
  className?: string
}) {
  return (
    <div data-slot="item-row" className={cn("flex items-center gap-2.5 border-b border-border px-4 py-2.5 last:border-b-0", className)}>
      {lead}
      <div className="min-w-0 flex-1">
        <div className="text-control">{title}</div>
        {detail && <div className="text-label text-muted-foreground">{detail}</div>}
      </div>
      {action && <div className="flex shrink-0 items-center gap-1.5">{action}</div>}
    </div>
  )
}

/** The mono label the design uses above a block. */
export function Eyebrow({ children }: { children: React.ReactNode }) {
  return <span className=" text-micro font-medium text-muted-foreground-soft">{children}</span>
}

/** Table cell classes, so every table in the section reads alike. */
/** Backups tables are Settings tables; cells do not wrap unless a column asks. */
export const TH = settingsTh
export const TD = cn(settingsTd, "whitespace-nowrap")

export interface TableColumn<T> {
  key: string
  header: React.ReactNode
  cell: (row: T) => React.ReactNode
  /** Right-aligned figure. */
  right?: boolean
  className?: string
}

/**
 * A table that becomes stacked rows at phone width (no horizontal scroll, 44 px
 * targets): `stack` says what the stacked row shows — a title, one quiet line
 * and what sits at its end. Exactly one of the two is rendered. A row that
 * opens something takes `onOpen` (Enter and click) and keeps its own controls
 * (a tick, a button) out of that click.
 */
export function BackupsTable<T>({ rows, rowKey, columns, label, stack, onOpen, isOpen, lead, rowProps, empty }: {
  rows: T[]
  rowKey: (row: T) => string
  columns: TableColumn<T>[]
  label: string
  stack: (row: T) => { title: React.ReactNode; sub?: React.ReactNode; end?: React.ReactNode }
  onOpen?: (row: T) => void
  isOpen?: (row: T) => boolean
  /** A leading column (a tick) that stays out of the row's own click. */
  lead?: { header: React.ReactNode; cell: (row: T) => React.ReactNode }
  rowProps?: (row: T) => Record<string, string | undefined>
  empty?: React.ReactNode
}) {
  const mobile = useIsMobile()
  if (rows.length === 0) return <p className="px-4 py-8 text-center text-label text-muted-foreground">{empty ?? "Nothing to show."}</p>
  const stop = { onClick: (e: React.SyntheticEvent) => e.stopPropagation(), onKeyDown: (e: React.SyntheticEvent) => e.stopPropagation() }
  if (mobile) {
    return (
      <ul aria-label={label} className="divide-y divide-border">
        {rows.map((r) => {
          const s = stack(r)
          return (
            <li key={rowKey(r)} {...rowProps?.(r)} className="flex coarse:min-h-[44px] items-center gap-2 px-3 py-2.5">
              {lead && <span className="shrink-0" {...stop}>{lead.cell(r)}</span>}
              <div role={onOpen ? "button" : undefined} tabIndex={onOpen ? 0 : undefined}
                onClick={onOpen ? () => onOpen(r) : undefined}
                onKeyDown={onOpen ? (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onOpen(r) } } : undefined}
                className={cn("flex min-w-0 flex-1 items-center gap-2 outline-none", onOpen && "cursor-pointer")}>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-control font-medium text-foreground">{s.title}</div>
                  {s.sub && <div className="truncate text-label text-muted-foreground">{s.sub}</div>}
                </div>
                {s.end && <div className="shrink-0">{s.end}</div>}
                {onOpen && <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />}
              </div>
            </li>
          )
        })}
      </ul>
    )
  }
  return (
    <div className="overflow-x-auto">
      <table className={settingsTable} aria-label={label}>
        <thead>
          <tr>
            {lead && <th className={cn(settingsTh, "w-9")}>{lead.header}</th>}
            {columns.map((c) => <th key={c.key} className={cn(settingsTh, c.right && "text-right")}>{c.header}</th>)}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={rowKey(r)} {...rowProps?.(r)} tabIndex={onOpen ? 0 : undefined} aria-selected={onOpen && isOpen ? isOpen(r) : undefined}
              onClick={onOpen ? () => onOpen(r) : undefined}
              onKeyDown={onOpen ? (e) => { if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); onOpen(r) } } : undefined}
              className={cn(onOpen && settingsTableRowLink, "aria-selected:bg-accent")}>
              {lead && <td className={settingsTd} {...stop}>{lead.cell(r)}</td>}
              {columns.map((c) => <td key={c.key} className={cn(settingsTd, "whitespace-nowrap", c.right && "text-right", c.className)}>{c.cell(r)}</td>)}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/**
 * How far a backup was checked, as the three steps (Checksum, Contents checked,
 * Test restore): three bars and the word for the highest one reached.
 */
export function ProofSteps({ level, partial, word = true }: { level: 0 | 1 | 2 | 3; partial?: boolean; word?: boolean }) {
  const names = ["Not checked", "Checksum", "Contents checked", "Test restore"]
  return (
    <span className="inline-flex items-center gap-2" data-proof={level}>
      <span role="img" aria-label={`${names[level]}${partial ? " · partial" : ""}`} className="inline-flex gap-0.5">
        {[1, 2, 3].map((n) => (
          <i key={n} className={cn("block h-1 w-3.5 rounded-full", level >= n ? (partial ? "bg-warn" : "bg-success") : "bg-control-border")} />
        ))}
      </span>
      {word && <span className="text-muted-foreground">{level === 0 ? "—" : names[level]}</span>}
    </span>
  )
}
