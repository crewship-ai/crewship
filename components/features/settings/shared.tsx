"use client"

import { useId, type CSSProperties, type ReactNode } from "react"
import Link from "next/link"
import { Lock, type LucideIcon } from "lucide-react"
import { cn } from "@/lib/utils"
import { PageSaveLabel, toastSaveError, usePageSave } from "@/components/ui/page-save-bar"

/**
 * Shared settings card shell (Harbor).
 *
 * One card per section (Harbor 2 card anatomy): a 50 px header row — a 32 px
 * icon tile, the title and a one-line subtitle, actions on the right — the
 * rows under a hairline, and an optional footer strip for a note or a
 * secondary action. rounded-card (10 px), 1 px border, no resting shadow.
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
// The control token, not the spacing scale: --spacing is 0.23rem, so h-8 is
// ~27px at the Small text size, where a settings field is 32 (--control-md).
// coarse:h-[44px] — a finger needs 44px; desktop density is untouched.
export const controlHeight = "h-[var(--control-md)] coarse:h-[44px]"
export const settingsControl = cn(controlHeight, "w-full sm:w-64")
/**
 * A control inside a table or list row (a role picker, a model picker): one
 * step smaller so the row keeps its rhythm, still a full finger on touch.
 * Outside a SettingsRow, a form uses `controlHeight` and the primitive's
 * text-control size; a dialog keeps the primitive's default.
 */
export const inlineControl = "h-[var(--control-sm)] text-xs coarse:h-[44px]"
/**
 * The one table style for Settings and Admin: a 12px sentence-case header in
 * the muted ink, 13px cells, 16px side padding, hairlines between rows and
 * none under the last. A row that opens something adds `settingsTableRowLink`.
 * Status in a cell is a StatusPill; machine text is `type-page-stamp`-sized mono.
 */
export const settingsTable = "w-full text-control tabular-nums [&_tbody_tr:last-child>td]:border-b-0"
export const settingsTh = "whitespace-nowrap border-b border-border px-4 py-2 text-left text-label font-medium text-muted-foreground"
export const settingsTd = "border-b border-border px-4 py-2.5 align-middle"
export const settingsTableRowLink = "cursor-pointer outline-none hover:bg-[var(--row-hover-bg)] focus-visible:bg-[var(--row-hover-bg)]"

/** A native <select> dressed as SelectTrigger, at control height. */
export const nativeSelect = cn(
  controlHeight,
  "rounded-md border border-control-border bg-surface-subtle px-2.5 text-control text-foreground outline-none hover:border-line-strong focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-50",
)

/** A custom picker button (popover combobox) dressed as SelectTrigger. */
export const settingsPickerButton = cn(
  settingsControl,
  "inline-flex items-center justify-between gap-2 rounded-md border border-control-border bg-surface-subtle px-3 text-control text-foreground outline-none transition-colors hover:border-line-strong focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-50",
)

/**
 * The DOM id a settings card answers to, from its title: "Danger zone" →
 * `settings-card-danger-zone`. The ⌘K palette links to a card with
 * `?card=<slug>` and the settings layout scrolls to this id (#3045).
 */
export function settingsCardSlug(title: string): string {
  return title.toLowerCase().replace(/&/g, "and").replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "")
}

