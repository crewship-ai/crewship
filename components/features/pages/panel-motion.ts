"use client"

/**
 * Motion for the panels (epic #1935) — the rules first, the hooks second.
 *
 * A page's whole claim is that it never shows anything untrue (§4, §9b.4). A
 * tween is in tension with that and the tension is real rather than rhetorical:
 * for a fraction of a second a tweened number displays a value **nobody
 * measured**. When the producer sends 0.6 and then 1.0, the screen genuinely
 * reads 0.7 on the way. That is an accepted convention for a *display*
 * transition, and it is accepted here only because every one of the following
 * holds — each is implemented in this file and asserted in
 * `panels/__tests__/panel-motion.test.tsx`.
 *
 *  1. **The last frame is the payload, exactly.** Every tween ends by throwing
 *     its interpolated frame away and rendering the value the producer sent, so
 *     no easing curve, rounding rule or dropped frame can leave a number on
 *     screen that the producer did not send. The easing is `ease.out`, whose
 *     control points all lie within [0,1] — it cannot overshoot and come back.
 *  2. **It is short.** `PANEL_TWEEN_MS` is the house `duration.base`. A long
 *     tween is a long time spent displaying a fiction.
 *  3. **It never crosses a category.** §9b.4: a measured `0` and an em dash
 *     ("no basis to compute") are different CLAIMS, not different magnitudes.
 *     `useTweenedValues` only interpolates a key that carried a number in both
 *     the old payload and the new one; a key that appears, disappears or has no
 *     predecessor cuts. There is no route by which a number can count up from
 *     an imagined zero.
 *  4. **A `stale` or `failed` panel does not move at all.** A dead panel that
 *     moves looks alive, which is precisely the lie §4 exists to prevent — so
 *     `panelMotion()` refuses on any state but `fresh`.
 *  5. **A sealed panel never moves.** It was sent no data to have received.
 *  6. **Nothing moves on mount.** The first payload a panel is seen with is a
 *     baseline, exactly as the arrival flash in `page-view.tsx` already treats
 *     it. Opening a page does not replay every panel's history.
 *
 * ## Two suppressions for `prefers-reduced-motion`, and why they differ
 *
 * The house has one idiom (`app/globals.css`'s `.agent-*`, `PANEL_ARRIVAL_CSS`
 * in `page-view.tsx`): set the DOM state unconditionally, and let a CSS media
 * query decide only how it is painted. **What is said must never be gated on
 * the media query.** That works for a cue whose entire existence is paint — a
 * ring, a fade — and it is what `PANEL_MOTION_CSS` below does for the change
 * marks, the row arrivals and the target meter.
 *
 * It cannot work for a value tween, because a tween's intermediate frames *are*
 * the DOM: CSS cannot un-write a number React already rendered. So the tween
 * hooks read `prefersReducedMotion()` in JS and decline to interpolate at all,
 * which leaves the reader with the payload's own value on the first frame.
 *
 * Both suppressions land in the same place — the settled DOM is byte-identical
 * either way, and it always says what the producer sent. The difference is only
 * whether a reader is shown the journey.
 */

import * as React from "react"
import { animate } from "motion/react"

import { duration, ease, prefersReducedMotion } from "@/lib/motion"
import type { PanelSnapshot, PanelSpec } from "./panels/types"

/**
 * How long a value takes to reach the number the producer sent.
 *
 * The house `duration.base` rather than a number invented here: 240 ms is long
 * enough to read as growth and short enough that rule 2 above is not a slogan.
 */
export const PANEL_TWEEN_MS = Math.round(duration.base * 1000)

/**
 * How long a changed row or cell stays marked.
 *
 * Shorter than the cell's arrival flash (`PANEL_ARRIVAL_MS`, 1200 ms) on
 * purpose: the two can fire in the same instant — the panel received data AND
 * this row's state changed — and the outer, longer cue should be the one that
 * survives the moment.
 */
export const PANEL_MARK_MS = 900

// ── who is allowed to move ────────────────────────────────────────────────

