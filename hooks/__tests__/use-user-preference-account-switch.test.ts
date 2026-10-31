import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"

// Two accounts in one browser (swarm close-ab S4c): the owner opens a private
// artifact, signs out, a reader signs in. The device copy of a preference was
// one key for everybody, so the reader's first paint showed the owner's value,
// the server had no value for the reader to override it with, and the
// reader's next write (the Library merges into "recently opened") PUT the
// owner's private slugs into the reader's server preference. The device copy
// now belongs to one signed-in user, and nothing another user wrote is sent.

const session = vi.hoisted(() => ({
  user: null as string | null,
  status: "authenticated" as "loading" | "authenticated" | "unauthenticated",
}))

vi.mock("@/hooks/use-auth", () => ({
  useSessionSafe: () => ({
    data: session.user ? { user: { id: session.user } } : null,
    status: session.status,
  }),
  useAuthAvailable: () => true,
}))

import { useUserPreference } from "@/hooks/use-user-preference"

const KEY = "artifacts.ws1.recentlyOpened"
type Recent = { slug: string; at: number }[]

let fetchMock: ReturnType<typeof vi.fn>
let storage: Map<string, string>
// What the server holds, per signed-in user (the cookie decides whose it is).
let server: Record<string, Record<string, unknown>>

const okJSON = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as unknown as Response
const putsFor = (user: string) =>
  fetchMock.mock.calls.filter(([, init], i) => (init as RequestInit | undefined)?.method === "PUT" && callUser[i] === user)
let callUser: (string | null)[]

// Let the signed-out write (and apiFetch's refresh-and-retry of its 401)
// finish before the next account signs in.
const settle = () => act(async () => {
  await vi.advanceTimersByTimeAsync(50)
})

function signIn(user: string | null) {
  session.user = user
  session.status = user ? "authenticated" : "unauthenticated"
}

beforeEach(() => {
  server = {}
  callUser = []
  fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    const user = session.user
    callUser.push(user)
    if (!user) return { ok: false, status: 401, json: async () => ({}) } as unknown as Response
    const prefs = (server[user] ??= {})
    if (init?.method === "PUT") {
      prefs[decodeURIComponent(String(url).split("/me/preferences/")[1])] = JSON.parse(String(init.body))
      return okJSON(null)
    }
    return okJSON({ ...prefs })
  })
  vi.stubGlobal("fetch", fetchMock)
  storage = new Map<string, string>()
  vi.spyOn(window.localStorage, "getItem").mockImplementation((k) => storage.get(k) ?? null)
  vi.spyOn(window.localStorage, "setItem").mockImplementation((k, v) => {
    storage.set(k, String(v))
  })
  vi.spyOn(window.localStorage, "removeItem").mockImplementation((k) => {
    storage.delete(k)
  })
  signIn("owner")
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const ownerPrivate: Recent = [{ slug: "owner-private", at: 1 }]

