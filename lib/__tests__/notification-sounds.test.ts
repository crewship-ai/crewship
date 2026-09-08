import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { SoundPreferences } from "../notification-sounds"

const param = () => ({ setValueAtTime: vi.fn(), linearRampToValueAtTime: vi.fn(), exponentialRampToValueAtTime: vi.fn() })
const oscillator = () => ({ frequency: param(), type: "sine", connect: vi.fn(), disconnect: vi.fn(), start: vi.fn(), stop: vi.fn(), onended: null as (() => void) | null })
const gain = () => ({ gain: param(), connect: vi.fn(), disconnect: vi.fn() })
class AudioMock {
  static instances: AudioMock[] = []
  state = "suspended"
  currentTime = 10
  destination = {}
  oscillators: ReturnType<typeof oscillator>[] = []
  gains: ReturnType<typeof gain>[] = []
  resume = vi.fn(async () => { this.state = "running" })
  createOscillator = vi.fn(() => { const node = oscillator(); this.oscillators.push(node); return node })
  createGain = vi.fn(() => { const node = gain(); this.gains.push(node); return node })
  constructor() { AudioMock.instances.push(this) }
}
let sounds: typeof import("../notification-sounds")
const key = (scope: string) => `crewship:notification-sounds:v1:${encodeURIComponent(scope)}`
beforeEach(async () => {
  vi.useFakeTimers()
  vi.resetModules()
  const stored = new Map<string, string>()
  vi.mocked(localStorage.getItem).mockImplementation(key => stored.get(key) ?? null)
  vi.mocked(localStorage.setItem).mockImplementation((key, value) => { stored.set(key, value) })
  AudioMock.instances = []
  vi.stubGlobal("AudioContext", AudioMock)
  vi.stubGlobal("navigator", { userActivation: { isActive: true, hasBeenActive: true } })
  sounds = await import("../notification-sounds")
})
afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe("sound preferences", () => {
  it("defaults to explicit opt-in and returns independent objects", () => {
    expect(sounds.readSoundPreferences("u/w")).toEqual({ enabled: false, dnd: false, volume: 0.35, chat: "soft-pop", inbox: "chime" })
    const copy = sounds.readSoundPreferences("u/w")
    copy.enabled = true
    expect(sounds.readSoundPreferences("u/w").enabled).toBe(false)
  })
  it.each(["{bad", "null", "[]", '"string"']) ("handles invalid stored values %s", raw => {
    localStorage.setItem(key("scope"), raw)
    expect(sounds.readSoundPreferences("scope")).toEqual(sounds.DEFAULT_SOUND_PREFERENCES)
  })
  it("sanitizes persisted values and clamps finite volume", () => {
    localStorage.setItem(key("scope"), JSON.stringify({ enabled: "true", dnd: true, volume: 30, chat: "remote.mp3", inbox: "off" }))
    expect(sounds.readSoundPreferences("scope")).toEqual({ enabled: false, dnd: true, volume: 1, chat: "soft-pop", inbox: "off" })
    expect(sounds.saveSoundPreferences("scope", { volume: -2 } as SoundPreferences).volume).toBe(0)
    expect(sounds.saveSoundPreferences("scope", { volume: NaN } as SoundPreferences).volume).toBe(0.35)
  })
  it("isolates user/workspace scopes and emits sanitized same-window updates", () => {
    const listener = vi.fn()
    window.addEventListener(sounds.SOUND_PREFERENCES_EVENT, listener)
    try {
      const prefs = sounds.saveSoundPreferences('["user-a","workspace-a"]', { ...sounds.DEFAULT_SOUND_PREFERENCES, enabled: true, chat: "glass" })
      expect(sounds.readSoundPreferences('["user-a","workspace-a"]')).toEqual(prefs)
      expect(sounds.readSoundPreferences('["user-a","workspace-b"]').enabled).toBe(false)
      expect(sounds.readSoundPreferences('["user-b","workspace-a"]').enabled).toBe(false)
      expect(listener.mock.calls[0][0].detail).toEqual({ scope: '["user-a","workspace-a"]', preferences: prefs })
    } finally { window.removeEventListener(sounds.SOUND_PREFERENCES_EVENT, listener) }
  })
  it("survives storage denial with per-page fallback and accepts later external changes", () => {
    const originalWrite = vi.mocked(localStorage.setItem).getMockImplementation()!
    const write = vi.spyOn(localStorage, "setItem").mockImplementation(() => { throw new DOMException("Denied") })
    const prefs = sounds.saveSoundPreferences("a", { ...sounds.DEFAULT_SOUND_PREFERENCES, enabled: true })
    expect(sounds.readSoundPreferences("a")).toEqual(prefs)
    expect(sounds.readSoundPreferences("b").enabled).toBe(false)
    write.mockImplementation(originalWrite)
    localStorage.setItem(key("a"), JSON.stringify({ ...prefs, dnd: true }))
    expect(sounds.readSoundPreferences("a").dnd).toBe(true)
    vi.spyOn(localStorage, "getItem").mockImplementation(() => { throw new DOMException("Denied") })
    expect(() => sounds.saveSoundPreferences("a", prefs)).not.toThrow()
    expect(sounds.readSoundPreferences("a")).toEqual(prefs)
  })
  it("is safe without a browser during server rendering", async () => {
    vi.stubGlobal("window", undefined)
    expect(sounds.readSoundPreferences("scope")).toEqual(sounds.DEFAULT_SOUND_PREFERENCES)
    expect(() => sounds.saveSoundPreferences("scope", sounds.DEFAULT_SOUND_PREFERENCES)).not.toThrow()
    expect(await sounds.unlockNotificationAudio()).toBe(false)
    expect(await sounds.playNotificationSound("chime", 0.5)).toBe(false)
  })
  it("refuses blank or malformed scope storage", () => {
    const write = vi.spyOn(localStorage, "setItem")
    for (const scope of ["", "  ", "\ud800", "a".repeat(1025)]) {
      sounds.saveSoundPreferences(scope, { ...sounds.DEFAULT_SOUND_PREFERENCES, enabled: true })
      expect(sounds.readSoundPreferences(scope).enabled).toBe(false)
    }
    expect(write).not.toHaveBeenCalled()
  })
})

