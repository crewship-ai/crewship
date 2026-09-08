"use client"

import { useEffect, useRef, useState } from "react"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"
import { getAutomaticSoundCapability, type AutomaticSoundCapability } from "@/lib/notification-sound-coordinator"
import { SOUND_PRESETS, SOUND_PREFERENCES_EVENT, readSoundPreferences, saveSoundPreferences, unlockNotificationAudio, playNotificationSound, isNotificationAudioReady, type SoundId, type SoundPreferences } from "@/lib/notification-sounds"

export function NotificationSoundSettings() {
  const { session } = useAuth()
  const { workspaceId } = useWorkspace()
  const scope = session?.user.id && workspaceId ? JSON.stringify([session.user.id, workspaceId]) : null
  if (!scope) return <p>Select a workspace to configure notification sounds.</p>
  return <ScopedSoundSettings key={scope} scope={scope} />
}

function ScopedSoundSettings({ scope }: { scope: string }) {
  const [preferences, setPreferences] = useState(() => readSoundPreferences(scope))
  const [ready, setReady] = useState(isNotificationAudioReady)
  const [capability, setCapability] = useState<AutomaticSoundCapability | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    setCapability(getAutomaticSoundCapability())
    const reload = () => setPreferences(readSoundPreferences(scope))
    window.addEventListener("storage", reload)
    window.addEventListener(SOUND_PREFERENCES_EVENT, reload)
    return () => { mounted.current = false; window.removeEventListener("storage", reload); window.removeEventListener(SOUND_PREFERENCES_EVENT, reload) }
  }, [scope])

  function update(patch: Partial<SoundPreferences>) {
    const next = { ...preferences, ...patch }
    setPreferences(next)
    setError(null)
    try { saveSoundPreferences(scope, next) }
    catch { setError("These preferences could not be saved in this browser.") }
  }
  async function activate(preview?: SoundId | "off") {
    if (busy) return
    setBusy(true); setError(null)
    setCapability(getAutomaticSoundCapability())
    try {
      // Start resume in the original click task, before awaiting anything.
      const unlocked = await unlockNotificationAudio()
      if (!mounted.current) return
      setReady(unlocked)
      if (!unlocked) { setError("Your browser blocked audio. Try again after interacting with this page."); return }
      if (preview && preview !== "off" && !await playNotificationSound(preview, preferences.volume) && mounted.current) {
        setReady(isNotificationAudioReady())
        setError("The preview could not play. Check your browser’s audio permissions and try again.")
      }
    } catch {
      if (mounted.current) { setReady(false); setError("Audio is unavailable in this browser. Please try again.") }
    } finally { if (mounted.current) setBusy(false) }
  }
  function enable() {
    // Keep the user's preference even when this browser needs another gesture.
    update({ enabled: true })
    void activate()
  }
  return <section aria-label="Notification sounds" className="max-w-2xl rounded-xl border bg-card p-6">
      <h2 className="text-lg font-semibold">Notification sounds</h2>
      <p className="mb-6 mt-2 text-sm text-muted-foreground">Your Chat and Inbox sounds for this account and workspace, saved in this browser.</p>
      <div className="space-y-5">
        <Button type="button" aria-pressed={preferences.enabled} disabled={busy} onClick={() => { if (preferences.enabled) update({ enabled: false }); else enable() }}>{preferences.enabled ? "Disable sounds" : "Enable sounds"}</Button>
        <label className="flex min-h-11 items-center gap-3 text-sm"><input type="checkbox" checked={preferences.dnd} onChange={event => update({ dnd: event.target.checked })} />Do not disturb</label>
        <p className="text-xs text-muted-foreground">Do not disturb silences notifications. Preview buttons still play the sound you choose.</p>
        <label className="block space-y-2 text-sm"><span>Notification volume · {Math.round(preferences.volume * 100)}%</span><input aria-label="Notification volume" className="w-full accent-primary" type="range" min="0" max="100" step="1" value={Math.round(preferences.volume * 100)} onChange={event => update({ volume: Number(event.target.value) / 100 })} /></label>
        {(["chat", "inbox"] as const).map(kind => <div key={kind} className="flex items-end gap-3"><label className="min-w-0 flex-1 space-y-1 text-sm"><span>{kind === "chat" ? "Chat sound" : "Inbox sound"}</span><select className="min-h-10 w-full rounded-md border bg-background px-3" value={preferences[kind]} onChange={event => update({ [kind]: event.target.value as SoundPreferences[typeof kind] })}><option value="off">Off</option>{SOUND_PRESETS.map(preset => <option key={preset.id} value={preset.id}>{preset.label}</option>)}</select></label><Button type="button" variant="outline" disabled={busy || preferences[kind] === "off" || preferences.volume === 0} aria-label={`Preview ${kind === "chat" ? "Chat" : "Inbox"} sound`} onClick={() => { void activate(preferences[kind]) }}>Preview</Button></div>)}
        {preferences.volume === 0 && <p className="text-xs text-muted-foreground">Volume is 0%. Raise it to hear previews and notifications.</p>}
        {preferences.enabled && !ready && <Button type="button" variant="outline" disabled={busy} onClick={() => { void activate() }}>Activate audio</Button>}
        <p role="status" className="text-xs text-muted-foreground">{busy ? "Preparing audio…" : !preferences.enabled ? "Notification sounds are off." : preferences.dnd ? "Do not disturb is on. Notifications are silent." : capability && capability !== "available" ? "Automatic notifications are unavailable: this browser needs shared storage and tab coordination. You can still preview sounds." : ready && capability === "available" ? "Audio is ready. Your device and browser volume still apply." : "Audio needs a click on Activate audio or Preview. Your browser may require this again after a reload."}</p>
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      </div>
  </section>
}
