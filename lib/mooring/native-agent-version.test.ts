import { describe, it, expect, vi } from "vitest"
import { nextPaint, createAgentVersionQueue, parseAgentVersionFields, publishAgentVersion, subscribeAgentVersions, AGENT_CHANGED_FINISH, AGENT_CHANGED_RELOAD, type AgentVersionEvent, type AgentVersionHost } from "./native-agent-version"

const HASH = "sha256:" + "a".repeat(64)
const cmds = [{ command: "cell.set" as const, params: { a: 1 } }]
const event = (version: string, extra: Partial<AgentVersionEvent> = {}): AgentVersionEvent => ({ pageId: "pg", version, contentHash: HASH, baseVersion: String(Number(version) - 1), commands: cmds, ...extra })

function rig(initial: { mode?: "none" | "reader" | "writable"; clean?: boolean; confirmed?: string } = {}) {
  const s = { mode: initial.mode ?? "writable", clean: initial.clean ?? true, confirmed: initial.confirmed ?? "4" as string | undefined }
  const execute = vi.fn(async () => ({ documentId: "d", revision: "r", applied: 1 }))
  const adopt = vi.fn((version: string) => { s.confirmed = version; return true })
  const notices: (string | undefined)[] = []
  const refreshReader = vi.fn(async () => {})
  const workingLog: boolean[] = []
  const host: AgentVersionHost = {
    working: on => workingLog.push(on),
    state: () => ({ mode: s.mode, clean: s.clean, confirmedVersion: s.clean && s.mode === "writable" ? s.confirmed : undefined }),
    targets: () => ({ port: { execute }, save: { adoptVersion: adopt } }),
    refreshReader, notice: t => notices.push(t),
  }
  return { s, host, execute, adopt, notices, refreshReader, workingLog, queue: createAgentVersionQueue(host) }
}

