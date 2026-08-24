"use client"

import type { ReactNode } from "react"
import { CrewshipLogo } from "@/components/branding/crewship-logo"
import { AnimatedMark, type MarkMotion } from "@/components/branding/animated-mark"

/**
 * Two-pane shell for the unauthenticated screens: form on the left, the
 * animated brand mark on the right.
 *
 * The gradient is lifted from the logo file's own `#cs-readme-bg`
 * (#1B75FE → #2B90FF) rather than invented, so the panel is the mark's tile
 * at full bleed. Its top is deepened toward #0A3FA8 because the app renders
 * dark and the undeepened brand blue glares next to a near-black form.
 *
 * It is a CSS gradient, not part of the canvas: if the canvas cannot get a
 * 2D context the panel degrades to the brand colour instead of to a blank
 * rectangle, and it is painted server-side so there is no flash of empty
 * panel before hydration.
 *
 * Below `lg` the panel becomes a short banner above the form — tall enough
 * to read as the mark, short enough that the form stays above the fold on a
 * phone.
 */

const PANEL_GRADIENT =
  "linear-gradient(160deg, #062a86 0%, #1257c8 45%, #1e7bfe 80%, #2b90ff 100%)"

interface Props {
  /** The form. Rendered in a readable column on the left pane. */
  children: ReactNode
  /** Small uppercase line above the panel headline. */
  eyebrow: string
  /** Panel headline. Two lines at most at the sizes below. */
  headline: string
  /** One sentence under the headline. */
  blurb: string
  variant?: MarkMotion
  /** Changing this fires a light sweep across the mark. */
  replayKey?: string | number
}

export function AuthSplitShell({
  children,
  eyebrow,
  headline,
  blurb,
  variant,
  replayKey,
}: Props) {
  return (
    <div className="grid min-h-screen grid-rows-[12rem_1fr] lg:grid-cols-[minmax(0,44fr)_minmax(0,56fr)] lg:grid-rows-1">
      {/* RIGHT on desktop, banner on top below lg */}
      <div
        className="relative order-first overflow-hidden lg:order-last"
        style={{ background: PANEL_GRADIENT }}
      >
        <AnimatedMark variant={variant} replayKey={replayKey} />
        {/* Shade only the corner the copy sits in. Painted over the canvas
            would dim the lower half of the mark to grey. */}
        <div
          className="pointer-events-none absolute inset-0"
          style={{
            background:
              "linear-gradient(to bottom right, transparent 55%, rgba(3,20,60,.30) 100%)",
          }}
        />
        {/* Hidden below lg: in a 12rem banner the headline runs straight
            through the sails, and on a phone the form is what the screen is
            for. The banner degrades to a clean brand strip. */}
        <div className="absolute z-10 hidden text-white lg:block lg:inset-x-[clamp(28px,3.4vw,52px)] lg:bottom-[clamp(40px,6vh,64px)] lg:max-w-[min(460px,62%)]">
          <p className="font-mono text-[11px] uppercase tracking-[0.09em] text-white/70">
            {eyebrow}
          </p>
          {/* A <p>, not a heading. The panel is order-first in the DOM, so an
              h2 here would be the first heading a screen reader meets and
              would sit above the form's own h1 in the outline. This copy is
              branding, not a document section. */}
          <p className="mt-3 text-balance text-2xl font-extrabold leading-[1.05] tracking-[-0.033em] lg:text-[clamp(28px,2.9vw,40px)]">
            {headline}
          </p>
          <p className="mt-3 max-w-[36ch] text-sm leading-relaxed text-white/80">
            {blurb}
          </p>
        </div>
      </div>

      {/* LEFT on desktop */}
      <div className="relative flex flex-col p-6 lg:p-[clamp(24px,3.2vw,44px)]">
        <div className="flex items-center gap-3">
          {/* The bare mark, not the tile. A squircle here would put the
              silhouette inside two nested boxes — the tile's padding and the
              viewBox's — and at lockup size the sails stop being readable.
              `tight` crops to the mark's own bounds; w-auto keeps its 1.07:1
              aspect. */}
          <CrewshipLogo tight className="h-9 w-auto text-foreground" />
          <span className="text-lg font-bold tracking-[-0.015em]">Crewship</span>
        </div>
        <div className="flex min-h-0 flex-1 items-center py-8">
          <div className="mx-auto w-full max-w-[352px]">{children}</div>
        </div>
      </div>
    </div>
  )
}
