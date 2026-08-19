"use client"

import * as React from "react"
import { Activity } from "lucide-react"

import { cn } from "@/lib/utils"
import { StatusBadge } from "@/components/ui/status-badge"
import { defaultEmptyHint, panelGate } from "./freshness"
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
    body = <FailedValue failure={data.failure} publicView={publicView} />
  } else if (gate.kind === "never") {
    body = <NeverProducedValue hint={data.emptyHint?.trim() || defaultEmptyHint(panel)} />
  } else {
    body = (
      <div className="flex flex-col gap-2">
        {gate.dimmed ? <PanelAge producedAt={data.provenance?.producedAt} now={clock} /> : null}
        <PanelValue basis="measured" dimmed={gate.dimmed}>
          {items.length === 0 ? (
            <p className="text-body text-muted-foreground">
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
        "flex min-w-0 items-center gap-2 rounded-lg border border-border/60 bg-surface-subtle px-2.5 py-2",
      )}
    >
      {/* Name over label, so a narrow panel loses neither. */}
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="type-row truncate">{name}</span>
        {label ? (
          <span className="truncate text-[11px] text-muted-foreground">{label}</span>
        ) : null}
      </div>
      <StatusBadge
        status={presentation.badge}
        className="shrink-0 text-[11px]"
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
