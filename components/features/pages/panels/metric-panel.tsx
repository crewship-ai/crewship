"use client";

import * as React from "react";
import { Gauge } from "lucide-react";

import { cn } from "@/lib/utils";
import {
  formatTweenFrame,
  panelMotion,
  useTweenedValues,
  type TweenFrame,
} from "@/components/features/pages/panel-motion";
import {
  EM_DASH,
  defaultEmptyHint,
  panelGate,
  provenanceProducedAt,
} from "./freshness";
import {
  FailedValue,
  NeverProducedValue,
  PanelAge,
  PanelFrame,
  PanelValue,
  resolveNow,
} from "./panel-frame";
import type { MetricPayload, PanelProps } from "./types";

/**
 * `metric.v1` — one number, delta, optional target and sparkline (§3).
 *
 * The sparkline is hand-written inline SVG. Under `output: "export"` a
 * recharts `ResponsiveContainer` measures with a client-side `ResizeObserver`
 * and paints nothing until hydration; these points are in the initial HTML.
 *
 * ## What moves, and what refuses to (epic #1935)
 *
 * The number counts to the value the producer sent, and the sparkline morphs
 * towards its new shape. Both are display transitions over values that WERE
 * measured, under the six rules in `panel-motion.ts` — the last frame is the
 * payload verbatim, it is over in 240 ms, and it never crosses the em-dash
 * boundary: a metric going from `—` to `12` cuts, because "no basis to
 * compute" is not a smaller number than twelve (§9b.4).
 *
 * Three things here deliberately do NOT move, and each is a claim rather than
 * a style preference:
 *
 *  · **The delta.** `▲ +12` is a statement about two payloads, not a magnitude
 *    on a scale; counting it up would animate an arithmetic result.
 *  · **The meter's caption and its ARIA.** `aria-valuenow` is the payload's own
 *    value from the first frame, so what a screen reader is told is never the
 *    interpolation. The fill's width is the only part that travels.
 *  · **The sparkline's `aria-label`.** It names the last measured value, not
 *    the frame on screen. A reader who cannot see the tween is never told one.
 */