export function settingsCardId(title: string): string {
  return `settings-card-${settingsCardSlug(title)}`
}

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
  footer,
  hideHeader = false,
}: {
  title: string
  description?: string
  actions?: ReactNode
  children: ReactNode
  /** The strip under the rows: a note on the left, an action on the right. */
  footer?: ReactNode
  /** A card the section head already names (Notification sounds): the title
   *  stays only as the region's accessible name. */
  hideHeader?: boolean
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
      id={settingsCardId(title)}
      data-slot="settings-card"
      aria-labelledby={titleId}
      className={cn("scroll-mt-4 overflow-hidden rounded-card border border-border bg-card transition-[outline-color] duration-300 data-[focused=true]:outline-2 data-[focused=true]:outline-offset-2 data-[focused=true]:outline-solid data-[focused=true]:outline-ring", className)}
    >
      {hideHeader ? (
        <h3 id={titleId} className="sr-only">{title}</h3>
      ) : (
        <SettingsCardHeader title={title} titleId={titleId} description={description} actions={actions} icon={icon} tint={tint} />
      )}
      <PageSaveLabel label={title}>
        <div className={cn(padded && "p-4", bodyClassName)}>{children}</div>
      </PageSaveLabel>
      {footer != null && <SettingsCardFooter>{footer}</SettingsCardFooter>}
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
    // The v67 card header (`.scard header`: 14 px padding around a 32 px tile,
    // a 13 px semibold title over a 12 px subtitle, 68 px with both) at the
    // Small text size (a 14.857px root; it scales with Standard and Large).
    // The height is the padding around the content, not a floor: a header with
    // no subtitle is the 60 px v67 draws, and a long description may grow.
    <div data-slot="settings-card-header" className="flex items-center gap-3 border-b border-border px-4 py-4">
      {Icon && (
        <span
          className="icon-tile inline-flex size-[var(--control-md)] shrink-0 items-center justify-center rounded-lg"
          style={{ "--ic": danger ? "var(--destructive)" : (tint ?? "var(--primary)") } as CSSProperties}
          aria-hidden
        >
          <Icon className="h-4 w-4" />
        </span>
      )}
      <div className="min-w-0 flex-1">
        <h3 id={titleId} className={cn("text-control font-semibold leading-5 tracking-[-0.01em]", danger ? "text-destructive" : "text-foreground")}>
          {title}
        </h3>
        {description && (
          <p className="mt-0.5 text-label leading-snug text-muted-foreground">{description}</p>
        )}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-1.5">{actions}</div>}
    </div>
  )
}

/**
 * Single row inside a SettingsCard (Harbor 2 row anatomy): label + optional
 * hint on the left, the control on the right. The label is text-control at
 * 500, the size of the field beside it; the hint is text-label in the muted
 * ink, the same as the card header's — a supporting line is 12px, never fine
 * print (docs/ux/README.md › Type).
 *
 * `lead` puts a 28 px grey icon tile before the label (sounds, devices);
 * `lock` says who may change the value ("Owner", "Owners and admins") with a
 * lock beside the label, before anyone tries.
 */
export function SettingsRow({
  label,
  description,
  children,
  border = true,
  className,
  lead,
  lock,
  lockTitle,
  "data-testid": testId,
}: {
  label: ReactNode
  description?: ReactNode
  children: ReactNode
  border?: boolean
  className?: string
  "data-testid"?: string
  /** Icon for the small grey tile before the label. */
  lead?: LucideIcon
  /** Who may change this row: a lock and this word beside the label. */
  lock?: string
  /** The longer reason, on the lock's tooltip. */
  lockTitle?: string
}) {
  return (
    <div
      data-slot="settings-row"
      data-testid={testId}
      className={cn(
        // Wraps rather than squeezes. The label side is at least 11rem; when
        // it and the control do not fit side by side (a phone), the control
        // drops under the label instead of pushing out of the card or
        // clipping a sentence-long hint mid-word.
        "flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3",
        border && "border-b border-border last:border-b-0",
        className,
      )}
    >
      {lead && <SettingsRowIcon icon={lead} />}
      <div className="min-w-0 flex-[1_1_11rem]">
        <div className="text-control font-medium text-foreground">
          {label}
          {lock && <SettingsLock label={lock} title={lockTitle} />}
        </div>
        {description && (
          <div className="text-label text-muted-foreground mt-0.5 leading-snug">{description}</div>
        )}
      </div>
      <div className="ml-auto flex max-w-full flex-wrap items-center justify-end gap-2">{children}</div>
    </div>
  )
}