describe("useUserPreference with two accounts in one browser", () => {
  it("does not show or upload the previous account's value to the next account", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const owner = renderHook(() => useUserPreference<Recent>(KEY, []))
    await waitFor(() => expect(owner.result.current[2].ready).toBe(true))
    act(() => owner.result.current[1](ownerPrivate))
    await act(async () => {
      vi.advanceTimersByTime(450)
    })
    await waitFor(() => expect(server.owner?.[KEY]).toEqual(ownerPrivate))
    owner.unmount()

    signIn("reader")
    const reader = renderHook(() => useUserPreference<Recent>(KEY, []))
    expect(reader.result.current[0]).toEqual([])
    await waitFor(() => expect(reader.result.current[2].ready).toBe(true))
    expect(reader.result.current[0]).toEqual([])

    // The Library merges the newly opened artifact into what it holds.
    act(() => reader.result.current[1]([{ slug: "reader-public", at: 2 }, ...reader.result.current[0]]))
    await act(async () => {
      vi.advanceTimersByTime(450)
    })
    await waitFor(() => expect(putsFor("reader")).toHaveLength(1))
    expect(server.reader?.[KEY]).toEqual([{ slug: "reader-public", at: 2 }])
    expect(JSON.stringify(server.reader)).not.toContain("owner-private")
  })

  it("never re-sends another account's unconfirmed change under the new account", async () => {
    // The owner's change never reached the server (the page went away first),
    // so the device keeps it marked unconfirmed.
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const owner = renderHook(() => useUserPreference<Recent>(KEY, []))
    await waitFor(() => expect(owner.result.current[2].ready).toBe(true))
    fetchMock.mockClear()
    callUser = []
    act(() => owner.result.current[1](ownerPrivate))
    // Signed out before the debounce fired; the hook goes with the page.
    signIn(null)
    owner.unmount()
    await settle()

    signIn("reader")
    const reader = renderHook(() => useUserPreference<Recent>(KEY, []))
    await waitFor(() => expect(reader.result.current[2].ready).toBe(true))
    await act(async () => {
      vi.advanceTimersByTime(1000)
    })
    expect(reader.result.current[0]).toEqual([])
    expect(putsFor("reader")).toHaveLength(0)
    expect(JSON.stringify(server.reader ?? {})).not.toContain("owner-private")
  })

  it("an account switch under a mounted hook drops the previous value and its pending write", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const hook = renderHook(() => useUserPreference<Recent>(KEY, []))
    await waitFor(() => expect(hook.result.current[2].ready).toBe(true))
    act(() => hook.result.current[1](ownerPrivate))

    signIn("reader")
    hook.rerender()
    expect(hook.result.current[0]).toEqual([])
    await act(async () => {
      vi.advanceTimersByTime(1000)
    })
    await waitFor(() => expect(hook.result.current[2].ready).toBe(true))
    expect(putsFor("reader")).toHaveLength(0)
    expect(JSON.stringify(server.reader ?? {})).not.toContain("owner-private")
    expect(hook.result.current[0]).toEqual([])
  })

  it("gives the owner their own value back, and re-sends their own unconfirmed change", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const owner = renderHook(() => useUserPreference<Recent>(KEY, []))
    await waitFor(() => expect(owner.result.current[2].ready).toBe(true))
    act(() => owner.result.current[1](ownerPrivate))
    signIn(null)
    owner.unmount()
    await settle()

    signIn("reader")
    const reader = renderHook(() => useUserPreference<Recent>(KEY, []))
    await waitFor(() => expect(reader.result.current[2].ready).toBe(true))
    reader.unmount()

    signIn("owner")
    const back = renderHook(() => useUserPreference<Recent>(KEY, []))
    expect(back.result.current[0]).toEqual(ownerPrivate)
    await waitFor(() => expect(server.owner?.[KEY]).toEqual(ownerPrivate))
    expect(JSON.stringify(server.reader ?? {})).not.toContain("owner-private")
  })

  it("reads no device copy while the session is still loading", async () => {
    storage.set(`crewship.pref.${KEY}`, JSON.stringify(ownerPrivate))
    session.user = null
    session.status = "loading"
    const hook = renderHook(() => useUserPreference<Recent>(KEY, []))
    expect(hook.result.current[0]).toEqual([])
    signIn("reader")
    hook.rerender()
    await waitFor(() => expect(hook.result.current[2].ready).toBe(true))
    expect(hook.result.current[0]).toEqual([])
  })
  it("does not retry the owner's write under another account after the 401 refresh", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const hook = renderHook(() => useUserPreference<Recent>(KEY, []))
    await waitFor(() => expect(hook.result.current[2].ready).toBe(true))
    let releaseRefresh!: () => void
    const refreshGate = new Promise<void>((resolve) => { releaseRefresh = resolve })
    const base = fetchMock.getMockImplementation()!
    let firstPut = true
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (String(url).includes("/api/auth/token/refresh") || (init?.method === "PUT" && firstPut)) callUser.push(session.user)
      if (String(url).includes("/api/auth/token/refresh")) {
        await refreshGate
        return okJSON(null)
      }
      if (init?.method === "PUT" && firstPut) {
        firstPut = false
        return { ok: false, status: 401, json: async () => ({ error: "session_expired" }), clone() { return this } } as unknown as Response
      }
      return base(url, init)
    })
    act(() => hook.result.current[1](ownerPrivate))
    await act(async () => {
      vi.advanceTimersByTime(450)
    })
    // The owner's request got its 401 and waits for the refresh; meanwhile a reader signs in.
    signIn("reader")
    hook.rerender()
    releaseRefresh()
    await settle()
    expect(putsFor("reader")).toHaveLength(0)
    expect(JSON.stringify(server.reader ?? {})).not.toContain("owner-private")
  })
  it("does not re-send an unmounted owner's write under a reader who signed in during the 401 refresh (Codex #4690 B1)", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const owner = renderHook(() => useUserPreference<Recent>(KEY, []))
    await waitFor(() => expect(owner.result.current[2].ready).toBe(true))
    let releaseRefresh!: () => void
    const refreshGate = new Promise<void>((resolve) => { releaseRefresh = resolve })
    const base = fetchMock.getMockImplementation()!
    let firstPut = true
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (String(url).includes("/api/auth/token/refresh") || (init?.method === "PUT" && firstPut)) callUser.push(session.user)
      if (String(url).includes("/api/auth/token/refresh")) {
        await refreshGate
        return okJSON(null)
      }
      if (init?.method === "PUT" && firstPut) {
        firstPut = false
        return { ok: false, status: 401, json: async () => ({ error: "session_expired" }), clone() { return this } } as unknown as Response
      }
      return base(url, init)
    })
    act(() => owner.result.current[1](ownerPrivate))
    await act(async () => {
      vi.advanceTimersByTime(450)
    })
    // The AuthProvider remounts its children on the account change: the owner's hook is gone.
    owner.unmount()
    signIn("reader")
    renderHook(() => useUserPreference<Recent>(KEY, []))
    releaseRefresh()
    await settle()
    expect(putsFor("reader").filter(([, init]) => String((init as RequestInit).body).includes("owner-private"))).toHaveLength(0)
    expect(JSON.stringify(server.reader ?? {})).not.toContain("owner-private")
  })
})
