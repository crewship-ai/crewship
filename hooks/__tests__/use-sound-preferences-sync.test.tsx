import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor, act } from "@testing-library/react"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

import { useSoundPreferencesSync, soundPreferenceKey } from "../use-sound-preferences-sync"
import { DEFAULT_SOUND_PREFERENCES, readSoundPreferences, saveSoundPreferences } from "@/lib/notification-sounds"

const USER = "u1"
const WS = "ws1"
const SCOPE = JSON.stringify([USER, WS])
const KEY = soundPreferenceKey(WS)

let store: Map<string, string>
beforeEach(() => {
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
  apiFetch.mockReset()
})
afterEach(() => vi.unstubAllGlobals())

const ok = (body: unknown) => Promise.resolve({ ok: true, json: () => Promise.resolve(body) })
const puts = () => apiFetch.mock.calls.filter(([, init]) => init?.method === "PUT")

describe("useSoundPreferencesSync", () => {
  it("applies the server's sounds for this workspace and does not echo them", async () => {
    apiFetch.mockImplementation(() => ok({ [KEY]: { ...DEFAULT_SOUND_PREFERENCES, enabled: true, chat: "glass" } }))
    renderHook(() => useSoundPreferencesSync(true, USER, WS))
    await waitFor(() => expect(readSoundPreferences(SCOPE).chat).toBe("glass"))
    expect(readSoundPreferences(SCOPE).enabled).toBe(true)
    await new Promise((r) => setTimeout(r, 500))
    expect(puts()).toHaveLength(0)
  })

  it("writes a local change back to this workspace's key, debounced", async () => {
    apiFetch.mockImplementation(() => ok({}))
    renderHook(() => useSoundPreferencesSync(true, USER, WS))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith("/api/v1/me/preferences"))
    await new Promise((r) => setTimeout(r, 0))
    act(() => {
      saveSoundPreferences(SCOPE, { ...DEFAULT_SOUND_PREFERENCES, enabled: true })
      saveSoundPreferences(SCOPE, { ...DEFAULT_SOUND_PREFERENCES, enabled: true, volume: 0.8 })
    })
    await waitFor(() => expect(puts()).toHaveLength(1))
    const [url, init] = puts()[0]
    expect(url).toBe(`/api/v1/me/preferences/${KEY}`)
    expect(JSON.parse(init.body)).toMatchObject({ enabled: true, volume: 0.8 })
  })

  it("seeds an empty server with a local non-default choice", async () => {
    saveSoundPreferences(SCOPE, { ...DEFAULT_SOUND_PREFERENCES, enabled: true, inbox: "attention" })
    apiFetch.mockImplementation(() => ok({}))
    renderHook(() => useSoundPreferencesSync(true, USER, WS))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(JSON.parse(puts()[0][1].body)).toMatchObject({ inbox: "attention" })
  })

  it("ignores another workspace's changes and sends nothing when not enabled", async () => {
    apiFetch.mockImplementation(() => ok({}))
    renderHook(() => useSoundPreferencesSync(false, USER, WS))
    act(() => { saveSoundPreferences(SCOPE, { ...DEFAULT_SOUND_PREFERENCES, enabled: true }) })
    await new Promise((r) => setTimeout(r, 500))
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("keys sounds inside the server's preference-key alphabet", () => {
    expect(soundPreferenceKey("cmuxw7qvo0002eb0d4eb1")).toMatch(/^[A-Za-z0-9._-]{1,64}$/)
  })
})
