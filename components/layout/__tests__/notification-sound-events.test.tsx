import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, render } from "@testing-library/react"

const mocks = vi.hoisted(() => ({
  userId: "viewer", workspaceId: "workspace", ready: false,
  enabled: true, dnd: false,
  unlock: vi.fn(), subscribe: vi.fn(), presence: vi.fn(), dispose: vi.fn(), reset: vi.fn(), handle: vi.fn(),
  read: vi.fn(),
}))
vi.mock("@/hooks/use-auth", () => ({ useAuth: () => ({ session: mocks.userId ? { user: { id: mocks.userId } } : null }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: mocks.workspaceId }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtime: () => ({ subscribe: mocks.subscribe }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/notification-sound-controller", () => ({ NotificationSoundController: class {
  reset = mocks.reset
  dispose = mocks.dispose
  handle = mocks.handle
} }))
vi.mock("@/lib/notification-sound-coordinator", () => ({ registerSoundPresence: mocks.presence, playSoundOnce: vi.fn() }))
vi.mock("@/lib/notification-sounds", () => ({
  readSoundPreferences: mocks.read,
  isNotificationAudioReady: () => mocks.ready,
  unlockNotificationAudio: mocks.unlock,
  SOUND_PREFERENCES_EVENT: "sound-prefs", SOUND_AUDIO_READY_EVENT: "sound-ready",
}))
import { NotificationSoundEvents } from "../notification-sound-events"

beforeEach(() => {
  vi.clearAllMocks()
  mocks.userId = "viewer"; mocks.workspaceId = "workspace"; mocks.ready = false
  mocks.enabled = true; mocks.dnd = false
  mocks.read.mockImplementation(() => ({ enabled: mocks.enabled, dnd: mocks.dnd, volume: .35 }))
  mocks.unlock.mockResolvedValue(true)
  mocks.presence.mockImplementation(() => vi.fn())
  mocks.subscribe.mockImplementation(() => vi.fn())
})
afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe("Notification sound global activation lifecycle", () => {
  it("restores saved opt-in only on trusted interaction, without activating for synthetic events, DND or disabled preferences", () => {
    const listeners = vi.spyOn(window, "addEventListener")
    render(<NotificationSoundEvents />)
    expect(mocks.unlock).not.toHaveBeenCalled()
    // Browser event trust is immutable; exercise the registered handler with a
    // trusted event-shaped input, and use real dispatch for the synthetic case.
    const pointer = listeners.mock.calls.find(([type]) => type === "pointerdown")![1] as EventListener
    const key = listeners.mock.calls.find(([type]) => type === "keydown")![1] as EventListener
    window.dispatchEvent(new Event("pointerdown"))
    window.dispatchEvent(new Event("keydown"))
    expect(mocks.unlock).not.toHaveBeenCalled()
    mocks.enabled = false; pointer({ isTrusted: true } as Event)
    mocks.enabled = true; mocks.dnd = true; key({ isTrusted: true } as Event)
    expect(mocks.unlock).not.toHaveBeenCalled()
    mocks.dnd = false; pointer({ isTrusted: true } as Event)
    expect(mocks.unlock).toHaveBeenCalledTimes(1)
    mocks.ready = true; key({ isTrusted: true } as Event)
    expect(mocks.unlock).toHaveBeenCalledTimes(1)
    mocks.ready = false; key({ isTrusted: true } as Event)
    expect(mocks.unlock).toHaveBeenCalledTimes(2)
  })

  it("resets the event baseline after audio activation and replaces subscriptions/presence on scope change and logout", () => {
    const view = render(<NotificationSoundEvents />)
    expect(mocks.presence).toHaveBeenLastCalledWith(JSON.stringify(["viewer", "workspace"]))
    window.dispatchEvent(new Event("sound-ready"))
    expect(mocks.reset).toHaveBeenCalledTimes(1)
    window.dispatchEvent(new CustomEvent("sound-prefs", { detail: { scope: '["someone-else","workspace"]' } }))
    expect(mocks.reset).toHaveBeenCalledTimes(1)
    window.dispatchEvent(new CustomEvent("sound-prefs", { detail: { scope: '["viewer","workspace"]' } }))
    expect(mocks.reset).toHaveBeenCalledTimes(2)
    const oldUnsubscribes = mocks.subscribe.mock.results.map(result => result.value)
    const oldPresenceCleanup = mocks.presence.mock.results[0].value
    mocks.workspaceId = "other-workspace"; view.rerender(<NotificationSoundEvents />)
    oldUnsubscribes.forEach(unsubscribe => expect(unsubscribe).toHaveBeenCalledOnce())
    expect(oldPresenceCleanup).toHaveBeenCalledOnce()
    expect(mocks.dispose).toHaveBeenCalledOnce()
    expect(mocks.presence).toHaveBeenLastCalledWith(JSON.stringify(["viewer", "other-workspace"]))
    mocks.userId = ""; view.rerender(<NotificationSoundEvents />)
    expect(mocks.dispose).toHaveBeenCalledTimes(2)
    const resetCount = mocks.reset.mock.calls.length
    window.dispatchEvent(new Event("sound-ready"))
    window.dispatchEvent(new Event("pointerdown"))
    expect(mocks.reset).toHaveBeenCalledTimes(resetCount)
    expect(mocks.unlock).not.toHaveBeenCalled()
  })
})
