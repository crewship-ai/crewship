// @vitest-environment node
// The replay over the person's unsaved work is the same for every Craft application; which commands replay
// and what makes them safe (a Sheet's cell names its sheet) is the application's adapter. An application without one
// replays nothing and the person gets the choice.
import { describe, expect, it, vi } from "vitest"
import { rebaseOverLocal, type ReplayAdapter } from "./native-rebase"
import { GRIDCRAFT_REPLAY } from "./gridcraft-replay"

const HASH = "sha256:" + "a".repeat(64)
const insert = { command: "text.insert", params: { anchor: "p-7", text: "Hello" } }
const log = (ops: unknown[]) => ({ status: 200, body: { from: 5, to: 6, head: 6, head_content_hash: HASH, replayable: true, versions: [{ version: 6, base_version: 5, actor_kind: "agent", operation_id: "op-1", engine_pin: "pin", replayable: true, ops }] } })
const deps = (ops: unknown[], replay: ReplayAdapter | null) => ({
  base: "5", enginePin: "pin", sameBuild: true, replay, savedRevision: "doc:4", label: "Agent: change", signal: new AbortController().signal,
  operations: vi.fn(async () => log(ops)), execute: vi.fn(async () => ({ revision: "doc:9" })), rebaseBase: vi.fn(() => true),
})
// What another application could bring: its own commands, its own rule for a stable address.
const anchored: ReplayAdapter = { replayable: op => op.command === "text.insert" && typeof op.params?.anchor === "string" }

describe("the replay with an application's adapter", () => {
  it("replays exactly what the adapter says replays", async () => {
    const d = deps([insert], anchored)
    await expect(rebaseOverLocal(d)).resolves.toMatchObject({ kind: "rebased", version: "6" })
    expect(d.execute).toHaveBeenCalledWith([insert], { keep: "human", label: "Agent: change", baseRevision: "doc:4" }, d.signal)
  })
  it("replays nothing the adapter refuses, and offers the choice", async () => {
    const d = deps([{ command: "text.insert", params: { at: 12, text: "x" } }], anchored)
    await expect(rebaseOverLocal(d)).resolves.toEqual({ kind: "layout", head: "6", headHash: HASH })
    expect(d.execute).not.toHaveBeenCalled()
  })
  it("replays nothing for an application without an adapter", async () => {
    const d = deps([insert], null)
    await expect(rebaseOverLocal(d)).resolves.toEqual({ kind: "layout", head: "6", headHash: HASH })
    expect(d.execute).not.toHaveBeenCalled()
  })
  it("keeps a Sheet's rules in the Sheet's adapter: a cell names its sheet, and only its four commands replay", () => {
    expect(GRIDCRAFT_REPLAY.replayable({ command: "cell.set", params: { cell: "'L'!B2", input: "1" } })).toBe(true)
    expect(GRIDCRAFT_REPLAY.replayable({ command: "cell.set", params: { cell: "B2", input: "1" } })).toBe(false)
    expect(GRIDCRAFT_REPLAY.replayable({ command: "formulas.calculateNow" })).toBe(true)
    expect(GRIDCRAFT_REPLAY.replayable(insert)).toBe(false)
  })
})