export interface PanelMotion {
  /**
   * This panel may move at all. `fresh` and unsealed, and nothing else — the
   * server's verdict is the authority, exactly as it is for the arrival flash.
   *
   * Never gated on `prefers-reduced-motion`: it decides what the DOM SAYS
   * (`data-panel-motion`, `data-panel-change`), which a reduced-motion reader
   * must get identically.
   */
  readonly animatable: boolean
  /**
   * …and this reader has not asked for reduced motion, so an interpolated
   * value may be displayed on the way to the real one.
   */
  readonly tween: boolean
}

/**
 * Not a hook — it calls none, and naming it one would imply an order
 * constraint it does not have. Callers may branch on the result freely.
 */
export function panelMotion(
  panel: Pick<PanelSpec, "sealed">,
  data: Pick<PanelSnapshot, "state">,
): PanelMotion {
  // A sealed panel is a permission decision rendered as a placeholder
  // (§11b.14). There is no payload behind it, so there is nothing that could
  // have arrived and nothing to animate.
  if (panel.sealed === true) return STILL
  // `stale`, `failed` and `never_produced` are silent by construction. A
  // normalisation that could not read the state lands on `never_produced` and
  // is silent too — never optimistically on `fresh`.
  if (data.state !== "fresh") return STILL
  return { animatable: true, tween: !prefersReducedMotion() }
}

const STILL: PanelMotion = { animatable: false, tween: false }

/**
 * `useLayoutEffect` in a browser, `useEffect` on the server.
 *
 * Under `output: "export"` these components are rendered to HTML at build
 * time, where a layout effect never runs and React logs a warning for asking.
 * The distinction is genuinely only about paint timing, so degrading to
 * `useEffect` on the server costs nothing: nothing is painted there.
 */
const useIsomorphicLayoutEffect =
  typeof window === "undefined" ? React.useEffect : React.useLayoutEffect

// ── tweening a set of measured numbers ────────────────────────────────────

/** One key's state on the way from the last payload to this one. */
export interface TweenFrame {
  /** What to draw this frame. Equal to the payload's value when `settled`. */
  readonly value: number
  /** Where this tween started — the previous payload's value for this key. */
  readonly from: number
  /** True when `value` IS the payload, with no interpolation in front of it. */
  readonly settled: boolean
}

/**
 * Interpolate a keyed set of measured numbers towards the payload's own.
 *
 * Keyed rather than positional, and that is the whole of rule 3. A caller
 * supplies a map of only the points it actually MEASURED — a `null` bar, an
 * absent metric value and a dropped table row are simply not in it — so:
 *
 *  - a key in both maps tweens (a magnitude changed);
 *  - a key only in the new map cuts (it has no predecessor: it either just
 *    appeared, or it crossed the em-dash boundary from "no basis to compute",
 *    and §9b.4 says that is a different claim rather than a bigger number);
 *  - a key only in the old map is forgotten (nothing draws it any more).
 *
 * The returned map always has an entry for every key in `target`, so a caller
 * never has to decide what to draw for a key mid-flight.
 *
 * Interrupting a tween in flight is safe and does not jump: the effect starts
 * the next one from the frame currently on screen, not from the payload that
 * was superseded.
 */
