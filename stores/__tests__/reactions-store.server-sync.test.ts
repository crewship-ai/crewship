import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import { useReactionsStore } from "@/stores/reactions-store"

// The reactions store is the ONLY writer of
//   POST   /api/v1/chats/{chatId}/messages/{messageId}/reactions   {emoji}
//   DELETE /api/v1/chats/{chatId}/messages/{messageId}/reactions/{emoji}
//   GET    /api/v1/chats/{chatId}/messages/{messageId}/reactions
// so the wire contract is asserted here rather than through the component.
// Both mutations answer 204 No Content; the list answers
// {"reactions":[{emoji,count,mine}]}.

const CHAT = "chat_1"
const MSG = "msg_1"
const BASE = `/api/v1/chats/${CHAT}/messages/${MSG}/reactions`

const noContent = () => ({ ok: true, status: 204 })
const list = (reactions: unknown[]) => ({
  ok: true,
  status: 200,
  json: async () => ({ reactions }),
})
const fail = (status: number) => ({ ok: false, status })

let mockFetch: ReturnType<typeof vi.fn>
let warnSpy: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  mockFetch = vi.fn()
  vi.stubGlobal("fetch", mockFetch)
  warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {})
  useReactionsStore.setState({ byTurn: {} })
})

afterEach(() => {
  warnSpy.mockRestore()
  vi.unstubAllGlobals()
})

describe("add", () => {
  it("POSTs the emoji and keeps the optimistic entry on 204", async () => {
    mockFetch.mockResolvedValue(noContent())

    await useReactionsStore.getState().add(CHAT, MSG, "👍")

    expect(mockFetch).toHaveBeenCalledTimes(1)
    const [url, init] = mockFetch.mock.calls[0]
    expect(url).toBe(BASE)
    expect(init.method).toBe("POST")
    expect(JSON.parse(init.body)).toEqual({ emoji: "👍" })
    expect(useReactionsStore.getState().byTurn[MSG]["👍"]).toEqual({
      count: 1,
      mine: true,
    })
  })

  it("counts on top of a teammate's existing reaction", async () => {
    mockFetch.mockResolvedValue(noContent())
    useReactionsStore.setState({ byTurn: { [MSG]: { "👍": { count: 3, mine: false } } } })

    await useReactionsStore.getState().add(CHAT, MSG, "👍")

    expect(useReactionsStore.getState().byTurn[MSG]["👍"]).toEqual({
      count: 4,
      mine: true,
    })
  })

  it("is idempotent — a second add by the same user issues no second POST", async () => {
    mockFetch.mockResolvedValue(noContent())

    await useReactionsStore.getState().add(CHAT, MSG, "🚀")
    await useReactionsStore.getState().add(CHAT, MSG, "🚀")

    expect(mockFetch).toHaveBeenCalledTimes(1)
    expect(useReactionsStore.getState().byTurn[MSG]["🚀"]).toEqual({
      count: 1,
      mine: true,
    })
  })

  it("rolls back when the server rejects the emoji (400)", async () => {
    mockFetch.mockResolvedValue(fail(400))

    await useReactionsStore.getState().add(CHAT, MSG, "👍")

    expect(useReactionsStore.getState().byTurn[MSG]?.["👍"]).toBeUndefined()
  })

  it("rolls back to the teammate-only entry when the POST fails", async () => {
    mockFetch.mockResolvedValue(fail(404))
    useReactionsStore.setState({ byTurn: { [MSG]: { "👍": { count: 2, mine: false } } } })

    await useReactionsStore.getState().add(CHAT, MSG, "👍")

    expect(useReactionsStore.getState().byTurn[MSG]["👍"]).toEqual({
      count: 2,
      mine: false,
    })
  })

  it("rolls back on a network rejection", async () => {
    mockFetch.mockRejectedValue(new Error("offline"))

    await useReactionsStore.getState().add(CHAT, MSG, "🎉")

    expect(useReactionsStore.getState().byTurn[MSG]?.["🎉"]).toBeUndefined()
  })
})

