// node --test e2e/acceptance/prd/craft-session/confirm.test.mjs
// (also run by e2e/acceptance/prd/report.test.mjs, the PRD runner step in CI)
import assert from "node:assert/strict"
import { test } from "node:test"
import { PAGE_STALLS_DESCRIPTION, finishPress, hourInterruptions, isUnconfirmedStatus, markTrail, pressOutcome, producerOf, saveAccounting, startPress, startVerify, trackedSaveWait, lastCommittedSave, pageStalls, verifyDue, auditStep, baselineBefore, cellInTable, confirmCycle, describeAudit, holdsMarker, labelVersion, markerReport, pendingReplies, sentSince, splitBy, tableRunsLeft } from "./confirm.ts"

const word = (paras) => JSON.stringify({ body: paras.map((text) => ({ kind: "para", text, runs: [{ len: text.length }] })) })
const C = "HOURf9vyrC010"
const typed = [C, `${C}B`]

// RC-4 (#4871): the table step saved v13 and v14 before cycle 10 typed; the typing never
// reached the document, yet "Saved · version 14" counted as the cycle's save.
test("a label that moved 12 -> 14 before the typing does not confirm markers the server lacks", () => {
  const head = { version: "14", text: word(["HOURf9vyrC009", "HOURf9vyrC009B", ""]) }
  const c = confirmCycle({ typed, baseline: "12", label: "14", head })
  assert.equal(c.confirmed, false)
  assert.match(c.problem, /server head v14 lacks 2 of 2 typed markers, first HOURf9vyrC010/)
})

test("markers in the server head confirm the cycle at the head version", () => {
  const head = { version: "15", text: word([`${C} the quick brown fox`, `${C}B second line`]) }
  assert.deepEqual(confirmCycle({ typed, baseline: "14", label: "15", head }), { confirmed: true, version: "15" })
})

test("the server confirms a save the label missed", () => {
  const head = { version: "15", text: word([`${C} x`, `${C}B y`]) }
  assert.deepEqual(confirmCycle({ typed, baseline: "14", label: undefined, head }), { confirmed: true, version: "15" })
})

test("only the B line on the server does not confirm the cycle line", () => {
  const head = { version: "15", text: word([`${C}B second line`]) }
  const c = confirmCycle({ typed, baseline: "14", label: "15", head })
  assert.equal(c.confirmed, false)
  assert.match(c.problem, /lacks 1 of 2 typed markers, first HOURf9vyrC010;/)
})

test("no shown save and no markers on the server is not confirmed", () => {
  const c = confirmCycle({ typed, baseline: "14", label: undefined, head: { version: "14", text: word(["x"]) } })
  assert.equal(c.confirmed, false)
  assert.match(c.problem, /no save newer than version 14 was shown/)
})

test("a cycle that typed nothing is never confirmed", () => {
  assert.equal(confirmCycle({ typed: [], baseline: "1", label: "2", head: { version: "2", text: "" } }).confirmed, false)
})

test("the baseline takes saves of the steps earlier in the cycle", () => {
  assert.equal(labelVersion("Saved · version 14"), "14")
  assert.equal(labelVersion("Unsaved draft"), undefined)
  assert.equal(baselineBefore("12", "Saved · version 14"), "14")
  assert.equal(baselineBefore("12", "Saving…"), "12")
  assert.equal(baselineBefore("15", "Saved · version 14"), "15")
})

test("markers match as whole tokens", () => {
  assert.equal(holdsMarker(`"${C}B second"`, C), false)
  assert.equal(holdsMarker(`"${C} the quick"`, C), true)
  assert.equal(holdsMarker('{"v":"HOURabR012"}', "HOURabR012"), true)
  assert.equal(holdsMarker('{"v":"HOURabR0120"}', "HOURabR012"), false)
})

test("the cell text counts only inside a table node", () => {
  const cell = "HOURCELLf9vyr"
  const table = (text) => JSON.stringify({ body: [{ kind: "para", text: "a" }, { kind: "table", rows: [{ cells: [{ body: [{ kind: "para", text }] }, { body: [] }] }] }] })
  assert.equal(cellInTable(table(cell), cell), true)
  // RC-4: the table was saved empty.
  assert.equal(cellInTable(table(""), cell), false)
  // Typed after the table instead of into it.
  assert.equal(cellInTable(JSON.stringify({ body: [{ kind: "table", rows: [] }, { kind: "para", text: cell }] }), cell), false)
  assert.equal(cellInTable("not json", cell), false)
})

