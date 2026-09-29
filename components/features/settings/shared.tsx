"use client"

import { useId, type CSSProperties, type ReactNode } from "react"
import type { LucideIcon } from "lucide-react"
import { cn } from "@/lib/utils"

/**
 * Shared settings card shell (Harbor).
 *
 * One card per section: a header row inside the card — an optional icon
 * tile, the title and a one-line description, actions on the right — and the
 * body under a hairline. 20px radius, 1px border, no resting shadow.
 *
 * ```tsx
 * <SettingsCard icon={User} title="Account" description="Your identity on this instance">
 *   <SettingsRow label="Email">{email}</SettingsRow>
 *   <SettingsRow label="Full name">{name}</SettingsRow>
 * </SettingsCard>
 * ```
 *
 * Use `padded` for free-form content that doesn't use the row layout
 * (e.g. dropdowns, large forms). Default is zero-padded so SettingsRow
 * handles its own px/py.
 */
/**
 * The size of a form control in a SettingsRow: one height and width for every
 * input, select and picker on the right of a row, so the column lines up and
 * text size comes from the control primitives (text-control).
 */
export const settingsControl = "h-8 w-full sm:w-64"

/** A custom picker button (popover combobox) dressed as SelectTrigger. */
export const settingsPickerButton = cn(
  settingsControl,
  "inline-flex items-center justify-between gap-2 rounded-md border border-control-border bg-surface-subtle px-3 text-control text-foreground outline-none transition-colors hover:border-line-strong focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-50",
)

export function SettingsCard({
  title,
  description,
  actions,
  children,
  className,
  padded = false,
  icon,
  tint,
  bodyClassName,
}: {
  title: string
  description?: string
  actions?: ReactNode
  children: ReactNode
  className?: string
  /** Classes for the body under the header (e.g. flex-1 in a stretched card). */
  bodyClassName?: string
  padded?: boolean
  /** Glyph for the header's icon tile. */
  icon?: LucideIcon
  /** Tile tint, a CSS colour; brand blue by default, var(--purple) for system items. */
  tint?: string
}) {
  // The card is a named region: screen readers list it by its title.
  const titleId = useId()
  return (
    <section
      data-slot="settings-card"
      aria-labelledby={titleId}
      className={cn("overflow-hidden rounded-card border border-border bg-card", className)}
    >
      <SettingsCardHeader title={title} titleId={titleId} description={description} actions={actions} icon={icon} tint={tint} />
      <div className={cn(padded && "p-4", bodyClassName)}>{children}</div>
    </section>
  )
}

function SettingsCardHeader({
  title,
  titleId,
  description,
  actions,
  icon: Icon,
  tint,
  danger = false,
}: {
  title: string
  titleId?: string
  description?: string
  actions?: ReactNode
  icon?: LucideIcon
  tint?: string
  danger?: boolean
}) {
  return (
    <div className="flex items-center gap-3 border-b border-border px-4 py-3">
      {Icon && (
        <span
          className="icon-tile inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg"
          style={{ "--ic": danger ? "var(--destructive)" : (tint ?? "var(--primary)") } as CSSProperties}
          aria-hidden
        >
          <Icon className="h-4 w-4" />
        </span>
      )}
      <div className="min-w-0 flex-1">
        <h3 id={titleId} className={cn("text-sm font-semibold leading-5 tracking-[-0.01em]", danger ? "text-destructive" : "text-foreground")}>
          {title}
        </h3>
        {description && (
          <p className="mt-0.5 text-[12px] leading-snug text-muted-foreground">{description}</p>
        )}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-1.5">{actions}</div>}
    </div>
  )
}

/**
 * Single row inside a SettingsCard: label + optional description on the
 * left, right-aligned content on the right. Uses text-xs for the label
 * and text-[11px] for the description so rows match the orchestration
 * row aesthetic.
 */
export function SettingsRow({
  label,
  description,
  children,
  border = true,
  className,
}: {
  label: ReactNode
  description?: ReactNode
  children: ReactNode
  border?: boolean
  className?: string
}) {
  return (
    <div
      className={cn(
        "flex items-center justify-between gap-4 px-4 py-2.5",
        border && "border-b border-border last:border-b-0",
        className,
      )}
    >
      {/* The label side flexes and may shrink; the control side never does.
          It used to be the other way round, which meant a row with a long
          description pushed its own control out of the card — the description
          clipped mid-word and the input sat on top of the text. A description
          is allowed to be a sentence, so the layout has to absorb one. */}
      <div className="min-w-0 flex-1">
        <div className="text-[13px] text-foreground">{label}</div>
        {description && (
          <div className="text-[11px] text-muted-foreground-soft mt-0.5 leading-snug">{description}</div>
        )}
      </div>
      <div className="flex items-center gap-2 shrink-0 justify-end">{children}</div>
    </div>
  )
}

