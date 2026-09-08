/** Local, original Web Audio cues; this module never fetches sound assets. */
export const SOUND_PRESETS = [
  { id: "soft-pop", label: "Soft pop" },
  { id: "glass", label: "Glass" },
  { id: "chime", label: "Chime" },
  { id: "inbox-drop", label: "Inbox drop" },
  { id: "attention", label: "Attention" },
] as const
export type SoundId = typeof SOUND_PRESETS[number]["id"]
export type SoundPreferences = {
  enabled: boolean
  dnd: boolean
  volume: number
  chat: SoundId | "off"
  inbox: SoundId | "off"
}
export const DEFAULT_SOUND_PREFERENCES: SoundPreferences = Object.freeze({
  enabled: false, dnd: false, volume: 0.35, chat: "soft-pop", inbox: "chime",
})
export const SOUND_AUDIO_READY_EVENT = "crewship:notification-audio-ready"
export const SOUND_PREFERENCES_EVENT = "crewship:notification-sound-preferences"
const STORAGE_PREFIX = "crewship:notification-sounds:v1:"
const soundIds = new Set<string>(SOUND_PRESETS.map(preset => preset.id))

function preferences(value: unknown): SoundPreferences {
  const input = value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown> : {}
  const choice = (v: unknown, fallback: SoundId | "off"): SoundId | "off" =>
    typeof v === "string" && (v === "off" || soundIds.has(v)) ? v as SoundId | "off" : fallback
  return {
    enabled: input.enabled === true,
    dnd: input.dnd === true,
    volume: typeof input.volume === "number" && Number.isFinite(input.volume)
      ? Math.min(1, Math.max(0, input.volume)) : DEFAULT_SOUND_PREFERENCES.volume,
    chat: choice(input.chat, DEFAULT_SOUND_PREFERENCES.chat),
    inbox: choice(input.inbox, DEFAULT_SOUND_PREFERENCES.inbox),
  }
}
function storageKey(scope: string): string | null {
  if (typeof scope !== "string" || !scope.trim() || scope.length > 1024) return null
  try { return STORAGE_PREFIX + encodeURIComponent(scope) } catch { return null }
}
// Storage denial should not disable controls for the current page. A changed
// storage value from another tab supersedes the temporary fallback. Bound the
// cache so switching identities cannot grow it indefinitely.
const volatilePreferences = new Map<string, { value: SoundPreferences; previous: string | null }>()
export function readSoundPreferences(scope: string): SoundPreferences {
  const key = storageKey(scope)
  if (!key || typeof window === "undefined") return { ...DEFAULT_SOUND_PREFERENCES }
  const fallback = volatilePreferences.get(key)
  try {
    const raw = window.localStorage.getItem(key)
    if (fallback && raw === fallback.previous) return { ...fallback.value }
    volatilePreferences.delete(key)
    return preferences(raw === null ? null : JSON.parse(raw))
  } catch {
    return fallback ? { ...fallback.value } : { ...DEFAULT_SOUND_PREFERENCES }
  }
}
export function saveSoundPreferences(scope: string, value: SoundPreferences): SoundPreferences {
  const sanitized = preferences(value)
  const key = storageKey(scope)
  if (!key || typeof window === "undefined") return sanitized
  let previous: string | null = null
  try {
    previous = window.localStorage.getItem(key)
    window.localStorage.setItem(key, JSON.stringify(sanitized))
    volatilePreferences.delete(key)
  } catch {
    volatilePreferences.delete(key)
    if (volatilePreferences.size >= 32) volatilePreferences.delete(volatilePreferences.keys().next().value!)
    volatilePreferences.set(key, { value: { ...sanitized }, previous })
  }
  window.dispatchEvent(new CustomEvent(SOUND_PREFERENCES_EVENT, {
    detail: { scope, preferences: { ...sanitized } },
  }))
  return sanitized
}

type Tone = { frequency: number; endFrequency?: number; offset: number; duration: number; weight: number; type?: OscillatorType }
const tones: Record<SoundId, readonly Tone[]> = {
  "soft-pop": [{ frequency: 880, endFrequency: 520, offset: 0, duration: 0.14, weight: 1 }],
  glass: [
    { frequency: 1320, offset: 0, duration: 0.28, weight: 0.75 },
    { frequency: 1980, offset: 0.012, duration: 0.22, weight: 0.25 },
  ],
  chime: [
    { frequency: 660, offset: 0, duration: 0.24, weight: 0.55 },
    { frequency: 990, offset: 0.1, duration: 0.3, weight: 0.55 },
    { frequency: 1320, offset: 0.2, duration: 0.28, weight: 0.3 },
  ],
  "inbox-drop": [
    { frequency: 740, endFrequency: 440, offset: 0, duration: 0.16, weight: 0.8 },
    { frequency: 554, offset: 0.1, duration: 0.18, weight: 0.5 },
  ],
  attention: [
    { frequency: 660, offset: 0, duration: 0.18, weight: 0.65, type: "triangle" },
    { frequency: 880, offset: 0.23, duration: 0.22, weight: 0.65, type: "triangle" },
  ],
}
let context: AudioContext | null = null
let unlocked = false
let pendingUnlock: Promise<boolean> | null = null
let activeCue: (() => void) | null = null

