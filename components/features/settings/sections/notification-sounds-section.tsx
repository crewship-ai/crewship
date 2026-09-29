"use client"

import { useEffect, useRef, useState } from "react"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { ChevronDown, Play, Volume2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { SettingsCard, SettingsRow } from "../shared"
import { getAutomaticSoundCapability, type AutomaticSoundCapability } from "@/lib/notification-sound-coordinator"
import { SOUND_PRESETS, SOUND_PREFERENCES_EVENT, readSoundPreferences, saveSoundPreferences, unlockNotificationAudio, playNotificationSound, isNotificationAudioReady, type SoundId, type SoundPreferences } from "@/lib/notification-sounds"

export function NotificationSoundSettings() {
  const { session } = useAuth()
  const { workspaceId } = useWorkspace()
  const scope = session?.user.id && workspaceId ? JSON.stringify([session.user.id, workspaceId]) : null
  if (!scope) return <p className="text-sm text-muted-foreground">Select a workspace to configure notification sounds.</p>
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
  const statusText = busy ? "Preparing audio…" : !preferences.enabled ? "Notification sounds are off." : preferences.dnd ? "Do not disturb is on. Notifications are silent." : capability && capability !== "available" ? "Automatic notifications are unavailable: this browser needs shared storage and tab coordination. You can still preview sounds." : ready && capability === "available" ? "Audio is ready. Your device and browser volume still apply." : "Audio needs a click on Activate audio or Preview. Your browser may require this again after a reload."
  return (
    <SettingsCard
      icon={Volume2}
      title="Notification sounds"
      description="Your Chat and Inbox sounds for this account and workspace, saved in this browser."
    >
      <SettingsRow label="Play notification sounds" description="Chat replies and new Inbox items make a sound.">
        <Switch
          aria-label="Notification sounds"
          checked={preferences.enabled}
          disabled={busy}
          onCheckedChange={(on) => { if (on) enable(); else update({ enabled: false }) }}
        />
      </SettingsRow>
      <SettingsRow label="Do not disturb" description="Silences notifications. Previews still play the sound you choose.">
        <Switch aria-label="Do not disturb" checked={preferences.dnd} onCheckedChange={(dnd) => update({ dnd })} />
      </SettingsRow>
      <SettingsRow label="Volume" description={preferences.volume === 0 ? "Volume is 0%. Raise it to hear previews and notifications." : undefined}>
        <div className="flex w-56 items-center gap-3">
          <input
            aria-label="Notification volume"
            className="h-1.5 w-full cursor-pointer accent-primary"
            type="range" min="0" max="100" step="1"
            value={Math.round(preferences.volume * 100)}
            onChange={(event) => update({ volume: Number(event.target.value) / 100 })}
          />
          <span className="w-9 shrink-0 text-right font-mono text-xs tabular-nums text-muted-foreground">{Math.round(preferences.volume * 100)}%</span>
        </div>
      </SettingsRow>
      {(["chat", "inbox"] as const).map((kind) => {
        const name = kind === "chat" ? "Chat" : "Inbox"
        return (
          <SettingsRow key={kind} label={`${name} sound`} description={kind === "chat" ? "When an agent replies to you." : "When something new lands in your Inbox."}>
            <div className="flex items-center gap-1.5">
              <NativeSelect
                aria-label={`${name} sound`}
                value={preferences[kind]}
                onChange={(value) => update({ [kind]: value as SoundPreferences[typeof kind] })}
                options={[{ value: "off", label: "Off" }, ...SOUND_PRESETS.map((p) => ({ value: p.id, label: p.label }))]}
              />
              <Button
                type="button" variant="outline" size="icon-sm"
                disabled={busy || preferences[kind] === "off" || preferences.volume === 0}
                aria-label={`Preview ${name} sound`}
                title={`Preview ${name.toLowerCase()} sound`}
                onClick={() => { void activate(preferences[kind]) }}
              >
                <Play className="h-3.5 w-3.5" />
              </Button>
            </div>
          </SettingsRow>
        )
      })}
      <div className="flex flex-wrap items-center justify-between gap-2 px-4 py-3">
        <p role="status" className="text-[11px] text-muted-foreground">{statusText}</p>
        {preferences.enabled && !ready && (
          <Button type="button" variant="outline" size="sm" className="h-7 px-2.5 text-xs" disabled={busy} onClick={() => { void activate() }}>Activate audio</Button>
        )}
      </div>
      {error && <p role="alert" className="px-4 pb-3 text-xs text-destructive">{error}</p>}
    </SettingsCard>
  )
}

/**
 * A native select dressed as the Settings select (same height, font, radius,
 * surface and chevron as SelectTrigger). Native keeps keyboard, screen-reader
 * and mobile pickers for free, and the value is a plain string.
 */
function NativeSelect({ value, onChange, options, ...rest }: { value: string; onChange: (value: string) => void; options: { value: string; label: string }[]; "aria-label": string }) {
  return (
    <div className="relative">
      <select
        {...rest}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className="h-8 w-40 cursor-pointer appearance-none rounded-md border border-control-border bg-surface-subtle pl-3 pr-8 text-control text-foreground outline-none transition-colors hover:border-line-strong focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50"
      >
        {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
      </select>
      <ChevronDown className="pointer-events-none absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden />
    </div>
  )
}
