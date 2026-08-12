"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { SectionCard } from "@/components/ui/section-card"
import {
  EM_DASH,
  formatAbsoluteAge,
  formatInstant,
  panelStateWord,
  provenanceProducedAt,
  provenanceRunId,
  toDate,
  type ValueBasis,
} from "./freshness"
import type { PanelProps } from "./types"

/**
 * The panel shell. One card idiom for every schema, copied from the Routines
 * overview (§9b.2): small icon + 11px uppercase tracking-wider label on the
 * left, right-aligned muted status word on the right — and the right-hand word
 * is always the *answer* ("current", "stale", "no data yet"), never a repeat of
 * the label.
 *
 * The shell is `SectionCard`, documented as the canonical container for
 * grouped content inside a page. Pages does not invent a panel shell.
 */
export interface PanelFrameProps extends PanelProps {
  icon: LucideIcon
  children: React.ReactNode
}

export function PanelFrame({
  panel,
  data,
  now,
  publicView = false,
  className,
  icon: Icon,
  children,
}: PanelFrameProps) {
  const label = panel.title?.trim() || panel.id
  return (
    <SectionCard
      data-slot="panel"
      data-panel-id={panel.id}
      data-panel-schema={panel.schema}
      data-panel-state={data.state}
      className={cn("gap-3 py-4", className)}
      title={
        <span
          data-slot="panel-label"
          className="inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-foreground/70"
        >
          <Icon className="h-3.5 w-3.5 text-muted-foreground-soft" aria-hidden="true" />
          <span>{label}</span>
        </span>
      }
      actions={
        <span data-slot="panel-status-word" className="text-[11px] text-muted-foreground">
          {panelStateWord(data.state)}
        </span>
      }
    >
      <div className="flex flex-col gap-3">
        {children}
        <PanelProvenance data={data} now={now} publicView={publicView} />
      </div>
    </SectionCard>
  )
}

/**
 * The value region. `data-basis` records the §9b.4 distinction in the DOM:
 * `measured` is a number we looked up (including `0`), `none` is the em dash.
 *
 * `dimmed` is the whole of the stale treatment — the value never renders as if
 * it were current, and the age rides beside it.
 */
export function PanelValue({
  basis,
  dimmed = false,
  tone = "normal",
  className,
  children,
}: {
  basis: ValueBasis
  dimmed?: boolean
  tone?: "normal" | "destructive" | "muted"
  className?: string
  children: React.ReactNode
}) {
  return (
    <div
      data-slot="panel-value"
      data-basis={basis}
      className={cn(
        tone === "destructive" && "text-destructive",
        tone === "muted" && "text-muted-foreground-soft",
        dimmed && "opacity-60",
        className,
      )}
    >
      {children}
    </div>
  )
}

/**
 * `—` in the destructive tone, plus the failure — but the reason is internal
 * vocabulary (container names, routine slugs, OOM traces), so a public view
 * gets the glyph and the age and nothing else (§7.3.2b).
 *
 * The age rides here rather than only on a stale panel: §7.3.2b is "show the
 * age, hide the reason", and *"a public panel always carries when its data was
 * produced"*. A failed panel is exactly the one an outsider must not read as
 * current, and the provenance footer's bare timestamp does not say how old.
 */
export function FailedValue({
  failure,
  publicView,
  producedAt,
  now,
}: {
  failure?: string | null
  publicView: boolean
  producedAt?: string | Date | null
  now?: Date
}) {
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <PanelValue basis="none" tone="destructive" className="text-display font-semibold leading-none">
          {EM_DASH}
        </PanelValue>
        {now ? <PanelAge producedAt={producedAt} now={now} /> : null}
      </div>
      {publicView ? (
        <p className="text-body text-muted-foreground">Data are not current.</p>
      ) : (
        <p className="text-body text-destructive/90">
          {failure?.trim() || "The producer's last run failed."}
        </p>
      )}
    </div>
  )
}

/**
 * `—` plus the sentence that names the next action (§9b.3), dimmed.
 *
 * The dimming is §10b.1: a panel restored by a rollback *"renders dimmed, in a
 * 'waiting for first data' state"*, and this is that state. A rollback is
 * exactly when someone is most likely to believe what they see, so the
 * waiting-for-data panel must not sit at the same contrast as a measured one.
 */
export function NeverProducedValue({ hint }: { hint: string }) {
  return (
    <div className="flex flex-col gap-1">
      <PanelValue
        basis="none"
        tone="muted"
        dimmed
        className="text-display font-semibold leading-none"
      >
        {EM_DASH}
      </PanelValue>
      <p className="text-body text-muted-foreground">{hint}</p>
    </div>
  )
}

/**
 * The stale age: an exact elapsed amount and the exact instant. Never "a
 * while ago" — §4 rules that phrasing out, and this component has no way to
 * produce it.
 */
export function PanelAge({
  producedAt,
  now,
}: {
  producedAt?: string | Date | null
  now: Date
}) {
  const age = formatAbsoluteAge(producedAt, now)
  if (!age) return null
  return (
    <span data-slot="panel-age" className="text-[11px] text-muted-foreground tabular-nums">
      {age}
    </span>
  )
}

/**
 * Provenance footer (§4.5) — producer, run id, timestamp, server-attached.
 * A public view keeps the timestamp and drops the internal names (§7.3.2b).
 */
export function PanelProvenance({
  data,
  now,
  publicView,
}: {
  data: PanelProps["data"]
  now?: Date
  publicView: boolean
}) {
  const at = toDate(provenanceProducedAt(data.provenance))
  const producer = data.provenance?.producer?.trim()
  const runId = provenanceRunId(data.provenance)?.trim()
  if (!at && !producer && !runId) return null
  return (
    <div
      data-slot="panel-provenance"
      className="flex flex-wrap items-center gap-x-2 gap-y-0.5 border-t border-border/60 pt-2 font-mono text-[11px] text-muted-foreground-soft"
    >
      {!publicView && producer ? <span>{producer}</span> : null}
      {!publicView && runId ? <span>{runId}</span> : null}
      {at ? <span className="tabular-nums">{formatInstant(at, now)}</span> : null}
    </div>
  )
}

/** The clock, injectable so an absolute age is deterministic in a test. */
export function resolveNow(now?: Date): Date {
  return now ?? new Date()
}