export function useTweenedValues(
  target: ReadonlyMap<string, number>,
  enabled: boolean,
  durationMs: number = PANEL_TWEEN_MS,
): ReadonlyMap<string, TweenFrame> {
  const [frame, setFrame] = React.useState<ReadonlyMap<string, TweenFrame> | null>(null)

  // The effect fires on `key`, a serialisation of the map's contents, because
  // a fresh Map object every render would restart the tween on every render.
  // It reads the values through a ref so the dep list can stay honest.
  const latest = React.useRef(target)
  latest.current = target
  const key = valuesKey(target)

  /** The numbers currently ON SCREEN — mid-tween ones included. */
  const shown = React.useRef<ReadonlyMap<string, number> | undefined>(undefined)
  const running = React.useRef<{ stop: () => void } | null>(null)

  // A LAYOUT effect, and the reason is visible on screen if it is not. React
  // has already rendered the new payload's values by the time an effect runs,
  // so a tween started in `useEffect` paints one frame at the destination and
  // then jumps back to the start — a flicker, on every push. This runs before
  // the browser paints and posts the t=0 frame synchronously, so the first
  // thing anybody sees is the value that was already there.
  useIsomorphicLayoutEffect(() => {
    const to = latest.current
    const from = shown.current
    /** Show the payload and nothing else. */
    const settle = () => {
      shown.current = to
      setFrame(null)
    }

    // Rule 6. The first payload a panel is seen with is a baseline.
    if (from === undefined) {
      settle()
      return
    }
    // Rules 4, 5 and reduced motion all arrive here as `enabled: false`.
    if (!enabled) {
      settle()
      return
    }

    // Rule 3: only a key measured in BOTH payloads is a magnitude change.
    let moving = 0
    for (const [k, v] of to) {
      const before = from.get(k)
      if (before !== undefined && before !== v) moving++
    }
    if (moving === 0) {
      settle()
      return
    }

    const frameAt = (t: number) => {
      const next = new Map<string, TweenFrame>()
      const displayed = new Map<string, number>()
      for (const [k, v] of to) {
        const before = from.get(k)
        // A key with no predecessor is drawn AT the payload from the first
        // frame — it cut, and the rest of the panel moving around it does not
        // drag it along.
        const value = before === undefined ? v : before + (v - before) * t
        next.set(k, { value, from: before ?? v, settled: false })
        displayed.set(k, value)
      }
      shown.current = displayed
      return next
    }

    running.current?.stop()
    setFrame(frameAt(0))
    const controls = animate(0, 1, {
      duration: durationMs / 1000,
      // Monotone by construction: every control point of `ease.out` lies
      // within [0,1], so the value cannot pass the target and come back.
      ease: ease.out,
      onUpdate: (t) => setFrame(frameAt(t)),
      // Rule 1. The interpolated frame is thrown away rather than rounded into
      // place: what lands on screen is the payload, by construction.
      onComplete: settle,
    })
    running.current = controls
    return () => {
      controls.stop()
    }
  }, [key, enabled, durationMs])

  React.useEffect(
    () => () => {
      running.current?.stop()
      running.current = null
    },
    [],
  )

  // Built per render rather than memoised: it is at most a few dozen entries,
  // and a memo keyed on anything but `key` would hand back a stale frame.
  const out = new Map<string, TweenFrame>()
  for (const [k, v] of target) {
    out.set(k, frame?.get(k) ?? { value: v, from: v, settled: true })
  }
  return out
}

function valuesKey(values: ReadonlyMap<string, number>): string {
  let out = ""
  for (const [k, v] of values) out += `${k}\u0000${v}`
  return out
}

/**
 * How many decimals a tweened frame may show.
 *
 * The endpoints decide, never the interpolation: going from `0.6` to `1` prints
 * `0.7`, not `0.7333333333333333` and not `1`. Capped, because a payload of
 * `0.1` and `0.2` sums to seventeen decimals in binary floating point and none
 * of them are a measurement.
 */
export function formatTweenFrame(from: number, to: number, value: number): string {
  const places = Math.min(4, Math.max(decimalsOf(from), decimalsOf(to)))
  return value.toFixed(places)
}

function decimalsOf(n: number): number {
  const s = String(n)
  const dot = s.indexOf(".")
  // An exponent-formatted number (1e-7) has no readable decimal count; treat
  // it as needing none, and let the cap above hold.
  if (dot === -1 || s.includes("e") || s.includes("E")) return 0
  return s.length - dot - 1
}

// ── marking what changed ──────────────────────────────────────────────────

export interface KeyedChanges {
  /** Keys whose signature differs from the previous payload's. */
  readonly changed: ReadonlySet<string>
  /** Keys the previous payload did not have at all. */
  readonly entered: ReadonlySet<string>
}

const NO_KEYS: ReadonlySet<string> = new Set<string>()

