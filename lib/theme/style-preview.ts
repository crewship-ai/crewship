import type { Scheme } from "@/lib/theme/appearance-style"

/**
 * Attributes that make one element wear a style, for the gallery's previews.
 * The style's token block applies to any element carrying its `data-style`,
 * and `.dark` selects the dark palette, so a tile can show a style in a scheme
 * the page is not in. Components spread this rather than naming the attribute:
 * which attribute selects a style is the theme's business (a test keeps the
 * rest of the app from knowing it).
 */
export function stylePreviewAttrs(id: string, scheme: Scheme): { "data-style": string; className?: string } {
  return scheme === "dark" ? { "data-style": id, className: "dark" } : { "data-style": id }
}