describe("createAgentVersionQueue", () => {
  it("replays the next version in the open editor and adopts it", async () => {
    const r = rig()
    r.queue.push(event("5")); await r.queue.idle()
    expect(r.execute).toHaveBeenCalledTimes(1)
    expect(r.adopt).toHaveBeenCalledWith("5", HASH)
    expect(r.notices.filter(Boolean)).toEqual([])
  })

  it("handles two versions in a quick row one after the other, each exactly base + 1", async () => {
    const r = rig()
    r.queue.push(event("5")); r.queue.push(event("6")); await r.queue.idle()
    expect(r.adopt.mock.calls.map(c => c[0])).toEqual(["5", "6"])
    expect(r.execute).toHaveBeenCalledTimes(2)
  })

  it("handles a version once, and ignores one that is not newer than the editor shows (a reloaded chat)", async () => {
    const r = rig()
    r.queue.push(event("5")); r.queue.push(event("5")); r.queue.push(event("3")); r.queue.push(event("4")); await r.queue.idle()
    expect(r.execute).toHaveBeenCalledTimes(1)
  })

  it("a version that skips one is not replayed: the person is told to reload, nothing is replaced", async () => {
    const r = rig()
    r.queue.push(event("7", { baseVersion: "6" })); await r.queue.idle()
    expect(r.execute).not.toHaveBeenCalled()
    expect(r.notices).toContain(AGENT_CHANGED_RELOAD)
  })

  it("never takes a change while the person has unsaved work: it waits and says so, then applies once saved", async () => {
    const r = rig({ clean: false })
    r.queue.push(event("5")); await r.queue.idle()
    expect(r.execute).not.toHaveBeenCalled()
    expect(r.notices).toEqual([AGENT_CHANGED_FINISH])
    r.s.clean = true; r.queue.settled(); await r.queue.idle()
    expect(r.execute).toHaveBeenCalledTimes(1)
    expect(r.adopt).toHaveBeenCalledWith("5", HASH)
  })

  it("while waiting, only the latest change is kept; if it no longer follows the base the person reloads", async () => {
    const r = rig({ clean: false })
    r.queue.push(event("5")); r.queue.push(event("6")); await r.queue.idle()
    r.s.clean = true; r.queue.settled(); await r.queue.idle()
    expect(r.execute).not.toHaveBeenCalled()
    expect(r.notices.at(-1)).toBe(AGENT_CHANGED_RELOAD)
  })

  it("a reading editor opens the latest version (the existing path); read-only never executes", async () => {
    const r = rig({ mode: "reader" })
    r.queue.push(event("5")); await r.queue.idle()
    expect(r.refreshReader).toHaveBeenCalledTimes(1)
    expect(r.execute).not.toHaveBeenCalled()
  })

  it("no open editor: nothing happens", async () => {
    const r = rig({ mode: "none" })
    r.queue.push(event("5")); await r.queue.idle()
    expect(r.execute).not.toHaveBeenCalled(); expect(r.refreshReader).not.toHaveBeenCalled()
  })

  it("commands left out or no hash: reload, never a guess", async () => {
    const r = rig()
    r.queue.push(event("5", { commands: undefined, commandsOmitted: true })); await r.queue.idle()
    expect(r.execute).not.toHaveBeenCalled(); expect(r.notices).toContain(AGENT_CHANGED_RELOAD)
    const q = rig(); q.queue.push(event("5", { contentHash: undefined })); await q.queue.idle()
    expect(q.execute).not.toHaveBeenCalled(); expect(q.notices).toContain(AGENT_CHANGED_RELOAD)
  })

  it("a refused execute ends in the reload notice and the baseline is not adopted", async () => {
    const r = rig(); r.execute.mockRejectedValueOnce(new Error("native_execute_refused"))
    r.queue.push(event("5")); await r.queue.idle()
    expect(r.adopt).not.toHaveBeenCalled()
    expect(r.notices).toContain(AGENT_CHANGED_RELOAD)
  })

  it("a disposed queue does nothing (page switch, panel closed)", async () => {
    const r = rig(); r.queue.dispose()
    r.queue.push(event("5")); await r.queue.idle()
    expect(r.execute).not.toHaveBeenCalled()
  })

  it("rejects a version that is not a plain positive number", async () => {
    const r = rig()
    for (const v of ["0", "05", "-1", "1e3", "abc", ""]) r.queue.push(event(v))
    await r.queue.idle(); expect(r.execute).not.toHaveBeenCalled()
  })
})

describe("the working note", () => {
  it("is up BEFORE the replay starts and down when it ends", async () => {
    const r = rig(); const seen: boolean[] = []
    r.execute.mockImplementation(async () => { seen.push(r.workingLog.at(-1) === true); return { documentId: "d", revision: "r", applied: 1 } })
    r.queue.push(event("5")); await r.queue.idle()
    expect(seen).toEqual([true]); expect(r.workingLog).toEqual([true, false])
  })
  it("comes down on a failure too, and for a reading editor around the replay", async () => {
    const r = rig(); r.execute.mockRejectedValueOnce(new Error("refused"))
    r.queue.push(event("5")); await r.queue.idle(); expect(r.workingLog).toEqual([true, false])
    const q = rig({ mode: "reader" }); q.queue.push(event("5")); await q.queue.idle()
    expect(q.workingLog).toEqual([true, false]); expect(q.refreshReader).toHaveBeenCalled()
  })
  it("is not shown for a version that is ignored or waits for the person's work", async () => {
    const r = rig(); r.queue.push(event("3")); await r.queue.idle(); expect(r.workingLog).toEqual([])
    const w = rig({ clean: false }); w.queue.push(event("5")); await w.queue.idle(); expect(w.workingLog).toEqual([])
  })
  it("draws a frame before heavy work: nextPaint waits for animation frames when there are any", async () => {
    const calls: string[] = []
    vi.stubGlobal("requestAnimationFrame", (cb: () => void) => { calls.push("raf"); setTimeout(cb, 0); return 1 })
    await nextPaint(); expect(calls).toEqual(["raf", "raf"]); vi.unstubAllGlobals()
  })
})

