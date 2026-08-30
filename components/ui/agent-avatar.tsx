"use client"

import { useEffect, useState, type ImgHTMLAttributes } from "react"

import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { queueAvatarBackfill, resolveStoredAvatarSrc } from "@/lib/agent-avatar-persist"
import { useAvatarStylesVersion } from "@/hooks/use-avatar-styles"
import { useCurrentWorkspaceId } from "@/hooks/use-workspace"
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
   * Agent row id, when this avatar belongs to a real persisted agent
   * (#1297). Supplying it opts the agent into having its render stored, so
   * its face stops changing when the generator is upgraded. Omit for
   * avatars that stand for something else — a crew, a skill author, a
   * comment byline — which have no row to store against.
   */
  agentId?: string
  /**
   * `avatar_url` from the API: the agent's stored render, or null/undefined
   * when it has none and should be generated from the seed.
   */
  avatarUrl?: string | null
  /**
   * Workspace to scope the backfill write to. Overrides the workspace store;
   * the dashboard leaves it unset, matching `panel-actions`' `workspaceId`.
   *
   * It exists for surfaces that know their workspace but never mount the
   * store: `/onboarding` renders a real agent's avatar
   * (`onboarding-setup-chat.tsx`) and nothing in `app/(onboarding)/**` calls
   * `useWorkspace()`, so the store stays empty for that whole visit and the
   * backfill would be skipped for the one agent that route shows.
   */
  workspaceId?: string | null
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
 */
export function AgentAvatar({
  seed,
  style,
  alt = "",
  className,
  agentId,
  avatarUrl,
  workspaceId: workspaceIdProp,
  ...rest
}: AgentAvatarProps) {
  // Re-render when a lazy DiceBear collection finishes loading so the
  // placeholder upgrades to the real avatar.
  useAvatarStylesVersion()

  // The backfill PUT is workspace-scoped and 400s without it (#2196). Read
  // here rather than inside lib/ so that module stays free of a store import,
  // matching how lib/conversation-search.ts takes its workspaceId from the
  // caller — and via the subscribe-only reader, so an avatar never becomes
  // the thing that fires GET /api/v1/workspaces.
  //
  // The prop wins where a caller has the id but the store was never loaded —
  // see AgentAvatarProps.workspaceId. Same shape as panel-actions.
  const storeWorkspaceId = useCurrentWorkspaceId()
  const workspaceId = workspaceIdProp ?? storeWorkspaceId

  // Set when the stored render fails to load, pinning this avatar to
  // generation for the rest of its mount. Keyed off avatarUrl so a genuinely
  // new render (different ?v=) gets a fresh chance rather than inheriting
  // the previous one's failure.
  const [failedUrl, setFailedUrl] = useState<string | null>(null)

  const storedSrc = resolveStoredAvatarSrc(avatarUrl)
  const useStored = storedSrc !== null && failedUrl !== storedSrc

  // Offer the server a render for an agent that has none. Fire-and-forget:
  // it self-limits per session and per page load, and a failure only means
  // the agent keeps generating from its seed.
  // workspaceId is in the deps so the attempt re-fires once the workspace
  // store resolves — on a cold load the first paint has none yet, and
  // queueAvatarBackfill deliberately does not spend the agent's one attempt
  // on a call it cannot make.
  useEffect(() => {
    if (!agentId || avatarUrl) return
    void queueAvatarBackfill(agentId, seed, style, workspaceId)
  }, [agentId, avatarUrl, seed, style, workspaceId])

  return (
    <img
      src={useStored ? storedSrc : getAgentAvatarUrl(seed, style)}
      alt={alt}
      className={cn("rounded-full", className)}
      onError={useStored ? () => setFailedUrl(storedSrc) : undefined}
      {...rest}
    />
  )
}
