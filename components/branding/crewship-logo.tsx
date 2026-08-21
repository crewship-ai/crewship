import { useId, type SVGProps } from "react"

import { SAIL_PATH } from "@/lib/brand-mark"

/**
 * Crewship brand mark — the cruise-ship sail silhouette.
 *
 * The path itself lives in lib/brand-mark.ts, which is the single source of
 * truth: components/branding/animated-mark.tsx splits that same string into
 * its three sails, and a second copy here would drift the first time the
 * mark is redrawn. Re-exported so existing importers keep working.
 *
 * Rendered inline (not <img src>) so:
 *   - No network fetch, no flash on first paint
 *   - `currentColor` fill picks up any text-* Tailwind utility
 *   - Path data lives in the JS bundle, immune to favicon cache layers
 */
export { SAIL_PATH }

/**
 * Just the sail silhouette — transparent background, currentColor fill.
 * Use inside any tinted container (`bg-primary` square, navy circle, etc.)
 * or apply a `text-*` class to color it directly.
 */
export function CrewshipLogo(props: SVGProps<SVGSVGElement>) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 1024 1024"
      fill="currentColor"
      role="img"
      aria-label="Crewship"
      {...props}
    >
      <g transform="translate(194.6 234.9) scale(0.79360 0.79360)">
        <path d={SAIL_PATH} />
      </g>
    </svg>
  )
}

/**
 * Full brand mark — squircle backdrop + sail fill. Standalone variant
 * matching the static SVGs in public/brand/. Use for hero areas, error
 * pages, and other places where the logo is the primary visual.
 *
 * `variant` mirrors the 4 brand assets in public/brand/:
 *   - "navy-white" — navy squircle + white sail (default; high contrast)
 *   - "navy-blue"  — navy squircle + blue gradient sail
 *   - "blue-white" — blue gradient squircle + white sail
 *   - "white-blue" — white squircle + blue gradient sail
 */
type LogoVariant = "navy-white" | "navy-blue" | "blue-white" | "white-blue"

const VARIANTS: Record<
  LogoVariant,
  { bgFill: string; sailFill: string | "gradient-sail" | "gradient-bg" }
> = {
  "navy-white": { bgFill: "#253043",     sailFill: "#FFFFFF" },
  "navy-blue":  { bgFill: "#253043",     sailFill: "gradient-sail" },
  "blue-white": { bgFill: "gradient-bg", sailFill: "#FFFFFF" },
  "white-blue": { bgFill: "#FFFFFF",     sailFill: "gradient-sail" },
}

export function CrewshipLogoMark({
  variant = "navy-white",
  ...props
}: SVGProps<SVGSVGElement> & { variant?: LogoVariant }) {
  // Stable IDs per instance — required because multiple <svg> in the
  // same page would otherwise collide on `id="cs-bg"` / `id="cs-sail"`.
  const reactId = useId().replace(/:/g, "")
  const squircleId = `cs-sq-${reactId}`
  const bgGradId = `cs-bg-${reactId}`
  const sailGradId = `cs-sail-${reactId}`
  const v = VARIANTS[variant]
  const bgFill = v.bgFill === "gradient-bg" ? `url(#${bgGradId})` : v.bgFill
  const sailFill = v.sailFill === "gradient-sail" ? `url(#${sailGradId})` : v.sailFill
  const showSailGradient = v.sailFill === "gradient-sail"
  const showBgGradient = v.bgFill === "gradient-bg"
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 1024 1024"
      role="img"
      aria-label="Crewship"
      {...props}
    >
      <defs>
        {showBgGradient && (
          <linearGradient id={bgGradId} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stopColor="#1B75FE" />
            <stop offset="1" stopColor="#2B90FF" />
          </linearGradient>
        )}
        {showSailGradient && (
          <linearGradient id={sailGradId} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stopColor="#73B1F4" />
            <stop offset="1" stopColor="#2E81FE" />
          </linearGradient>
        )}
        <clipPath id={squircleId}>
          <rect x="0" y="0" width="1024" height="1024" rx="230" ry="230" />
        </clipPath>
      </defs>
      <g clipPath={`url(#${squircleId})`}>
        <rect width="1024" height="1024" fill={bgFill} />
        <g
          transform="translate(194.6 234.9) scale(0.79360 0.79360)"
          fill={sailFill}
        >
          <path d={SAIL_PATH} />
        </g>
      </g>
    </svg>
  )
}

/**
 * `CrewshipLogoTile` — sail mark inside the brand "blue-white" tile.
 * Convenience wrapper for the common "logo on rounded blue square"
 * pattern used in login/signup hero, onboarding welcome, error pages.
 *
 * The default tint mirrors `crewship-blue-white.svg` exactly:
 *   - vertical gradient `#1B75FE → #2B90FF`
 *   - white sail (`text-white` so `currentColor` resolves)
 *   - 230/1024 corner radius (≈ `rounded-2xl` at the default 48px size)
 *
 * That way the favicon (which uses the same SVG variant) and any
 * in-app tile that uses this component remain visually identical.
 * Pass Tailwind utilities via `className` to override.
 */
export function CrewshipLogoTile({
  className,
  size = "h-12 w-12",
  rounded = "rounded-2xl",
  iconSize = "h-6 w-6",
}: {
  className?: string
  size?: string
  rounded?: string
  iconSize?: string
}) {
  return (
    <div
      className={`flex items-center justify-center bg-gradient-to-b from-[#1B75FE] to-[#2B90FF] text-white ${size} ${rounded} ${className ?? ""}`}
    >
      <CrewshipLogo className={iconSize} />
    </div>
  )
}
