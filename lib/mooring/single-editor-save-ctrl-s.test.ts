// @vitest-environment node
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { createSingleEditorSave, NativeSaveError, type NativeSaveReceipt, type NativeSaveRequest, type NativeSaveState } from "./single-editor-save"

// Owner's decision (#5075): Ctrl+S without a content change creates no new
// content version. It confirms the saved state; with an unclear result it first
// looks up the original attempt. A repeat must not create a duplicate write.
beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => vi.unstubAllGlobals())

const receipt = (r: NativeSaveRequest) => ({ ...r, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })
const unchangedAnswer = (r: NativeSaveRequest) => ({ ...r, version: r.baseVersion, unchanged: true, durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })
type Script = "commit" | "unchanged" | "unchanged-elsewhere" | "lose-reply" | "hold"
type Lookup = () => unknown

function fixture(app: "wordcraft" | "gridcraft", extra: { loaded?: { bytes: Uint8Array; mountedState?: string }; state?: string } = {}) {
  const live = { value: 1, state: extra.state ?? "state:1", captures: 0, confirmFails: 0, confirmHold: undefined as Promise<void> | undefined }
  const requests: NativeSaveRequest[] = [], lookups: NativeSaveRequest[] = [], states: NativeSaveState[] = [], receipts: NativeSaveReceipt[] = [], retired: string[] = []
  const script: Script[] = []
  let lookup: Lookup | undefined, release = () => {}
  const controller = createSingleEditorSave({
    workspaceId: "w", pageId: "p", applicationId: app, enginePin: "engine", baseVersion: "1", generation: "1", sessionId: "session", idleMs: 5, timeoutMs: 1000, reconcileBackoffMs: [60_000],
    ...(extra.loaded ? { loaded: extra.loaded } : {}),
    journal: {
      async persist() {},
      async confirm(d) { if (live.confirmHold) await live.confirmHold; if (live.confirmFails > 0) { live.confirmFails--; throw new Error("storage") } retired.push(d.editorRevision) },
      async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {}, async discardRecovered() {},
    },
    engine: {
      async snapshot(_s, id) { live.captures++; return { bytes: new Uint8Array([live.value]), editorRevision: id ?? `rpc:${live.captures}`, ...(app === "wordcraft" ? { nativeState: live.state } : {}) } },
      async validate() {},
    },
    transport: {
      async save(r) {
        requests.push(r)
        const step = script.shift() ?? "commit"
        if (step === "lose-reply") throw new Error("net")
        if (step === "hold") await new Promise<void>(done => { release = done })
        if (step === "unchanged-elsewhere") return { ...unchangedAnswer(r), version: "9" }
        return step === "unchanged" ? unchangedAnswer(r) : receipt(r)
      },
      async reconcile(r) { lookups.push(r); return lookup ? lookup() : receipt(r) },
    },
    onState(s) { states.push(s) },
    onReceipt(r) { receipts.push(r) },
  })
  return { controller, requests, lookups, states, receipts, retired, live, script, release: () => release(),
    setLookup(next: Lookup) { lookup = next },
    edit(v: number) { live.value = v; controller.edited() },
  }
}
type Fixture = ReturnType<typeof fixture>
const phase = (r: Fixture) => r.states.at(-1)?.phase
const clean = (r: Fixture) => vi.waitFor(() => expect(r.states.at(-1)).toEqual({ phase: "saved", dirty: false }))
const uncertain = (r: Fixture) => vi.waitFor(() => expect(phase(r)).toBe("uncertain"))
let ticket = 0
// Word carries a ticket; Sheet's request has none.
const press = (r: Fixture, app: string) => r.controller.save(app === "wordcraft" ? `ticket-${++ticket}` : undefined)

