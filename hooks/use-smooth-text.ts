"use client"

import { useEffect, useRef, useState } from "react"

/** Baseline reveal rate. ~160 chars/s reads as a calm, deliberate stream —
 *  fast enough to never feel throttled on short replies. */
const BASE_CPS = 160
/** Backlog catch-up factor: effective rate grows with the number of
 *  not-yet-revealed characters, so a large buffered burst (reconnect replay,
 *  slow tab) drains in well under a second instead of typing forever. */
const CATCHUP_PER_CHAR = 2.5

/**
 * Decouples network chunk arrival from the visual text reveal.
 *
 * Raw WS deltas arrive in bursts, so rendering them directly makes whole
 * sentences pop in at once. This hook takes the full text-so-far and returns
 * the prefix to render, advanced every animation frame at a smoothed,
 * backlog-adaptive character rate (the Claude.ai / ChatGPT feel).
 *
 * - `streaming=false` from the start (history load) returns the text as-is.
 * - When streaming ends mid-reveal, the tail finishes animating — no snap.
 * - A non-append change (session swap, part replaced) snaps to the new text.
 */
export function useSmoothText(text: string, streaming: boolean): string {
  // Non-browser / no-rAF environments render immediately.
  const canAnimate = typeof requestAnimationFrame === "function"
  // A part that mounts already-complete (history) never animates; a part that
  // has ever streamed keeps animating through its final catch-up.
  const everStreamedRef = useRef(streaming)
  if (streaming) everStreamedRef.current = true

  // Render reads stateRef (the source of truth); this state exists only to
  // trigger a re-render when the reveal advances. It is a monotonic counter,
  // NOT the visible count: the replacement-snap path mutates stateRef during
  // render without setState, so a count-valued state could later be set to an
  // equal (stale) value and React would bail out of the re-render, freezing
  // the reveal on a truncated prefix.
  const [, bumpRender] = useState(0)
  const stateRef = useRef({ visible: streaming && canAnimate ? 0 : text.length, lastTs: 0, carry: 0 })
  const textRef = useRef(text)
  const rafIdRef = useRef<number | null>(null)

  // Detect replacement (not an append): the previously revealed prefix no
  // longer prefixes the new text, or the text shrank. Snap to the new value.
  if (text.length < stateRef.current.visible || !text.startsWith(textRef.current.slice(0, stateRef.current.visible))) {
    stateRef.current.visible = text.length
    stateRef.current.carry = 0
  }
  textRef.current = text

  useEffect(() => {
    if (!canAnimate || !everStreamedRef.current) return
    if (stateRef.current.visible >= textRef.current.length) return
    if (rafIdRef.current !== null) return

    const step = (ts: number) => {
      rafIdRef.current = null
      const st = stateRef.current
      const target = textRef.current.length
      const backlog = target - st.visible
      if (backlog <= 0) {
        st.lastTs = 0
        return
      }
      const dt = st.lastTs > 0 ? Math.min((ts - st.lastTs) / 1000, 0.1) : 1 / 60
      st.lastTs = ts
      const cps = BASE_CPS + backlog * CATCHUP_PER_CHAR
      const advanceExact = cps * dt + st.carry
      let advance = Math.max(1, Math.floor(advanceExact))
      st.carry = Math.max(0, advanceExact - advance)
      let next = Math.min(target, st.visible + advance)
      // Never end the visible prefix on a lone high surrogate — it would
      // render as U+FFFD until the next frame completes the pair.
      const lastCode = textRef.current.charCodeAt(next - 1)
      if (next < target && lastCode >= 0xd800 && lastCode <= 0xdbff) next += 1
      st.visible = next
      bumpRender((n) => n + 1)
      if (next < target) {
        rafIdRef.current = requestAnimationFrame(step)
      } else {
        st.lastTs = 0
      }
    }
    rafIdRef.current = requestAnimationFrame(step)
    return () => {
      if (rafIdRef.current !== null) {
        cancelAnimationFrame(rafIdRef.current)
        rafIdRef.current = null
      }
    }
  }, [text, streaming, canAnimate])

  if (!canAnimate || !everStreamedRef.current) return text
  const shown = stateRef.current.visible
  return shown >= text.length ? text : text.slice(0, shown)
}
