import { isNotificationAudioReady, playNotificationSound, readSoundPreferences, type SoundId } from "@/lib/notification-sounds"

export type AutomaticSoundCapability = "available" | "unsupported-locks" | "unavailable-storage"
/** Call from an interaction, not render: shared-storage availability needs a probe. */
export function getAutomaticSoundCapability(): AutomaticSoundCapability {
  if (typeof navigator === "undefined" || typeof navigator.locks?.request !== "function") return "unsupported-locks"
  let key: string | undefined
  try {
    key = "crewship:sound-capability:" + crypto.randomUUID()
    localStorage.setItem(key, key)
    return localStorage.getItem(key) === key ? "available" : "unavailable-storage"
  } catch {
    return "unavailable-storage"
  } finally {
    if (key) { try { localStorage.removeItem(key) } catch { /* Storage may be denied. */ } }
  }
}

const PREFIX = "crewship:sound-presence:v1:"
const READING_EVENT = "crewship:sound-reading"
const reading = new Map<string, string>()

/** Registered by the actual transcript, including its scroll position. */
export function setSoundReadingConversation(scope: string, id: string, active: boolean) {
  if (active) reading.set(scope, id)
  else if (reading.get(scope) === id) reading.delete(scope)
  if (typeof window !== "undefined") window.dispatchEvent(new Event(READING_EVENT))
}

export function registerSoundPresence(scope: string): () => void {
  const key = PREFIX + encodeURIComponent(scope) + ":" + crypto.randomUUID()
  const update = () => {
    try {
      localStorage.setItem(key, JSON.stringify({
        at: Date.now(),
        conversation: document.visibilityState === "visible" && document.hasFocus() ? reading.get(scope) ?? null : null,
      }))
    } catch { /* Automatic sounds fail closed when shared storage is unavailable. */ }
  }
  update()
  const interval = window.setInterval(update, 5000)
  window.addEventListener("focus", update)
  window.addEventListener("blur", update)
  window.addEventListener(READING_EVENT, update)
  document.addEventListener("visibilitychange", update)
  return () => {
    clearInterval(interval)
    window.removeEventListener("focus", update)
    window.removeEventListener("blur", update)
    window.removeEventListener(READING_EVENT, update)
    document.removeEventListener("visibilitychange", update)
    try { localStorage.removeItem(key) } catch { /* no persistent state required */ }
  }
}

function beingRead(scope: string, conversation: string, now: number): boolean {
  if (document.visibilityState === "visible" && document.hasFocus() && reading.get(scope) === conversation) return true
  const prefix = PREFIX + encodeURIComponent(scope) + ":"
  const stale: string[] = []
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i)
    if (!key?.startsWith(prefix)) continue
    const value = JSON.parse(localStorage.getItem(key) ?? "null")
    if (!value || typeof value.at !== "number" || now - value.at > 15000) stale.push(key)
    else if (value.conversation === conversation) return true
  }
  stale.forEach(key => localStorage.removeItem(key))
  return false
}

export interface SoundCandidate { key: string; category: "chat" | "inbox"; conversation?: string }

/** One bounded ledger per user/workspace, atomically checked across tabs. */
export async function playSoundOnce(scope: string, candidate: SoundCandidate, stillCurrent: () => boolean): Promise<boolean> {
  if (!navigator.locks || !isNotificationAudioReady()) return false
  try {
    return await navigator.locks.request("crewship:sounds:" + scope, { ifAvailable: true }, async lock => {
      if (!lock || !stillCurrent()) return false
      const prefs = readSoundPreferences(scope)
      const sound = prefs[candidate.category]
      if (!prefs.enabled || prefs.dnd || prefs.volume <= 0 || sound === "off") return false
      const key = "crewship:sound-ledger:v1:" + encodeURIComponent(scope)
      const now = Date.now()
      const stored = JSON.parse(localStorage.getItem(key) ?? "null")
      const entries: [string, number][] = Array.isArray(stored?.entries)
        ? stored.entries.filter((v: unknown): v is [string, number] => Array.isArray(v) && typeof v[0] === "string" && typeof v[1] === "number" && now - v[1] < 120000).slice(-255)
        : []
      if (entries.some(([id]) => id === candidate.key)) return false
      // Consume suppressed burst/read events too: a later invalidation must not replay them.
      const silent = (typeof stored?.lastPlayed === "number" && now - stored.lastPlayed < 2000)
        || (!!candidate.conversation && beingRead(scope, candidate.conversation, now))
      localStorage.setItem(key, JSON.stringify({ entries: [...entries, [candidate.key, now]], lastPlayed: silent ? stored?.lastPlayed ?? 0 : now }))
      if (silent || !stillCurrent()) return false
      return playNotificationSound(sound as SoundId, prefs.volume)
    })
  } catch { return false }
}
