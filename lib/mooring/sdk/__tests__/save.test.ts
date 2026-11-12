/** Contract of the SDK save lifecycle, asserted only through the public SDK. */
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { SAVE_PHASES, SaveError, saveLabel, saveSettled, type SavePhase, type SaveRequest } from ".."
import { deferred, receipt, saveRig } from "./rig"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

describe("Mooring SDK save lifecycle", () => {
  it("reports Saved only after the durable receipt, and journals before sending", async () => {
    const r = saveRig()
    r.type("hello")
    await r.controller.save()
    expect(r.journal.persist.mock.invocationCallOrder[0]).toBeLessThan(r.transport.save.mock.invocationCallOrder[0])
    expect(r.last()).toMatchObject({ phase: "saved", dirty: false })
    expect(saveLabel(r.last() as never)).toBe("Uloženo")
    expect(r.controller.confirmedVersion()).toBe("2")
    expect(r.journal.rows.size).toBe(0)
    await r.controller.close()
  })

  it("never rolls a status back: a late receipt does not hide a newer local draft", async () => {
    const r = saveRig(), reply = deferred<unknown>()
    r.transport.save.mockImplementationOnce(() => reply.promise)
    r.type("first")
    const saving = r.controller.save()
    await vi.waitFor(() => expect(r.transport.save).toHaveBeenCalledOnce())
    r.type("second") // typed while the first save is in flight
    reply.resolve(receipt(r.transport.save.mock.calls[0][0]))
    await saving
    expect(r.last()).toMatchObject({ phase: "dirty", dirty: true })
    expect(saveLabel(r.last() as never)).not.toBe("Uloženo")
    expect(saveSettled(r.last() as never)).toBe(false)
    expect(r.controller.confirmedVersion()).toBeUndefined()
    await r.controller.close()
  })

  it("keeps a lost reply uncertain and proves it with an identical reconcile, never a blind resend", async () => {
    const r = saveRig()
    r.transport.save.mockRejectedValueOnce(new Error("network"))
    r.type("hello")
    await r.controller.save()
    expect(r.last()).toMatchObject({ phase: "uncertain" })
    expect(saveLabel(r.last() as never)).toBe("Výsledek nepotvrzen")
    await r.controller.save()
    expect(r.transport.save).toHaveBeenCalledOnce()
    await r.controller.reconcile()
    const [sent] = r.transport.save.mock.calls[0] as [SaveRequest]
    const [proved] = r.transport.reconcile.mock.calls[0] as [SaveRequest]
    expect(proved).toEqual(sent)
    expect(r.last()).toMatchObject({ phase: "saved", dirty: false })
    await r.controller.close()
  })

  it("leaves the draft in place on 409 conflict and stops sending", async () => {
    const r = saveRig()
    r.transport.save.mockRejectedValueOnce(new SaveError("conflict"))
    r.type("mine")
    await r.controller.save()
    expect(r.last()).toMatchObject({ phase: "draft", dirty: true, reason: "conflict" })
    expect(saveLabel(r.last() as never)).toBe("Koncept zůstává v tomto editoru")
    expect(r.journal.confirm).not.toHaveBeenCalled()
    expect(new TextDecoder().decode([...r.journal.rows.values()][0].bytes)).toBe("mine")
    r.type("more")
    await r.controller.save()
    expect(r.transport.save).toHaveBeenCalledOnce()
    await r.controller.close()
  })

  it("refuses a receipt for another generation as unconfirmed", async () => {
    const r = saveRig({ generation: "4" })
    r.transport.save.mockImplementationOnce(async req => ({ ...receipt(req), generation: "5" }))
    r.type("x")
    await r.controller.save()
    expect(r.last()).toMatchObject({ phase: "uncertain", reason: "invalid_receipt" })
    expect(r.journal.rows.size).toBe(1)
    await r.controller.close()
  })

  it("sends every write fenced by the lease generation and the base version", async () => {
    const r = saveRig({ baseVersion: "7", generation: "3" })
    r.type("x")
    await r.controller.save()
    expect(r.transport.save.mock.calls[0][0]).toMatchObject({ generation: "3", baseVersion: "7" })
    r.type("y")
    await r.controller.save()
    expect(r.transport.save.mock.calls[1][0]).toMatchObject({ generation: "3", baseVersion: "8" })
    await r.controller.close()
  })

  it("labels every phase and treats only clean saved or idle as settled", () => {
    const all: Record<SavePhase, true> = { idle: true, dirty: true, saving: true, saved: true, draft: true, uncertain: true, "read-only": true, stopped: true, revoked: true }
    expect([...SAVE_PHASES].sort()).toEqual(Object.keys(all).sort())
    for (const phase of SAVE_PHASES) expect(saveLabel({ phase, dirty: false })).toBeTruthy()
    expect(SAVE_PHASES.filter(phase => saveSettled({ phase, dirty: false }))).toEqual(["idle", "saved"])
    expect(saveSettled({ phase: "saved", dirty: true })).toBe(false)
  })
})
