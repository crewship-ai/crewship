"use client"

import * as React from "react"
import { AlertTriangle, CloudOff } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusPill } from "@/components/ui/status-pill"
import type { StatusTone } from "@/lib/format-status"
import type { Tone } from "./backups-model"
import type { Resource } from "./use-backups-data"

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
      <div className="text-[13px] text-muted-foreground">
        {label}
        {hint && <small className="block text-[12px] text-muted-foreground-soft">{hint}</small>}
      </div>
      <div className="min-w-0 text-[13px] text-foreground">
        <div>{children}</div>
        {detail && <div className={cn("mt-0.5 text-[12.5px]", detailTone ? TONE_TEXT[detailTone] : "text-muted-foreground")}>{detail}</div>}
      </div>
    </div>
  )
}

/** The warn bar: something the admin should know before anything else. */
export function WarnBar({ title, children, action, tone = "warn" }: { title: React.ReactNode; children?: React.ReactNode; action?: React.ReactNode; tone?: "warn" | "bad" }) {
  return (
    <div role="status" data-slot="warn-bar"
      className={cn("flex items-start gap-2.5 rounded-lg border px-3 py-2.5 text-[12.5px]",
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
      className={cn("h-7 px-2.5 text-xs coarse:h-[2.75rem]", className)} {...props} />
  )
}

const AVATAR_TINT = ["var(--primary)", "var(--purple)", "var(--warn)", "var(--success)", "var(--muted-foreground)"]

/** A workspace's initial on a tinted tile; the instance has its own tint. */
export function WsAvatar({ name, instance, size = 20 }: { name: string; instance?: boolean; size?: number }) {
  const i = [...name].reduce((a, c) => a + c.charCodeAt(0), 0) % AVATAR_TINT.length
  return (
    <span aria-hidden className="grid shrink-0 place-items-center rounded-[6px] text-[10px] font-bold text-white"
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
      {here && <span className="rounded border border-dashed border-control-border px-1 font-mono text-[9.5px] text-muted-foreground-soft">here</span>}
    </span>
  )
}

/** A number field inline in a sentence ("wait up to [2 h]"). */
export function InlineInput({ className, ...props }: React.ComponentProps<"input">) {
  return (
    <input {...props}
      className={cn("mx-1 h-7 w-[5.5rem] rounded-md border border-control-border bg-surface-subtle px-2 text-[13px] text-foreground outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 coarse:h-[2.75rem]", className)} />
  )
}

/** A dot for a legend. */
export function Dot({ className, style }: { className?: string; style?: React.CSSProperties }) {
  return <span aria-hidden className={cn("mr-1.5 inline-block h-[7px] w-[7px] rounded-full", className)} style={style} />
}

/** Quiet: the endpoint behind this part is not on this server yet. */
export function Unavailable({ what, className }: { what?: string; className?: string }) {
  return (
    <div data-slot="backups-unavailable" className={cn("flex items-center gap-2 rounded-lg border border-dashed border-border px-4 py-3 text-[12.5px] text-muted-foreground", className)}>
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
      <p role="alert" className="rounded-lg border border-border px-4 py-3 text-[12.5px] text-destructive">
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
        <div className="text-[13.5px]">{title}</div>
        {detail && <div className="text-[12.5px] text-muted-foreground">{detail}</div>}
      </div>
      {action && <div className="flex shrink-0 items-center gap-1.5">{action}</div>}
    </div>
  )
}

/** The mono label the design uses above a block. */
export function Eyebrow({ children }: { children: React.ReactNode }) {
  return <span className="font-mono text-[11px] font-medium uppercase tracking-[0.08em] text-muted-foreground-soft">{children}</span>
}

/** Table cell classes, so every table in the section reads alike. */
export const TH = "whitespace-nowrap border-b border-border px-3.5 py-2 text-left font-mono text-[11px] font-medium uppercase tracking-[0.06em] text-muted-foreground-soft"
export const TD = "whitespace-nowrap border-b border-border px-3.5 py-2 text-[13px]"
