"use client"

import { useEffect, useRef } from "react"
import { useReducedMotion } from "motion/react"
import { MARK_SAILS, MARK_GEOMETRY } from "@/lib/brand-mark"

/**
 * The Crewship mark, blown up to fill a panel and animated.
 *
 * The logo is one `<path>`, but that path is three subpaths — three sails.
 * lib/brand-mark.ts splits them so each can carry its own motion while the
 * silhouette stays exactly the mark we already ship: this is the logo
 * moving, not an illustration of it.
 *
 * Each sail runs three independent sines — bob, swell, heel — on periods
 * that do not divide into each other, so the loop never visibly repeats and
 * the sails never sync into a pulse. Rotation is the channel that reads as
 * wind rather than as animation, which is why it is there at all.
 *
 * Canvas rather than SVG + motion: three Path2D fills and one clipped
 * gradient is about eight draw calls a frame, where three transform-animated
 * 2.5 KB paths make the compositor re-rasterise them every frame.
 */

export type MarkMotion = "swell" | "assemble" | "drift"

/** Per-sail periods in ms. Coprime-ish on purpose — see the note above. */
const SAILS = [
  { bob: 5400, swell: 7800, heel: 9100, phase: 0 },
  { bob: 6900, swell: 9400, heel: 11600, phase: 2.1 },
  { bob: 8100, swell: 11200, heel: 13700, phase: 4.2 },
] as const

const MOTION: Record<MarkMotion, { amp: number; sweep: number; replay: boolean }> = {
  swell: { amp: 1, sweep: 7000, replay: false },
  assemble: { amp: 0.55, sweep: 5200, replay: true },
  drift: { amp: 0.28, sweep: 9000, replay: false },
}

const BOB = 0.022 // of mark height
const SWELL = 0.035 // scale, about the sail's foot
const HEEL = 1.5 // degrees, about the sail's foot
const ENTRANCE_MS = 900
const STAGGER_MS = 90

const GLOW_DARK = "rgba(255,255,255,.16)"
const GLOW_LIGHT = "rgba(255,255,255,.22)"

interface Props {
  /** Which motion the panel runs. Defaults to the continuous one. */
  variant?: MarkMotion
  /**
   * Change this to fire a light sweep across the mark — and, in the
   * "assemble" variant, to replay the entrance. Onboarding passes its step
   * so the panel answers the form instead of running as ambient wallpaper.
   */
  replayKey?: string | number
  className?: string
}