/** The 28 px grey tile a row's lead icon sits in. */
export function SettingsRowIcon({ icon: Icon }: { icon: LucideIcon }) {
  return (
    <span aria-hidden data-slot="settings-row-icon" className="grid h-7 w-7 shrink-0 place-items-center rounded-lg border border-border bg-surface-subtle text-muted-foreground">
      <Icon className="h-3.5 w-3.5" />
    </span>
  )
}

/** "🔒 Owner" beside a label: who may change the value. */
export function SettingsLock({ label, title }: { label: string; title?: string }) {
  return (
    <span
      data-slot="settings-lock"
      title={title ?? `Only ${label.toLowerCase()} can change this`}
      className="ml-1.5 inline-flex items-center gap-0.5 align-middle text-micro font-medium text-muted-foreground"
    >
      <Lock className="h-2.5 w-2.5" aria-hidden />
      {label}
    </span>
  )
}

/** The strip under a card's rows: a note on the left, an action on the right. */
export function SettingsCardFooter({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      data-slot="settings-card-footer"
      className={cn("flex flex-wrap items-center justify-between gap-2.5 border-t border-border bg-surface-subtle px-4 py-2.5 text-label text-muted-foreground", className)}
    >
      {children}
    </div>
  )
}

/** A line of actions under a list in a card ("Sign out everywhere else · Show all"). */
export function SettingsCardActions({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div data-slot="settings-card-actions" className={cn("flex flex-wrap items-center gap-3 px-4 pb-3 pt-2", className)}>
      {children}
    </div>
  )
}

export interface SettingsCount {
  icon: LucideIcon
  value: ReactNode
  label: string
  /** Makes the tile a link (Classification → Vault). */
  href?: string
}

/**
 * Figures side by side in a card (Usage, Classification): an icon and the
 * number on one line, the label under it. A tile with `href` is a link. Three
 * columns from `sm`, one on a phone.
 */
export function SettingsCounts({ items, className }: { items: SettingsCount[]; className?: string }) {
  const tile = "grid grid-cols-[auto_1fr] items-center gap-x-2 rounded-card border border-border bg-card px-3 py-2.5 text-left"
  return (
    <div data-slot="settings-counts" className={cn("grid grid-cols-1 gap-2.5 px-4 py-3 sm:grid-cols-3", className)}>
      {items.map(({ icon: Icon, value, label, href }) => {
        const body = (
          <>
            <Icon className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
            <b className="text-default font-semibold tabular-nums text-foreground">{value}</b>
            <span className="col-span-2 text-label text-muted-foreground">{label}</span>
          </>
        )
        return href ? (
          <Link
            key={label}
            href={href}
            className={cn(tile, "transition-colors hover:border-line-strong focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50")}
          >
            {body}
          </Link>
        ) : (
          <div key={label} className={tile}>{body}</div>
        )
      })}
    </div>
  )
}

/**
 * The head of a Settings section or nested page: a 40 px tile in the accent,
 * the title (text-title, 21 px) and one sentence about what the section is for.
 */
