"use client"

import * as React from "react"
import { Activity } from "lucide-react"

import { cn } from "@/lib/utils"
import { StatusBadge } from "@/components/ui/status-badge"
import { STATUS_DOT_CLASSES } from "@/lib/colors"
import { defaultEmptyHint, panelGate, provenanceProducedAt } from "./freshness"
import {
  FailedValue,
  NeverProducedValue,
  PanelAge,
  PanelFrame,
  PanelValue,
  resolveNow,
} from "./panel-frame"
import type { PanelProps, StatusItem, StatusPayload } from "./types"

/**
 * `status.v1` — a status grid (§3). State carries a glyph *and* a word, never
 * colour alone, so the panel survives a monochrome print and protanopia alike.
 *
 * The pill is `StatusBadge`, which routes through `STATUS_BADGE_CLASSES` —
 * Pages does not invent a second status colour map.
 */
export function StatusPanel({ panel, data, now, publicView = false, className }: PanelProps) {
  const clock = resolveNow(now)
  const gate = panelGate(data)
  const payload = (data.payload ?? {}) as StatusPayload
  const items = Array.isArray(payload.items) ? payload.items : []

  let body: React.ReactNode
  if (gate.kind === "failed") {
    body = (
      <FailedValue
        failure={data.failure}
        publicView={publicView}
        producedAt={provenanceProducedAt(data.provenance)}
        now={clock}
      />
    )
  } else if (gate.kind === "never") {
    body = <NeverProducedValue hint={data.emptyHint?.trim() || defaultEmptyHint(panel)} />
  } else {
    body = (
      <div className="flex flex-col gap-2">
        {gate.dimmed ? (
          <PanelAge producedAt={provenanceProducedAt(data.provenance)} now={clock} />
        ) : null}
        <PanelValue basis="measured" dimmed={gate.dimmed}>
          {items.length === 0 ? (
            <p className="type-page-value text-muted-foreground">
              The producer reported no items in its latest push.
            </p>
          ) : (
            <div
              data-slot="panel-container"
              className="@container/panel grid grid-cols-1 gap-2 @md/panel:grid-cols-2"
            >
              {items.map((item, i) => (
                <StatusRow key={`${item?.name ?? "item"}-${i}`} item={item} />
              ))}
            </div>
          )}
        </PanelValue>
      </div>
    )
  }

  return (
    <PanelFrame
      panel={panel}
      data={data}
      now={clock}
      publicView={publicView}
      className={className}
      icon={Activity}
    >
      {body}
    </PanelFrame>
  )
}

/**
 * An unrecognised state degrades to a neutral row. A payload is machine-written
 * and, per §8, may be agent-written — it does not get to crash the page by
 * inventing a fourth state.
 */
const STATE_PRESENTATION: Record<string, { glyph: string; word: string; badge: string }> = {
  ok: { glyph: "✓", word: "ok", badge: "COMPLETED" },
  warning: { glyph: "!", word: "warning", badge: "BLOCKED" },
  critical: { glyph: "✕", word: "critical", badge: "FAILED" },
}

const UNKNOWN_PRESENTATION = { glyph: "?", word: "unknown", badge: "UNKNOWN" }

/**
 * The state rail — the row's left edge, in the state's own colour.
 *
 * This is the one panel §3 hands a palette to: *"status colours are reserved"*
 * is a rule that reserves them FOR this triad, and a grid of a dozen services
 * that a reader has to parse pill by pill is the case the colour exists to
 * solve. The rail is scanned; the pill is read.
 *
 * It routes through `STATUS_DOT_CLASSES` for the same reason the pill routes
 * through `STATUS_BADGE_CLASSES` — Pages does not get a second status colour
 * map, and the fallback for a state a producer invented is the same neutral the
 * badge falls back to, so an unknown state is grey on both.
 *
 * Colour is never the carrier: the glyph and the word are still in the pill
 * beside it, and `data-state` still holds the machine-readable truth.
 */
function stateRailClass(badge: string): string {
  return STATUS_DOT_CLASSES[badge] ?? "bg-muted-foreground"
}

function StatusRow({ item }: { item: StatusItem }) {
  const rawState = typeof item?.state === "string" ? item.state.toLowerCase() : ""
  const known = Object.prototype.hasOwnProperty.call(STATE_PRESENTATION, rawState)
  const presentation = known ? STATE_PRESENTATION[rawState] : UNKNOWN_PRESENTATION
  const name = typeof item?.name === "string" && item.name.trim() ? item.name : "unnamed"
  const label = typeof item?.label === "string" ? item.label.trim() : ""

  return (
    <div
      data-slot="status-item"
      data-state={known ? rawState : "unknown"}
      className={cn(
        "flex min-w-0 items-stretch gap-2 overflow-hidden rounded-lg border border-border/60 bg-surface-subtle py-2 pr-2.5 pl-0",
      )}
    >
      <span
        data-slot="status-rail"
        aria-hidden="true"
        className={cn("-my-2 w-1 shrink-0 rounded-r-sm", stateRailClass(presentation.badge))}
      />
      {/* Name over label, so a narrow panel loses neither.
       *
       * The name is the row's CONTENT and the label qualifies it, so they take
       * the register's value and meta roles — not `.type-row` and `text-micro`,
       * which is what they were. Those two are the same 14px and 11px by
       * coincidence of history rather than by a rule, and `.type-row`'s 1.3rem
       * leading disagreed with the 1.25rem the collapsed table cards next to it
       * use for the identical job. One 14px in Pages, and it is the one
       * PropertyRow is written in (`app/globals.css`, "The Pages register"). */}
      <div className="flex min-w-0 flex-1 flex-col justify-center">
        <span className="type-page-value truncate">{name}</span>
        {label ? (
          <span className="type-page-meta truncate text-muted-foreground">{label}</span>
        ) : null}
      </div>
      {/* No size override: the house pill is 12px, which IS the register's meta
       * role. A panel that restated it would be the drift this round removed. */}
      <StatusBadge
        status={presentation.badge}
        className="shrink-0 self-center"
        label={
          <>
            <span data-slot="status-glyph" aria-hidden="true" className="font-semibold">
              {presentation.glyph}
            </span>
            <span data-slot="status-word">{presentation.word}</span>
          </>
        }
      />
    </div>
  )
}