/**
 * Which keys changed, and which are new, for `markMs` after they did.
 *
 * This is the answer to *"a grid where everything animates on every push is
 * noise"*: a status grid of twelve services pushed every thirty seconds has an
 * event worth marking perhaps twice a day, and the other 2878 pushes should
 * leave the screen completely still. So the caller supplies a SIGNATURE per key
 * — for a status row that is the state word alone, deliberately not the label,
 * because a latency label that reads `6 ms` then `7 ms` has not changed state
 * and marking it would restore exactly the noise this avoids.
 *
 * Not gated on `prefers-reduced-motion`, on purpose: the mark is a fact about
 * the data and it goes into the DOM for every reader. `PANEL_MOTION_CSS`
 * decides whether it is painted as a decaying ring or a still one.
 *
 * Mount is a baseline (rule 6), and so is any payload observed while the panel
 * was not `animatable` — which mirrors the arrival flash exactly: coming back
 * from `stale` marks the rows whose data genuinely changed, never the recovery
 * itself.
 */
export function useKeyedChanges(
  signatures: ReadonlyMap<string, string>,
  enabled: boolean,
  markMs: number = PANEL_MARK_MS,
): KeyedChanges {
  const [changed, setChanged] = React.useState<ReadonlySet<string>>(NO_KEYS)
  const [entered, setEntered] = React.useState<ReadonlySet<string>>(NO_KEYS)

  const latest = React.useRef(signatures)
  latest.current = signatures
  const key = signaturesKey(signatures)

  const seen = React.useRef<ReadonlyMap<string, string> | undefined>(undefined)
  const timers = React.useRef(new Map<string, ReturnType<typeof setTimeout>>())

  React.useEffect(() => {
    const now = latest.current
    const before = seen.current
    // Recorded BEFORE the eligibility check, so a panel that spends a while
    // stale does not mark its whole grid the moment it goes fresh again.
    seen.current = now

    if (before === undefined) return
    if (!enabled) {
      setChanged(NO_KEYS)
      setEntered(NO_KEYS)
      return
    }

    const changedNow = new Set<string>()
    const enteredNow = new Set<string>()
    for (const [k, v] of now) {
      const was = before.get(k)
      if (was === undefined) enteredNow.add(k)
      else if (was !== v) changedNow.add(k)
    }
    if (changedNow.size === 0 && enteredNow.size === 0) return

    if (changedNow.size > 0) setChanged((prev) => withKeys(prev, changedNow))
    if (enteredNow.size > 0) setEntered((prev) => withKeys(prev, enteredNow))

    // One timer per key rather than one for the batch: two rows that change a
    // quarter of a second apart are two events, and merging their windows
    // would make the second one's mark shorter than the first's.
    const scheduled = timers.current
    for (const k of [...changedNow, ...enteredNow]) {
      const inFlight = scheduled.get(k)
      if (inFlight !== undefined) clearTimeout(inFlight)
      scheduled.set(
        k,
        setTimeout(() => {
          scheduled.delete(k)
          setChanged((prev) => withoutKey(prev, k))
          setEntered((prev) => withoutKey(prev, k))
        }, markMs),
      )
    }
  }, [key, enabled, markMs])

  React.useEffect(() => {
    const scheduled = timers.current
    return () => {
      for (const id of scheduled.values()) clearTimeout(id)
      scheduled.clear()
    }
  }, [])

  return { changed, entered }
}

function signaturesKey(signatures: ReadonlyMap<string, string>): string {
  let out = ""
  for (const [k, v] of signatures) out += `${k}\u0000${v}`
  return out
}

/** Returns `prev` itself when nothing is added, so React can bail out. */
function withKeys(prev: ReadonlySet<string>, add: ReadonlySet<string>): ReadonlySet<string> {
  let grew = false
  for (const k of add) {
    if (!prev.has(k)) {
      grew = true
      break
    }
  }
  if (!grew) return prev
  const next = new Set(prev)
  for (const k of add) next.add(k)
  return next
}

function withoutKey(prev: ReadonlySet<string>, k: string): ReadonlySet<string> {
  if (!prev.has(k)) return prev
  const next = new Set(prev)
  next.delete(k)
  return next
}