export function SettingsPageHead({ icon: Icon, title, description, actions, className, level = 2 }: {
  icon: LucideIcon
  title: string
  description?: ReactNode
  actions?: ReactNode
  className?: string
  /** 2 under a sub-bar, which already carries the page h1 ("Settings › …");
   *  1 on a page without one (restricted Account security). */
  level?: 1 | 2
}) {
  const Heading = level === 1 ? "h1" : "h2"
  return (
    <div data-slot="settings-page-head" className={cn("flex flex-wrap items-center gap-3.5", className)}>
      <span className="page-tile page-tile-40" aria-hidden><Icon /></span>
      <div className="min-w-0 flex-[1_1_14rem]">
        <Heading className="text-title font-semibold leading-tight tracking-tight text-foreground">{title}</Heading>
        {description && <p className="mt-0.5 text-body text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
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
    <div className="px-4 py-6 text-center text-label text-muted-foreground">
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
      id={settingsCardId(title)}
      data-slot="settings-card"
      aria-labelledby={titleId}
      className="scroll-mt-4 overflow-hidden rounded-card border border-destructive/30 bg-card transition-[outline-color] duration-300 data-[focused=true]:outline-2 data-[focused=true]:outline-offset-2 data-[focused=true]:outline-solid data-[focused=true]:outline-destructive"
    >
      <SettingsCardHeader title={title} titleId={titleId} description={description} actions={actions} icon={icon} danger />
      <PageSaveLabel label={title}>{children}</PageSaveLabel>
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

/** One statement in a SettingsSummary; `n` is a tabular sans figure. */
export function SummaryItem({ n, children, tone }: { n?: ReactNode; children?: ReactNode; tone?: keyof typeof SUMMARY_DOT }) {
  return (
    <span className="inline-flex items-center">
      {tone && <span className={cn("mr-1.5 h-1.5 w-1.5 rounded-full", SUMMARY_DOT[tone])} aria-hidden />}
      {n != null && <span className="tabular-nums text-foreground">{n}</span>}
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
          // v67 `.seg button`: 28 px high (--control-sm) with a 13 px label.
          className={cn("h-[var(--control-sm)] rounded-md px-2.5 text-control transition-colors disabled:opacity-50",
            value === o.value ? "bg-card font-medium text-foreground shadow-[0_0_0_1px_var(--border)]" : "text-muted-foreground hover:text-foreground")}>
          {o.label}
        </button>
      ))}
    </div>
  )
}

/**
 * The floating bar over ticked rows ("3 selected · Add to workspace …"), in
 * the save bar's green (--save-bar-*): like unsaved edits, a selection is
 * work in progress that must not be overlooked at the bottom of the page.
 * A steady glow, not the save bar's pulse: nothing is lost if it is left.
 */
export const selectionBar = cn(
  "z-40 flex items-center gap-2 rounded-xl border border-[var(--save-bar-border)] bg-[var(--save-bar-bg)] px-3 py-2 text-xs text-[var(--save-bar-fg)]",
  "shadow-[0_0_0_1px_color-mix(in_oklab,var(--save-bar-fg)_45%,transparent),0_0_18px_color-mix(in_oklab,var(--save-bar-fg)_20%,transparent),0_16px_40px_rgb(0_0_0/0.45)]",
)
/** A secondary action on the selection bar: outlined in its green. */
export const selectionBarButton = "h-8 gap-1 border-[var(--save-bar-border)] bg-transparent px-3 text-xs text-[var(--save-bar-fg)] hover:bg-[color-mix(in_oklab,var(--save-bar-fg)_10%,transparent)] hover:text-[var(--save-bar-fg)] dark:bg-transparent dark:hover:bg-[color-mix(in_oklab,var(--save-bar-fg)_10%,transparent)]"
/** The selection bar's main action: filled, like the save bar's Save. */
export const selectionBarAction = "h-8 gap-1 bg-[var(--save-bar-action)] px-3 text-xs font-semibold text-[var(--save-bar-action-fg)] hover:bg-[var(--save-bar-action)] hover:brightness-110"

/**
 * One Save for a page of fields. Inside a page with a PageSaveProvider
 * (Settings, Admin, nested pages) it hands its edits to the page's floating
 * bar and draws nothing; on its own it draws the bar itself.
 */
