import { describe, expect, it } from "vitest"
import { durabilityOf, initialSaveState, losesWork, saveReducer, saveStateLabel, saveWordOf, type SaveEvent, type SaveState } from "../save-state"

const run = (...events: SaveEvent[]): SaveState => events.reduce(saveReducer, initialSaveState)
const opened: SaveEvent = { type: "opened", canWrite: true }

describe("durability", () => {
  // The five Document states, derived exactly as the Document editor always has.
  it.each([
    ["nothing unsent", [opened], "confirmed"],
    ["an edit before the journal is ready", [opened, { type: "edit", pending: 1 }], "unsaved"],
    ["an edit whose journal write is running, journal not ready", [opened, { type: "edit", pending: 1 }, { type: "journal", writes: 1 }], "writing"],
    ["an edit whose journal write is running", [opened, { type: "stored" }, { type: "edit", pending: 1 }, { type: "journal", writes: 1 }], "writing"],
    ["an edit retained by the journal", [opened, { type: "stored" }, { type: "edit", pending: 1 }, { type: "journal", writes: 0 }], "local"],
    ["an edit after the journal failed", [opened, { type: "stored" }, { type: "edit", pending: 1 }, { type: "journal", writes: 1 }, { type: "storage-unavailable", failed: true }], "unsaved"],
    ["an earlier draft held", [opened, { type: "held", drafts: 1, pending: 1 }], "held"],
    ["a held draft discarded", [opened, { type: "held", drafts: 1, pending: 1 }, { type: "held", drafts: 0, pending: 0 }], "confirmed"],
    ["an acknowledged edit", [opened, { type: "stored" }, { type: "edit", pending: 1 }, { type: "send" }, { type: "ack", pending: 0 }], "confirmed"],
  ] as [string, SaveEvent[], string][])("%s → %s", (_name, events, durability) => {
    expect(durabilityOf(run(...events))).toBe(durability)
  })
})

describe("save words", () => {
  it.each([
    ["before the head is read", [], "opening"],
    ["opened, nothing written yet", [opened], "syncing"],
    ["read only", [{ type: "opened", canWrite: false }], "readonly"],
    ["an edit queued", [opened, { type: "edit", pending: 1 }], "saving"],
    ["an edit with no save route", [{ type: "opened", canWrite: true, canSend: false }, { type: "edit", pending: 1 }], "unsaved"],
    ["a write in flight", [opened, { type: "edit", pending: 1 }, { type: "send" }], "syncing"],
    ["a verified receipt", [opened, { type: "edit", pending: 1 }, { type: "send" }, { type: "ack", pending: 0 }], "saved"],
    ["a replayed receipt", [opened, { type: "edit", pending: 1 }, { type: "send" }, { type: "duplicate-ack", pending: 0 }], "saved"],
    ["a transient failure", [opened, { type: "edit", pending: 1 }, { type: "send" }, { type: "transient-fail", retryAfterMs: 1000 }], "waiting"],
    ["rate limited", [opened, { type: "edit", pending: 1 }, { type: "send" }, { type: "rate-limited" }], "waiting"],
    ["retry budget spent", [opened, { type: "edit", pending: 1 }, { type: "send" }, { type: "transient-fail", stuck: true }], "unsaved"],
    ["offline", [opened, { type: "edit", pending: 1 }, { type: "send" }, { type: "offline" }], "unsaved"],
    ["a conflict", [opened, { type: "edit", pending: 1 }, { type: "send" }, { type: "conflict" }], "unsaved"],
    ["a rejection", [opened, { type: "edit", pending: 1 }, { type: "send" }, { type: "rejected" }], "unsaved"],
    ["a 2xx without a receipt", [opened, { type: "edit", pending: 1 }, { type: "send" }, { type: "unconfirmed" }], "unsaved"],
    ["denied", [opened, { type: "edit", pending: 1 }, { type: "denied" }], "readonly"],
    ["another tab holds the journal", [opened, { type: "storage-unavailable", busy: true }], "busy"],
    ["reset", [opened, { type: "edit", pending: 1 }, { type: "reset" }], "opening"],
  ] as [string, SaveEvent[], string][])("%s → %s", (_name, events, word) => {
    expect(saveWordOf(run(...events))).toBe(word)
  })
})

describe("DX-02: never saved without a verified receipt", () => {
  it.each([
    ["a recount to zero", { type: "recount", pending: 0 }],
    ["a discarded held draft", { type: "held", drafts: 0, pending: 0 }],
    ["a failure", { type: "transient-fail" }],
    ["the journal becoming ready", { type: "stored" }],
  ] as [string, SaveEvent][])("%s does not claim a receipt", (_name, event) => {
    const state = run(opened, { type: "edit", pending: 1 }, { type: "send" }, event)
    expect(state.receipt).toBe(false)
    expect(saveWordOf(state)).not.toBe("saved")
  })
  it("a later edit withdraws an earlier receipt", () => {
    const state = run(opened, { type: "edit", pending: 1 }, { type: "send" }, { type: "ack", pending: 0 }, { type: "edit", pending: 1 })
    expect(state.receipt).toBe(false)
    expect(durabilityOf(state)).not.toBe("confirmed")
  })
  it("an ack that leaves later writes queued is not saved", () => {
    const state = run(opened, { type: "edit", pending: 2 }, { type: "send" }, { type: "ack", pending: 1 })
    expect(saveWordOf(state)).toBe("saving")
    expect(losesWork(state)).toBe(true)
  })
  it("an edit always counts as unsent work", () => {
    expect(run(opened, { type: "edit", pending: 0 }).pending).toBe(1)
  })
  it("returns the same state for an event that changes nothing", () => {
    const state = run(opened, { type: "journal", writes: 0 })
    expect(saveReducer(state, { type: "journal", writes: 0 })).toBe(state)
  })
})

describe("labels", () => {
  it.each([
    ["confirmed", true, "Confirmed by server"],
    ["confirmed", false, undefined],
    ["writing", false, "Saving to this browser… · not yet confirmed by server"],
    ["local", false, "Saved locally in this browser · not yet confirmed by server"],
    ["unsaved", false, "Not stored in this browser or on the server · keep this tab open"],
    ["held", false, "An earlier unsent draft is held in this browser and was not restored"],
  ] as const)("%s (saved %s)", (durability, saved, label) => { expect(saveStateLabel(durability, saved)).toBe(label) })
})
