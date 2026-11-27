// @vitest-environment node
import { describe, expect, it, vi } from "vitest"
import { keepLocalOver, rebaseOverLocal, sameEngineBuild, type RebaseDeps } from "./native-rebase"
import { GRIDCRAFT_REPLAY } from "./gridcraft-replay"

const HASH = "sha256:" + "a".repeat(64)
const clear = { command: "edit.clearContents", params: { range: "'L'!B8:B577" } }
const log = (over: Record<string, unknown> = {}, versions: Record<string, unknown>[] = [{ version: 6, base_version: 5, actor_kind: "agent", operation_id: "op-1", engine_pin: "pin", calculate: "saved", replayable: true, ops: [clear], digest: null }]) =>
  ({ status: 200, body: { from: 5, to: 6, head: 6, head_content_hash: HASH, replayable: true, versions, ...over } })
function deps(answer: { status: number; body: unknown }, over: Partial<RebaseDeps> = {}) {
  return {
    base: "5", enginePin: "pin", sameBuild: true, replay: GRIDCRAFT_REPLAY, savedRevision: "doc:4", label: "Morgan: change", signal: new AbortController().signal,
    operations: vi.fn(async () => answer),
    execute: vi.fn(async () => ({ revision: "doc:9", conflicts: [{ sheet: "L", cell: "B9", agent: "", kept: "LN-1" }] })),
    rebaseBase: vi.fn(() => true),
    ...over,
  }
}