export function MetricPanel({
  panel,
  data,
  now,
  publicView = false,
  className,
}: PanelProps) {
  const clock = resolveNow(now);
  const gate = panelGate(data);
  const payload = (data.payload ?? {}) as MetricPayload;
  const motion = panelMotion(panel, data);

  // `null`/absent alone is "no basis to compute". An empty string is a value
  // the producer measured: `IsNoData()` in internal/pages/payload.go treats
  // JSON null and nothing else as no data, and the em dash is the one glyph
  // both sides have to agree on (§9b.4).
  const hasValue = payload.value !== null && payload.value !== undefined;
  const numeric =
    typeof payload.value === "number" && Number.isFinite(payload.value)
      ? payload.value
      : null;
  const spark = sparkPoints(payload.sparkline);

  // Only what was MEASURED goes into the tween. A value that is absent, or is
  // a string rather than a number, is simply not a key — so there is no route
  // by which it could be interpolated towards or away from.
  const tweenTarget = new Map<string, number>();
  if (numeric !== null) tweenTarget.set(METRIC_VALUE_KEY, numeric);
  // Keyed on the ORIGINAL index, and only for points that carry a number. A
  // gap has nothing to tween towards, and giving it a key would interpolate a
  // value into a slot the producer said it could not measure.
  spark.forEach((v, i) => {
    if (v !== null) tweenTarget.set(`${SPARK_KEY_PREFIX}${i}`, v);
  });
  const frames = useTweenedValues(tweenTarget, motion.tween);

  let body: React.ReactNode;
  if (gate.kind === "failed") {
    body = (
      <FailedValue
        failure={data.failure}
        publicView={publicView}
        producedAt={provenanceProducedAt(data.provenance)}
        now={clock}
      />
    );
  } else if (gate.kind === "never") {
    body = (
      <NeverProducedValue
        hint={data.emptyHint?.trim() || defaultEmptyHint(panel)}
      />
    );
  } else {
    // Mid-flight, this frame is a number nobody measured — which is exactly
    // why it is thrown away rather than rounded into place. `settled` means the
    // tween is over (or never started), and then the text is the payload's own
    // `String(...)`, byte for byte, with no formatter between them.
    const frame = frames.get(METRIC_VALUE_KEY);
    const tweening = frame !== undefined && !frame.settled && numeric !== null;
    const shownValue = tweening
      ? formatTweenFrame(frame.from, numeric, frame.value)
      : String(payload.value);
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
              <span
                data-slot="panel-metric-value"
                data-panel-tween={tweening ? "running" : "settled"}
                className="type-page-metric"
              >
                {shownValue}
              </span>
              {payload.unit ? (
                <span className="type-page-value text-muted-foreground">
                  {payload.unit}
                </span>
              ) : null}
              <MetricDelta
                delta={payload.delta}
                deltaGood={payload.delta_good}
              />
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
            <PanelAge
              producedAt={provenanceProducedAt(data.provenance)}
              now={clock}
            />
          ) : null}
        </div>
        <TargetMeter
          value={numeric}
          target={payload.target}
          deltaGood={payload.delta_good}
          dimmed={gate.dimmed}
        />
        <Sparkline points={spark} frames={frames} dimmed={gate.dimmed} />
      </div>
    );
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
  );
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
  delta?: number | null;
  deltaGood?: "up" | "down" | null;
}) {
  if (typeof delta !== "number" || !Number.isFinite(delta) || delta === 0)
    return null;
  const up = delta > 0;
  const good = deltaGood
    ? up
      ? deltaGood === "up"
      : deltaGood === "down"
    : null;
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
  );
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
  value: number | null;
  target?: number | null;
  deltaGood?: "up" | "down" | null;
  dimmed: boolean;
}) {
  if (
    value === null ||
    typeof target !== "number" ||
    !Number.isFinite(target) ||
    target <= 0
  ) {
    return null;
  }
  const pct = Math.max(0, Math.min(100, (value / target) * 100));
  const reached = value >= target;
  // `null` is "reached, but the payload never said whether that is good news".
  const good = reached && deltaGood ? deltaGood === "up" : null;
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
        {/* The fill travels to its new width in CSS rather than in JS: the
            width IS the payload's percentage from the first frame, so there is
            no interpolated number anywhere in the DOM and no way for the
            settled state to be anything but exact. `data-slot` is what
            `PANEL_MOTION_CSS` hangs the transition on — and that rule is scoped
            under `[data-panel-motion="on"]`, so a stale or failed meter is
            still. The track above is fixed height with `overflow-hidden`, so a
            width that moves cannot move anything else on the page. */}
        <div
          data-slot="panel-target-fill"
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
  );
}

const SPARK_W = 100;
const SPARK_H = 28;
const SPARK_PAD = 2;

/** Tween keys. Namespaced so the value can never collide with a point. */
const METRIC_VALUE_KEY = "value";
const SPARK_KEY_PREFIX = "spark:";

/**
 * The points this panel will actually draw. Shared between the component and
 * the tween so the two can never disagree about which index is which value —
 * a filter applied in one place and not the other would morph point 3 towards
 * a number that belongs to point 4.
 */
function sparkPoints(values?: (number | null)[] | null): (number | null)[] {
  return Array.isArray(values)
    ? values.map((v) =>
        typeof v === "number" && Number.isFinite(v) ? v : null,
      )
    : [];
}

/**
 * Inline SVG sparkline. `viewBox` + `preserveAspectRatio="none"` means the
 * geometry is fixed at render time and the browser does the scaling, so the
 * shape is present in the exported HTML with no client-side measurement.
 *
 * It MORPHS rather than snapping (epic #1935), and the morph is computed in
 * data space rather than in screen space: each point travels towards its own
 * new value, and the min/max the line is scaled against is recomputed from the
 * frame. That is what makes a rolling window slide instead of jumping — on the
 * live `sít'` page this is forty latency samples that shift by one every five
 * seconds, and rescaling to the final domain while the points are still moving
 * would kink the whole line on every push.
 *
 * The label is the exception and stays exact: it names the last MEASURED value
 * from the payload, never the frame on screen, so a reader who is being told
 * the number rather than shown it is never told the interpolation.
 */
