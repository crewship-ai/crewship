"use client"

import { useEffect } from "react"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtime } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"
import { NotificationSoundController } from "@/lib/notification-sound-controller"
import { playSoundOnce, registerSoundPresence } from "@/lib/notification-sound-coordinator"
import { isNotificationAudioReady, readSoundPreferences, unlockNotificationAudio, SOUND_PREFERENCES_EVENT, SOUND_AUDIO_READY_EVENT } from "@/lib/notification-sounds"

export function NotificationSoundEvents() {
  const { session } = useAuth()
  const { workspaceId } = useWorkspace()
  const { subscribe } = useRealtime()
  const userId = session?.user?.id
  useEffect(() => {
    if (!workspaceId || !userId) return
    const scope = JSON.stringify([userId, workspaceId])
    const presenceCleanup = registerSoundPresence(scope)
    const controller = new NotificationSoundController(workspaceId, userId, {
      get: async <T,>(path: string, signal: AbortSignal): Promise<T> => {
        const response = await apiFetch("/api/v1/" + path, { signal })
        if (!response.ok) throw new Error("Notification unavailable")
        return response.json() as Promise<T>
      },
      enabled: () => {
        const prefs = readSoundPreferences(scope)
        return prefs.enabled && !prefs.dnd && prefs.volume > 0 && isNotificationAudioReady()
      },
      deliver: (candidate, current) => playSoundOnce(scope, candidate, current),
    })
    const unsubscribes = (["conversation.updated", "inbox.updated", "escalation.created", "pipeline.waitpoint.created", "realtime.reconnected"] as const)
      .map(type => subscribe(type, event => { void controller.handle(type, event.payload) }))
    const reset = () => controller.reset()
    const storage = (event: StorageEvent) => {
      if (event.key === null || event.key === "crewship:notification-sounds:v1:" + encodeURIComponent(scope)) reset()
    }
    const changed = (event: Event) => {
      if ((event as CustomEvent<{ scope?: string }>).detail?.scope === scope) reset()
    }
    // Honor a saved opt-in on the next real interaction (for example Send),
    // rather than requiring a trip to Settings after every page reload.
    const activate = (event: Event) => {
      const prefs = readSoundPreferences(scope)
      if (event.isTrusted && prefs.enabled && !prefs.dnd && !isNotificationAudioReady()) void unlockNotificationAudio()
    }
    window.addEventListener("pointerdown", activate)
    window.addEventListener("keydown", activate)
    window.addEventListener("storage", storage)
    window.addEventListener(SOUND_AUDIO_READY_EVENT, reset)
    window.addEventListener(SOUND_PREFERENCES_EVENT, changed)
    return () => {
      unsubscribes.forEach(unsubscribe => unsubscribe())
      presenceCleanup()
      controller.dispose()
      window.removeEventListener("pointerdown", activate)
      window.removeEventListener("keydown", activate)
      window.removeEventListener("storage", storage)
      window.removeEventListener(SOUND_AUDIO_READY_EVENT, reset)
      window.removeEventListener(SOUND_PREFERENCES_EVENT, changed)
    }
  }, [workspaceId, userId, subscribe])
  return null
}
