"use client"

import { useLayoutEffect, useRef, useState, useSyncExternalStore, type ImgHTMLAttributes, type RefObject } from "react"

import { getAgentAvatarUrl, isAnimatableAvatarStyle, resolveAvatarStyle, shouldAnimateAvatar } from "@/lib/agent-avatar"
import {
  DEFAULT_AVATAR_ANIMATION,
  DEFAULT_MOTION,
  readAvatarAnimation,
  readMotion,
  subscribeAppearance,
} from "@/lib/theme/appearance"
import { prefersReducedMotion, subscribeReducedMotion } from "@/lib/motion"
import { resolveStoredAvatarSrc } from "@/lib/agent-avatar-persist"
import { useAvatarStylesVersion } from "@/hooks/use-avatar-styles"
import { cn } from "@/lib/utils"

/**
 * Props for {@link AgentAvatar}. Extends the native <img> attributes so
 * callers can keep passing the same `alt`, `title`, `aria-hidden`,
 * `data-testid`, etc. they used on the inline tags this replaces.
 *
 * `src` is owned by the component (derived from seed/style) and `style`
 * is renamed to the avatar *style slug* — neither is forwarded as a raw
 * img attribute, so both are omitted from the inherited prop set.
 */
export interface AgentAvatarProps
  extends Omit<ImgHTMLAttributes<HTMLImageElement>, "src" | "style"> {
  /** Deterministic avatar seed (agent slug, id, or display name). */
  seed: string
  /** DiceBear style slug; falls back to the default when null/unknown. */
  style?: string | null
  /** Alt text. Defaults to "" so decorative avatars stay out of the a11y tree. */
  alt?: string
  /**
   * `avatar_url` from the API: the agent's stored render, or null/undefined
   * when it has none and should be generated from the seed.
   */
  avatarUrl?: string | null
  /** Never animate, whatever the size and the viewer's settings. */
  still?: boolean
}

const serverMotion = () => DEFAULT_MOTION
const serverAvatarAnimation = () => DEFAULT_AVATAR_ANIMATION
const serverReduced = () => false

/**
 * The rendered width of an animatable avatar, in CSS px; 0 until measured
 * and for any style that cannot move (nothing to decide, so nothing to
 * observe). Measured rather than passed in because ~100 call sites size the
 * avatar with a class, and the text-size setting scales those classes.
 */
function useRenderedWidth(ref: RefObject<HTMLImageElement | null>, active: boolean): number {
  const [width, setWidth] = useState(0)
  useLayoutEffect(() => {
    const el = ref.current
    if (!active || !el) return
    setWidth(el.offsetWidth)
    if (typeof ResizeObserver === "undefined") return
    const ro = new ResizeObserver(() => setWidth(el.offsetWidth))
    ro.observe(el)
    return () => ro.disconnect()
  }, [ref, active])
  return active ? width : 0
}

/**
 * Renders an agent's DiceBear avatar as an <img>.
 *
 * Behaviour-preserving wrapper around the ~30 inline
 * `<img src={getAgentAvatarUrl(...)} className="… rounded-full …" />`
 * call sites. The only baked-in class is `rounded-full`; because `cn`
 * runs through tailwind-merge, a caller passing `rounded-lg`,
 * `rounded-xl`, `rounded-2xl`, `rounded-md`, etc. overrides it cleanly,
 * matching the exact shape each former call site rendered. Size and any
 * extra layout classes (`shrink-0`, `ring-*`, `mt-0.5`, …) come through
 * `className`; all other img attributes pass through via `...rest`.
 *
 * Avatar source, in order of preference:
 *
 *   1. the agent's stored render, when it has one and the browser can
 *      actually authenticate the request (see resolveStoredAvatarSrc);
 *   2. generation from (seed, style) — the original behaviour, and the
 *      fallback for everything else, including a stored render that fails
 *      to load. Generating always works, so it is never right to leave a
 *      broken-image icon on screen instead.
 *
 * Voxel avatars (#3789) animate at AVATAR_ANIMATION_MIN_PX and above, and
 * stay still below it, under Reduce motion: On, with Animated avatars off,
 * with `still`, or (Reduce motion on System) when the OS asks for reduced
 * motion — checked here and again by the SVG's own media query. The animation never carries meaning: status always has its own
 * badge, dot or label beside the avatar.
 *
 * Rendering never writes (#2876): an agent without a stored render is drawn
 * from its seed and left alone. Renders are stored where an agent is created
 * or edited, and by the explicit backfill — see lib/agent-avatar-persist.ts.
 */
export function AgentAvatar({
  seed,
  style,
  alt = "",
  className,
  avatarUrl,
  still = false,
  ...rest
}: AgentAvatarProps) {
  // Re-render when a lazy DiceBear collection finishes loading so the
  // placeholder upgrades to the real avatar.
  useAvatarStylesVersion()

  // Set when the stored render fails to load, pinning this avatar to
  // generation for the rest of its mount. Keyed off avatarUrl so a genuinely
  // new render (different ?v=) gets a fresh chance rather than inheriting
  // the previous one's failure.
  const [failedUrl, setFailedUrl] = useState<string | null>(null)

  const storedSrc = resolveStoredAvatarSrc(avatarUrl)
  const useStored = storedSrc !== null && failedUrl !== storedSrc

  const ref = useRef<HTMLImageElement>(null)
  const motion = useSyncExternalStore(subscribeAppearance, readMotion, serverMotion)
  const avatarAnimation = useSyncExternalStore(subscribeAppearance, readAvatarAnimation, serverAvatarAnimation)
  const osReducedMotion = useSyncExternalStore(subscribeReducedMotion, prefersReducedMotion, serverReduced)
  const mayMove = !still && !useStored && isAnimatableAvatarStyle(resolveAvatarStyle(style))
  const sizePx = useRenderedWidth(ref, mayMove)
  const animated = mayMove && shouldAnimateAvatar({ style, sizePx, motion, avatarAnimation, osReducedMotion })

  return (
    <img
      ref={ref}
      src={useStored ? storedSrc : getAgentAvatarUrl(seed, style, animated ? { animated: true } : undefined)}
      data-animated={animated ? "true" : undefined}
      alt={alt}
      className={cn("rounded-full", className)}
      onError={useStored ? () => setFailedUrl(storedSrc) : undefined}
      {...rest}
    />
  )
}