describe("gesture-gated synthesis", () => {
  it("does not construct or resume audio on event delivery", async () => {
    expect(await sounds.playNotificationSound("chime", 0.5)).toBe(false)
    expect(AudioMock.instances).toHaveLength(0)
    expect(sounds.isNotificationAudioReady()).toBe(false)
    vi.stubGlobal("navigator", { userActivation: { isActive: false, hasBeenActive: true } })
    expect(await sounds.unlockNotificationAudio()).toBe(false)
    expect(AudioMock.instances).toHaveLength(0)
  })
  it("announces only successful unlock transitions, not repeated previews or failures", async () => {
    const listener = vi.fn()
    window.addEventListener(sounds.SOUND_AUDIO_READY_EVENT, listener)
    try {
      expect(await sounds.unlockNotificationAudio()).toBe(true)
      expect(listener).toHaveBeenCalledTimes(1)
      expect(await sounds.unlockNotificationAudio()).toBe(true)
      expect(listener).toHaveBeenCalledTimes(1)
      const audio = AudioMock.instances[0]
      audio.state = "suspended"
      audio.resume.mockRejectedValueOnce(new Error("blocked"))
      expect(await sounds.unlockNotificationAudio()).toBe(false)
      expect(listener).toHaveBeenCalledTimes(1)
      expect(await sounds.unlockNotificationAudio()).toBe(true)
      expect(listener).toHaveBeenCalledTimes(2)
    } finally { window.removeEventListener(sounds.SOUND_AUDIO_READY_EVENT, listener) }
  })
  it("only resumes from explicit unlock, including after suspension", async () => {
    expect(await sounds.unlockNotificationAudio()).toBe(true)
    const audio = AudioMock.instances[0]
    expect(audio.resume).toHaveBeenCalledTimes(1)
    audio.state = "suspended"
    expect(await sounds.playNotificationSound("chime", 0.5)).toBe(false)
    expect(audio.resume).toHaveBeenCalledTimes(1)
    expect(await sounds.unlockNotificationAudio()).toBe(true)
    expect(audio.resume).toHaveBeenCalledTimes(2)
  })
  it("handles unsupported and failing audio constructors", async () => {
    vi.stubGlobal("AudioContext", undefined)
    expect(await sounds.unlockNotificationAudio()).toBe(false)
    vi.stubGlobal("AudioContext", class { constructor() { throw new Error("unsupported") } })
    expect(await sounds.unlockNotificationAudio()).toBe(false)
  })
  it("handles blocked autoplay rejection and allows a later gesture retry", async () => {
    vi.stubGlobal("AudioContext", class extends AudioMock { resume = vi.fn(async () => { throw new Error("NotAllowedError") }) })
    expect(await sounds.unlockNotificationAudio()).toBe(false)
    const audio = AudioMock.instances[0]
    expect(await sounds.playNotificationSound("glass", 0.4)).toBe(false)
    audio.resume.mockImplementation(async () => { audio.state = "running" })
    expect(await sounds.unlockNotificationAudio()).toBe(true)
  })
  it("bounds a browser resume promise that never settles", async () => {
    vi.stubGlobal("AudioContext", class extends AudioMock { resume = vi.fn(() => new Promise<void>(() => {})) })
    const unlocking = sounds.unlockNotificationAudio()
    await vi.advanceTimersByTimeAsync(2000)
    expect(await unlocking).toBe(false)
    expect(sounds.isNotificationAudioReady()).toBe(false)
  })
  it.each(["off", "invalid"])("ignores %s, mute, and nonfinite volume", async id => {
    await sounds.unlockNotificationAudio()
    expect(await sounds.playNotificationSound(id as "off", 0.5)).toBe(false)
    for (const volume of [0, -1, NaN, Infinity]) expect(await sounds.playNotificationSound("chime", volume)).toBe(false)
    expect(AudioMock.instances[0].oscillators).toHaveLength(0)
  })
  it("plays all five original cues with bounded envelopes, voices and no external requests", async () => {
    const fetch = vi.fn()
    vi.stubGlobal("fetch", fetch)
    await sounds.unlockNotificationAudio()
    const audio = AudioMock.instances[0]
    expect(sounds.SOUND_PRESETS).toHaveLength(5)
    for (const preset of sounds.SOUND_PRESETS) {
      audio.oscillators = []; audio.gains = []
      expect(await sounds.playNotificationSound(preset.id, 2)).toBe(true)
      expect(audio.oscillators.length).toBeGreaterThan(0)
      expect(audio.oscillators.length).toBeLessThanOrEqual(3)
      for (const node of audio.oscillators) {
        expect(node.stop.mock.calls[0][0] - audio.currentTime).toBeLessThan(1)
        expect(node.start.mock.calls[0][0]).toBeGreaterThan(audio.currentTime)
      }
      for (const node of audio.gains) {
        expect(node.gain.setValueAtTime.mock.calls[0][0]).toBe(0)
        expect(node.gain.linearRampToValueAtTime.mock.calls[0][0]).toBeLessThanOrEqual(0.16)
        expect(node.gain.linearRampToValueAtTime.mock.calls.at(-1)?.[0]).toBe(0)
      }
      await vi.advanceTimersByTimeAsync(999)
      for (const node of audio.oscillators) expect(node.disconnect).toHaveBeenCalled()
      for (const node of audio.gains) expect(node.disconnect).toHaveBeenCalled()
    }
    expect(fetch).not.toHaveBeenCalled()
  })
  it("drops overlap rather than queuing and discards suspended unfinished cues", async () => {
    await sounds.unlockNotificationAudio()
    const audio = AudioMock.instances[0]
    expect(await sounds.playNotificationSound("chime", 0.5)).toBe(true)
    expect(await sounds.playNotificationSound("attention", 0.5)).toBe(false)
    expect(audio.oscillators).toHaveLength(3)
    audio.state = "suspended"
    await vi.advanceTimersByTimeAsync(600)
    for (const node of audio.oscillators) expect(node.disconnect).toHaveBeenCalledTimes(1)
    await sounds.unlockNotificationAudio()
    expect(await sounds.playNotificationSound("attention", 0.5)).toBe(true)
  })
  it("survives browser teardown errors without retaining a busy cue", async () => {
    await sounds.unlockNotificationAudio()
    const audio = AudioMock.instances[0]
    expect(await sounds.playNotificationSound("soft-pop", 0.5)).toBe(true)
    audio.oscillators[0].disconnect.mockImplementation(() => { throw new Error("closed graph") })
    audio.gains[0].disconnect.mockImplementation(() => { throw new Error("closed graph") })
    await vi.advanceTimersByTimeAsync(600)
    expect(await sounds.playNotificationSound("glass", 0.5)).toBe(true)
  })
  it("cleans partially constructed graphs and recovers after a browser error", async () => {
    await sounds.unlockNotificationAudio()
    const audio = AudioMock.instances[0]
    audio.createGain.mockImplementationOnce(() => { throw new Error("graph unavailable") })
    expect(await sounds.playNotificationSound("chime", 0.5)).toBe(false)
    expect(audio.oscillators[0].disconnect).toHaveBeenCalled()
    expect(await sounds.playNotificationSound("soft-pop", 0.5)).toBe(true)
    audio.oscillators.at(-1)!.onended!()
    expect(await sounds.playNotificationSound("glass", 0.5)).toBe(true)
  })
})
