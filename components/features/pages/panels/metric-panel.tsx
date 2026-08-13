"use client"

import * as React from "react"
import { Gauge } from "lucide-react"

import { cn } from "@/lib/utils"
import { EM_DASH, defaultEmptyHint, panelGate, provenanceProducedAt } from "./freshness"
import {
  FailedValue,
  NeverProducedValue,
  PanelAge,
  PanelFrame,
  PanelValue,
  resolveNow,
} from "./panel-frame"
import type { MetricPayload, PanelProps } from "./types"

/**
 * `metric.v1` — one number, delta, optional target and sparkline (§3).
 *
 * The sparkline is hand-written inline SVG. Under `output: "export"` a
 * recharts `ResponsiveContainer` measures with a client-side `ResizeObserver`
 * and paints nothing until hydration; these points are in the initial HTML.
 */
export function MetricPanel({ panel, data, now, publicView = false, className }: PanelProps) {
  const clock = resolveNow(now)
  const gate = panelGate(data)
  const payload = (data.payload ?? {}) as MetricPayload

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
    // `null`/absent alone is "no basis to compute". An empty string is a value
    // the producer measured: `IsNoData()` in internal/pages/payload.go treats
    // JSON null and nothing else as no data, and the em dash is the one glyph
    // both sides have to agree on (§9b.4).
    const hasValue = payload.value !== null && payload.value !== undefined
    const numeric = typeof payload.value === "number" && Number.isFinite(payload.value)
      ? payload.value
      : null
    body = (
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
          {hasValue ? (
            <PanelValue
              basis="measured"
              dimmed={gate.dimmed}
              className="flex flex-wrap items-baseline gap-x-1.5"
            >
              {/* A measured 0 is a 0. Only "no basis to compute" is an em dash. */}
              <span className="type-page-metric">{String(payload.value)}</span>
              {payload.unit ? (
                <span className="type-page-value text-muted-foreground">{payload.unit}</span>
              ) : null}
              <MetricDelta delta={payload.delta} deltaGood={payload.delta_good} />
            </PanelValue>
          ) : (
            <PanelValue
              basis="none"
              tone="muted"
              dimmed={gate.dimmed}
              className="type-page-metric"
            >
              {EM_DASH}
            </PanelValue>
          )}
          {gate.dimmed ? (
            <PanelAge producedAt={provenanceProducedAt(data.provenance)} now={clock} />
          ) : null}
        </div>
        <TargetMeter
          value={numeric}
          target={payload.target}
          deltaGood={payload.delta_good}
          dimmed={gate.dimmed}
        />
        <Sparkline values={payload.sparkline} dimmed={gate.dimmed} />
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
      icon={Gauge}
    >
      {body}
    </PanelFrame>
  )
}

/**
 * The delta carries an explicit sign and an arrow glyph, so direction never
 * depends on colour alone. Tone stays muted unless the payload's `delta_good`
 * (§11b.9) declared which way is an improvement — §3 does not say whether a
 * rising number is good, and a green "up" on an error rate is a lie the panel
 * is not entitled to. The value arrives under the wire name; this parameter is
 * local and may be spelled however React reads best.
 */
function MetricDelta({
  delta,
  deltaGood,
}: {
  delta?: number | null
  deltaGood?: "up" | "down" | null
}) {
  if (typeof delta !== "number" || !Number.isFinite(delta) || delta === 0) return null
  const up = delta > 0
  const good = deltaGood ? (up ? deltaGood === "up" : deltaGood === "down") : null
  return (
    <span
      data-slot="panel-delta"
      className={cn(
        "type-page-meta tabular-nums",
        good === null && "text-muted-foreground",
        good === true && "text-success",
        good === false && "text-destructive",
      )}
    >
      <span aria-hidden="true">{up ? "▲" : "▼"}</span>
      {` ${up ? "+" : "−"}${Math.abs(delta)}`}
    </span>
  )
}