describe("remove", () => {
  it("DELETEs the emoji as a path segment and drops the entry on 204", async () => {
    mockFetch.mockResolvedValue(noContent())
    useReactionsStore.setState({ byTurn: { [MSG]: { "👍": { count: 1, mine: true } } } })

    await useReactionsStore.getState().remove(CHAT, MSG, "👍")

    expect(mockFetch).toHaveBeenCalledTimes(1)
    const [url, init] = mockFetch.mock.calls[0]
    expect(url).toBe(`${BASE}/${encodeURIComponent("👍")}`)
    expect(init.method).toBe("DELETE")
    expect(useReactionsStore.getState().byTurn[MSG]["👍"]).toBeUndefined()
  })

  it("leaves a teammate's count behind when only my reaction goes", async () => {
    mockFetch.mockResolvedValue(noContent())
    useReactionsStore.setState({ byTurn: { [MSG]: { "👍": { count: 3, mine: true } } } })

    await useReactionsStore.getState().remove(CHAT, MSG, "👍")

    expect(useReactionsStore.getState().byTurn[MSG]["👍"]).toEqual({
      count: 2,
      mine: false,
    })
  })

  it("restores the reaction when the DELETE fails", async () => {
    mockFetch.mockResolvedValue(fail(500))
    useReactionsStore.setState({ byTurn: { [MSG]: { "👍": { count: 1, mine: true } } } })

    await useReactionsStore.getState().remove(CHAT, MSG, "👍")

    expect(useReactionsStore.getState().byTurn[MSG]["👍"]).toEqual({
      count: 1,
      mine: true,
    })
  })

  it("issues no request for an emoji the user never reacted with", async () => {
    useReactionsStore.setState({ byTurn: { [MSG]: { "👍": { count: 1, mine: false } } } })

    await useReactionsStore.getState().remove(CHAT, MSG, "👍")

    expect(mockFetch).not.toHaveBeenCalled()
    expect(useReactionsStore.getState().byTurn[MSG]["👍"]).toEqual({
      count: 1,
      mine: false,
    })
  })
})

describe("toggle", () => {
  it("POSTs when the user has not reacted yet", async () => {
    mockFetch.mockResolvedValue(noContent())

    await useReactionsStore.getState().toggle(CHAT, MSG, "👍")

    expect(mockFetch.mock.calls[0][1].method).toBe("POST")
  })

  it("DELETEs when the user already reacted", async () => {
    mockFetch.mockResolvedValue(noContent())
    useReactionsStore.setState({ byTurn: { [MSG]: { "👍": { count: 1, mine: true } } } })

    await useReactionsStore.getState().toggle(CHAT, MSG, "👍")

    expect(mockFetch.mock.calls[0][1].method).toBe("DELETE")
  })
})

describe("hydrate", () => {
  it("replaces local state with the server's list", async () => {
    mockFetch.mockResolvedValue(
      list([
        { emoji: "👍", count: 2, mine: true },
        { emoji: "🎉", count: 1, mine: false },
      ]),
    )
    useReactionsStore.setState({ byTurn: { [MSG]: { "👻": { count: 9, mine: true } } } })

    await useReactionsStore.getState().hydrate(CHAT, MSG)

    const [url, init] = mockFetch.mock.calls[0]
    expect(url).toBe(BASE)
    expect(init?.method ?? "GET").toBe("GET")
    expect(useReactionsStore.getState().byTurn[MSG]).toEqual({
      "👍": { count: 2, mine: true },
      "🎉": { count: 1, mine: false },
    })
  })

  it("stands down when a mutation started and FINISHED inside its window", async () => {
    // The losing case an in-flight check cannot see. Click 👍 the moment a
    // turn renders: the POST returns before the list does, so `inflight` is
    // empty again by the time hydrate checks it — and the pre-click snapshot
    // overwrites a reaction the server has already accepted. The chip
    // vanishes until the next mount.
    let releaseList: (v: unknown) => void = () => {}
    const listPending = new Promise((r) => { releaseList = r })

    mockFetch.mockImplementation(async (_url: string, init?: RequestInit) => {
      if ((init?.method ?? "GET") === "GET") {
        await listPending
        // The server's answer as of BEFORE the click.
        return list([])
      }
      return noContent()
    })

    const hydrating = useReactionsStore.getState().hydrate(CHAT, MSG)
    // Runs to completion while the GET is still outstanding.
    await useReactionsStore.getState().add(CHAT, MSG, "👍")
    releaseList(null)
    await hydrating

    expect(useReactionsStore.getState().byTurn[MSG]).toEqual({
      "👍": { count: 1, mine: true },
    })
  })

  it("leaves local state alone when the GET fails", async () => {
    mockFetch.mockResolvedValue(fail(404))
    useReactionsStore.setState({ byTurn: { [MSG]: { "👍": { count: 1, mine: true } } } })

    await useReactionsStore.getState().hydrate(CHAT, MSG)

    expect(useReactionsStore.getState().byTurn[MSG]).toEqual({
      "👍": { count: 1, mine: true },
    })
  })
})

describe("localStorage", () => {
  it("no longer persists reactions", async () => {
    mockFetch.mockResolvedValue(noContent())

    await useReactionsStore.getState().add(CHAT, MSG, "👍")

    expect(localStorage.setItem).not.toHaveBeenCalledWith(
      "crewship-reactions",
      expect.anything(),
    )
  })

  it("drops the pre-server-sync key on module load", async () => {
    // The legacy state was per-browser, unattributed, and keyed by a
    // turn id with no chat id beside it — it cannot be replayed onto
    // the server, so it is deleted rather than half-migrated. Asserted
    // against a fresh module instance because the removal runs once at
    // import time.
    vi.resetModules()
    vi.mocked(localStorage.removeItem).mockClear()

    await import("@/stores/reactions-store")

    expect(localStorage.removeItem).toHaveBeenCalledWith("crewship-reactions")
  })
})