// ── the paint, and the one place reduced motion turns it off ──────────────

/**
 * Everything a panel animates in CSS rather than in JS, in full.
 *
 * Three rules, and the property each animates is the point:
 *
 *  · **`box-shadow`** for a changed row or cell — the same inset-ring
 *    vocabulary as the cell's arrival flash, one level in. `box-shadow` paints
 *    over the element's own background instead of replacing it, which
 *    `background-color` cannot do: a keyframe ending at a transparent
 *    background would blank `bg-surface-subtle` for the length of the mark and
 *    then snap it back.
 *  · **`opacity`** for a row that just appeared. Ink only. A row takes its full
 *    height from the first frame, so nothing below it moves — an animation that
 *    reflowed the grid while somebody was reading would be worse than no
 *    animation, and a height tween is exactly that.
 *  · **`width`** on the target meter's fill. The one layout property here, and
 *    it is confined: the fill lives inside a fixed-height `overflow-hidden`
 *    track, so its width cannot move a single pixel outside that track.
 *
 * Every rule is scoped under `[data-panel-motion="on"]`, which `PanelFrame`
 * sets from `panelMotion()`. A `stale`, `failed`, never-produced or sealed
 * panel therefore has no selector that could match it, independently of every
 * refusal in the hooks above — the constraint is structural, not just careful.
 *
 * The reduced-motion block is the house idiom (`PANEL_ARRIVAL_CSS`,
 * `app/globals.css`'s `.agent-*`): `animation: none`, and the meaning survives
 * without it. A marked row still shows its ring for the same window, as a
 * steady one with no decay — same information, no movement. `data-panel-change`
 * is set identically either way, so nothing about WHAT is said depends on the
 * media query; only how it is drawn.
 *
 * Declared here rather than in `app/globals.css` for the reason
 * `PANEL_ARRIVAL_CSS` gives: the rule is meaningless outside these components,
 * and the whole cue — selector, duration and fallback — should read in one
 * place.
 */
export const PANEL_MOTION_CSS = `
@keyframes crewship-panel-change {
  0%   { box-shadow: inset 0 0 0 1.5px rgba(30, 123, 254, 0.55); }
  100% { box-shadow: inset 0 0 0 1.5px rgba(30, 123, 254, 0); }
}
@keyframes crewship-panel-enter {
  0%   { opacity: 0; }
  100% { opacity: 1; }
}
[data-panel-motion="on"] [data-panel-change="marked"] {
  animation: crewship-panel-change ${PANEL_MARK_MS}ms ease-out 1;
}
[data-panel-motion="on"] [data-panel-enter="new"] {
  animation: crewship-panel-enter ${PANEL_TWEEN_MS}ms ease-out 1;
}
[data-panel-motion="on"] [data-slot="panel-target-fill"] {
  transition: width ${PANEL_TWEEN_MS}ms cubic-bezier(0.16, 1, 0.3, 1);
}
@media (prefers-reduced-motion: reduce) {
  [data-panel-motion="on"] [data-panel-change="marked"] {
    animation: none;
    box-shadow: inset 0 0 0 1.5px rgba(30, 123, 254, 0.35);
  }
  [data-panel-motion="on"] [data-panel-enter="new"] {
    animation: none;
  }
  [data-panel-motion="on"] [data-slot="panel-target-fill"] {
    transition: none;
  }
}
`

/**
 * React 19 hoists a `<style>` carrying `href` + `precedence` into the head and
 * dedupes it by href, so every panel can declare the rule and exactly one copy
 * ships — the same mechanism `PanelArrivalStyles` uses one level up in
 * `page-view.tsx`.
 *
 * Built with `createElement` rather than JSX only because this module is the
 * shared helper the panels agree on and is therefore a `.ts` file.
 */
export function PanelMotionStyles(): React.ReactElement {
  return React.createElement(
    "style",
    { href: "crewship-panel-motion", precedence: "medium" },
    PANEL_MOTION_CSS,
  )
}
