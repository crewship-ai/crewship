import { createAvatar, type Style } from "@dicebear/core"
import * as botttsNeutral from "@dicebear/bottts-neutral"
import * as adventurer from "@dicebear/adventurer"
import * as funEmoji from "@dicebear/fun-emoji"
import * as pixelArt from "@dicebear/pixel-art"
import * as micah from "@dicebear/micah"
import * as notionists from "@dicebear/notionists"
import * as thumbs from "@dicebear/thumbs"
import * as lorelei from "@dicebear/lorelei"
import * as bigSmile from "@dicebear/big-smile"
import * as avataaars from "@dicebear/avataaars"

/** Map of available DiceBear avatar styles, keyed by style slug. */
export const AVATAR_STYLES: Record<string, { label: string; style: Style<object> }> = {
  "bottts-neutral": { label: "Robots", style: botttsNeutral as unknown as Style<object> },
  adventurer: { label: "Adventurer", style: adventurer as unknown as Style<object> },
  "fun-emoji": { label: "Fun Emoji", style: funEmoji as unknown as Style<object> },
  "pixel-art": { label: "Pixel Art", style: pixelArt as unknown as Style<object> },
  micah: { label: "Micah", style: micah as unknown as Style<object> },
  notionists: { label: "Notionists", style: notionists as unknown as Style<object> },
  thumbs: { label: "Thumbs", style: thumbs as unknown as Style<object> },
  lorelei: { label: "Lorelei", style: lorelei as unknown as Style<object> },
  "big-smile": { label: "Big Smile", style: bigSmile as unknown as Style<object> },
  avataaars: { label: "Avataaars", style: avataaars as unknown as Style<object> },
}

/** Default avatar style used when an agent has no explicit style set. */
export const DEFAULT_AVATAR_STYLE = "bottts-neutral"

const _avatarCache = new Map<string, string>()

/**
 * Generate a DiceBear avatar data URI for an agent. Results are cached in memory.
 * @param seed - Deterministic seed for avatar generation (typically the agent slug).
 * @param styleName - Avatar style key from AVATAR_STYLES; defaults to bottts-neutral.
 */
export function getAgentAvatarUrl(seed: string, styleName?: string | null): string {
  const key = `${styleName ?? DEFAULT_AVATAR_STYLE}:${seed}`
  const cached = _avatarCache.get(key)
  if (cached) return cached
  const entry = AVATAR_STYLES[styleName || DEFAULT_AVATAR_STYLE] ?? AVATAR_STYLES[DEFAULT_AVATAR_STYLE]
  const uri = createAvatar(entry.style, { seed, size: 128 }).toDataUri()
  _avatarCache.set(key, uri)
  return uri
}

