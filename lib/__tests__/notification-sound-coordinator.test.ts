import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
const audio = vi.hoisted(() => ({ ready: vi.fn(() => true), play: vi.fn(async () => true), prefs: vi.fn(() => ({ enabled: true, dnd: false, volume: .35, chat: "soft-pop", inbox: "chime" })) }))
vi.mock("@/lib/notification-sounds", () => ({ isNotificationAudioReady: audio.ready, playNotificationSound: audio.play, readSoundPreferences: audio.prefs }))
import { getAutomaticSoundCapability, playSoundOnce, registerSoundPresence, setSoundReadingConversation } from "../notification-sound-coordinator"

const candidate = { key: "message:r:m", category: "chat" as const, conversation: "r" }
beforeEach(() => {
  const storage = new Map<string, string>()
  vi.mocked(localStorage.getItem).mockImplementation(key => storage.get(key) ?? null)
  vi.mocked(localStorage.setItem).mockImplementation((key, value) => { storage.set(key, value) })
  vi.mocked(localStorage.removeItem).mockImplementation(key => { storage.delete(key) })
  Object.defineProperty(localStorage, "length", { configurable: true, get: () => storage.size })
  Object.defineProperty(localStorage, "key", { configurable: true, value: (i: number) => [...storage.keys()][i] ?? null })
  vi.clearAllMocks()
  audio.ready.mockReturnValue(true)
  audio.prefs.mockReturnValue({ enabled: true, dnd: false, volume: .35, chat: "soft-pop", inbox: "chime" })
  Object.defineProperty(navigator, "locks", { configurable: true, value: { request: vi.fn(async (_key, _options, callback) => callback({ name: "lock" })) } })
})
afterEach(() => { setSoundReadingConversation("scope", "r", false); vi.restoreAllMocks() })

describe("cross-tab sound coordinator", () => {
  it("checks shared storage roundtrip and Web Locks without leaving probe data", () => {
    expect(getAutomaticSoundCapability()).toBe("available")
    expect(localStorage.length).toBe(0)
    vi.mocked(localStorage.setItem).mockImplementation(() => {})
    expect(getAutomaticSoundCapability()).toBe("unavailable-storage")
    vi.mocked(localStorage.setItem).mockImplementation(() => { throw new Error("denied") })
    expect(getAutomaticSoundCapability()).toBe("unavailable-storage")
    Object.defineProperty(navigator, "locks", { configurable: true, value: undefined })
    expect(getAutomaticSoundCapability()).toBe("unsupported-locks")
  })
  it("persists canonical event claims so another instance cannot replay", async () => {
    expect(await playSoundOnce("scope", candidate, () => true)).toBe(true)
    expect(await playSoundOnce("scope", candidate, () => true)).toBe(false)
    expect(audio.play).toHaveBeenCalledTimes(1)
    expect(navigator.locks.request).toHaveBeenCalledWith("crewship:sounds:scope", { ifAvailable: true }, expect.any(Function))
  })
  it("refuses sound if another tab holds the exclusive lock", async () => {
    vi.mocked(navigator.locks.request).mockImplementation(async (_name, _options, callback) => callback!(null as never) as never)
    expect(await playSoundOnce("scope", candidate, () => true)).toBe(false)
    expect(audio.play).not.toHaveBeenCalled()
  })
  it("silences a message being read in another focused tab", async () => {
    localStorage.setItem("crewship:sound-presence:v1:scope:other", JSON.stringify({ at: Date.now(), conversation: "r" }))
    expect(await playSoundOnce("scope", candidate, () => true)).toBe(false)
    expect(audio.play).not.toHaveBeenCalled()
    localStorage.removeItem("crewship:sound-presence:v1:scope:other")
    expect(await playSoundOnce("scope", candidate, () => true)).toBe(false)
  })
  it("ignores expired presence and isolates users/workspaces", async () => {
    localStorage.setItem("crewship:sound-presence:v1:scope:old", JSON.stringify({ at: Date.now() - 20000, conversation: "r" }))
    localStorage.setItem("crewship:sound-presence:v1:other:live", JSON.stringify({ at: Date.now(), conversation: "r" }))
    expect(await playSoundOnce("scope", candidate, () => true)).toBe(true)
    expect(await playSoundOnce("other-user", candidate, () => true)).toBe(true)
  })
  it("groups a chat/inbox burst without replaying suppressed events later", async () => {
    await playSoundOnce("scope", candidate, () => true)
    await playSoundOnce("scope", { key: "inbox:1", category: "inbox" }, () => true)
    expect(audio.play).toHaveBeenCalledTimes(1)
  })
  it("fails closed without locks, shared storage or unlocked audio", async () => {
    audio.ready.mockReturnValue(false)
    expect(await playSoundOnce("scope", candidate, () => true)).toBe(false)
    audio.ready.mockReturnValue(true)
    vi.mocked(localStorage.setItem).mockImplementation(() => { throw new Error("denied") })
    expect(await playSoundOnce("scope", candidate, () => true)).toBe(false)
    Object.defineProperty(navigator, "locks", { configurable: true, value: undefined })
    expect(await playSoundOnce("scope", candidate, () => true)).toBe(false)
    expect(audio.play).not.toHaveBeenCalled()
  })
  it("rechecks DND and current identity inside the acquired lock", async () => {
    audio.prefs.mockReturnValue({ enabled: true, dnd: true, volume: .35, chat: "soft-pop", inbox: "chime" })
    expect(await playSoundOnce("scope", candidate, () => true)).toBe(false)
    expect(await playSoundOnce("scope", candidate, () => false)).toBe(false)
    expect(audio.play).not.toHaveBeenCalled()
  })
  it("registers focused reading and removes presence on teardown", () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(true)
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" })
    const cleanup = registerSoundPresence("scope")
    setSoundReadingConversation("scope", "r", true)
    const key = localStorage.key(0)!
    expect(JSON.parse(localStorage.getItem(key)!).conversation).toBe("r")
    cleanup()
    expect(localStorage.getItem(key)).toBeNull()
  })
})
