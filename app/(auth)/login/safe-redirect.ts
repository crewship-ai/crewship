/** Whitelist for the post-login redirect target. Only allow same-origin
 *  relative paths — block protocol-relative (`//evil`), absolute URLs,
 *  and `/login` itself (which would just bounce back here). */
export function safeRedirectPath(raw: string | null): string {
  if (!raw) return "/"
  if (!raw.startsWith("/") || raw.startsWith("//")) return "/"
  // Mirror the server-side isSafeRedirect (internal/api/helpers.go):
  // reject the protocol-relative `/\` bypass and any backslash anywhere.
  // Browsers normalize "\" → "/", so "\\evil.com" or "/\evil.com" would
  // become protocol-relative URLs if this ever feeds window.location.
  if (raw.startsWith("/\\") || raw.includes("\\")) return "/"
  // Block every shape that would bounce the user back to /login —
  // bare /login, /login?…, /login/…, AND /login#hash. The fragment
  // form was the missing branch: a fragment-only redirect would
  // otherwise satisfy the !startsWith("/login?") test.
  if (
    raw === "/login" ||
    raw.startsWith("/login?") ||
    raw.startsWith("/login/") ||
    raw.startsWith("/login#")
  ) {
    return "/"
  }
  return raw
}

