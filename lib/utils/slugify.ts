/**
 * Convert a text string into a URL-friendly slug.
 *
 * Lowercase, replace spaces with hyphens, remove non-alphanumeric
 * characters (except hyphens), collapse multiple hyphens, and trim
 * hyphens from edges.
 */
export function slugify(text: string): string {
  return text
    .toLowerCase()
    .trim()
    .replace(/\s+/g, "-")
    .replace(/[^a-z0-9-]/g, "")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
}
