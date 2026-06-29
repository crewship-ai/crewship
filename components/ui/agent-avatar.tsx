import type { ImgHTMLAttributes } from "react"

import { getAgentAvatarUrl } from "@/lib/agent-avatar"
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
 */
export function AgentAvatar({
  seed,
  style,
  alt = "",
  className,
  ...rest
}: AgentAvatarProps) {
  return (
    <img
      src={getAgentAvatarUrl(seed, style)}
      alt={alt}
      className={cn("rounded-full", className)}
      {...rest}
    />
  )
}
