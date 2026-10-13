// Invitation proof stays in this tab during sign-in/signup, never in redirect
// URLs. Capture legacy query links too, but replace the current history entry.
const key = "crewship.pending-invitation"

export function captureInvitationProof(): string {
  const url = new URL(window.location.href)
  const fragment = new URLSearchParams(url.hash.slice(1))
  const incoming = fragment.get("invitation_token") ?? url.searchParams.get("invitation_token")
  if (incoming !== null) {
    url.searchParams.delete("invitation_token")
    fragment.delete("invitation_token")
    url.hash = fragment.toString()
    window.history.replaceState(window.history.state, "", url.pathname + url.search + url.hash)
    try { window.sessionStorage.setItem(key, incoming) } catch { /* In-memory acceptance remains possible. */ }
    return incoming
  }
  try { return window.sessionStorage.getItem(key) ?? "" } catch { return "" }
}

export function clearInvitationProof(): void {
  try { window.sessionStorage.removeItem(key) } catch { /* No durable proof was stored. */ }
}
