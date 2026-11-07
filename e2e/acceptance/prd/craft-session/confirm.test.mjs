// node --test e2e/acceptance/prd/craft-session/confirm.test.mjs
// (also run by e2e/acceptance/prd/report.test.mjs, the PRD runner step in CI)
import assert from "node:assert/strict"
import { test } from "node:test"
import { PAGE_STALLS_DESCRIPTION, hourInterruptions, isUnconfirmedStatus, saveAccounting, lastCommittedSave, pageStalls, verifyDue, auditStep, baselineBefore, cellInTable, confirmCycle, describeAudit, holdsMarker, labelVersion, markerReport, pendingReplies, sentSince, splitBy, tableRunsLeft } from "./confirm.ts"

const word = (paras) => JSON.stringify({ body: paras.map((text) => ({ kind: "para", text, runs: [{ len: text.length }] })) })
const C = "HOURf9vyrC010"
const typed = [C, `${C}B`]

// RC-4 (#4871): the table step saved v13 and v14 before cycle 10 typed; the typing never
// reached the document, yet "Uloženo · verze 14" counted as the cycle's save.
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
  assert.match(c.problem, /no save newer than verze 14 was shown/)
})

test("a cycle that typed nothing is never confirmed", () => {
  assert.equal(confirmCycle({ typed: [], baseline: "1", label: "2", head: { version: "2", text: "" } }).confirmed, false)
})

test("the baseline takes saves of the steps earlier in the cycle", () => {
  assert.equal(labelVersion("Uloženo · verze 14"), "14")
  assert.equal(labelVersion("Neuložený koncept"), undefined)
  assert.equal(baselineBefore("12", "Uloženo · verze 14"), "14")
  assert.equal(baselineBefore("12", "Ukládám…"), "12")
  assert.equal(baselineBefore("15", "Uloženo · verze 14"), "15")
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

test("Ověřit výsledek is due only for that status and only past the deadline", () => {
  const s = "Výsledek nepotvrzenHistorie 26300 / 17179869184 B"
  assert.equal(isUnconfirmedStatus(s), true)
  assert.equal(verifyDue(s, 1_000, 15_999), false)
  assert.equal(verifyDue(s, 1_000, 16_000), true)
  assert.equal(verifyDue("Uloženo · verze 3", 1_000, 99_000), false)
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
  assert.equal(r.note, `Ověřit výsledek pressed 0 times, interrupted 0 s in total (page-poll gaps and unconfirmed spells), longest interruption 0 s; 0 page-poll gap(s) of at least 10 s (${PAGE_STALLS_DESCRIPTION}).`)
})

test("the page-stall description says what was measured and what was not", () => {
  assert.match(PAGE_STALLS_DESCRIPTION, /craft\/proposals/)
  assert.match(PAGE_STALLS_DESCRIPTION, /not a measured renderer or input stall/)
})

// Save accounting: how each save of an hour row happened.
const at = (s) => new Date(Date.UTC(2026, 10, 7, 10, 0, s)).toISOString()
const wait = (cycle, pressed, version) => ({ cycle, at: at(cycle === "warmup" ? 0 : cycle), pressed, version })
const forced = (cycle, site, newVersion, reconcileOps = []) => ({ cycle, at: at(cycle), site, reason: site === "save-wait" ? "no autosave within 4 s" : "table cell text not in a table cell of the server head v3 after 4 s, status Neuložený koncept", newVersion, version: newVersion ? "9" : null, reconcileOps })

test("saveAccounting counts autosaves, forced Ctrl+S, automatic reconciles and Ověřit výsledek apart", () => {
  const waits = [wait("warmup", false, "2"), wait(0, false, "3"), wait(1, true, "4"), wait(2, false, "5"), wait(3, true, null), wait(3, false, "6")]
  const presses = [forced(1, "save-wait", true), forced(2, "table-cell", true, [7]), forced(3, "save-wait", false)]
  const ops = [
    { n: 1, kind: "save" }, { n: 2, kind: "reconcile" }, { n: 3, kind: "save" }, { n: 4, kind: "reconcile" },
    { n: 5, kind: "save" }, { n: 6, kind: "reconcile" }, { n: 7, kind: "reconcile" },
  ]
  const verifications = [{ cycle: 3, firstSeenAt: at(30), pressedAt: at(45), unconfirmedForMs: 15_000, statusAfter: "", reconcile: { status: 200 }, reconcileOps: [4] }]
  const a = saveAccounting({ waits, forced: presses, ops, verifications })
  // The warm-up and cycles 0, 2 and the second wait of 3 saved by themselves; a wait that pressed is not an autosave.
  assert.equal(a.autosaves, 4)
  assert.equal(a.forcedCtrlS, 3)
  assert.deepEqual(a.forcedCtrlSEvents.map((e) => [e.cycle, e.site, e.newVersion]), [[1, "save-wait", true], [2, "table-cell", true], [3, "save-wait", false]])
  // 4 followed the Ověřit výsledek press, 7 the table step's Ctrl+S: only 2 and 6 were the editor's own.
  assert.deepEqual(a.editorReconcileOps, [2, 6])
  assert.equal(a.editorReconciles, 2)
  assert.equal(a.verifyPresses, 1)
  assert.equal(a.note, "autosave 4×, forced Ctrl+S 3× (1 without a new version), automatic reconcile 2×, Ověřit výsledek 1×.")
  assert.match(a.description, /not a gate/)
})

test("saveAccounting of a clean hour: every save automatic, nothing forced", () => {
  const a = saveAccounting({ waits: [wait(0, false, "2"), wait(1, false, "3")], forced: [], ops: [{ n: 1, kind: "save" }, { n: 2, kind: "save" }], verifications: [] })
  assert.deepEqual([a.autosaves, a.forcedCtrlS, a.editorReconciles, a.verifyPresses], [2, 0, 0, 0])
  assert.equal(a.note, "autosave 2×, forced Ctrl+S 0×, automatic reconcile 0×, Ověřit výsledek 0×.")
})

test("a press recorded before the reconcile numbers existed (no reconcileOps) still counts", () => {
  const a = saveAccounting({ waits: [], forced: [], ops: [{ n: 1, kind: "reconcile" }], verifications: [{ cycle: 0, firstSeenAt: at(0), pressedAt: at(15), unconfirmedForMs: 15_000, statusAfter: "", reconcile: null }] })
  assert.deepEqual([a.verifyPresses, a.editorReconciles], [1, 1])
})

test("hourInterruptions puts the save accounting first in the row's note", () => {
  const a = saveAccounting({ waits: [wait(0, false, "2"), wait(1, true, "3")], forced: [forced(1, "save-wait", true)], ops: [], verifications: [] })
  const r = hourInterruptions([], [], a)
  assert.match(r.note, /^autosave 1×, forced Ctrl\+S 1×, automatic reconcile 0×, Ověřit výsledek 0×\. Ověřit výsledek pressed 0 times/)
})