// RC-4 rerun (#4877): the retry click landed in the previous cycle's B line and the cell
// text split "HOURtq7f5C009B"; the row reported that as a lost save.
const T = "HOURtq7f5"
const savedT = [`${T}C009`, `${T}C009B`, `${T}C010`, `${T}C010B`]
const beforeT = word([`${T}C009 the quick brown fox`, `${T}C009B second line of the cycle`, `${T}C010 the quick`, `${T}C010B second line`])
const CELL = "HOURCELLtq7f5"

test("a saved marker split by the step's cell text is harness input, not a lost save", () => {
  const after = word([`${T}C009 the quick brown fox`, `${T}C009${CELL}B second line of the cycle`, `${T}C010 the quick`, `${T}C010B second line`])
  const a = auditStep({ before: beforeT, after, saved: savedT, inserted: CELL })
  assert.deepEqual(a.missingBefore, [])
  assert.deepEqual(a.unexplained, [])
  assert.deepEqual(a.splitByStep.map((d) => d.marker), [`${T}C009B`])
  assert.match(a.splitByStep[0].context, /HOURtq7f5C009HOURCELLtq7f5B second/)
  assert.match(describeAudit(a, "the retry", "v14 -> v15"), /^harness-input: the retry split 1 saved marker\(s\) \(v14 -> v15\), not a lost save: HOURtq7f5C009B in /)
})

test("the separators around the cell text are found inside a split marker too", () => {
  const after = word([`${T}C009 x`, `${T}C00 ${CELL} 9B second`, `${T}C010 x`, `${T}C010B y`])
  const a = auditStep({ before: beforeT, after, saved: savedT, inserted: CELL })
  assert.deepEqual(a.splitByStep.map((d) => d.marker), [`${T}C009B`])
  assert.equal(splitBy(after, `${T}C010B`, CELL), "")
})

test("a marker gone without the cell text in it stays unexplained (possibly lost), never harness input", () => {
  const after = word([`${T}C009 the quick brown fox`, `${T}C010 the quick`, `${T}C010B second line`, ` ${CELL} `])
  const a = auditStep({ before: beforeT, after, saved: savedT, inserted: CELL })
  assert.deepEqual(a.splitByStep, [])
  assert.deepEqual(a.unexplained, [{ marker: `${T}C009B`, context: "" }])
  assert.match(describeAudit(a, "typing right after the insert", "v14 -> v15"), /^1 saved marker\(s\) held before typing right after the insert are missing after it/)
})

test("markers already missing before the step are reported apart and not blamed on it", () => {
  const before = word([`${T}C009 x`, `${T}C010 x`, `${T}C010B y`])
  const a = auditStep({ before, after: before, saved: savedT, inserted: CELL })
  assert.deepEqual(a.missingBefore, [`${T}C009B`])
  assert.deepEqual(a.splitByStep, [])
  assert.deepEqual(a.unexplained, [])
  assert.equal(describeAudit(a, "x", "v1 -> v1"), "")
})

test("an intact head after the step reports nothing; the cell text with separators beside a marker does not split it", () => {
  const after = word([`${T}C009 x`, `${T}C009B second ${CELL} `, `${T}C010 x`, `${T}C010B y`])
  const a = auditStep({ before: beforeT, after, saved: savedT, inserted: CELL })
  assert.deepEqual(a, { missingBefore: [], splitByStep: [], unexplained: [] })
})

test("the table step runs at most once more after a failure, and never again after it damaged saved text", () => {
  assert.equal(tableRunsLeft(1, false), 1)
  assert.equal(tableRunsLeft(2, false), 0)
  assert.equal(tableRunsLeft(1, true), 0)
})

test("the marker report counts whole tokens, caps the missing list and places the cell text", () => {
  const saved = Array.from({ length: 14 }, (_, i) => `${T}C0${String(i).padStart(2, "0")}`)
  const text = JSON.stringify({ body: [{ kind: "para", text: `${saved[0]} x ${saved[1]}B` }, { kind: "table", rows: [{ cells: [{ blocks: [{ kind: "para", text: ` ${CELL} ` }] }] }] }] })
  const r = markerReport(text, saved, CELL)
  assert.equal(r.saved, 14)
  assert.equal(r.held, 1, "C001B does not hold C001")
  assert.equal(r.missing.length, 10)
  assert.equal(r.missing[0], saved[1])
  assert.equal(r.cell, "in-table")
  assert.equal(markerReport(word([` ${CELL} `]), [], CELL).cell, "elsewhere")
  assert.equal(markerReport(word(["x"]), [], CELL).cell, "absent")
  assert.equal(markerReport(word(["x"]), []).cell, undefined)
})