for (const app of ["wordcraft", "gridcraft"] as const) describe(app, () => {
  it("Ctrl+S after a confirmed autosave sends nothing, retires its row and confirms the saved version", async () => {
    const r = fixture(app)
    try {
      r.edit(2); await clean(r)
      expect(r.requests).toHaveLength(1)
      const retiredBefore = r.retired.length
      await press(r, app)
      expect(r.requests).toHaveLength(1)
      expect(r.retired.length).toBe(retiredBefore + 1)
      expect(r.receipts).toHaveLength(2)
      expect(r.receipts[1]).toMatchObject({ unchanged: true, version: "2", baseVersion: "2", contentHash: r.requests[0].contentHash, candidateHash: r.requests[0].contentHash })
      // The ACK names this press's own capture, not the autosave's.
      expect(r.receipts[1].editorRevision).not.toBe(r.receipts[0].editorRevision)
      await clean(r)
      expect(r.controller.confirmedVersion()).toBe("2")
    } finally { await r.controller.close() }
  })

  it("repeated presses never transmit and the next real change continues from the same version", async () => {
    const r = fixture(app)
    try {
      r.edit(2); await clean(r)
      await press(r, app); await press(r, app); await press(r, app)
      expect(r.requests).toHaveLength(1)
      r.edit(3); await clean(r)
      expect(r.requests).toHaveLength(2)
      expect(r.requests[1].baseVersion).toBe("2")
      expect(r.receipts.at(-1)).toMatchObject({ version: "3" })
    } finally { await r.controller.close() }
  })

  it("different bytes still save on Ctrl+S", async () => {
    const r = fixture(app)
    try {
      r.edit(2); await clean(r)
      r.live.value = 3
      await press(r, app)
      await vi.waitFor(() => expect(r.requests).toHaveLength(2))
      expect(r.receipts.at(-1)).toMatchObject({ version: "3" })
      expect(r.receipts.at(-1)?.unchanged).toBeUndefined()
    } finally { await r.controller.close() }
  })

  it("the same bytes at another base still save (A, B, back to A)", async () => {
    const r = fixture(app)
    try {
      r.edit(2); await clean(r)
      r.edit(3); await clean(r)
      r.live.value = 2
      await press(r, app)
      await vi.waitFor(() => expect(r.requests).toHaveLength(3))
      expect(r.requests[2]).toMatchObject({ baseVersion: "3", contentHash: r.requests[0].contentHash })
    } finally { await r.controller.close() }
  })

  it("an edit racing the ticket is not dropped: the press confirms its own capture and the edit still saves", async () => {
    const r = fixture(app)
    try {
      r.edit(2); await clean(r)
      const pressing = press(r, app)
      // The capture holds value 2; the edit lands while the capture is being decided.
      r.edit(3)
      await pressing
      await vi.waitFor(() => expect(r.requests).toHaveLength(2))
      expect(r.requests[1].baseVersion).toBe("2")
      await clean(r)
      expect(r.controller.confirmedVersion()).toBe("3")
    } finally { await r.controller.close() }
  })

  it("while a save is in flight the press waits for it and decides from its result", async () => {
    const r = fixture(app)
    try {
      r.edit(2); await clean(r)
      r.script.push("hold")
      r.edit(3)
      await vi.waitFor(() => expect(r.requests).toHaveLength(2))
      const pressing = press(r, app)
      await new Promise(done => setTimeout(done, 20))
      expect(r.requests).toHaveLength(2)
      r.release()
      await pressing
      await clean(r)
      expect(r.requests).toHaveLength(2)
      expect(r.controller.confirmedVersion()).toBe("3")
      // Word's ticket waits and gets its own confirmation; Sheet's has none to wait for.
      if (app === "wordcraft") expect(r.receipts.at(-1)).toMatchObject({ unchanged: true, version: "3" })
    } finally { r.release(); await r.controller.close() }
  })

  it("a seeded, untouched document sends nothing and confirms the loaded version", async () => {
    const r = fixture(app, { loaded: { bytes: new Uint8Array([1]), ...(app === "wordcraft" ? { mountedState: "state:1" } : {}) } })
    try {
      await press(r, app)
      expect(r.requests).toHaveLength(0)
      expect(r.receipts).toHaveLength(1)
      expect(r.receipts[0]).toMatchObject({ unchanged: true, version: "1", baseVersion: "1" })
      await clean(r)
      expect(r.controller.confirmedVersion()).toBe("1")
    } finally { await r.controller.close() }
  })

  it("a local draft that differs from a seeded document still saves", async () => {
    const r = fixture(app, { loaded: { bytes: new Uint8Array([9]) } })
    try {
      await press(r, app)
      await vi.waitFor(() => expect(r.requests).toHaveLength(1))
      expect(r.receipts[0].unchanged).toBeUndefined()
    } finally { await r.controller.close() }
  })

  it("a row that storage cannot retire is kept and gets no ACK until it is retired", async () => {
    const r = fixture(app)
    try {
      r.edit(2); await clean(r)
      const before = r.receipts.length
      r.live.confirmFails = 1
      if (app === "wordcraft") await expect(press(r, app)).rejects.toThrow("storage")
      else await press(r, app)
      expect(r.requests).toHaveLength(1)
      expect(r.receipts).toHaveLength(before)
      await vi.waitFor(() => expect(r.receipts).toHaveLength(before + 1))
      expect(r.receipts.at(-1)).toMatchObject({ unchanged: true, version: "2" })
      await clean(r)
      expect(r.requests).toHaveLength(1)
    } finally { await r.controller.close() }
  })

  it("the server's unchanged answer confirms the attempt without moving the base", async () => {
    const r = fixture(app)
    try {
      r.script.push("unchanged")
      r.edit(2); await clean(r)
      expect(r.receipts[0]).toMatchObject({ unchanged: true, version: "1" })
      expect(r.controller.confirmedVersion()).toBe("1")
      r.edit(3); await clean(r)
      expect(r.requests[1].baseVersion).toBe("1")
      expect(r.receipts.at(-1)).toMatchObject({ version: "2" })
    } finally { await r.controller.close() }
  })

  it("an unchanged answer naming another version is not a confirmation", async () => {
    const r = fixture(app)
    try {
      r.script.push("unchanged-elsewhere")
      r.edit(2); await uncertain(r)
      expect(r.receipts).toHaveLength(0)
      expect(r.controller.confirmedVersion()).toBeUndefined()
    } finally { await r.controller.close() }
  })

  it("a press during a return check looks nothing up and sends nothing", async () => {
    const r = fixture(app)
    try {
      r.script.push("lose-reply")
      r.edit(2); await uncertain(r)
      r.controller.hold()
      await press(r, app).catch(() => {})
      expect(r.lookups).toHaveLength(0)
      expect(r.requests).toHaveLength(1)
      r.controller.release()
    } finally { await r.controller.close() }
  })

  it("an unconfirmed save that committed: the press looks it up, sends nothing and confirms it", async () => {
    const r = fixture(app)
    try {
      r.script.push("lose-reply")
      r.edit(2); await uncertain(r)
      expect(r.requests).toHaveLength(1)
      await press(r, app)
      expect(r.lookups).toHaveLength(1)
      expect(r.lookups[0].attemptId).toBe(r.requests[0].attemptId)
      expect(r.requests).toHaveLength(1)
      expect(r.receipts.map(x => [x.version, x.unchanged])).toEqual([["2", undefined], ["2", true]])
      await clean(r)
    } finally { await r.controller.close() }
  })

  it("an unconfirmed save the server retired: the press saves once, as a fresh attempt", async () => {
    const r = fixture(app)
    try {
      r.script.push("lose-reply")
      r.edit(2); await uncertain(r)
      r.setLookup(() => { throw new NativeSaveError("absent") })
      await press(r, app)
      expect(r.lookups).toHaveLength(1)
      await vi.waitFor(() => expect(r.requests).toHaveLength(2))
      expect(r.requests[1].attemptId).not.toBe(r.requests[0].attemptId)
      expect(r.requests[1].baseVersion).toBe("1")
      await clean(r)
      expect(r.requests).toHaveLength(2)
    } finally { await r.controller.close() }
  })

  for (const [name, answer] of [
    ["a network failure", () => { throw new Error("net") }],
    ["a proof-less conflict", () => { throw new NativeSaveError("conflict") }],
    ["an invalid answer", () => ({ nonsense: true })],
    ["an unchanged answer", null],
  ] as const) it(`an unconfirmed save after ${name}: the press writes nothing and fails visibly`, async () => {
    const r = fixture(app)
    try {
      r.script.push("lose-reply")
      r.edit(2); await uncertain(r)
      r.setLookup(answer ?? (() => unchangedAnswer(r.lookups.at(-1)!)))
      if (app === "wordcraft") await expect(press(r, app)).rejects.toThrow("unconfirmed")
      else await press(r, app)
      expect(r.lookups).toHaveLength(1)
      expect(r.requests).toHaveLength(1)
      expect(r.receipts).toHaveLength(0)
      expect(phase(r)).toBe("uncertain")
      expect(r.controller.confirmedVersion()).toBeUndefined()
    } finally { await r.controller.close() }
  })

  it("a press during a lookup already running joins it instead of asking twice", async () => {
    const r = fixture(app)
    try {
      r.script.push("lose-reply")
      r.edit(2); await uncertain(r)
      let answer!: (value: unknown) => void
      r.setLookup(() => new Promise(done => { answer = done }))
      const lookup = r.controller.reconcile()
      await vi.waitFor(() => expect(r.lookups).toHaveLength(1))
      const pressing = press(r, app)
      await new Promise(done => setTimeout(done, 20))
      answer(receipt(r.lookups[0]))
      await lookup; await pressing
      expect(r.lookups).toHaveLength(1)
      expect(r.requests).toHaveLength(1)
      await clean(r)
    } finally { await r.controller.close() }
  })

  it("revocation during a save looks up that same attempt id, never a new one", async () => {
    const r = fixture(app)
    try {
      r.script.push("hold")
      r.edit(2)
      await vi.waitFor(() => expect(r.requests).toHaveLength(1))
      await r.controller.revokeWrite()
      expect(r.lookups).toHaveLength(1)
      expect(r.lookups[0].attemptId).toBe(r.requests[0].attemptId)
      expect(r.requests).toHaveLength(1)
    } finally { r.release(); await r.controller.close() }
  })

  it("a press cannot reach the transport after writing was revoked during its lookup", async () => {
    const r = fixture(app)
    try {
      r.script.push("lose-reply")
      r.edit(2); await uncertain(r)
      r.setLookup(() => new Promise(() => {}))
      const pressing = press(r, app).catch(error => error)
      await vi.waitFor(() => expect(r.lookups).toHaveLength(1))
      await r.controller.revokeWrite()
      await pressing
      expect(r.requests).toHaveLength(1)
    } finally { await r.controller.close() }
  })
})