/**
 * Hand-drawn meter — one div, one width. No chart library, no measurement.
 *
 * **Reaching the target is the one thing on this panel colour is allowed to
 * say, and only when the payload said which way is good.** §11b.10 calls
 * `target` a ceiling; §3 reserves the status colours; and §11b.9 settles the
 * argument for the delta right above — *"green-up on an error rate would be a
 * lie, so the payload has to say which way is good."* A target is the same
 * shape of claim: 128 of 150 invoices reaching 150 is an achievement, 128 of
 * 150 open incidents reaching 150 is a fire, and nothing in `{value, target}`
 * distinguishes them. So the same opt-in decides it:
 *
 *   crossed + `delta_good: "up"`    → success  (up is good; the goal is met)
 *   crossed + `delta_good: "down"`  → destructive (down is good; the cap is hit)
 *   crossed, no `delta_good`        → the neutral bar, and the word "reached"
 *
 * The word rides with the colour in every case, so the meter still reports the
 * fact on a monochrome print and to a reader who cannot separate the hues —
 * the same rule §3 pins on `status.v1`, applied here rather than re-argued.
 */
function TargetMeter({
  value,
  target,
  deltaGood,
  dimmed,
}: {
  value: number | null
  target?: number | null
  deltaGood?: "up" | "down" | null
  dimmed: boolean
}) {
  if (value === null || typeof target !== "number" || !Number.isFinite(target) || target <= 0) {
    return null
  }
  const pct = Math.max(0, Math.min(100, (value / target) * 100))
  const reached = value >= target
  // `null` is "reached, but the payload never said whether that is good news".
  const good = reached && deltaGood ? deltaGood === "up" : null
  return (
    <div
      data-slot="panel-target"
      data-reached={reached ? "true" : "false"}
      role="meter"
      aria-valuenow={value}
      aria-valuemin={0}
      aria-valuemax={target}
      aria-label={
        reached
          ? `${value} of a target of ${target}, target reached`
          : `${value} of a target of ${target}`
      }
      className={cn("flex flex-col gap-1", dimmed && "opacity-60")}
    >
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
        <div
          className={cn(
            "h-full rounded-full",
            good === null && "bg-primary",
            good === true && "bg-success",
            good === false && "bg-destructive",
          )}
          style={{ width: `${pct.toFixed(1)}%` }}
        />
      </div>
      <span
        className={cn(
          "type-page-meta tabular-nums",
          good === null && "text-muted-foreground",
          good === true && "text-success",
          good === false && "text-destructive",
        )}
      >
        {reached ? `${target} target · reached` : `of ${target} target`}
      </span>
    </div>
  )
}

const SPARK_W = 100
const SPARK_H = 28
const SPARK_PAD = 2

/**
 * Inline SVG sparkline. `viewBox` + `preserveAspectRatio="none"` means the
 * geometry is fixed at render time and the browser does the scaling, so the
 * shape is present in the exported HTML with no client-side measurement.
 */
function Sparkline({ values, dimmed }: { values?: number[] | null; dimmed: boolean }) {
  const points = Array.isArray(values)
    ? values.filter((v): v is number => typeof v === "number" && Number.isFinite(v))
    : []
  if (points.length === 0) return null

  const min = Math.min(...points)
  const max = Math.max(...points)
  const span = max - min
  const x = (i: number) =>
    points.length === 1
      ? SPARK_W / 2
      : SPARK_PAD + (i * (SPARK_W - 2 * SPARK_PAD)) / (points.length - 1)
  const y = (v: number) =>
    span === 0
      ? SPARK_H / 2
      : SPARK_H - SPARK_PAD - ((v - min) / span) * (SPARK_H - 2 * SPARK_PAD)

  const path = points.map((v, i) => `${x(i).toFixed(2)},${y(v).toFixed(2)}`).join(" ")
  const last = points[points.length - 1]

  return (
    <svg
      data-slot="sparkline"
      viewBox={`0 0 ${SPARK_W} ${SPARK_H}`}
      preserveAspectRatio="none"
      role="img"
      aria-label={`Trend over the last ${points.length} values, ending at ${last}`}
      focusable="false"
      className={cn("h-7 w-full text-primary", dimmed && "opacity-60")}
    >
      <polyline
        points={path}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.5}
        strokeLinecap="round"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
      {points.length === 1 ? (
        <circle cx={x(0)} cy={y(points[0])} r={1.5} fill="currentColor" />
      ) : null}
    </svg>
  )
}
