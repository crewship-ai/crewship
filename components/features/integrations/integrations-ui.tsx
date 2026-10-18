"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import { ChevronLeft, ChevronRight } from "lucide-react"

import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"

/**
 * The pieces every Integrations space draws with (Harbor 2, template C):
 * the page head, the health strip, the list that is a table on a desktop and
 * stacked rows on a phone, the object page with its back link and tabs, and
 * the four-step flow on a trigger's or a channel's Overview.
 */

/** List head: the view's name as the page's h1 and one line about it. */
export function SpaceHead({ title, description, children }: { title: React.ReactNode; description?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-3.5" data-slot="space-head">
      <div className="min-w-0 flex-[1_1_220px]">
        <h1 className="text-title font-semibold tracking-[-0.015em] text-foreground">{title}</h1>
        {description && <p className="mt-0.5 text-body text-muted-foreground">{description}</p>}
      </div>
      {children}
    </div>
  )
}

export type HealthTone = "ok" | "bad" | "warn" | "muted"

export interface HealthItem {
  key: string
  tone: HealthTone
  icon: LucideIcon
  label: string
  value: React.ReactNode
  sub?: React.ReactNode
  /** Opens the view this figure counts. */
  onClick?: () => void
}

const TONE_ICON: Record<HealthTone, string> = {
  ok: "text-success",
  bad: "text-destructive",
  warn: "text-warn",
  muted: "text-muted-foreground",
}

/** Up to five figures above a list; each opens the view it counts. */
export function HealthStrip({ items, label }: { items: HealthItem[]; label: string }) {
  if (items.length === 0) return null
  return (
    <div role="group" aria-label={label} data-slot="health-strip" className="grid grid-cols-2 gap-2 md:grid-cols-3 xl:grid-cols-5">
      {items.map((h) => {
        const Icon = h.icon
        const body = (
          <>
            <Icon className={cn("size-[15px]", TONE_ICON[h.tone])} aria-hidden />
            <b className={cn("text-heading font-semibold tabular-nums", h.tone === "bad" && "text-destructive", h.tone === "warn" && "text-warn")}>{h.value}</b>
            <span className="col-span-2 min-w-0 text-label text-muted-foreground">
              {h.label}
              {h.sub && <small className="block truncate text-micro text-muted-foreground-soft">{h.sub}</small>}
            </span>
          </>
        )
        const cls = "grid grid-cols-[auto_1fr] items-center gap-x-2 gap-y-1 rounded-card border border-border bg-card px-3 py-2.5 text-left"
        return h.onClick ? (
          <button key={h.key} type="button" onClick={h.onClick} className={cn(cls, "kit-tap transition-colors hover:border-line-strong")}>
            {body}
          </button>
        ) : (
          <div key={h.key} className={cls}>
            {body}
          </div>
        )
      })}
    </div>
  )
}

export interface ListColumn {
  key: string
  label: string
  className?: string
}

export interface ListItem {
  id: string
  /** Desktop: one cell per column, in column order. */
  cells: React.ReactNode[]
  /** Phone row. */
  lead: React.ReactNode
  title: React.ReactNode
  sub?: React.ReactNode
  meta?: React.ReactNode
  state?: React.ReactNode
  /** The row's accessible name ("Open #ops-alerts"). */
  label: string
}