describe("wordcraft native state", () => {
  it("a newer save running when the row has retired fails the older ticket instead of confirming it", async () => {
    const r = fixture("wordcraft")
    try {
      r.edit(2); await clean(r)
      let release!: () => void
      r.live.confirmHold = new Promise<void>(done => { release = done })
      const older = press(r, "wordcraft"); void older.catch(() => {})
      await vi.waitFor(() => expect(r.live.captures).toBe(2))
      await new Promise(done => setTimeout(done, 10))
      r.live.value = 3
      const newer = press(r, "wordcraft")
      await vi.waitFor(() => expect(r.requests).toHaveLength(2))
      r.live.confirmHold = undefined; release()
      await expect(older).rejects.toThrow("pending")
      await newer
      expect(r.receipts.filter(x => x.unchanged)).toHaveLength(0)
      expect(r.receipts.at(-1)).toMatchObject({ version: "3" })
    } finally { await r.controller.close() }
  })

  it("equal bytes with another native state are not confirmed locally: the server decides", async () => {
    const r = fixture("wordcraft")
    try {
      r.edit(2); await clean(r)
      r.live.state = "state:2"
      r.script.push("unchanged")
      await press(r, "wordcraft")
      await vi.waitFor(() => expect(r.requests).toHaveLength(2))
      expect(r.receipts.at(-1)).toMatchObject({ unchanged: true, version: "2" })
      await clean(r)
    } finally { await r.controller.close() }
  })

  it("equal bytes of a seeded document with another native state go to the server too", async () => {
    const r = fixture("wordcraft", { state: "state:after-undo", loaded: { bytes: new Uint8Array([1]), mountedState: "state:mounted" } })
    try {
      r.script.push("unchanged")
      await press(r, "wordcraft")
      await vi.waitFor(() => expect(r.requests).toHaveLength(1))
      expect(r.receipts[0]).toMatchObject({ unchanged: true, version: "1" })
    } finally { await r.controller.close() }
  })

  it("Word's normalized rendition of an untouched document confirms the loaded version with no RPC", async () => {
    const r = fixture("wordcraft", { state: "state:mounted", loaded: { bytes: new Uint8Array([7]), mountedState: "state:mounted" } })
    try {
      await press(r, "wordcraft")
      expect(r.requests).toHaveLength(0)
      expect(r.receipts[0]).toMatchObject({ unchanged: true, version: "1", baseVersion: "1" })
    } finally { await r.controller.close() }
  })

  it("an edit before the press keeps a normalized rendition from counting as untouched", async () => {
    const r = fixture("wordcraft", { state: "state:mounted", loaded: { bytes: new Uint8Array([7]), mountedState: "state:mounted" } })
    try {
      r.controller.edited()
      await press(r, "wordcraft")
      await vi.waitFor(() => expect(r.requests).toHaveLength(1))
    } finally { await r.controller.close() }
  })
})