test("the chain keeps the requests themselves, so a reply after the cycle end still shows", () => {
  const ops = [{ n: 1, status: 200 }, { n: 2 }, { n: 3 }]
  const sent = sentSince(ops, 1)
  assert.deepEqual(pendingReplies(sent), [2, 3], "both pending at the cycle end")
  ops[1].status = 200
  ops[1].version = "15"
  ops[2].failed = "net::ERR_CONNECTION_RESET"
  assert.equal(sent[0].version, "15", "the late reply is in the chain")
  assert.deepEqual(pendingReplies(sent), [])
  assert.deepEqual(JSON.parse(JSON.stringify(sent)), [{ n: 2, status: 200, version: "15" }, { n: 3, failed: "net::ERR_CONNECTION_RESET" }])
})

// RC f3035b06c, Word lost reply: an autosave and a Ctrl+S ticket stored the same bytes as
// v5 and v6. The test wanted the label to be v5, the next version after the reconciled v4.
test("the editor is compared with the last committed save, not the next version", () => {
  const ops = [
    { kind: "save", status: 200, version: "2" },
    { kind: "save", status: 200, version: "3" },
    { kind: "save" },
    { kind: "reconcile", status: 200, version: "4" },
    { kind: "save", status: 200, version: "5" },
    { kind: "save", status: 200, version: "6" },
  ]
  assert.equal(lastCommittedSave(ops), "6")
  assert.equal(lastCommittedSave([{ kind: "save" }, { kind: "reconcile", status: 200, version: "4" }]), undefined)
})

test("Check the result is due only for that status and only past the deadline", () => {
  const s = "Result not confirmedHistory 26300 / 17179869184 B"
  assert.equal(isUnconfirmedStatus(s), true)
  assert.equal(verifyDue(s, 1_000, 15_999), false)
  assert.equal(verifyDue(s, 1_000, 16_000), true)
  assert.equal(verifyDue("Saved · version 3", 1_000, 99_000), false)
})

test("a press does not make a cycle confirmed: the server's head decides", () => {
  const c = confirmCycle({ typed, baseline: "3", label: "4", head: { version: "4", text: word(["other"]) } })
  assert.equal(c.confirmed, false)
})

test("gaps in the page's polling are reported as stalls", () => {
  const poll = (s) => ({ at: `2026-11-06T09:47:${s}.000Z`, who: "person", path: "craft/proposals" })
  const trail = [poll("00"), { at: "2026-11-06T09:47:01.000Z", who: "lease-poll", path: "x" }, poll("05"), poll("45")]
  assert.deepEqual(pageStalls(trail), [{ from: "2026-11-06T09:47:05.000Z", to: "2026-11-06T09:47:45.000Z", gapMs: 40_000 }])
})

test("hourInterruptions does not count an overlap twice and names the longest interruption", () => {
  const at = (s) => new Date(Date.UTC(2026, 10, 6, 10, 0, s)).toISOString()
  const stalls = [{ from: at(0), to: at(30), gapMs: 30_000 }, { from: at(40), to: at(55), gapMs: 15_000 }]
  const presses = [{ cycle: 1, firstSeenAt: at(10), pressedAt: at(35), unconfirmedForMs: 25_000, statusAfter: "", reconcile: null }]
  const r = hourInterruptions(presses, stalls)
  assert.equal(r.presses, 1)
  assert.equal(r.interruptedMs, 50_000)
  assert.equal(r.longestStallMs, 35_000)
  assert.equal(r.pollGaps, 2)
  assert.match(r.note, /pressed 1 times, interrupted 50 s in total \(page-poll gaps and unconfirmed spells\), longest interruption 35 s; 2 page-poll gap\(s\) of at least 10 s/)
  assert.match(r.note, /not a measured renderer or input stall\)\. Saving did not recover by itself/)
  // The note does not claim that the page froze.
  assert.doesNotMatch(r.note, /longest stall|page stall/)
})

test("hourInterruptions reports a clean hour as zero, without the warning", () => {
  const r = hourInterruptions([], [])
  assert.deepEqual([r.presses, r.interruptedMs, r.longestStallMs], [0, 0, 0])
  assert.equal(r.note, `Check the result pressed 0 times, interrupted 0 s in total (page-poll gaps and unconfirmed spells), longest interruption 0 s; 0 page-poll gap(s) of at least 10 s (${PAGE_STALLS_DESCRIPTION}).`)
})