/** A table on a desktop and tablet, stacked rows on a phone (no sideways scroll). */
export function ResponsiveList({ columns, items, onOpen, caption }: { columns: ListColumn[]; items: ListItem[]; onOpen: (id: string) => void; caption: string }) {
  const isMobile = useIsMobile()
  if (isMobile) {
    return (
      <ul aria-label={caption} className="overflow-hidden rounded-card border border-border bg-card" data-slot="integrations-rows">
        {items.map((r) => (
          <li key={r.id} className="border-t border-border first:border-t-0">
            <button type="button" onClick={() => onOpen(r.id)} aria-label={r.label} className="kit-tap flex w-full min-w-0 items-center gap-2.5 px-3 py-2.5 text-left hover:bg-[var(--row-hover-bg)]">
              {r.lead}
              <span className="flex min-w-0 flex-1 flex-col">
                <b className="truncate text-body font-semibold text-foreground">{r.title}</b>
                {r.sub && <small className="truncate text-label text-muted-foreground">{r.sub}</small>}
                {r.meta && <span className="mt-0.5 flex min-w-0 items-center gap-1 text-micro text-muted-foreground">{r.meta}</span>}
              </span>
              {r.state}
            </button>
          </li>
        ))}
      </ul>
    )
  }
  return (
    <div className="overflow-hidden rounded-card border border-border bg-card" data-slot="integrations-table">
      <table className="w-full text-body">
        <caption className="sr-only">{caption}</caption>
        <thead>
          <tr>
            {columns.map((c) => (
              <th key={c.key} scope="col" className={cn("whitespace-nowrap border-b border-border px-3 py-2 text-left text-label font-medium text-muted-foreground", c.className)}>
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {items.map((r) => (
            <tr
              key={r.id}
              tabIndex={0}
              aria-label={r.label}
              onClick={() => onOpen(r.id)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault()
                  onOpen(r.id)
                }
              }}
              className="cursor-pointer outline-none hover:bg-[var(--row-hover-bg)] focus-visible:bg-[var(--row-hover-bg)] [&:last-child>td]:border-b-0"
            >
              {r.cells.map((cell, i) => (
                <td key={columns[i]?.key ?? i} className={cn("border-b border-border px-3 py-2 align-middle", columns[i]?.className)}>
                  {cell}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** Name + one line, the first cell of a row. */
export function CellMain({ lead, title, sub, mono = false }: { lead?: React.ReactNode; title: React.ReactNode; sub?: React.ReactNode; mono?: boolean }) {
  return (
    <span className="flex min-w-0 items-center gap-2.5">
      {lead}
      <span className="flex min-w-0 flex-col">
        <span className={cn("truncate font-medium text-foreground", mono && "font-mono text-label")}>{title}</span>
        {sub && <small className="truncate text-label text-muted-foreground">{sub}</small>}
      </span>
    </span>
  )
}

/** A 30–52 px square tile with a glyph, for things without a brand mark. */
export function GlyphTile({ icon: Icon, size = 30, className }: { icon: LucideIcon; size?: 24 | 28 | 30 | 52; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        "icon-tile inline-flex shrink-0 items-center justify-center rounded-lg",
        size === 52 ? "size-[52px] [&>svg]:size-6" : size === 30 ? "size-[30px] [&>svg]:size-4" : size === 28 ? "size-7 [&>svg]:size-4" : "size-6 [&>svg]:size-3.5",
        className,
      )}
      style={{ "--ic": "var(--primary)" } as React.CSSProperties}
    >
      <Icon />
    </span>
  )
}

export interface PageTab {
  id: string
  label: string
}

/**
 * An object's own page: "‹ {space}", a 52 px head (logo or tile, h1, one line,
 * chips), then tabs. The page's h1 is its name, so the top bar's identity is
 * not a heading here.
 */
export function ObjectPage({
  backLabel,
  onBack,
  lead,
  title,
  titleMono = false,
  description,
  chips,
  tabs,
  tab,
  onTab,
  children,
}: {
  backLabel: string
  onBack: () => void
  lead: React.ReactNode
  title: React.ReactNode
  titleMono?: boolean
  description?: React.ReactNode
  chips?: React.ReactNode
  tabs: PageTab[]
  tab: string
  onTab: (id: string) => void
  children: React.ReactNode
}) {
  return (
    <div className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-3.5" data-slot="integrations-object">
      <button type="button" onClick={onBack} className="kit-tap inline-flex items-center gap-1 justify-self-start rounded-md px-1 py-0.5 text-label font-medium text-muted-foreground hover:text-foreground">
        <ChevronLeft className="size-3.5" aria-hidden />
        {backLabel}
      </button>
      <div className="flex items-start gap-4">
        {lead}
        <div className="grid min-w-0 gap-1.5">
          <h1 className={cn("break-words text-title font-semibold tracking-[-0.02em] text-foreground", titleMono && "font-mono")}>{title}</h1>
          {description && <p className="text-body text-muted-foreground">{description}</p>}
          {chips && <div className="flex flex-wrap items-center gap-1.5">{chips}</div>}
        </div>
      </div>
      <Tabs value={tab} onValueChange={onTab}>
        <TabsList variant="line" aria-label="Sections" className="h-9 w-full justify-start gap-4 overflow-x-auto border-b border-border px-0 [scrollbar-width:none]">
          {tabs.map((t) => (
            <TabsTrigger key={t.id} value={t.id} className="flex-none px-0 text-body">
              {t.label}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>
      <div className="grid min-w-0 gap-3.5" role="tabpanel" aria-label={tabs.find((t) => t.id === tab)?.label}>
        {children}
      </div>
    </div>
  )
}

/** A quiet chip beside the status pill on an object head ("5 kinds of events"). */
export function MetaChip({ icon: Icon, children }: { icon?: LucideIcon; children: React.ReactNode }) {
  return (
    <span className="inline-flex h-6 items-center gap-1 rounded-md border border-border bg-surface-subtle px-2 text-label text-muted-foreground">
      {Icon && <Icon className="size-3.5" aria-hidden />}
      {children}
    </span>
  )
}

export interface FlowStep {
  icon: React.ReactNode
  label: string
  value: React.ReactNode
}

/** Sender → Checks → Becomes → Starts; When → Only if → Message → Sends to. */
export function FlowSteps({ steps, label }: { steps: FlowStep[]; label: string }) {
  return (
    <ol aria-label={label} className="flex flex-wrap items-stretch gap-1.5" data-slot="integrations-flow">
      {steps.map((s, i) => (
        <li key={s.label} className="flex min-w-0 flex-[1_1_160px] items-stretch gap-1.5">
          {i > 0 && <ChevronRight className="hidden size-3.5 shrink-0 self-center text-muted-foreground-soft md:block" aria-hidden />}
          <div className="grid min-w-0 flex-1 content-start gap-0.5 rounded-card border border-border bg-card px-3.5 py-3">
            <span className="flex h-6 items-center text-muted-foreground">{s.icon}</span>
            <small className="text-micro font-medium text-muted-foreground-soft">{s.label}</small>
            <b className="break-words text-body font-semibold text-foreground">{s.value}</b>
          </div>
        </li>
      ))}
    </ol>
  )
}

/** A card with a header (tile, title, right side) — the Overview tab's unit. */
export function InfoCard({ icon: Icon, title, right, className, children }: { icon?: LucideIcon; title: string; right?: React.ReactNode; className?: string; children: React.ReactNode }) {
  const id = React.useId()
  return (
    <section aria-labelledby={id} className={cn("min-w-0 overflow-hidden rounded-card border border-border bg-card", className)}>
      <header className="flex items-center gap-2.5 border-b border-border px-4 py-2.5">
        {Icon && <GlyphTile icon={Icon} size={24} />}
        <h2 id={id} className="min-w-0 flex-1 truncate text-body font-semibold text-foreground">
          {title}
        </h2>
        {right}
      </header>
      {children}
    </section>
  )
}

/** A banner above an Overview: failing, signed out, turned off for the server. */
export function Banner({ tone, title, children, action }: { tone: "bad" | "warn" | "info"; title: string; children?: React.ReactNode; action?: React.ReactNode }) {
  return (
    <div
      role={tone === "bad" ? "alert" : "status"}
      className={cn(
        "flex flex-wrap items-start gap-3 rounded-card border px-4 py-3 text-body",
        tone === "bad" ? "border-destructive/30 bg-chip-danger-bg text-chip-danger-fg" : tone === "warn" ? "border-warn/30 bg-chip-warn-bg text-chip-warn-fg" : "border-border bg-chip-info-bg text-chip-info-fg",
      )}
    >
      <div className="min-w-0 flex-1">
        <b className="font-semibold">{title}</b>
        {children && <div className="mt-0.5">{children}</div>}
      </div>
      {action}
    </div>
  )
}

/** "CLI" card rows: a command, a copy button, what it does. */
export function CliLines({ lines }: { lines: [string, string][] }) {
  return (
    <ol className="grid gap-2 px-4 py-3">
      {lines.map(([cmd, what]) => (
        <li key={cmd} className="grid gap-0.5">
          <code className="break-all font-mono text-label text-foreground">{cmd}</code>
          <small className="text-label text-muted-foreground">{what}</small>
        </li>
      ))}
    </ol>
  )
}
