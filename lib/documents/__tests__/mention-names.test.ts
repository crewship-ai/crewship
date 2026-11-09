import { afterEach, describe, expect, it, vi } from "vitest"
import { createMentionNames, mentionLabel } from "../mention-names"

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
})