test("the page-stall description says what was measured and what was not", () => {
  assert.match(PAGE_STALLS_DESCRIPTION, /craft\/proposals/)
  assert.match(PAGE_STALLS_DESCRIPTION, /not a measured renderer or input stall/)
})

// Save accounting: how each save of an hour row happened. The timelines below are what
// instrument() and the session record: ops in the order they were sent, presses with the
// trail length at dispatch.
const at = (s) => new Date(Date.UTC(2026, 10, 7, 10, 0, 0, s * 1000)).toISOString()
const save = (n, s, version, status = 200) => ({ n, kind: "save", sentAt: at(s), status, version })
const rec = (n, s) => ({ n, kind: "reconcile", sentAt: at(s), status: 200 })
const autoWait = (cycle, s, version) => ({ cycle, at: at(s), endedAt: at(s + 1), pressed: false, version, completed: true })
const accountingOf = (timeline) => saveAccounting({ waits: [], forced: [], ops: [], verifications: [], ...timeline })

/** Runs trackedSaveWait with a fake wait and a fake clock: `script(onPress, ops, clock)` plays the timeline. */
async function playWait(ops, cycle, script) {
  const recd = { saveWaits: [], forcedCtrlS: [] }
  const clock = { s: 0 }
  const now = () => new Date(at(clock.s))
  const result = await trackedSaveWait(recd, ops, { cycle, reason: "no autosave within 4 s", now }, (onPress) => script(onPress, ops, clock)).then((v) => ({ v }), (e) => ({ e }))
  return { recd, result }
}

test("an autosave acknowledged after the 4 s cutoff stays an autosave; Ctrl+S only confirmed it", async () => {
  // Save 1 is sent at 0.3 s; its reply (v5) lands at 4.5 s, after the harness pressed at 4 s.
  // The press sends no save: the editor already holds those bytes (#5075).
  const ops = []
  const { recd, result } = await playWait(ops, 0, async (onPress) => {
    ops.push(save(1, 0.3, undefined, undefined))
    onPress()
    ops[0].status = 200
    ops[0].version = "5"
    return { version: "5" }
  })
  assert.equal(result.v, "5")
  const a = accountingOf({ waits: recd.saveWaits, forced: recd.forcedCtrlS, ops })
  assert.deepEqual([a.committedWrites, a.autosaves, a.unknownOrigin, a.forcedCtrlS], [1, 1, 0, 1])
  assert.equal(a.forcedCtrlSEvents[0].outcome, "confirm-only")
  assert.equal(a.forcedCtrlSEvents[0].newVersion, true)
  assert.match(a.note, /^forced Ctrl\+S 1×, Check the result 0×; server-confirmed writes 1× = autosave 1× \+ origin unknown 0×, press outcomes 1 confirm-only/)
})

