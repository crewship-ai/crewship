/** Encode one identifier as path data, never URL structure. */
export function encodeApiPathSegment(value: string): string | null {
  if (!value || value === "." || value === "..") return null
  return encodeURIComponent(value)
}

/** For asynchronous command handlers: invalid identifiers fail before dispatch. */
export function requireApiPathSegment(value: string): string {
  const segment = encodeApiPathSegment(value)
  if (segment === null) throw new TypeError("Invalid API resource identifier")
  return segment
}