export function SettingsSaveBar({ count, onSave, onDiscard, saving = false, canSave = true, label = "changes" }: {
  count: number; onSave: () => void | Promise<unknown>; onDiscard: () => void; saving?: boolean; canSave?: boolean
  /** What the error toast calls these edits: "Couldn't save Limits". */
  label?: string
}) {
  const inPage = usePageSave({ label, count, saving, canSave, save: onSave, discard: onDiscard })
  if (inPage || count === 0) return null
  return (
    // Green like the page's own save bar (--save-bar-*), which this stands in for.
    <div role="region" aria-label="Unsaved changes" data-slot="settings-save-bar"
      className="save-bar-glow sticky bottom-4 z-10 mx-auto flex w-fit items-center gap-3 rounded-xl border border-[var(--save-bar-border)] bg-[var(--save-bar-bg)] py-1.5 pl-4 pr-1.5 text-xs font-medium text-[var(--save-bar-fg)]">
      <span><span className="tabular-nums">{count}</span> unsaved change{count === 1 ? "" : "s"}</span>
      <button type="button" onClick={onDiscard} disabled={saving}
        className="h-7 rounded-md px-2.5 opacity-85 hover:bg-[color-mix(in_oklab,var(--save-bar-fg)_10%,transparent)] hover:opacity-100 disabled:opacity-50">Discard</button>
      <button type="button" disabled={saving || !canSave}
        onClick={() => { Promise.resolve().then(onSave).catch((e) => toastSaveError(label, e instanceof Error ? e.message : null)) }}
        className="h-7 rounded-md bg-[var(--save-bar-action)] px-3 font-semibold text-[var(--save-bar-action-fg)] hover:brightness-110 disabled:opacity-50">Save</button>
    </div>
  )
}

/** The first sentence of a server-written description; the rest belongs in a tooltip. */
export function firstSentence(text: string): string {
  const m = /^(.+?[.!?])(\s|$)/.exec(text.trim())
  return m ? m[1] : text.trim()
}

/**
 * v67 dates: day, short month, and the year ("12 Oct 2026"), or with
 * `year: "if-not-current"` only when it is not this year ("3 Mar"). Always English
 * month names in the day-first order the wireframe uses, whatever the browser
 * locale (a locale that writes "Oct 12" or "12. 10." is how two pages ended up
 * with two formats).
 */
export function formatSettingsDate(iso: string | Date, opts: { year?: boolean | "if-not-current" } = {}): string {
  const d = iso instanceof Date ? iso : new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  const month = d.toLocaleDateString("en-US", { month: "short" })
  const withYear = opts.year === "if-not-current" ? d.getFullYear() !== new Date().getFullYear() : opts.year !== false
  return `${d.getDate()} ${month}${withYear ? ` ${d.getFullYear()}` : ""}`
}

export interface SettingsKpi {
  icon: LucideIcon
  label: string
  value: ReactNode
  /** Makes the tile a button (a figure that filters the page). */
  onClick?: () => void
  pressed?: boolean
}

/**
 * The strip of figures at the head of a page (v67 `.s-strip`): a bordered tile
 * for each — a 30 px grey icon box, the label in small grey over the figure in
 * a tabular sans. A tile with `onClick` is a button. They share the row, and
 * wrap on a phone.
 */
export function SettingsKpiStrip({ items, className, slot = "settings-kpis" }: { items: SettingsKpi[]; className?: string; slot?: string }) {
  const tile = "flex min-w-0 flex-[1_1_9.4rem] items-center gap-2.5 rounded-card border border-border bg-card px-3 py-2.5 text-left"
  return (
    <div data-slot={slot} className={cn("flex flex-wrap gap-2", className)}>
      {items.map(({ icon: Icon, label, value, onClick, pressed }) => {
        const body = (
          <>
            <span aria-hidden className="grid size-[2.0192rem] shrink-0 place-items-center rounded-lg bg-surface-subtle text-muted-foreground">
              <Icon className="h-4 w-4" />
            </span>
            <span className="min-w-0">
              <small className="block truncate text-micro text-muted-foreground">{label}</small>
              <b className="block text-default font-semibold leading-tight tabular-nums text-foreground">{value}</b>
            </span>
          </>
        )
        return onClick ? (
          <button key={label} type="button" data-slot="settings-kpi" aria-pressed={pressed} onClick={onClick}
            className={cn(tile, "transition-colors hover:border-line-strong focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50", pressed && "border-primary")}>
            {body}
          </button>
        ) : (
          <div key={label} data-slot="settings-kpi" className={tile}>{body}</div>
        )
      })}
    </div>
  )
}