export function isNotificationAudioReady(): boolean {
  return unlocked && context?.state === "running"
}
/** Call directly from an explicit enable/preview click, before awaiting work. */
export async function unlockNotificationAudio(): Promise<boolean> {
  if (typeof window === "undefined") return false
  // Browser autoplay fallback still applies where UserActivation is unavailable.
  if (window.navigator.userActivation && !window.navigator.userActivation.isActive) return false
  if (isNotificationAudioReady()) return true
  if (pendingUnlock) return pendingUnlock
  try {
    if (!context || context.state === "closed") {
      const Constructor = window.AudioContext ?? (window as Window & { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
      if (!Constructor) return false
      unlocked = false
      context = new Constructor()
    }
    const audio = context
    // Resume is called synchronously while the user's transient activation lives.
    const resumed = audio.state === "running" ? Promise.resolve() : audio.resume()
    pendingUnlock = (async () => {
      let timer: ReturnType<typeof setTimeout> | undefined
      try {
        const completed = await Promise.race([
          resumed.then(() => true, () => false),
          new Promise<boolean>(resolve => { timer = setTimeout(() => resolve(false), 2000) }),
        ])
        unlocked = completed && audio === context && audio.state === "running"
        if (unlocked) window.dispatchEvent(new Event(SOUND_AUDIO_READY_EVENT))
        return unlocked
      } finally {
        if (timer !== undefined) clearTimeout(timer)
        pendingUnlock = null
      }
    })()
    return pendingUnlock
  } catch {
    unlocked = false
    return false
  }
}

/** Event delivery never creates/resumes audio. Busy cues are dropped, not queued. */
export async function playNotificationSound(id: SoundId | "off", volume: number): Promise<boolean> {
  if (!soundIds.has(id) || !Number.isFinite(volume) || volume <= 0 || !isNotificationAudioReady() || activeCue) return false
  const audio = context!
  const preset = tones[id as SoundId]
  const nodes: Array<{ oscillator: OscillatorNode; gain: GainNode }> = []
  let timer: ReturnType<typeof setTimeout> | undefined
  const cleanup = () => {
    if (timer !== undefined) clearTimeout(timer)
    for (const { oscillator, gain } of nodes) {
      oscillator.onended = null
      try { oscillator.stop() } catch { /* Already stopped, or failed to start. */ }
      try { oscillator.disconnect() } catch { /* Browser teardown may have closed the graph. */ }
      try { gain.disconnect() } catch { /* Browser teardown may have closed the graph. */ }
    }
    if (activeCue === cleanup) activeCue = null
  }
  activeCue = cleanup
  try {
    const start = audio.currentTime + 0.005
    let remaining = preset.length
    for (const tone of preset) {
      const oscillator = audio.createOscillator()
      let gain: GainNode
      try { gain = audio.createGain() } catch (error) {
        try { oscillator.disconnect() } catch { /* Failed graph construction. */ }
        throw error
      }
      nodes.push({ oscillator, gain })
      const from = start + tone.offset
      const end = from + tone.duration
      oscillator.type = tone.type ?? "sine"
      oscillator.frequency.setValueAtTime(tone.frequency, from)
      if (tone.endFrequency) oscillator.frequency.exponentialRampToValueAtTime(tone.endFrequency, end - 0.02)
      gain.gain.setValueAtTime(0, from)
      gain.gain.linearRampToValueAtTime(Math.min(1, volume) * 0.16 * tone.weight, from + 0.008)
      gain.gain.exponentialRampToValueAtTime(0.0001, end - 0.012)
      gain.gain.linearRampToValueAtTime(0, end)
      oscillator.connect(gain)
      gain.connect(audio.destination)
      oscillator.onended = () => { if (--remaining === 0) cleanup() }
      oscillator.start(from)
      oscillator.stop(end + 0.002)
    }
    // Wall-clock cleanup also discards unfinished notes if the context gets
    // suspended/backgrounded; they cannot unexpectedly replay on a later click.
    const duration = Math.max(...preset.map(tone => tone.offset + tone.duration))
    timer = setTimeout(cleanup, Math.ceil((duration + 0.08) * 1000))
    return true
  } catch {
    cleanup()
    return false
  }
}