describe("replaying others' saved versions over the person's unsaved work", () => {
  // Keep mine = the save base moves to the head without a replay; the agent's version stays in history.
  it("lets the person keep their work over a layout change by saving it on top of the head as it is", async () => {
    const d = deps(log())
    const kept = keepLocalOver({ head: "6", headHash: HASH }, d.rebaseBase)
    expect(kept).toBe(true)
    expect(d.rebaseBase).toHaveBeenCalledWith("6", HASH)
    expect(keepLocalOver({ head: "6" }, d.rebaseBase), "without the head's hash nothing is moved").toBe(false)
  })
  it("names the undo step and the change after the agent that wrote it", async () => {
    const d = deps(log({}, [{ version: "6", base_version: "5", actor_kind: "agent", actor_name: "Morgan", operation_id: "op-1", engine_pin: "pin", replayable: true, ops: [clear] }]), { label: "Agent: change" })
    await expect(rebaseOverLocal(d)).resolves.toMatchObject({ kind: "rebased", actorName: "Morgan" })
    expect(d.execute).toHaveBeenCalledWith([clear], { keep: "human", label: "Morgan: change", baseRevision: "doc:4" }, d.signal)
  })
  it("replays the log keeping the person's cells, then saves on top of the newest version", async () => {
    const d = deps(log())
    await expect(rebaseOverLocal(d)).resolves.toEqual({ kind: "rebased", version: "6", conflicts: [{ sheet: "L", cell: "B9", agent: "", kept: "LN-1" }], truncated: false, operationIds: ["op-1"] })
    expect(d.operations).toHaveBeenCalledWith("5", d.signal)
    expect(d.execute).toHaveBeenCalledWith([clear], { keep: "human", label: "Morgan: change", baseRevision: "doc:4" }, d.signal)
    expect(d.rebaseBase).toHaveBeenCalledWith("6", HASH)
  })
  it("gives precedence only to the agent's authorized operations in a chat Sheet", async () => {
    const replayed = { revision: "doc:9", savedRevision: "doc:saved-8", dirty: false, conflicts: [{ sheet: "L", cell: "B9" }] }
    const d = deps(log(), { keep: "agent", execute: vi.fn(async () => replayed) })
    await expect(rebaseOverLocal(d)).resolves.toMatchObject({ kind: "rebased", version: "6", conflicts: replayed.conflicts })
    expect(d.execute).toHaveBeenCalledWith([clear], { keep: "agent", label: "Morgan: change", baseRevision: "doc:4" }, d.signal)
    expect(d.rebaseBase).toHaveBeenCalledWith("6", HASH, replayed)

    const human = deps(log({}, [{ version: 6, base_version: 5, actor_kind: "human", engine_pin: "pin", replayable: true, ops: [clear] }]), { keep: "agent" })
    await expect(rebaseOverLocal(human)).resolves.toMatchObject({ kind: "layout" })
    expect(human.execute).not.toHaveBeenCalled()
  })
  it("leaves the person's work untouched when a version cannot be replayed, and offers the choice", async () => {
    for (const answer of [log({ replayable: false }, [{ version: 6, base_version: 5, replayable: false, engine_pin: "pin" }])]) {
      const d = deps(answer)
      await expect(rebaseOverLocal(d)).resolves.toEqual({ kind: "layout", head: "6", headHash: HASH })
      expect(d.execute).not.toHaveBeenCalled(); expect(d.rebaseBase).not.toHaveBeenCalled()
    }
    const refused = deps(log(), { execute: vi.fn(async () => { throw new Error("not_replayable") }) })
    await expect(rebaseOverLocal(refused)).resolves.toEqual({ kind: "layout", head: "6", headHash: HASH })
    expect(refused.rebaseBase).not.toHaveBeenCalled()
  })
  it("opens the version instead when the log is too large, malformed, gapped or from another engine", async () => {
    const cases: [ReturnType<typeof log> | { status: number; body: unknown }, string][] = [
      [{ status: 413, body: { code: "too_many" } }, "too_many"],
      [{ status: 403, body: {} }, "operations_403"],
      [log({ from: 4 }), "operations_malformed"],
      [log({ head: 7, to: 7 }), "operations_malformed"],
      [log({}, [{ version: 7, base_version: 6, replayable: true, ops: [clear], engine_pin: "pin" }]), "operations_malformed"],
      [log({ head_content_hash: undefined }), "no_head_hash"],
    ]
    for (const [answer, reason] of cases) {
      const d = deps(answer)
      await expect(rebaseOverLocal(d), reason).resolves.toEqual({ kind: "reload", reason })
      expect(d.execute).not.toHaveBeenCalled()
    }
  })
  // A cell without its sheet would be written into whichever sheet the person has open.
  it("never replays an address without its sheet", async () => {
    for (const op of [{ command: "cell.set", params: { cell: "C1", input: "done" } }, { command: "range.setValues", params: { range: "D1:E1", values: [[1, "x"]] } }, { command: "edit.clearContents", params: {} }]) {
      const d = deps(log({}, [{ version: "6", base_version: "5", actor_kind: "agent", engine_pin: "pin", replayable: true, ops: [op] }]))
      await expect(rebaseOverLocal(d), JSON.stringify(op)).resolves.toMatchObject({ kind: "layout" })
      expect(d.execute).not.toHaveBeenCalled()
    }
    const named = deps(log({}, [{ version: "6", base_version: "5", actor_kind: "agent", engine_pin: "pin", replayable: true, ops: [{ command: "cell.set", params: { cell: "'L'!C1", input: "done" } }] }]))
    await expect(rebaseOverLocal(named)).resolves.toMatchObject({ kind: "rebased" })
  })
  // A version replays only on the engine that wrote it, named in every version.
  it("replays nothing when a version's engine is missing or another, and keeps the person's work with the choice", async () => {
    for (const pin of [undefined, "", "other"]) {
      const d = deps(log({}, [{ version: "6", base_version: "5", actor_kind: "agent", replayable: true, ops: [clear], ...(pin === undefined ? {} : { engine_pin: pin }) }]))
      await expect(rebaseOverLocal(d), String(pin)).resolves.toEqual({ kind: "layout", head: "6", headHash: HASH })
      expect(d.execute).not.toHaveBeenCalled(); expect(d.rebaseBase).not.toHaveBeenCalled()
    }
    const later = deps(log({ to: "7", head: "7" }, [
      { version: "6", base_version: "5", actor_kind: "agent", engine_pin: "pin", replayable: true, ops: [clear] },
      { version: "7", base_version: "6", actor_kind: "agent", replayable: true, ops: [clear] },
    ]))
    await expect(rebaseOverLocal(later), "one version without its engine stops the whole replay").resolves.toMatchObject({ kind: "layout", head: "7" })
    expect(later.execute).not.toHaveBeenCalled()
  })
  it("replays a change on two sheets in one step over the person's unsaved work, keeping their cells on both", async () => {
    const l = { command: "edit.clearContents", params: { range: "'L'!B8:B577" } }, q = { command: "range.setValues", params: { range: "'Q 3'!A1:B1", values: [[1, "x"]] } }
    const conflicts = [{ sheet: "L", cell: "B9", agent: "", kept: "LN-1" }, { sheet: "Q 3", cell: "A1", agent: "1", kept: "7" }]
    const d = deps(log({}, [{ version: "6", base_version: "5", actor_kind: "agent", engine_pin: "pin", replayable: true, ops: [l, q] }]), { execute: vi.fn(async () => ({ revision: "doc:9", conflicts })) })
    await expect(rebaseOverLocal(d)).resolves.toMatchObject({ kind: "rebased", version: "6", conflicts })
    expect(d.execute).toHaveBeenCalledTimes(1)
    expect(d.execute).toHaveBeenCalledWith([l, q], { keep: "human", label: "Morgan: change", baseRevision: "doc:4" }, d.signal)
    expect(d.rebaseBase).toHaveBeenCalledWith("6", HASH)
  })
  it("does nothing when nothing is newer than the base", async () => {
    const d = deps({ status: 200, body: { from: 5, to: 5, head: 5, replayable: true, versions: [] } })
    await expect(rebaseOverLocal(d)).resolves.toEqual({ kind: "current" })
    expect(d.execute).not.toHaveBeenCalled()
  })
  // The editor and the server run the same engine build, or nothing is replayed; a build either
  // side does not name counts as another.
  it("replays nothing when the editor's engine build is not the server's, and offers the choice", async () => {
    const d = deps(log(), { sameBuild: false })
    await expect(rebaseOverLocal(d)).resolves.toEqual({ kind: "layout", head: "6", headHash: HASH })
    expect(d.execute).not.toHaveBeenCalled(); expect(d.rebaseBase).not.toHaveBeenCalled()
  })
  // A replay keeping the person's cells is based on the saved version the editor confirmed; without one
  // nothing is sent and the person's work stays as it is, with the choice.
  it("sends no replay without the saved version the editor confirmed, and offers the choice", async () => {
    const d = deps(log(), { savedRevision: undefined })
    await expect(rebaseOverLocal(d)).resolves.toEqual({ kind: "layout", head: "6", headHash: HASH })
    expect(d.execute).not.toHaveBeenCalled(); expect(d.rebaseBase).not.toHaveBeenCalled()
  })
})

it("counts builds as the same only when both sides name the same one", () => {
  const b = "0.19.1+59f6b6c14a37 patch " + "d".repeat(64)
  expect(sameEngineBuild(b, b)).toBe(true)
  expect(sameEngineBuild(b, "0.19.1+59f6b6c14a37 patch " + "e".repeat(64))).toBe(false)
  expect(sameEngineBuild(undefined, b)).toBe(false)
  expect(sameEngineBuild(b, undefined)).toBe(false)
  expect(sameEngineBuild(undefined, undefined), "two development builds are not known to match").toBe(false)
})
