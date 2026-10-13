/** Encode one identifier as path data, never URL structure. */
export function encodeApiPathSegment(value: string): string | null {
  if (!value || value === "." || value === "..") return null
  return encodeURIComponent(value)
}