function Sparkline({
  points,
  frames,
  dimmed,
}: {
  points: (number | null)[];
  frames: ReadonlyMap<string, TweenFrame>;
  dimmed: boolean;
}) {
  if (points.length === 0) return null;

  // A `null` stays a null all the way to the geometry. The schema calls it "a
  // gap the producer knows about, so the line breaks instead of interpolating
  // across missing data", and this used to filter them out instead — which did
  // two wrong things at once: it drew a straight line THROUGH the gap, and it
  // shifted every later point leftwards, so a window with one missing sample
  // silently compressed its own time axis.
  const drawn = points.map((v, i) =>
    v === null ? null : (frames.get(`${SPARK_KEY_PREFIX}${i}`)?.value ?? v),
  );
  const measured = drawn.filter((v): v is number => v !== null);
  if (measured.length === 0) return null;

  const min = Math.min(...measured);
  const max = Math.max(...measured);
  const span = max - min;
  // Positions come from the ORIGINAL index and the ORIGINAL length, so a gap
  // holds its place on the axis rather than closing it.
  const x = (i: number) =>
    drawn.length === 1
      ? SPARK_W / 2
      : SPARK_PAD + (i * (SPARK_W - 2 * SPARK_PAD)) / (drawn.length - 1);
  const y = (v: number) =>
    span === 0
      ? SPARK_H / 2
      : SPARK_H - SPARK_PAD - ((v - min) / span) * (SPARK_H - 2 * SPARK_PAD);

  // One polyline per contiguous run of measured points. A run of one is drawn
  // too — as a dot rather than a line, because a lone sample between two gaps
  // is still something the producer measured and dropping it would be the same
  // erasure this fix is about.
  const runs: { i: number; v: number }[][] = [];
  let run: { i: number; v: number }[] = [];
  drawn.forEach((v, i) => {
    if (v === null) {
      if (run.length > 0) runs.push(run);
      run = [];
      return;
    }
    run.push({ i, v });
  });
  if (run.length > 0) runs.push(run);

  const lastMeasured = [...points]
    .reverse()
    .find((v): v is number => v !== null);
  const gaps = points.length - measured.length;

  return (
    <svg
      data-slot="sparkline"
      viewBox={`0 0 ${SPARK_W} ${SPARK_H}`}
      preserveAspectRatio="none"
      role="img"
      // The gaps are named rather than left to be inferred from a shape a
      // screen reader cannot see.
      aria-label={
        `Trend over the last ${points.length} values, ending at ${lastMeasured ?? EM_DASH}` +
        (gaps > 0 ? `, with ${gaps} not measured` : "")
      }
      focusable="false"
      className={cn("h-7 w-full text-primary", dimmed && "opacity-60")}
    >
      {/* Only a run of two or more is a LINE. A one-point polyline draws
          nothing at all — it is DOM that says a segment exists where none
          does, and the dot below is what actually represents that reading. */}
      {runs
        .filter((r) => r.length > 1)
        .map((r) => (
          <polyline
            key={r[0].i}
            data-slot="sparkline-run"
            points={r
              .map((pt) => `${x(pt.i).toFixed(2)},${y(pt.v).toFixed(2)}`)
              .join(" ")}
            fill="none"
            stroke="currentColor"
            strokeWidth={1.5}
            strokeLinecap="round"
            strokeLinejoin="round"
            vectorEffect="non-scaling-stroke"
          />
        ))}
      {/* A run of one has no line to draw, so it is drawn as a dot. That covers
          the single-sample panel it always covered, and now also a lone
          measurement stranded between two gaps — which is a real reading and
          would otherwise vanish. */}
      {runs
        .filter((r) => r.length === 1)
        .map((r) => (
          <circle
            key={r[0].i}
            data-slot="sparkline-point"
            cx={x(r[0].i)}
            cy={y(r[0].v)}
            r={1.5}
            fill="currentColor"
          />
        ))}
    </svg>
  );
}
