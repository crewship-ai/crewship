"use client"

import type { ReactNode } from "react"
import { CrewshipLogoTile } from "@/components/branding/crewship-logo"
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
          <h2 className="mt-3 text-balance text-2xl font-extrabold leading-[1.05] tracking-[-0.033em] lg:text-[clamp(28px,2.9vw,40px)]">
            {headline}
          </h2>
          <p className="mt-3 max-w-[36ch] text-sm leading-relaxed text-white/80">
            {blurb}
          </p>
        </div>
      </div>

      {/* LEFT on desktop */}
      <div className="relative flex flex-col p-6 lg:p-[clamp(24px,3.2vw,44px)]">
        <div className="flex items-center gap-2.5">
          {/* size/rounded/iconSize are props, not className — passing sizing
              through className would collide with the component's defaults.
              An explicit radius, because the project's --radius is 1.3rem
              and rounded-lg derives from it, which is nearly a circle at
              28px. */}
          <CrewshipLogoTile size="h-7 w-7" rounded="rounded-[8px]" iconSize="h-4 w-4" />
          <span className="text-[15px] font-bold tracking-[-0.01em]">Crewship</span>
        </div>
        <div className="flex min-h-0 flex-1 items-center py-8">
          <div className="mx-auto w-full max-w-[352px]">{children}</div>
        </div>
      </div>
    </div>
  )
}
