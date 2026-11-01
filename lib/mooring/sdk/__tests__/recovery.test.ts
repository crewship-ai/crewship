/** Contract of the SDK recovery module, asserted through the public SDK. */
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { compareRecovery, openRecoveryJournal, recoveryOffers, type RecoveryDraft } from ".."
import { openNativeDraftJournal } from "../../native-draft"
import { memoryJournal, saveRig } from "./rig"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })
const bytes = (text: string) => new TextEncoder().encode(text)
const draft = (text: string): RecoveryDraft => ({ applicationId: "notecraft", enginePin: "pin", sessionId: "tab-old", generation: "2", baseVersion: "5", editorRevision: "note:9", bytes: bytes(text) })

describe("Mooring SDK recovery", () => {
  it("is the host's private draft journal, not a second store", () => {
    expect(openRecoveryJournal).toBe(openNativeDraftJournal)
  })

  it("offers another mount's draft for compare and copy only", async () => {
    const rows = new Map([["old-tab", draft("lost words")]])
    const offers = await recoveryOffers(memoryJournal(rows))
    expect(offers).toHaveLength(1)
    const [offer] = offers
    expect(Object.keys(offer).sort()).toEqual(["actions", "compare", "copy", "draft"])
    expect(offer.actions).toEqual(["compare", "copy"])
    const copy = offer.copy()
    copy[0] = 0
    expect(new TextDecoder().decode(offer.copy())).toBe("lost words")
    const differs = await offer.compare({ version: "6", bytes: bytes("server words") })
    expect(differs).toMatchObject({ serverVersion: "6", draftBaseVersion: "5", draftGeneration: "2", equal: false })
    expect(differs.serverHash).toMatch(/^[a-f0-9]{64}$/)
    expect((await offer.compare({ version: "6", bytes: bytes("lost words") })).equal).toBe(true)
    expect(rows.size).toBe(1)
  })

  it("never auto-saves a recovered draft over the new holder's version", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    const rows = new Map([["old-tab", draft("lost words")]])
    const r = saveRig({ journal: memoryJournal(rows), generation: "3", baseVersion: "6" })
    const offers = await recoveryOffers(r.journal)
    await offers[0].compare({ version: "6", bytes: bytes("server") })
    offers[0].copy()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(r.transport.save).not.toHaveBeenCalled()
    expect(r.journal.confirm).not.toHaveBeenCalled()
    expect(rows.get("old-tab")?.generation).toBe("2")
    await r.controller.close()
  })

  it("hashes exactly the bytes it compares", async () => {
    const result = await compareRecovery({ version: "1", bytes: bytes("") }, draft(""))
    expect(result.serverHash).toBe("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
    expect(result.equal).toBe(true)
  })
})
