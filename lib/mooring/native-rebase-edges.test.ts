// @vitest-environment node
import { describe, expect, it, vi } from "vitest"
import { NATIVE_EXECUTE_MAX_COMMANDS } from "./native-editor-port"
import { keepLocalOver, rebaseOverLocal, type RebaseDeps } from "./native-rebase"
import { GRIDCRAFT_REPLAY } from "./gridcraft-replay"

// Edges of the concurrent-editing replay the main suite does not reach: every way out
// must leave the person's unsaved work as it was unless the replay and the base move both succeeded.
const HASH = "sha256:" + "b".repeat(64)
const set = (cell: string) => ({ command: "cell.set", params: { cell, input: "x" } })
const version = (n: number, over: Record<string, unknown> = {}) => ({ version: n, base_version: n - 1, engine_pin: "pin", replayable: true, ops: [set(`'L'!A${n}`)], ...over })
const answer = (versions: unknown[], over: Record<string, unknown> = {}) => ({ status: 200, body: { from: 5, head: 5 + versions.length, head_content_hash: HASH, replayable: true, versions, ...over } })
function deps(a: { status: number; body: unknown }, over: Partial<RebaseDeps> = {}) {
  return {
    base: "5", enginePin: "pin", sameBuild: true, replay: GRIDCRAFT_REPLAY, savedRevision: "doc:4", label: "Morgan: change", signal: new AbortController().signal,
    operations: vi.fn(async () => a),
    execute: vi.fn(async () => ({ revision: "doc:9" })),
    rebaseBase: vi.fn(() => true),
    ...over,
  }
}