describe("the bus", () => {
  it("reaches only the editor of that page and stops after unsubscribe", () => {
    const a = vi.fn(), b = vi.fn()
    const offA = subscribeAgentVersions("pg_a", a), offB = subscribeAgentVersions("pg_b", b)
    publishAgentVersion({ pageId: "pg_a", version: "2" })
    expect(a).toHaveBeenCalledTimes(1); expect(b).not.toHaveBeenCalled()
    offA(); offB()
    publishAgentVersion({ pageId: "pg_a", version: "3" })
    expect(a).toHaveBeenCalledTimes(1)
  })
})

describe("parseAgentVersionFields", () => {
  it("reads the typed fields and drops malformed ones", () => {
    expect(parseAgentVersionFields({ version: 5, content_hash: HASH, base_version: 4, commands: cmds })).toEqual({ version: "5", contentHash: HASH, baseVersion: "4", commands: cmds })
    expect(parseAgentVersionFields({ version: "5", content_hash: "nothex", base_version: "x", commands: [{ nope: 1 }] })).toEqual({ version: "5" })
    expect(parseAgentVersionFields({ version: 5, commands_omitted: true, commands: cmds })).toEqual({ version: "5", commandsOmitted: true })
    expect(parseAgentVersionFields({ version: 0 })).toBeUndefined()
    expect(parseAgentVersionFields({})).toBeUndefined()
  })
})

// Concurrent editing: the agent's version reaches an editor with the person's unsaved work.
describe("an agent version over the person's unsaved work", () => {
  it("is replayed over it with the person's cells kept, instead of waiting for the person", async () => {
    const r = rig({ clean: false })
    const rebase = vi.fn(async () => ({ kind: "rebased" as const, version: "5", conflicts: [{ sheet: "L", cell: "B9", agent: "", kept: "LN-1" }], truncated: false, operationIds: ["op-1"] }))
    const rebased = vi.fn()
    r.host.rebase = rebase; r.host.rebased = rebased
    r.queue.push(event("5")); await r.queue.idle()
    expect(rebase).toHaveBeenCalledTimes(1)
    expect(rebased).toHaveBeenCalledWith(expect.objectContaining({ version: "5" }), expect.objectContaining({ kind: "rebased", conflicts: [{ sheet: "L", cell: "B9", agent: "", kept: "LN-1" }] }))
    // Said while the replay runs (the work is kept), taken down once it is in.
    expect(r.notices[0]).toBe(AGENT_CHANGED_FINISH)
    expect(r.notices.at(-1)).toBeUndefined()
    expect(r.workingLog).toEqual([true, false])
  })
  it("hands the choice to the host for a layout change, never a silent change", async () => {
    const r = rig({ clean: false })
    const rebased = vi.fn(), outcome = { kind: "layout" as const, head: "5" }
    r.host.rebase = vi.fn(async () => outcome); r.host.rebased = rebased
    r.queue.push(event("5")); await r.queue.idle()
    expect(rebased).toHaveBeenCalledWith(expect.anything(), outcome)
  })
  // A person with unsaved work is never told to reload (they could lose it); the change waits for their save.
  it("never tells a person with unsaved work to reload when the replay cannot be done: the change waits for their save", async () => {
    for (const reason of ["too_many", "operations_500", "operations_malformed", "no_head_hash", "execute_failed", "rebase_refused", "operations_404", "operations_0"]) {
      const r = rig({ clean: false })
      r.host.rebase = vi.fn(async () => ({ kind: "reload" as const, reason }))
      r.queue.push(event("5")); await r.queue.idle()
      expect(r.notices, reason).not.toContain(AGENT_CHANGED_RELOAD)
      expect(r.notices.at(-1), reason).toBe(AGENT_CHANGED_FINISH)
      // Saved: the change is handled again, now over a clean editor.
      r.s.clean = true; r.s.confirmed = "4"
      r.host.rebase = vi.fn(async () => ({ kind: "rebased" as const, version: "5", conflicts: [], truncated: false, operationIds: [] }))
      r.queue.settled(); await r.queue.idle()
      expect(r.host.rebase, reason).toHaveBeenCalledTimes(1)
    }
  })
  it("tells a clean editor to reload when the change cannot be shown otherwise", async () => {
    const r = rig()
    r.host.rebase = vi.fn(async () => ({ kind: "reload" as const, reason: "too_many" }))
    r.queue.push(event("5", { commands: undefined, contentHash: undefined })); await r.queue.idle()
    expect(r.notices).toContain(AGENT_CHANGED_RELOAD)
  })
  it("also brings a clean editor to a workbook version that came without commands", async () => {
    const r = rig()
    const rebase = vi.fn(async () => ({ kind: "rebased" as const, version: "5", conflicts: [], truncated: false, operationIds: [] }))
    r.host.rebase = rebase
    r.queue.push(event("5", { commands: undefined, contentHash: undefined })); await r.queue.idle()
    expect(rebase).toHaveBeenCalledTimes(1)
    expect(r.notices).not.toContain(AGENT_CHANGED_RELOAD)
  })
})

