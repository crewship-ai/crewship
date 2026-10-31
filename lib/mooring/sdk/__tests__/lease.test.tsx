/** Contract of the SDK lease module, asserted through the public SDK. */
import { webcrypto } from "node:crypto"
import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { NativeCraftLeaseBar, type NativeCraftLeaseView } from "@/components/features/pages/native-craft-lease-bar"
import { LeaseError, SaveError, createLeaseActions, createLeaseState, describeLease, ownsLease, parseLease, type LeaseCustody, type LeaseView } from ".."
import { saveRig } from "./rig"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.useRealTimers() })

const future = () => new Date(Date.now() + 60_000).toISOString()
const alice = { actor_id: "u-alice", display_name: "Alice", kind: "human" as const }
const bob = { actor_id: "u-bob", display_name: "Bob", kind: "human" as const, request_id: "req-bob" }
const held = (patch: Partial<LeaseView> = {}): LeaseView => ({ holder: alice, can_edit: true, mine: true, generation: "3", can_write: true, acquired_at: "2026-10-31T10:00:00Z", expires_at: future(), handoff_requested_by: [], ...patch })
const vacant = (): LeaseView => ({ holder: null, can_edit: true, mine: false, generation: null, can_write: false, acquired_at: null, expires_at: null, handoff_requested_by: [] })

describe("Mooring SDK lease view", () => {
  it("accepts the server view and refuses malformed or contradictory ones", () => {
    expect(parseLease(held())).toMatchObject({ mine: true, generation: "3" })
    expect(parseLease(vacant()).holder).toBeNull()
    for (const bad of [null, { ...held(), generation: "0" }, { ...held(), holder: null }, { ...held(), generation: null }, { ...held(), handoff_requested_by: [{ ...alice }] }, { ...held(), can_edit: "yes" }, { ...held(), mine: "true" }]) {
      expect(() => parseLease(bad), JSON.stringify(bad)).toThrow(LeaseError)
    }
  })

  it("owns the lease only while it is mine, writable, unexpired and at the expected generation", () => {
    expect(ownsLease(held())).toBe(true)
    expect(ownsLease(held(), "3")).toBe(true)
    expect(ownsLease(held(), "2")).toBe(false)
    expect(ownsLease(held({ can_write: false }))).toBe(false)
    expect(ownsLease(held({ can_edit: false }))).toBe(false)
    expect(ownsLease(held({ expires_at: new Date(Date.now() - 1).toISOString() }))).toBe(false)
  })

  it("describes the same controls the native lease bar renders", () => {
    const cases: [string, LeaseView, boolean][] = [
      ["vacant", vacant(), true],
      ["vacant read-only", vacant(), false],
      ["held by me", held(), true],
      ["held by me with a request", held({ handoff_requested_by: [bob] }), true],
      ["held by someone else", held({ mine: false, can_write: false, holder: { ...bob } }), true],
      ["held by an agent that may be stopped", held({ mine: false, can_write: false, holder: { actor_id: "a", display_name: " ", kind: "agent" }, can_stop_agent: true }), true],
      ["mine but write lost", held({ can_write: false }), true],
    ]
    for (const [name, view, mayEdit] of cases) {
      const controls = describeLease(view, mayEdit)
      render(<NativeCraftLeaseBar view={view as NativeCraftLeaseView} mayEdit={mayEdit} onAction={() => {}} onStopAgent={() => {}} />)
      const bar = screen.getByRole("toolbar")
      expect(bar.textContent, name).toContain(controls.label)
      const has = (label: string) => screen.queryByRole("button", { name: label }) !== null
      expect(has("Převzít úpravy"), name).toBe(controls.canAcquire)
      expect(has("Požádat o předání"), name).toBe(controls.canRequestHandoff)
      expect(has("Zastavit agenta"), name).toBe(controls.canStopAgent)
      expect(has("Uvolnit úpravy"), name).toBe(controls.canRelease)
      for (const target of controls.handoffTargets) expect(has(target.label), name).toBe(true)
      expect(bar.textContent?.includes("místní koncept zachován"), name).toBe(controls.draftKeptReadOnly)
      cleanup()
    }
    expect(describeLease(held({ mine: false, can_write: false }), true).label).toBe("Upravuje Alice")
    expect(describeLease(vacant(), true).label).toBe("Volné")
  })

  it("never rolls the holder back to an older generation delivered late", () => {
    const state = createLeaseState()
    expect(state.observe(held({ generation: "4" }))?.generation).toBe("4")
    expect(state.observe(held({ generation: "3", holder: { ...bob }, mine: false }))).toBeUndefined()
    expect(state.current?.generation).toBe("4")
    expect(state.observe(held({ generation: "5", holder: { ...bob }, mine: false }))?.holder?.display_name).toBe("Bob")
    expect(() => state.observe({ mine: true })).toThrow(LeaseError)
  })
})