export function AnimatedMark({ variant = "swell", replayKey, className }: Props) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  // Animation state lives in refs: it changes 60 times a second and nothing
  // outside the draw loop reads it, so putting it in state would re-render
  // the tree for no one's benefit.
  const enterAtRef = useRef(0)
  const sweepAtRef = useRef(0)
  const clockRef = useRef(0)
  const reduced = useReducedMotion()

  // Fire the sweep (and, for "assemble", the entrance) on demand. Kept out
  // of the draw effect so a step change does not tear down the RAF loop.
  useEffect(() => {
    if (replayKey === undefined) return
    sweepAtRef.current = clockRef.current
    if (MOTION[variant].replay) enterAtRef.current = clockRef.current
  }, [replayKey, variant])

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return

    const ctx = canvas.getContext("2d")
    // No 2D context in happy-dom, and DOMMatrix is missing in some test
    // environments. The wrapper's CSS gradient still paints, so the panel
    // degrades to the brand colour rather than to a blank rectangle.
    if (!ctx || typeof DOMMatrix === "undefined") return

    const paths = MARK_SAILS.map((d) => new Path2D(d))
    const motion = MOTION[variant]
    const G = MARK_GEOMETRY

    let width = 0
    let height = 0
    let base = new DOMMatrix()
    let dark = true
    let frame = 0
    let running = true
    // pointer parallax, damped toward the target so it never snaps
    let px = 0
    let py = 0
    let targetX = 0
    let targetY = 0

    const readTheme = () => {
      dark = document.documentElement.classList.contains("dark")
    }

    const resize = () => {
      const rect = canvas.getBoundingClientRect()
      const dpr = Math.min(2, window.devicePixelRatio || 1)
      width = rect.width
      height = rect.height
      canvas.width = Math.round(width * dpr)
      canvas.height = Math.round(height * dpr)
      base = new DOMMatrix([dpr, 0, 0, dpr, 0, 0])
    }

    const entrance = (i: number) => {
      if (reduced) return 1
      const t = (clockRef.current - enterAtRef.current - i * STAGGER_MS) / ENTRANCE_MS
      const p = Math.min(1, Math.max(0, t))
      return 1 - Math.pow(1 - p, 3)
    }

    /**
     * Where the mark sits and how big. Kept fully inside the frame so it
     * still reads as the logo rather than as a cropped texture.
     *
     * Two placements, because the panel has two shapes. As a tall column it
     * sits high and right of centre, leaving the lower-left corner for the
     * headline. Below lg the panel is a wide, short banner with no copy in
     * it, so the mark centres and takes more of the height instead of
     * shrinking into a corner of it.
     */
    const markBox = () => {
      const banner = height > 0 && width / height > 1.6
      return banner
        ? {
            k: (height * 0.62) / G.height,
            cx: width * 0.5 + px,
            cy: height * 0.5 + py,
          }
        : {
            k: Math.min(height * 0.52, width * 0.62) / G.height,
            cx: width * 0.56 + px,
            cy: height * 0.4 + py,
          }
    }

    /**
     * One matrix per sail. Returned rather than applied so the same
     * transform drives both the fill and the clip for the light sweep —
     * they cannot drift apart into a sweep that leaks off the mark.
     */
    const sailMatrix = (i: number) => {
      const s = SAILS[i] ?? SAILS[SAILS.length - 1]
      const box = markBox()
      const now = clockRef.current
      const e = entrance(i)
      const bob = Math.sin((now / s.bob) * Math.PI * 2 + s.phase) * G.height * BOB * motion.amp
      const swell = 1 + Math.sin((now / s.swell) * Math.PI * 2 + s.phase * 1.3) * SWELL * motion.amp
      const heel = Math.sin((now / s.heel) * Math.PI * 2 + s.phase * 0.7) * HEEL * motion.amp
      const foot = G.feet[i] ?? G.cx
      const scale = swell * (0.96 + 0.04 * e)
      return new DOMMatrix()
        .translateSelf(box.cx, box.cy)
        .scaleSelf(box.k, box.k)
        .translateSelf(-G.cx, -G.cy)
        .translateSelf(0, bob + (1 - e) * G.height * 0.12)
        .translateSelf(foot, G.baseline)
        .rotateSelf(heel)
        .scaleSelf(scale, scale)
        .translateSelf(-foot, -G.baseline)
    }

    const draw = () => {
      ctx.setTransform(base)
      ctx.clearRect(0, 0, width, height)

      px += (targetX - px) * 0.06
      py += (targetY - py) * 0.06
      const box = markBox()

      // a breathing bloom, so the mark sits in light rather than on flat blue
      const breath = reduced ? 0 : Math.sin((clockRef.current / 4300) * Math.PI * 2) * 0.04
      const radius = box.k * G.height * (0.78 + breath)
      if (radius > 0) {
        const bloom = ctx.createRadialGradient(box.cx, box.cy, 0, box.cx, box.cy, radius)
        bloom.addColorStop(0, dark ? GLOW_DARK : GLOW_LIGHT)
        bloom.addColorStop(1, "rgba(255,255,255,0)")
        ctx.fillStyle = bloom
        ctx.fillRect(0, 0, width, height)
      }

      const mats = paths.map((_, i) => sailMatrix(i))
      paths.forEach((path, i) => {
        ctx.setTransform(base.multiply(mats[i]))
        ctx.globalAlpha = entrance(i)
        ctx.fillStyle = "#fff"
        ctx.fill(path)
      })
      ctx.setTransform(base)
      ctx.globalAlpha = 1

      // Specular sweep, clipped to the live union of the moving sails.
      // Phase runs from the last trigger so a step change restarts it
      // cleanly instead of cutting into the middle of a free-running one.
      const cycle = reduced ? 2 : ((clockRef.current - sweepAtRef.current) / motion.sweep) % 1
      if (cycle < 1 && cycle >= 0) {
        const union = new Path2D()
        mats.forEach((m, i) => union.addPath(paths[i], m))
        ctx.save()
        ctx.clip(union)
        const span = box.k * G.width
        const x = box.cx - span * 1.1 + cycle * span * 2.4
        const sweep = ctx.createLinearGradient(
          x - span * 0.34,
          box.cy - span * 0.5,
          x + span * 0.34,
          box.cy + span * 0.5
        )
        sweep.addColorStop(0, "rgba(255,255,255,0)")
        sweep.addColorStop(0.5, "rgba(190,222,255,.55)")
        sweep.addColorStop(1, "rgba(255,255,255,0)")
        ctx.fillStyle = sweep
        ctx.fillRect(0, 0, width, height)
        ctx.restore()
      }
    }

    const tick = (ts: number) => {
      if (!running) return
      clockRef.current = ts
      draw()
      frame = requestAnimationFrame(tick)
    }

    const onPointerMove = (e: PointerEvent) => {
      if (reduced) return
      const rect = canvas.getBoundingClientRect()
      targetX = ((e.clientX - rect.left) / rect.width - 0.5) * 20
      targetY = ((e.clientY - rect.top) / rect.height - 0.5) * 20
    }
    const onPointerLeave = () => {
      targetX = 0
      targetY = 0
    }

    // A background tab should not hold a 60 Hz loop open.
    const onVisibility = () => {
      if (document.hidden) {
        running = false
        cancelAnimationFrame(frame)
      } else if (!running && !reduced) {
        running = true
        frame = requestAnimationFrame(tick)
      }
    }

    const themeObserver = new MutationObserver(readTheme)
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["class"],
    })
    const resizeObserver = new ResizeObserver(() => {
      resize()
      if (reduced) draw()
    })
    resizeObserver.observe(canvas)

    readTheme()
    resize()
    canvas.addEventListener("pointermove", onPointerMove)
    canvas.addEventListener("pointerleave", onPointerLeave)
    document.addEventListener("visibilitychange", onVisibility)

    if (reduced) {
      // Settle to one composed frame: entrance complete, sails at rest.
      // The panel still looks finished standing still.
      clockRef.current = 0
      draw()
    } else {
      frame = requestAnimationFrame(tick)
    }

    return () => {
      running = false
      cancelAnimationFrame(frame)
      themeObserver.disconnect()
      resizeObserver.disconnect()
      canvas.removeEventListener("pointermove", onPointerMove)
      canvas.removeEventListener("pointerleave", onPointerLeave)
      document.removeEventListener("visibilitychange", onVisibility)
    }
  }, [variant, reduced])

  return (
    <canvas
      ref={canvasRef}
      aria-hidden="true"
      data-testid="animated-mark"
      className={className ?? "absolute inset-0 h-full w-full"}
    />
  )
}