describe("a save refused because the Sheet moved on", () => {
  it("replays what was saved meanwhile over the person's work, through the same queue", async () => {
    const r = rig({ clean: false })
    const rebase = vi.fn(async () => ({ kind: "rebased" as const, version: "6", conflicts: [], truncated: false, operationIds: [] }))
    const rebased = vi.fn()
    r.host.rebase = rebase; r.host.rebased = rebased
    r.queue.moved(); await r.queue.idle()
    expect(rebase).toHaveBeenCalledTimes(1)
    expect(rebased).toHaveBeenCalledWith(undefined, expect.objectContaining({ kind: "rebased" }))
  })
  it("after a moved save it cannot replay, asks the server again instead of telling the person to reload", async () => {
    const r = rig({ clean: false })
    const retry = vi.fn()
    r.host.rebase = vi.fn(async () => ({ kind: "reload" as const, reason: "too_many" })); r.host.retryMoved = retry
    r.queue.moved(); await r.queue.idle()
    expect(r.notices).not.toContain(AGENT_CHANGED_RELOAD)
    expect(retry).toHaveBeenCalledTimes(1)
  })
})

describe("a save refused as moved when nothing newer is logged", () => {
  it("lifts the refusal and saves again instead of leaving the work stuck unsaved", async () => {
    const r = rig({ clean: false })
    const retry = vi.fn()
    r.host.rebase = vi.fn(async () => ({ kind: "current" as const }))
    r.host.retryMoved = retry
    r.queue.moved(); await r.queue.idle()
    expect(retry).toHaveBeenCalledTimes(1)
  })
})

describe("an agent version whose commands the editor cannot replay as they are", () => {
  it("is brought in through the operations log, not replayed and then reloaded", async () => {
    const r = rig()
    const rebase = vi.fn(async () => ({ kind: "rebased" as const, version: "5", conflicts: [], truncated: false, operationIds: [] }))
    r.host.rebase = rebase
    r.queue.push(event("5", { commands: [{ command: "range.clear" as never, params: { range: "'L'!B8:B577" } }] })); await r.queue.idle()
    expect(r.execute).not.toHaveBeenCalled()
    expect(rebase).toHaveBeenCalledTimes(1)
    expect(r.notices).not.toContain(AGENT_CHANGED_RELOAD)
  })
})

describe("an error while bringing in an agent version", () => {
  it("over unsaved work keeps it and waits, and never says reload", async () => {
    const r = rig({ clean: false })
    r.host.rebase = vi.fn(async () => { throw new TypeError("boom") })
    r.queue.push(event("5")); await r.queue.idle()
    expect(r.notices).not.toContain(AGENT_CHANGED_RELOAD)
    expect(r.notices.at(-1)).toBe(AGENT_CHANGED_FINISH)
  })
})