/** Empty state row used when a list in a settings card has no items. */
export function SettingsEmpty({
  children,
}: {
  children: ReactNode
}) {
  return (
    <div className="px-4 py-6 text-center text-[11px] text-muted-foreground">
      {children}
    </div>
  )
}

/** Destructive variant of SettingsCard for "Danger zone" sections. */
export function SettingsDangerCard({
  title,
  description,
  actions,
  children,
  icon,
}: {
  title: string
  description?: string
  actions?: ReactNode
  children: ReactNode
  icon?: LucideIcon
}) {
  // Named like SettingsCard, so "Danger zone" is a region a reader can jump to.
  const titleId = useId()
  return (
    <section
      data-slot="settings-card"
      aria-labelledby={titleId}
      className="overflow-hidden rounded-card border border-destructive/30 bg-card"
    >
      <SettingsCardHeader title={title} titleId={titleId} description={description} actions={actions} icon={icon} danger />
      {children}
    </section>
  )
}

/**
 * The line above a page's cards that says where things stand, in words:
 * "17 limits in 7 areas · 1 changed from default". It replaces a row of KPI
 * tiles — Crew links set the pattern. A figure only gets a coloured dot when
 * it asks for attention.
 */
export function SettingsSummary({ children, className, slot = "settings-summary" }: { children: ReactNode; className?: string; slot?: string }) {
  return (
    <div data-slot={slot} className={cn("flex min-h-7 flex-wrap items-center gap-x-5 gap-y-1.5 text-xs text-muted-foreground", className)}>
      {children}
    </div>
  )
}

const SUMMARY_DOT = { success: "bg-success", warn: "bg-warn", danger: "bg-destructive", info: "bg-primary" } as const

/** One statement in a SettingsSummary; `n` is set in the mono figure face. */
export function SummaryItem({ n, children, tone }: { n?: ReactNode; children?: ReactNode; tone?: keyof typeof SUMMARY_DOT }) {
  return (
    <span className="inline-flex items-center">
      {tone && <span className={cn("mr-1.5 h-1.5 w-1.5 rounded-full", SUMMARY_DOT[tone])} aria-hidden />}
      {n != null && <span className="font-mono tabular-nums text-foreground">{n}</span>}
      {n != null && children != null && "\u00a0"}
      {children}
    </span>
  )
}

/** A small segmented control for a SettingsRow or a summary line. */
export function SettingsSegmented<T extends string | number>({ label, options, value, onChange, disabled }: {
  label: string; options: { value: T; label: string }[]; value: T; onChange: (v: T) => void; disabled?: boolean
}) {
  return (
    <div className="inline-flex flex-wrap gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={String(o.value)} type="button" aria-pressed={value === o.value} disabled={disabled} onClick={() => onChange(o.value)}
          className={cn("h-7 rounded-md px-2.5 text-xs transition-colors disabled:opacity-50",
            value === o.value ? "bg-card font-medium text-foreground shadow-[0_0_0_1px_var(--border)]" : "text-muted-foreground hover:text-foreground")}>
          {o.label}
        </button>
      ))}
    </div>
  )
}

/**
 * One Save for a page of fields: it appears, pinned to the bottom of the
 * scrolling pane, once something differs from what the server holds.
 */
export function SettingsSaveBar({ count, onSave, onDiscard, saving = false, canSave = true }: {
  count: number; onSave: () => void; onDiscard: () => void; saving?: boolean; canSave?: boolean
}) {
  if (count === 0) return null
  return (
    <div role="region" aria-label="Unsaved changes" data-slot="settings-save-bar"
      className="sticky bottom-4 z-10 mx-auto flex w-fit items-center gap-3 rounded-xl border border-border bg-card py-1.5 pl-4 pr-1.5 text-xs shadow-lg">
      <span><span className="font-mono tabular-nums">{count}</span> unsaved change{count === 1 ? "" : "s"}</span>
      <button type="button" onClick={onDiscard} disabled={saving}
        className="h-7 rounded-md px-2.5 text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-50">Discard</button>
      <button type="button" onClick={onSave} disabled={saving || !canSave}
        className="h-7 rounded-md bg-primary px-3 font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50">Save</button>
    </div>
  )
}

/** The first sentence of a server-written description; the rest belongs in a tooltip. */
export function firstSentence(text: string): string {
  const m = /^(.+?[.!?])(\s|$)/.exec(text.trim())
  return m ? m[1] : text.trim()
}