test("an autosave that was preparing at the press and sends 100 ms after it is unknown, not forced", async () => {
  // The capture started before the press (snapshot, draft write, validation) and its
  // request leaves at 4.1 s, 100 ms after Ctrl+S at 4 s: the trail cannot tell it from the forced save.
  const ops = []
  const { recd } = await playWait(ops, 3, async (onPress, ops, clock) => {
    clock.s = 4
    onPress()
    ops.push(save(1, 4.1, "6"))
    clock.s = 5
    return { version: "6" }
  })
  const a = accountingOf({ waits: recd.saveWaits, forced: recd.forcedCtrlS, ops })
  assert.deepEqual([a.committedWrites, a.autosaves, a.unknownOrigin, a.unknownOriginOps, a.forcedCtrlSOutcomes.unattributed], [1, 0, 1, [1], 1])
  assert.equal(recd.forcedCtrlS[0].outcome, "unattributed")
  assert.match(a.note, /forced Ctrl\+S 1×, .*origin unknown 1× \(sent after a harness Ctrl\+S.*cannot tell a forced save from an autosave, #5112\), press outcomes 1 unattributed, automatic reconcile/)
})

test("a repeat save after the press that answers the autosave's version does not take its origin", () => {
  // #5075: identical bytes answer the existing version; the first save carrying it committed it.
  const ops = [save(1, 0.3, "7"), save(2, 4.2, "7")]
  assert.equal(producerOf(ops, "7").n, 1)
  assert.equal(pressOutcome({ opsAtPress: 1, version: "7", completed: true }, ops), "confirm-only")
  assert.equal(pressOutcome({ opsAtPress: 1, version: null, completed: true }, ops), "no-version")
  assert.equal(pressOutcome({ opsAtPress: 1, version: "8", completed: true }, ops), "unattributed")
  assert.equal(pressOutcome({ opsAtPress: 1, version: "7", completed: false }, ops), "incomplete")
})

test("markTrail records the trail length right before a harness action", () => {
  const marks = []
  markTrail(marks, [1, 2, 3], at(9))
  assert.deepEqual(marks, [{ at: at(9), opsAt: 3 }])
})

test("a wait interrupted after the press leaves an explicit incomplete event in the evidence", async () => {
  const ops = []
  let seenAtDispatch = -1
  const recd = { saveWaits: [], forcedCtrlS: [] }
  const p = trackedSaveWait(recd, ops, { cycle: 4, reason: "no autosave within 4 s" }, async (onPress) => {
    onPress()
    seenAtDispatch = recd.forcedCtrlS.length
    throw new Error("Target page, context or browser has been closed")
  })
  await assert.rejects(p, /has been closed/)
  // The event existed when the key went out, and survives the throw.
  assert.equal(seenAtDispatch, 1)
  const [e] = recd.forcedCtrlS
  assert.equal(e.outcome, "incomplete")
  assert.notEqual(e.endedAt, null)
  assert.deepEqual([recd.saveWaits[0].completed, recd.saveWaits[0].pressed], [false, true])
  const a = accountingOf({ waits: recd.saveWaits, forced: recd.forcedCtrlS, ops })
  assert.deepEqual([a.forcedCtrlS, a.forcedCtrlSOutcomes.incomplete, a.committedWrites], [1, 1, 0])
})

test("startPress and finishPress keep a table-cell press that saw no newer head as no-version", () => {
  const forced = []
  const e = startPress(forced, { cycle: 2, site: "table-cell", reason: "table cell text not in a table cell", opsAtPress: 0, at: at(10) })
  assert.equal(forced[0].outcome, "incomplete")
  finishPress(e, [], { version: null, completed: true }, at(21))
  assert.deepEqual([e.outcome, e.newVersion, e.endedAt], ["no-version", false, at(21)])
})

test("an automatic reconcile retry during a press wait is reported as cause unknown, not as press-caused", () => {
  // The editor's own reconcile timer fires at 20 s and 40 s while the harness waits after
  // a Ctrl+S pressed at 4 s (the wait ends at 64 s); the network trail cannot tell them apart.
  const forced = [{ cycle: 1, at: at(4), site: "save-wait", reason: "x", opsAtPress: 0, endedAt: at(64), newVersion: false, version: null, outcome: "no-version" }]
  const ops = [rec(1, 20), rec(2, 40), rec(3, 200)]
  const a = accountingOf({ forced, ops })
  assert.deepEqual(a.reconcilesDuringPressWaitOps, [1, 2])
  assert.deepEqual(a.editorReconcileOps, [3])
  assert.match(a.note, /reconcile during a press wait \(cause unknown\) 2×, automatic reconcile 1×\.$/)
})

test("a press-triggered reconcile sent just after its wait ended is not counted as automatic", () => {
  // Check the result at 30 s, the session stopped waiting at 45 s, the request left at 46 s.
  const verifications = [{ cycle: 3, firstSeenAt: at(10), pressedAt: at(30), unconfirmedForMs: 20_000, statusAfter: "", reconcile: null, waitEndedAt: at(45) }]
  const a = accountingOf({ verifications, ops: [rec(1, 46), rec(2, 90)] })
  assert.deepEqual([a.reconcilesDuringPressWaitOps, a.editorReconcileOps, a.verifyPresses], [[1], [2], 1])
  // A press recorded without its wait end still gets a window (15 s).
  const legacy = accountingOf({ verifications: [{ ...verifications[0], waitEndedAt: undefined }], ops: [rec(1, 44), rec(2, 60)] })
  assert.deepEqual([legacy.reconcilesDuringPressWaitOps, legacy.editorReconcileOps], [[1], [2]])
})

test("saveAccounting of a clean hour: every save automatic, nothing forced", () => {
  const ops = [save(1, 1, "2"), save(2, 31, "3")]
  const a = accountingOf({ waits: [autoWait(0, 1, "2"), autoWait(1, 31, "3")], ops })
  assert.deepEqual([a.committedWrites, a.autosaves, a.unknownOrigin, a.forcedCtrlS, a.releases, a.editorReconciles, a.reconcilesDuringPressWait, a.verifyPresses], [2, 2, 0, 0, 0, 0, 0, 0])
  assert.equal(a.note, "forced Ctrl+S 0×, Check the result 0×; server-confirmed writes 2× = autosave 2× + origin unknown 0×, automatic reconcile 0×.")
  assert.match(a.description, /not a gate/)
  assert.match(a.description, /cannot tell a forced save from an autosave/)
})

test("hourInterruptions puts the save accounting first in the row's note", () => {
  const ops = [save(1, 1, "2"), save(2, 40, "3")]
  const forced = [{ cycle: 1, at: at(4), site: "save-wait", reason: "x", opsAtPress: 1, endedAt: at(41), newVersion: true, version: "3", outcome: "unattributed" }]
  const waits = [autoWait(0, 1, "2"), { cycle: 1, at: at(0), endedAt: at(41), pressed: true, opsAtPress: 1, pressedAt: at(4), version: "3", completed: true }]
  const r = hourInterruptions([], [], accountingOf({ waits, forced, ops, releases: [{ at: at(50), opsAt: 2 }] }))
  assert.match(r.note, /^forced Ctrl\+S 1×, Check the result 0×, release 1×; server-confirmed writes 2× = autosave 1× \+ origin unknown 1× \(.*\), press outcomes 1 unattributed, automatic reconcile 0×\. Check the result pressed 0 times/)
})

// #5112: the accounting counts server-confirmed writes, once each, not save waits. The
// origin of a write is its save request; a request sent after a harness Ctrl+S (or a
// release flush) carries no trigger, so it is "unknown" until that editor instance ended,
// however long after the press it was sent (an autosave can queue behind a slow capture).
const sumOf = (a) => a.autosaves + a.unknownOrigin
/** The snapshot identity a save or reconcile request carries (what the server's receipt binds). */
const snap = (base, hash, attempt = `att-${hash}`) => ({ baseVersion: base, contentHash: `sha256:${hash}`, editorRevision: `rev-${hash}`, sessionId: "s1", attemptId: attempt })

test("#5112: two autosaves committed before one wait are two writes, not one", () => {
  // v5 and v6 both commit before the wait sees "verze 6"; nothing is pressed.
  const ops = [save(1, 0.3, "5"), save(2, 0.9, "6")]
  ops.forEach((o, i) => { o.baseVersion = String(4 + i) })
  const a = accountingOf({ waits: [autoWait(0, 0, "6")], ops })
  assert.deepEqual([a.committedWrites, a.autosaves, a.unknownOrigin, sumOf(a)], [2, 2, 0, 2])
  assert.deepEqual(a.writes.map((w) => [w.version, w.op, w.origin]), [["5", 1, "autosave"], ["6", 2, "autosave"]])
})

test("#5112: an autosave queued behind a slow capture and sent 36 s after Ctrl+S is unknown, never forced", () => {
  // Ctrl+S at 4 s; the queued autosave leaves at 40 s, past the old 30 s window.
  // The stored outcome is derived again from the final trail.
  const forced = [{ cycle: 3, at: at(4), site: "save-wait", reason: "x", opsAtPress: 0, endedAt: at(41), newVersion: true, version: "6", outcome: "confirm-only" }]
  const waits = [{ cycle: 3, at: at(0), endedAt: at(41), pressed: true, opsAtPress: 0, pressedAt: at(4), version: "6", completed: true }]
  const ops = [{ ...save(1, 40, "6"), baseVersion: "5" }]
  const a = accountingOf({ waits, forced, ops })
  assert.deepEqual([a.committedWrites, a.autosaves, a.unknownOrigin, a.forcedCtrlS], [1, 0, 1, 1])
  assert.equal(a.forcedCtrlSEvents[0].outcome, "unattributed")
  assert.equal(a.forcedCtrlSOutcomes["new-save"], undefined)
  assert.match(a.writes[0].reason, /after a harness Ctrl\+S/)
  assert.match(a.note, /cannot tell a forced save from an autosave/)
})

test("#5112: a write no wait observed is still counted once, by its own request", () => {
  // Cycle 1 pressed at 4 s; v3 (sent at 2 s) and v4 (sent at 6 s) commit; the wait saw v4 only.
  const forced = [{ cycle: 1, at: at(4), site: "save-wait", reason: "x", opsAtPress: 1, endedAt: at(7), newVersion: true, version: "4", outcome: "unattributed" }]
  const waits = [{ cycle: 1, at: at(0), endedAt: at(7), pressed: true, opsAtPress: 1, pressedAt: at(4), version: "4", completed: true }]
  const ops = [{ ...save(1, 2, "3"), baseVersion: "2" }, { ...save(2, 6, "4"), baseVersion: "3" }]
  const a = accountingOf({ waits, forced, ops })
  assert.deepEqual([a.committedWrites, a.autosaves, a.unknownOrigin, a.unknownOriginOps], [2, 1, 1, [2]])
})

test("#5112: repeats, unchanged answers and a reconcile of a lost reply add no second write", () => {
  const ops = [
    { ...save(1, 1, "5"), baseVersion: "4" },
    { ...save(2, 2, "5"), baseVersion: "4" }, // attempt repeat: same receipt
    { ...save(3, 3, "5"), baseVersion: "5", unchanged: true }, // #5075: no version created
    { ...save(4, 4, undefined, undefined), ...snap("5", "aa"), failed: "net::ERR_FAILED" }, // reply lost
    { ...rec(5, 20), ...snap("5", "aa"), version: "6" }, // proves v6
    { ...rec(6, 21), ...snap("5", "aa"), version: "6" },
  ]
  const a = accountingOf({ ops })
  assert.deepEqual(a.writes.map((w) => [w.version, w.op, w.confirmedBy, w.origin]), [["5", 1, 1, "autosave"], ["6", 4, 5, "autosave"]])
  assert.deepEqual([a.committedWrites, sumOf(a)], [2, 2])
  // A reconcile whose save request is not in the trail: confirmed, origin unknown.
  const orphan = accountingOf({ ops: [{ ...rec(1, 5), ...snap("8", "bb"), version: "9" }] })
  assert.deepEqual([orphan.committedWrites, orphan.unknownOrigin], [1, 1])
  assert.match(orphan.writes[0].reason, /no save request/)
})

test("#5112: closing the page ends an earlier press; a release flush is a harness intervention", () => {
  const forced = [{ cycle: 2, at: at(4), site: "table-cell", reason: "x", opsAtPress: 0, endedAt: at(10), newVersion: false, version: null, outcome: "no-version" }]
  const ops = [{ ...save(1, 50, "3"), baseVersion: "2" }, { ...save(2, 200, "4"), baseVersion: "3" }, { ...save(3, 300, "5"), baseVersion: "4" }]
  // The page closed at 100 s (trail length 1); a release at 250 s (trail length 2) flushed v5.
  const a = accountingOf({ forced, ops, editorEnds: [{ at: at(100), opsAt: 1 }], releases: [{ at: at(250), opsAt: 2 }] })
  assert.deepEqual(a.writes.map((w) => w.origin), ["unknown", "autosave", "unknown"])
  assert.match(a.writes[2].reason, /release/)
  assert.deepEqual([a.committedWrites, a.autosaves, a.unknownOrigin], [3, 1, 2])
})

test("#5112: every server-confirmed write lands in exactly one bucket", () => {
  // A pseudo-random hour: saves, repeats, unchanged answers, lost replies with reconciles, presses.
  let seed = 7
  const rnd = () => ((seed = (seed * 1103515245 + 12345) % 2 ** 31) / 2 ** 31)
  for (let round = 0; round < 50; round++) {
    const ops = [], forced = []
    let head = 1
    for (let s = 1; s < 120; s += 1 + Math.floor(rnd() * 5)) {
      const r = rnd()
      if (r < 0.1) forced.push({ cycle: 1, at: at(s), site: "save-wait", reason: "x", opsAtPress: ops.length, endedAt: at(s + 3), newVersion: false, version: null, outcome: "no-version" })
      else if (r < 0.2) ops.push({ ...save(ops.length + 1, s, String(head)), baseVersion: String(head), unchanged: true })
      else if (r < 0.3 && ops.length) ops.push({ ...ops[ops.length - 1], n: ops.length + 1, sentAt: at(s) })
      else if (r < 0.4) {
        ops.push({ ...save(ops.length + 1, s, undefined, undefined), ...snap(String(head), String(s)), failed: "lost" })
        ops.push({ ...rec(ops.length + 1, s + 0.5), ...snap(String(head), String(s)), version: String(++head) })
      } else ops.push({ ...save(ops.length + 1, s, String(head + 1)), baseVersion: String(head++) })
    }
    const versions = new Set(ops.filter((o) => o.status === 200 && o.version !== undefined && !o.unchanged && o.version !== o.baseVersion).map((o) => o.version))
    const a = accountingOf({ forced, ops })
    assert.equal(a.committedWrites, versions.size)
    assert.equal(sumOf(a), a.committedWrites)
    assert.equal(new Set(a.writes.map((w) => w.version)).size, a.writes.length)
  }
})

test("#5112 review: a fresh save after an Check the result press is unknown, not an autosave", () => {
  // The reconcile at 21 s proves the attempt absent; the editor then sends a fresh save at 22 s.
  const verifications = [{ cycle: 4, firstSeenAt: at(5), pressedAt: at(20), unconfirmedForMs: 15_000, statusAfter: "", reconcile: null, waitEndedAt: at(30), opsAtPress: 1 }]
  const ops = [{ ...save(1, 3, undefined, undefined), ...snap("5", "aa"), failed: "timeout" }, { ...rec(2, 21), ...snap("5", "aa"), status: 409 }, { ...save(3, 22, "6"), ...snap("5", "ab") }]
  const a = accountingOf({ verifications, ops })
  assert.deepEqual([a.committedWrites, a.autosaves, a.unknownOrigin], [1, 0, 1])
  assert.match(a.writes[0].reason, /Check the result/)
  // A press recorded without its trail cut is placed by time.
  const legacy = accountingOf({ verifications: [{ ...verifications[0], opsAtPress: undefined }], ops })
  assert.equal(legacy.unknownOrigin, 1)
  // Before the press it stays the editor's own.
  assert.equal(accountingOf({ verifications, ops: [{ ...save(1, 3, "6"), ...snap("5", "aa") }] }).autosaves, 1)
})

test("#5112 review: a reconcile is matched to its save only by the whole snapshot identity", () => {
  // An earlier attempt with the same base and bytes but another attempt id is not the reconciled one.
  const forced = [{ cycle: 2, at: at(10), site: "save-wait", reason: "x", opsAtPress: 1, endedAt: at(20), newVersion: true, version: "6", outcome: "unattributed" }]
  const ops = [{ ...save(1, 2, undefined, undefined), ...snap("5", "aa", "att-1"), failed: "lost" }, { ...rec(2, 30), ...snap("5", "aa", "att-2"), version: "6" }]
  const a = accountingOf({ forced, ops })
  assert.deepEqual([a.committedWrites, a.autosaves, a.unknownOrigin], [1, 0, 1])
  assert.equal(a.writes[0].op, undefined)
  assert.equal(a.forcedCtrlSEvents[0].outcome, "unattributed")
  // Without the revision a request proves nothing either.
  const bare = accountingOf({ ops: [{ ...ops[0], editorRevision: undefined }, { ...ops[1], attemptId: "att-1", editorRevision: undefined }] })
  assert.equal(bare.unknownOrigin, 1)
  // The same attempt matches: the write is the pre-press save's.
  assert.equal(accountingOf({ forced, ops: [ops[0], { ...ops[1], attemptId: "att-1" }] }).autosaves, 1)
})

test("#5112 review 2: a later repeat of the same attempt may have committed; the write is unknown", () => {
  // Request 1 (before the press) and its repeat 2 (after it) carry the same attempt; only 1's answer is recorded.
  const forced = [{ cycle: 1, at: at(4), site: "save-wait", reason: "x", opsAtPress: 1, endedAt: at(9), newVersion: true, version: "6", outcome: "unattributed" }]
  const ops = [{ ...save(1, 2, "6"), ...snap("5", "aa") }, { ...save(2, 5, undefined, undefined), ...snap("5", "aa"), failed: "lost" }]
  const a = accountingOf({ forced, ops })
  assert.deepEqual(a.writes.map((w) => [w.ops, w.origin]), [[[1, 2], "unknown"]])
  assert.equal(a.forcedCtrlSEvents[0].outcome, "unattributed")
})

test("#5112 review 2: startVerify records an Check the result press before the click", () => {
  const verifications = []
  const v = startVerify(verifications, { cycle: 3, firstSeenAt: at(5), opsAtPress: 1, at: at(20) })
  assert.deepEqual([verifications.length, v.waitEndedAt, v.opsAtPress, v.unconfirmedForMs], [1, undefined, 1, 15_000])
  // The click threw; the press still makes the later fresh save unknown.
  const a = accountingOf({ verifications, ops: [{ ...save(1, 3, "6"), ...snap("5", "aa") }, { ...save(2, 22, "7"), ...snap("6", "ab") }] })
  assert.deepEqual([a.verifyPresses, a.autosaves, a.unknownOrigin], [1, 1, 1])
})
