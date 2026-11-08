/**
 * Present and Play take the whole screen (Harbor 2 v87). The overlay itself is
 * already fixed over the app; this asks the browser for real full screen too,
 * and quietly stays in the overlay where the browser refuses (iOS, an iframe,
 * no user gesture).
 */
export function enterFullscreen(): void {
  if (typeof document === "undefined" || document.fullscreenElement) return
  try { void document.documentElement.requestFullscreen?.()?.catch?.(() => {}) } catch { /* the overlay still covers the app */ }
}

export function exitFullscreen(): void {
  if (typeof document === "undefined" || !document.fullscreenElement) return
  try { void document.exitFullscreen?.()?.catch?.(() => {}) } catch { /* already left */ }
}