describe("concurrent-editing replay: the edges", () => {
  it("refuses before asking the server when the undo label is empty or longer than the editor takes", async () => {
    for (const label of ["", "   ", "x".repeat(81)]) {
      const d = deps(answer([version(6)]), { label })
      await expect(rebaseOverLocal(d)).resolves.toEqual({ kind: "reload", reason: "invalid_label" })
      expect(d.operations).not.toHaveBeenCalled()
    }
  })

  it("replays several versions in order, as one undo step, and keeps the last agent's name", async () => {
    const d = deps(answer([version(6, { actor_kind: "agent", actor_name: "Ada", operation_id: "op-6" }), version(7, { actor_kind: "human", actor_name: "Bob" }), version(8, { actor_kind: "agent", actor_name: "  Morgan  ", operation_id: "op-8" })]))
    await expect(rebaseOverLocal(d)).resolves.toEqual({ kind: "rebased", version: "8", conflicts: [], truncated: false, operationIds: ["op-6", "op-8"], actorName: "Morgan" })
    expect(d.execute).toHaveBeenCalledTimes(1)
    expect(d.execute).toHaveBeenCalledWith([set("'L'!A6"), set("'L'!A7"), set("'L'!A8")], { keep: "human", label: "Morgan: change", baseRevision: "doc:4" }, d.signal)
    expect(d.rebaseBase).toHaveBeenCalledWith("8", HASH)
  })

  it("treats one unreplayable version among replayable ones as a layout change and replays nothing", async () => {
    const d = deps(answer([version(6), version(7, { replayable: false, ops: undefined }), version(8)]))
    await expect(rebaseOverLocal(d)).resolves.toEqual({ kind: "layout", head: "8", headHash: HASH })
    expect(d.execute).not.toHaveBeenCalled()
  })

  it("treats a command outside the replay list, or params that are not an object, as not replayable", async () => {
    for (const op of [{ command: "sheet.insert", params: { name: "X" } }, { command: "cell.set", params: "'L'!A1" }, { command: "cell.set", params: null }, "cell.set"]) {
      const d = deps(answer([version(6, { ops: [op] })]))
      await expect(rebaseOverLocal(d), JSON.stringify(op)).resolves.toMatchObject({ kind: "layout" })
      expect(d.execute).not.toHaveBeenCalled()
    }
  })

  it("replays a recalculation, which names no cell, and quoted sheet names with apostrophes and spaces", async () => {
    const ops = [{ command: "formulas.calculateNow" }, set("'Q1 ''26'!$B$2"), { command: "range.setValues", params: { range: "Data!A1:C3", values: [[1]] } }]
    const d = deps(answer([version(6, { ops })]))
    await expect(rebaseOverLocal(d)).resolves.toMatchObject({ kind: "rebased", version: "6" })
    expect(vi.mocked(d.execute).mock.calls[0][0]).toEqual([{ command: "formulas.calculateNow" }, ...ops.slice(1)])
  })

  it("does not replay an address longer than 300 characters", async () => {
    const d = deps(answer([version(6, { ops: [set(`'${"x".repeat(300)}'!A1`)] })]))
    await expect(rebaseOverLocal(d)).resolves.toMatchObject({ kind: "layout" })
  })

  it("opens the version when the log is beyond what the editor executes in one call", async () => {
    const many = Array.from({ length: NATIVE_EXECUTE_MAX_COMMANDS + 1 }, (_, i) => set(`'L'!A${i + 1}`))
    const d = deps(answer([version(6, { ops: many })]))
    await expect(rebaseOverLocal(d)).resolves.toEqual({ kind: "reload", reason: "too_large" })
    const big = deps(answer([version(6, { ops: [{ command: "range.setValues", params: { range: "'L'!A1:A2", values: [["y".repeat(70 * 1024)]] } }] })]))
    await expect(rebaseOverLocal(big)).resolves.toEqual({ kind: "reload", reason: "too_large" })
    expect(d.execute).not.toHaveBeenCalled(); expect(big.execute).not.toHaveBeenCalled()
  })

  it("rejects a log whose entries are not objects or whose versions are not positive integers", async () => {
    for (const versions of [[null], ["6"], [version(6, { version: 0 })], [version(6, { version: "06" })], [version(6, { version: 6.5 })]]) {
      const d = deps(answer(versions as unknown[]))
      await expect(rebaseOverLocal(d), JSON.stringify(versions)).resolves.toEqual({ kind: "reload", reason: "operations_malformed" })
    }
    for (const body of [null, [], { from: 5, head: 6 }, "ok"]) {
      await expect(rebaseOverLocal(deps({ status: 200, body })), JSON.stringify(body)).resolves.toEqual({ kind: "reload", reason: "operations_malformed" })
    }
  })

  it("ignores a head hash that is not sha256 hex, so Keep mine cannot move the base on it", async () => {
    const d = deps(answer([version(6, { replayable: false })], { head_content_hash: "sha256:XYZ" }))
    const outcome = await rebaseOverLocal(d)
    expect(outcome).toEqual({ kind: "layout", head: "6" })
    expect(keepLocalOver(outcome as { head: string }, d.rebaseBase)).toBe(false)
    expect(keepLocalOver({ head: "6", headHash: "md5:abc" }, d.rebaseBase)).toBe(false)
    expect(d.rebaseBase).not.toHaveBeenCalled()
  })

  it("reports Keep mine as refused when the save controller will not move its base", () => {
    expect(keepLocalOver({ head: "6", headHash: HASH }, () => false)).toBe(false)
  })

  it("names the editor's refusal reason only for a stale base or another engine", async () => {
    for (const [code, reason] of [["stale_base", "stale_base"], ["pin_mismatch", "pin_mismatch"], ["native_port_closed", "execute_failed"], ["", "execute_failed"]]) {
      const d = deps(answer([version(6)]), { execute: vi.fn(async () => { throw new Error(code) }) })
      await expect(rebaseOverLocal(d), code).resolves.toEqual({ kind: "reload", reason })
      expect(d.rebaseBase).not.toHaveBeenCalled()
    }
    const thrownString = deps(answer([version(6)]), { execute: vi.fn(async () => { throw "stale_base" }) })
    await expect(rebaseOverLocal(thrownString)).resolves.toEqual({ kind: "reload", reason: "execute_failed" })
  })

  it("rethrows when the person left while the replay ran, instead of reporting a reason", async () => {
    const ac = new AbortController()
    const abort = new DOMException("aborted", "AbortError")
    const d = deps(answer([version(6)]), { signal: ac.signal, execute: vi.fn(async () => { ac.abort(); throw abort }) })
    await expect(rebaseOverLocal(d)).rejects.toBe(abort)
    expect(d.rebaseBase).not.toHaveBeenCalled()
  })

  it("opens the version when the replay ran but the save controller refuses the new base", async () => {
    const d = deps(answer([version(6)]), { rebaseBase: vi.fn(() => false) })
    await expect(rebaseOverLocal(d)).resolves.toEqual({ kind: "reload", reason: "rebase_refused" })
  })

  it("passes on a truncated conflict list so the card can say there are more", async () => {
    const d = deps(answer([version(6)]), { execute: vi.fn(async () => ({ revision: "doc:9", conflicts: [{ sheet: "L", cell: "A6", agent: "x", kept: "y" }], conflictsTruncated: true })) })
    await expect(rebaseOverLocal(d)).resolves.toMatchObject({ kind: "rebased", truncated: true, conflicts: [{ cell: "A6" }] })
  })

  it("drops an operation id longer than 128 characters and an agent name over 64", async () => {
    const d = deps(answer([version(6, { actor_kind: "agent", actor_name: "n".repeat(65), operation_id: "o".repeat(129) })]))
    const outcome = await rebaseOverLocal(d)
    expect(outcome).toMatchObject({ kind: "rebased", operationIds: [] })
    expect(outcome).not.toHaveProperty("actorName")
    expect(vi.mocked(d.execute).mock.calls[0][1]).toEqual({ keep: "human", label: "Morgan: change", baseRevision: "doc:4" })
  })

  it("accepts a log that names its head only as `to`", async () => {
    const d = deps(answer([version(6)], { head: undefined, to: 6 }))
    await expect(rebaseOverLocal(d)).resolves.toMatchObject({ kind: "rebased", version: "6" })
  })
})
