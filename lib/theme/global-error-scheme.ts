/**
 * The scheme app/global-error.tsx paints in. The root layout — and with it the
 * theme provider that normally sets `.dark` on <html> — is gone by the time
 * that page renders, so the stored choice (next-themes' "theme" key) and the
 * system preference are read directly. The page used to be light-only inline
 * hex: a white screen in the middle of a dark session.
 */
export function globalErrorScheme(): "light" | "dark" {
  if (typeof window === "undefined") return "dark"
  let stored: string | null = null
  try {
    stored = window.localStorage.getItem("theme")
  } catch {
    // Storage may be blocked; fall back to the system preference.
  }
  if (stored === "light" || stored === "dark") return stored
  try {
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"
  } catch {
    return "dark"
  }
}
