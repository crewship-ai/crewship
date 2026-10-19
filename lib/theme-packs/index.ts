import type { ThemePack } from "./types"
import { afterglow } from "./afterglow"

export * from "./types"

/**
 * Every theme pack this build ships. To add one: create lib/theme-packs/<id>.ts,
 * add it here, then run `UPDATE_PACKS_CSS=1 pnpm vitest run lib/theme-packs` to
 * regenerate app/styles/packs.generated.css (docs/guides/theme-packs.mdx).
 */
export const PACKS: ThemePack[] = [afterglow]

export function getPack(id: string | undefined | null): ThemePack | undefined {
  return id ? PACKS.find((p) => p.id === id) : undefined
}

/**
 * The avatar style a pack asks for, only if the caller can render it. A slug
 * this build does not know (an avatar style not shipped yet) yields undefined,
 * so the caller keeps its own default.
 */
export function packAvatarStyle(styleId: string | undefined | null, known: (slug: string) => boolean): string | undefined {
  const slug = getPack(styleId)?.avatarStyle
  return slug && known(slug) ? slug : undefined
}
