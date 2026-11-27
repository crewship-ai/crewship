// @vitest-environment node
import { describe, expect, it, vi } from "vitest"
import { applyAgentVersion } from "./native-live-apply"

const hash = `sha256:${"a".repeat(64)}`
const setup = (execute = vi.fn(async () => ({ documentId: "d", revision: "d:7", applied: 1 })), adopt = vi.fn(() => true)) => {
  const reopen = vi.fn(async () => {})
  return { execute, adopt, reopen, targets: { port: { execute }, save: { adoptVersion: adopt } } }
}
const base = (reopen: () => Promise<void>) => ({ version: "5", contentHash: hash, commands: [{ command: "cell.set" as const, params: { cell: "A1", input: "1" } }], baseRevision: "d:3", reopen, signal: new AbortController().signal })

describe("applyAgentVersion", () => {
  it("replays the commands, adopts the agent's version as the baseline and does not reload", async () => {
    const s = setup()
    await expect(applyAgentVersion(s.targets, base(s.reopen))).resolves.toEqual({ mode: "replayed" })
    expect(s.execute).toHaveBeenCalledWith(expect.any(Array), { baseRevision: "d:3" }, expect.anything())
    expect(s.adopt).toHaveBeenCalledWith("5", hash)
    expect(s.reopen).not.toHaveBeenCalled()
  })
  it("opens the whole version when there is nothing to replay", async () => {
    const s = setup()
    for (const commands of [undefined, []]) {
      s.reopen.mockClear()
      await expect(applyAgentVersion(s.targets, { ...base(s.reopen), commands })).resolves.toEqual({ mode: "reopened", reason: "no_commands" })
      expect(s.reopen).toHaveBeenCalledTimes(1)
    }
    expect(s.execute).not.toHaveBeenCalled()
  })
  it("falls back to the whole open when the editor refuses, without adopting anything", async () => {
    const s = setup(vi.fn(async () => { throw new Error("A cell or text box is being edited.") }))
    const out = await applyAgentVersion(s.targets, base(s.reopen))
    expect(out).toEqual({ mode: "reopened", reason: "A cell or text box is being edited." })
    expect(s.adopt).not.toHaveBeenCalled()
    expect(s.reopen).toHaveBeenCalledTimes(1)
  })
  it("falls back when the baseline cannot be adopted (unsaved work of the person), so the editor is never left on a state the server does not hold", async () => {
    const s = setup(undefined, vi.fn(() => false))
    await expect(applyAgentVersion(s.targets, base(s.reopen))).resolves.toEqual({ mode: "reopened", reason: "baseline_refused" })
    expect(s.reopen).toHaveBeenCalledTimes(1)
  })
  it("an abort is not turned into a fallback, and a failing reopen is reported", async () => {
    const c = new AbortController()
    const s = setup(vi.fn(async () => { c.abort(); throw new Error("native_request_aborted") }))
    await expect(applyAgentVersion(s.targets, { ...base(s.reopen), signal: c.signal })).rejects.toThrow("native_request_aborted")
    expect(s.reopen).not.toHaveBeenCalled()
    const failing = setup(vi.fn(async () => { throw new Error("x") }))
    failing.reopen.mockRejectedValueOnce(new Error("open failed"))
    await expect(applyAgentVersion(failing.targets, base(failing.reopen))).rejects.toThrow("open failed")
  })
})
