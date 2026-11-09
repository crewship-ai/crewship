import { afterEach, describe, expect, it, vi } from "vitest"
import { REFRESH_MS, createMentionNames, mentionLabel } from "../mention-names"

afterEach(() => { vi.useRealTimers() })

/** A people endpoint that names only the readers it is given, as the server does. */
function people(readers: Record<string, string>, calls: string[], status = () => 200) {
  return vi.fn(async (url: string) => {
    calls.push(url)
    const code = status()
    if (code !== 200) return new Response(JSON.stringify({ error: "x" }), { status: code, headers: { "Retry-After": "1" } })
    const ids = new URL(url, "http://x").searchParams.get("ids")!.split(",")
    return Response.json({ people: ids.filter(id => id in readers).map(id => ({ id, name: readers[id] })) })
  })
}
const settle = () => new Promise(resolve => setTimeout(resolve, 0)).then(() => new Promise(resolve => setTimeout(resolve, 0)))

describe("mention names", () => {
  it("asks the Document's people lookup once for every id rendered together, and names only the people it returns", async () => {
    const calls: string[] = []
    const names = createMentionNames("ws 1", "page-1", people({ ada: "Ada Lovelace" }, calls))
    const seen = vi.fn()
    names.subscribe(seen)
    names.want(["gone", "ada", "ada"]); names.want(["ada"])
    expect(mentionLabel(names, "ada")).toBe("@Unknown member")
    await settle()
    expect(calls).toEqual(["/api/v1/pages/%40page-1/document/people?ids=ada%2Cgone&workspace_id=ws%201"])
    expect([mentionLabel(names, "ada"), mentionLabel(names, "gone"), mentionLabel(undefined, "ada")]).toEqual(["@Ada Lovelace", "@Unknown member", "@Unknown member"])
    expect(seen).toHaveBeenCalled()
    // A settled id, named or not, is never asked again.
    names.want(["ada", "gone"])
    await settle()
    expect(calls).toHaveLength(1)
  })

  it("stays within the lookup's bounds: at most 100 ids a call, and an id the server would refuse is never sent", async () => {
    const calls: string[] = []
    const names = createMentionNames("ws", "p", people({}, calls))
    names.want([...Array.from({ length: 150 }, (_, i) => `u${String(i).padStart(3, "0")}`), "bad id", "x".repeat(65)])
    await settle()
    const sent = calls.map(url => new URL(url, "http://x").searchParams.get("ids")!.split(","))
    expect(sent.map(ids => ids.length)).toEqual([100, 50])
    expect(sent.flat()).not.toContain("bad id")
    expect(mentionLabel(names, "bad id")).toBe("@Unknown member")
  })

  it("waits as the server asks after a rate limit, then names the person; a refusal leaves the neutral label", async () => {
    vi.useFakeTimers()
    const calls: string[] = []
    let next = 429
    const names = createMentionNames("ws", "p", people({ ada: "Ada" }, calls, () => { const code = next; next = 200; return code }))
    names.want(["ada"])
    await vi.advanceTimersByTimeAsync(0)
    expect(calls).toHaveLength(1)
    expect(mentionLabel(names, "ada")).toBe("@Unknown member")
    await vi.advanceTimersByTimeAsync(999)
    expect(calls).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(calls).toHaveLength(2)
    expect(mentionLabel(names, "ada")).toBe("@Ada")
    const refused = createMentionNames("ws", "p", people({ ada: "Ada" }, [], () => 404))
    refused.want(["ada"])
    await vi.advanceTimersByTimeAsync(0)
    expect(mentionLabel(refused, "ada")).toBe("@Unknown member")
  })

  it("retries a network or server failure with backoff, and gives up after a few attempts", async () => {
    vi.useFakeTimers()
    const calls: string[] = []
    const codes = [503, 599]
    const names = createMentionNames("ws", "p", vi.fn(async (url: string) => {
      const code = codes.shift() ?? 200
      if (code === 599) { calls.push(url); throw new TypeError("offline") }
      return people({ ada: "Ada" }, calls, () => code)(url)
    }))
    names.want(["ada"])
    await vi.advanceTimersByTimeAsync(0)
    expect(calls).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(2000)
    expect(calls).toHaveLength(2)
    expect(mentionLabel(names, "ada")).toBe("@Unknown member")
    await vi.advanceTimersByTimeAsync(4000)
    expect(calls).toHaveLength(3)
    expect(mentionLabel(names, "ada")).toBe("@Ada")
    const broken: string[] = []
    const down = createMentionNames("ws", "p", people({ ada: "Ada" }, broken, () => 500))
    down.want(["ada"])
    // Backoff 2, 4, 8 and 16 s, then five requests in all.
    await vi.advanceTimersByTimeAsync(2 * 60_000)
    expect(broken).toHaveLength(5)
    expect(mentionLabel(down, "ada")).toBe("@Unknown member")
  })

  it("asks again while names are shown, so a reader who lost access stops being named; dispose stops asking", async () => {
    vi.useFakeTimers()
    const calls: string[] = []
    const readers: Record<string, string> = { ada: "Ada" }
    const names = createMentionNames("ws", "p", people(readers, calls))
    const seen = vi.fn()
    names.subscribe(seen)
    names.want(["ada"])
    await vi.advanceTimersByTimeAsync(0)
    expect(mentionLabel(names, "ada")).toBe("@Ada")
    // An unchanged answer re-renders nothing.
    await vi.advanceTimersByTimeAsync(REFRESH_MS + 10)
    expect(calls).toHaveLength(2)
    expect(seen).toHaveBeenCalledTimes(1)
    // Not before the answer is five minutes old, and within one 30 s check after.
    delete readers.ada
    const before = calls.length
    await vi.advanceTimersByTimeAsync(REFRESH_MS - 30_000 - 20)
    expect(calls).toHaveLength(before)
    await vi.advanceTimersByTimeAsync(90_000)
    expect(calls).toHaveLength(before + 1)
    expect(mentionLabel(names, "ada")).toBe("@Unknown member")
    expect(seen).toHaveBeenCalledTimes(2)
    // Once nothing shows the mention it is not asked again.
    names.release(["ada"])
    await vi.advanceTimersByTimeAsync(3 * REFRESH_MS)
    expect(calls).toHaveLength(before + 1)
    // A session that ended sends nothing it had queued.
    const ended: string[] = []
    const closed = createMentionNames("ws", "p", people({}, ended))
    closed.want(["ada"]); closed.dispose()
    await vi.advanceTimersByTimeAsync(1000)
    expect(ended).toHaveLength(0)
    // A remount (React StrictMode) that wants the ids again asks for them.
    closed.want(["ada"])
    await vi.advanceTimersByTimeAsync(0)
    expect(ended).toHaveLength(1)
  })

  it("counts failed requests per id: an id that joined late is not given up with the one that failed longest", async () => {
    vi.useFakeTimers()
    const calls: string[] = []
    const names = createMentionNames("ws", "p", people({ ada: "Ada", bea: "Bea" }, calls, () => calls.length <= 5 ? 500 : 200))
    names.want(["ada"])
    // Ada is asked at 0, 2, 6 and 14 s; Bea, shown at 10 s, joins the 14 s request.
    await vi.advanceTimersByTimeAsync(10_000)
    names.want(["bea"])
    await vi.advanceTimersByTimeAsync(20_000)
    // At 30 s Ada's fifth request fails and she settles neutral; Bea, on her second, is asked again and named.
    expect(calls).toHaveLength(5)
    expect(mentionLabel(names, "ada")).toBe("@Unknown member")
    await vi.advanceTimersByTimeAsync(4_000)
    expect(calls).toHaveLength(6)
    expect(new URL(calls[5], "http://x").searchParams.get("ids")).toBe("bea")
    expect(mentionLabel(names, "bea")).toBe("@Bea")
  })

  it("keeps the server's wait after giving up on a rate limit", async () => {
    vi.useFakeTimers()
    const calls: string[] = []
    const names = createMentionNames("ws", "p", people({ ada: "Ada", bea: "Bea" }, calls, () => calls.length <= 5 ? 429 : 200))
    names.want(["ada"])
    // Requests at 0, 1, 2, 3 and 4 s; the fifth 429 gives up but still asks for 1 s of quiet.
    await vi.advanceTimersByTimeAsync(4500)
    expect(calls).toHaveLength(5)
    expect(mentionLabel(names, "ada")).toBe("@Unknown member")
    names.want(["bea"])
    await vi.advanceTimersByTimeAsync(499)
    expect(calls).toHaveLength(5)
    await vi.advanceTimersByTimeAsync(1)
    expect(calls).toHaveLength(6)
    expect(mentionLabel(names, "bea")).toBe("@Bea")
  })
})
