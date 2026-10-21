/**
 * Which signed-out screen the login page opens on (#3787, wireframe round 64
 * Spojení §4), and what it may say about the person who was signed in.
 *
 *   expired     the session ran out (401 session_expired, a dead refresh, a
 *               session_invalid token)                → "Your session expired"
 *   revoked     the server says the session was ended (session_revoked: a
 *               Log out on another device, an admin, a suspended account, a
 *               refresh replay)                       → "Signed out on this device"
 *   signed-out  this browser pressed Log out          → "You're signed out"
 *
 * The kind travels as `/login?reason=…`. Who was signed in and whether a chat
 * draft is waiting travel in sessionStorage: same tab, gone with it, never in
 * the address bar or the server's logs.
 */

export type SignedOutKind = "expired" | "revoked" | "signed-out"

/** Where an explicit Log out lands. */
export const SIGNED_OUT_URL = "/login?reason=signed-out"

/** Where to go after `signOut()`: the signed-out screen only when the server
 *  confirmed it (false = it did not, and the session may still be alive). */
export function afterSignOut(confirmed: boolean | void): string {
  return confirmed === false ? "/login" : SIGNED_OUT_URL
}

const CONTEXT_KEY = "crewship:signed-out"
/** zustand persist key of stores/composer-store.ts, where chat drafts live. */
const COMPOSER_KEY = "crewship-composer"

/** The kind for an auth event's reason (lib/api-fetch.ts), or null for a
 *  visitor who never had a session (no_credentials), who gets plain sign-in. */
export function signedOutKind(reason: string | undefined | null): SignedOutKind | null {
  if (reason === "no_credentials") return null
  if (reason === "session_revoked") return "revoked"
  return "expired"
}

/** The kind named by `?reason=`, or null for plain sign-in. */
export function signedOutKindFromQuery(value: string | null): SignedOutKind | null {
  return value === "expired" || value === "revoked" || value === "signed-out" ? value : null
}

// The latest composer with unsent text. Module state on purpose: the composer
// reports here as the person types (components/features/chat/composer), and
// the auth provider reads it in the instant before it leaves the page.
let unsentDraft: { key: string; agent: string | undefined } | null = null

/** Called by the chat composer on every change of its persisted draft. */
export function noteUnsentDraft(key: string, agent: string | undefined, text: string): void {
  if (text.trim()) unsentDraft = { key, agent }
  else if (!key || unsentDraft?.key === key) unsentDraft = null
}

function storedDraft(key: string): string {
  try {
    const raw = localStorage.getItem(COMPOSER_KEY)
    const drafts = raw ? (JSON.parse(raw)?.state?.drafts as Record<string, unknown> | undefined) : undefined
    const text = drafts?.[key]
    return typeof text === "string" ? text : ""
  } catch {
    return ""
  }
}

/** Remember, for this tab's login page, who was signed in and which agent an
 *  unsent message is for. Best effort: the screen reads fine without it. */
export function rememberSignedOut(email: string | undefined | null): void {
  try {
    const draft = unsentDraft && storedDraft(unsentDraft.key).trim() ? unsentDraft : null
    sessionStorage.setItem(CONTEXT_KEY, JSON.stringify({ email: email || null, draftKey: draft?.key ?? null, draftAgent: draft ? draft.agent ?? "" : null }))
  } catch { /* storage unavailable */ }
}

/**
 * What the signed-out screen may say. The draft is named only while it is
 * still on this device — the composer persists it per user and session, and
 * nothing on the way to the login page removes it, so signing back in as the
 * same person restores it in the conversation.
 */
export function readSignedOut(): { email: string | null; draftAgent: string | null } {
  try {
    const raw = sessionStorage.getItem(CONTEXT_KEY)
    if (!raw) return { email: null, draftAgent: null }
    const ctx = JSON.parse(raw) as { email?: unknown; draftKey?: unknown; draftAgent?: unknown }
    const email = typeof ctx.email === "string" && ctx.email ? ctx.email : null
    const kept = typeof ctx.draftKey === "string" && storedDraft(ctx.draftKey).trim() !== ""
    const draftAgent = kept && typeof ctx.draftAgent === "string" ? ctx.draftAgent : null
    return { email, draftAgent }
  } catch {
    return { email: null, draftAgent: null }
  }
}

export function forgetSignedOut(): void {
  try { sessionStorage.removeItem(CONTEXT_KEY) } catch { /* storage unavailable */ }
}
