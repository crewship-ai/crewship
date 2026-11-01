/** A read revocation is one terminal path, whichever call learns of it:
 * stop, purge the journal, emit `revoked` once, never report a capture. */
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { SaveError } from ".."
import { saveRig } from "./rig"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => { vi.unstubAllGlobals() })
const revoked = () => Promise.reject(new SaveError("read_revoked"))
const terminal = (r: ReturnType<typeof saveRig>) => r.states.filter(s => s.phase === "revoked" || s.phase === "stopped")

describe("read revocation in the save lifecycle", () => {
  it("proveLost with a read-revoked reconcile purges the journal, emits revoked and reports no capture", async () => {
    const r = saveRig()
    r.transport.save.mockRejectedValueOnce(new Error("network"))
    r.type("secret")
    await expect(r.controller.save("t1")).rejects.toThrow()
    expect(r.last()).toMatchObject({ phase: "uncertain" })
    expect(r.journal.rows.size).toBe(1)
    r.transport.reconcile.mockImplementation(revoked)
    await expect(r.controller.revokeWrite()).resolves.toBe(false)
    expect(r.journal.revokeRead).toHaveBeenCalled()
    expect(r.journal.rows.size).toBe(0)
    expect(terminal(r)).toEqual([{ phase: "revoked", dirty: false, reason: "read_revoked" }])
    await expect(r.controller.revokeWrite()).resolves.toBe(false)
    await r.controller.close()
    expect(terminal(r)).toHaveLength(1)
  })

  it("a read-revoked save is the same single terminal state; flush and later saves reject with read_revoked", async () => {
    const r = saveRig()
    r.transport.save.mockImplementation(revoked)
    r.type("secret")
    await expect(r.controller.flush()).rejects.toMatchObject({ reason: "read_revoked" })
    expect(r.journal.rows.size).toBe(0)
    expect(terminal(r)).toEqual([{ phase: "revoked", dirty: false, reason: "read_revoked" }])
    await expect(r.controller.save("t2")).rejects.toMatchObject({ reason: "read_revoked" })
    await expect(r.controller.revokeWrite()).resolves.toBe(false)
    expect(r.transport.save).toHaveBeenCalledOnce()
  })

  it("a read-revoked explicit reconcile purges and emits revoked", async () => {
    const r = saveRig()
    r.transport.save.mockRejectedValueOnce(new Error("network"))
    r.type("secret")
    await expect(r.controller.save("t1")).rejects.toThrow()
    r.transport.reconcile.mockImplementation(revoked)
    await r.controller.reconcile()
    expect(r.journal.rows.size).toBe(0)
    expect(terminal(r)).toEqual([{ phase: "revoked", dirty: false, reason: "read_revoked" }])
  })

  it("an earlier successful capture is not reported once read was revoked", async () => {
    const r = saveRig()
    r.type("words")
    const first = r.controller.revokeWrite()
    await expect(first).resolves.toBe(true)
    await r.controller.revokeRead()
    expect(terminal(r)).toEqual([{ phase: "revoked", dirty: false, reason: "read_revoked" }])
    await expect(r.controller.revokeWrite()).resolves.toBe(false)
    expect(r.journal.rows.size).toBe(0)
  })
})