function custody(patch: Partial<LeaseCustody> = {}): LeaseCustody & { calls: string[] } {
  const calls: string[] = []
  return { calls, generation: "3", flush: vi.fn(async () => { calls.push("flush") }), confirmedVersion: vi.fn(() => "9"), revokeWrite: vi.fn(async () => { calls.push("revoke"); return true }), ...patch }
}
function transport(after: LeaseView = held({ mine: false, can_write: false, holder: { ...bob }, generation: "4" })) {
  const calls: string[] = []
  return {
    calls,
    leaseAction: vi.fn(async (action: string, body: object) => { calls.push(`${action}:${JSON.stringify(body)}`); return {} }),
    lease: vi.fn(async () => after),
  }
}

describe("Mooring SDK lease actions", () => {
  const signal = new AbortController().signal

  it("refuses a stale generation locally, before any save or request", async () => {
    const t = transport(), c = custody({ generation: "2" })
    await expect(createLeaseActions(t, signal).release(held(), c)).rejects.toMatchObject({ reason: "stale_generation" })
    await expect(createLeaseActions(t, signal).release(held({ mine: false }), custody())).rejects.toMatchObject({ reason: "stale_generation" })
    expect(c.flush).not.toHaveBeenCalled()
    expect(t.leaseAction).not.toHaveBeenCalled()
  })

  it("hands off in custody order: final save, draft capture, request, proof", async () => {
    const t = transport(), c = custody()
    const proof = await createLeaseActions(t, signal).handOff(held({ handoff_requested_by: [bob] }), "req-bob", c)
    expect(c.calls).toEqual(["flush", "revoke"])
    expect(t.calls).toEqual([`handoff:${JSON.stringify({ generation: "3", request_id: "req-bob" })}`])
    expect(proof.holder?.display_name).toBe("Bob")
    expect(describeLease(proof, true).label).toBe("Upravuje Bob")
  })

  it("never asks the server when the final save or the draft capture is unconfirmed", async () => {
    for (const [c, reason] of [
      [custody({ flush: vi.fn(async () => { throw new Error("native_save_unconfirmed") }) }), "final_save_unconfirmed"],
      [custody({ confirmedVersion: () => undefined }), "final_save_unconfirmed"],
      [custody({ revokeWrite: vi.fn(async () => false) }), "draft_capture_failed"],
    ] as const) {
      const t = transport()
      await expect(createLeaseActions(t, signal).release(held(), c)).rejects.toMatchObject({ reason })
      expect(t.leaseAction).not.toHaveBeenCalled()
    }
  })

  it("refuses a hand-off to a request the server did not list", async () => {
    const t = transport()
    await expect(createLeaseActions(t, signal).handOff(held(), "req-ghost", custody())).rejects.toMatchObject({ reason: "unknown_request" })
    expect(t.leaseAction).not.toHaveBeenCalled()
  })

  it("maps a 409 from the server to a conflict and proves custody moved", async () => {
    const t = transport()
    t.leaseAction.mockRejectedValueOnce(new SaveError("conflict"))
    await expect(createLeaseActions(t, signal).release(held(), custody())).rejects.toMatchObject({ reason: "conflict" })
    const stuck = transport(held())
    await expect(createLeaseActions(stuck, signal).release(held(), custody())).rejects.toMatchObject({ reason: "custody_transfer_unconfirmed" })
  })

  it("acquires and requests a hand-off through the authorized transport", async () => {
    const t = transport(held())
    expect((await createLeaseActions(t, signal).acquire()).mine).toBe(true)
    await createLeaseActions(t, signal).requestHandoff()
    expect(t.calls).toEqual(["acquire:{}", "request-handoff:{}"])
  })

  it("never auto-saves a draft after hand-off: later input stays local", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    const r = saveRig({ generation: "3" })
    r.type("final words")
    const t = transport()
    const handing = createLeaseActions(t, signal).handOff(held({ handoff_requested_by: [bob] }), "req-bob", { generation: "3", flush: () => r.controller.flush(), confirmedVersion: () => r.controller.confirmedVersion(), revokeWrite: () => r.controller.revokeWrite() })
    await vi.runAllTimersAsync()
    await handing
    expect(r.transport.save).toHaveBeenCalledOnce()
    expect(new TextDecoder().decode(r.transport.save.mock.calls[0][0].bytes)).toBe("final words")
    r.type("typed after hand-off")
    await vi.advanceTimersByTimeAsync(5_000)
    await r.controller.save().catch(() => {})
    expect(r.transport.save).toHaveBeenCalledOnce()
    expect(r.transport.reconcile).not.toHaveBeenCalled()
    expect(r.last().phase).toBe("read-only")
    await r.controller.close()
  })
})
