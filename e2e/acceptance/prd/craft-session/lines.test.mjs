// node --test e2e/acceptance/prd/craft-session/lines.test.mjs
// (also run by e2e/acceptance/prd/report.test.mjs, the PRD runner step in CI)
import assert from "node:assert/strict"
import { test } from "node:test"
import { checkDraftCopies, checkLines, checkNoLines, editorTakesInput, evaluateLines, foundLines, holdsAnyLine, lineLedger } from "./lines.ts"

const P = "PRDLINE"
const word = (paras) => JSON.stringify({ body: paras.map((text) => ({ kind: "para", text, runs: [{ len: text.length }] })) })
const sheet = (column) =>
  JSON.stringify({
    sheets: [
      {
        name: "Sheet1",
        // Rows deliberately inserted out of order: the check reads the grid, not the JSON order.
        cells: { bands: { "0": { rows: Object.fromEntries(column.map((v, r) => [String(r + 4), { "0": { value: { t: "Text", v }, formula: null, style: 0 } }]).reverse()) } }, len: column.length },
      },
    ],
  })
const lines = (n, from = 0) => Array.from({ length: n }, (_, i) => `${P}-${from + i}`)

test("every accepted line present once and in order passes", () => {
  const ledger = lineLedger(P, { typed: 5, accepted: [0, 1, 2, 3, 4] })
  const c = checkLines(ledger, word(["M1saved", ...lines(5), ""]), "server head")
  assert.deepEqual(c.problems, [])
  assert.equal(c.kind, "word")
  assert.deepEqual(c.found, [0, 1, 2, 3, 4])
  assert.deepEqual(ledger.lines[3].seen, [{ stage: "server head", count: 1 }])
})

test("a dropped accepted line fails and names the line and its last state", () => {
  const ledger = lineLedger(P, { typed: 5, accepted: [0, 1, 2, 3, 4] })
  checkLines(ledger, word(lines(5)), "draft in the open editor")
  const kept = lines(5).filter((m) => m !== `${P}-2`)
  const c = checkLines(ledger, word(kept), "server head v7 after the reload")
  assert.equal(c.problems.length, 1)
  assert.match(c.problems[0], /lost PRDLINE-2 in "server head v7 after the reload"/)
  assert.match(c.problems[0], /accepted by the editor/)
  assert.match(c.problems[0], /last seen in "draft in the open editor" \(x1\)/)
})

test("a dropped Sheet cell fails; the grid is read row by row", () => {
  const ledger = lineLedger(P, { typed: 4, accepted: [0, 1, 2, 3] })
  assert.deepEqual(checkLines(ledger, sheet(lines(4)), "sheet ok").problems, [])
  const c = evaluateLines(ledger, sheet([`${P}-0`, `${P}-1`, `${P}-3`]), "sheet lost")
  assert.equal(c.kind, "sheet")
  assert.equal(c.problems.length, 1)
  assert.match(c.problems[0], /lost PRDLINE-2/)
})

test("the last line lost while the editor turned read-only fails once a copy held it", () => {
  // The r3 blind spot: line 3 typed as the status turned "odpojen", so status
  // alone does not prove it; the draft in the open editor held it.
  const ledger = lineLedger(P, { typed: 4, accepted: [0, 1, 2], stoppedBy: "Editor disconnected · draft kept" })
  assert.deepEqual(checkLines(ledger, word(lines(4)), "draft in the open editor").problems, [])
  const c = checkLines(ledger, word(lines(3)), "draft after the reload")
  assert.equal(c.problems.length, 1)
  assert.match(c.problems[0], /lost PRDLINE-3 .*an earlier copy held it/)
})

test("an unproven line is required when a later line is in the copy", () => {
  const ledger = lineLedger(P, { typed: 6, accepted: [0, 1, 3, 4, 5] })
  const c = evaluateLines(ledger, word([`${P}-0`, `${P}-1`, `${P}-3`, `${P}-4`, `${P}-5`]), "draft")
  assert.equal(c.problems.length, 1)
  assert.match(c.problems[0], /lost PRDLINE-2 .*the editor kept the later line PRDLINE-5/)
})

test("an unproven last line that no copy ever held is not required", () => {
  const ledger = lineLedger(P, { typed: 4, accepted: [0, 1, 2], stoppedBy: "Read-only" })
  assert.deepEqual(checkLines(ledger, word(lines(3)), "draft").problems, [])
})

test("a doubled line fails", () => {
  const ledger = lineLedger(P, { typed: 3, accepted: [0, 1, 2] })
  const c = evaluateLines(ledger, word([`${P}-0`, `${P}-1`, `${P}-1`, `${P}-2`]), "server head")
  assert.ok(c.problems.some((p) => /holds PRDLINE-1 2 times/.test(p)), c.problems.join("\n"))
})

test("lines out of order fail", () => {
  const ledger = lineLedger(P, { typed: 3, accepted: [0, 1, 2] })
  const c = evaluateLines(ledger, word([`${P}-0`, `${P}-2`, `${P}-1`]), "server head")
  assert.ok(c.problems.some((p) => /out of order: -0 -2 -1/.test(p)), c.problems.join("\n"))
})

test("a line that was never typed fails; the line in flight may be there once", () => {
  const ledger = lineLedger(P, { typed: 2, accepted: [0, 1] })
  assert.deepEqual(evaluateLines(ledger, word(lines(3)), "draft").problems, [])
  const c = evaluateLines(ledger, word(lines(4)), "draft")
  assert.ok(c.problems.some((p) => /PRDLINE-3, which was never typed/.test(p)), c.problems.join("\n"))
})

test("X-1 is not read from X-10, and a cut last line is not a second X-1", () => {
  assert.deepEqual(foundLines(P, word([`${P}-10`])).found, [10])
  const ledger = lineLedger(P, { typed: 13, accepted: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11], stoppedBy: "Read-only" })
  // Line 12 was cut after "PRDLINE-1" when the editor turned read-only.
  assert.deepEqual(evaluateLines(ledger, word([...lines(12), `${P}-1`]), "draft").problems, [])
  // Without a stop, the same text is a doubled line.
  const running = lineLedger(P, { typed: 13, accepted: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11] })
  assert.notDeepEqual(evaluateLines(running, word([...lines(12), `${P}-1`]), "draft").problems, [])
})

test("one complete draft among several passes; none complete reports every draft", () => {
  const ledger = lineLedger(P, { typed: 3, accepted: [0, 1, 2] })
  assert.deepEqual(checkDraftCopies(ledger, [word(lines(1)), word(lines(3))], "drafts").problems, [])
  const bad = checkDraftCopies(lineLedger(P, { typed: 3, accepted: [0, 1, 2] }), [word(lines(1)), word(lines(2))], "drafts")
  assert.equal(bad.problems.length, 3)
  assert.deepEqual(checkDraftCopies(ledger, [], "drafts").problems, ['"drafts": no draft was offered'])
})

test("a copy that must hold nothing fails on any typed line", () => {
  const ledger = lineLedger(P, { typed: 3, accepted: [0, 1, 2] })
  assert.deepEqual(checkNoLines(ledger, word(["M1saved"]), "server head").problems, [])
  assert.match(checkNoLines(ledger, word(["M1saved", `${P}-2`]), "server head").problems[0], /holds 1 typed line\(s\).*PRDLINE-2/)
  assert.equal(holdsAnyLine(ledger, word([`${P}-2`])), true)
})

test("plain text (a non-native copy) is still checked", () => {
  const ledger = lineLedger(P, { typed: 3, accepted: [0, 1, 2] })
  const c = evaluateLines(ledger, `${P}-0\n${P}-2\n`, "text")
  assert.equal(c.kind, "text")
  assert.match(c.problems[0], /lost PRDLINE-1/)
})

test("a line held by any offered draft is required in later copies, not only the first draft's lines", () => {
  // Review r1 #1: both drafts pass, only the second proves the unproven line 3.
  const ledger = lineLedger(P, { typed: 4, accepted: [0, 1, 2], stoppedBy: "Editor disconnected · draft kept" })
  assert.deepEqual(checkDraftCopies(ledger, [word(lines(3)), word(lines(4))], "drafts in the open editor").problems, [])
  const c = checkDraftCopies(ledger, [word(lines(3))], "draft offered after the reload")
  assert.equal(c.problems.length, 1, c.problems.join("\n"))
  assert.match(c.problems[0], /lost PRDLINE-3 .*an earlier copy held it/)
})

test("the line in flight, once seen, is required in later copies", () => {
  // Review r1 #2: the marker went out but typing threw before Enter, so typed stayed 2.
  const ledger = lineLedger(P, { typed: 2, accepted: [0, 1] })
  assert.deepEqual(checkLines(ledger, word(lines(3)), "draft in the open editor").problems, [])
  const c = checkLines(ledger, word(lines(2)), "draft offered after the reload")
  assert.equal(c.problems.length, 1, c.problems.join("\n"))
  assert.match(c.problems[0], /lost PRDLINE-2 .*an earlier copy held it/)
})

test("a cut-line reading never excuses a real duplicate", () => {
  // Review r2: line 12 is complete, so a trailing X-1 is a second X-1, not a cut X-12.
  const ledger = lineLedger(P, { typed: 13, accepted: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11], stoppedBy: "Read-only" })
  const c = evaluateLines(ledger, word([...lines(13), `${P}-1`]), "draft")
  assert.ok(c.problems.some((p) => /holds PRDLINE-1 2 times/.test(p)), c.problems.join("\n"))
  // The cut must be the final paragraph: a second X-1 followed by more text is a duplicate.
  const notLast = evaluateLines(ledger, word([...lines(12), `${P}-1`, "after"]), "draft")
  assert.ok(notLast.problems.some((p) => /holds PRDLINE-1 2 times/.test(p)), notLast.problems.join("\n"))
  // An accepted last line cannot be cut: the editor still took input after it.
  const acceptedLast = lineLedger(P, { typed: 13, accepted: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12], stoppedBy: "Read-only" })
  assert.ok(evaluateLines(acceptedLast, word([...lines(12), `${P}-1`]), "draft").problems.length > 0)
})

test("a dirty editor that lost custody does not take input although its status reads Unsaved draft", () => {
  // G3 RC-3 (e3f048555) stale-generation rows: after the handoff the status stayed
  // "Unsaved draft" and only the toolbar showed the peer; every later line was
  // counted as accepted although the editor showed only the first lines.
  assert.equal(editorTakesInput("Unsaved draft History 4714 / 17179869184 B", "PRD Lease Peer is editing Request edit"), false)
  assert.equal(editorTakesInput("Unsaved draft", "PRD QA owner is editing Read-only · local draft kept"), false)
  assert.equal(editorTakesInput("Unsaved draft", "PRD QA owner is editing Read-only"), false)
  // Explicit read-only or disconnected status still ends input.
  assert.equal(editorTakesInput("Read-only", "PRD QA owner is editing Hand back"), false)
  assert.equal(editorTakesInput("Editor disconnected", null), false)
  // In the Artifacts frame the status chip stands in for the closed popover's toolbar (#5132).
  assert.equal(editorTakesInput("Unsaved draft", "chip:held Status: PRD Lease Peer is editing"), false)
  assert.equal(editorTakesInput("Unsaved draft", "chip:agent Status: Scout is editing"), false)
})

test("an editor that holds custody takes input; an unreadable toolbar leaves the status to decide", () => {
  assert.equal(editorTakesInput("Unsaved draft", "PRD QA owner is editing Hand back"), true)
  assert.equal(editorTakesInput("Ready to edit", "PRD QA owner is editing Hand back Hand over to PRD Lease Peer"), true)
  assert.equal(editorTakesInput("Saved · version 3", null), true)
  assert.equal(editorTakesInput("Unsaved draft", "chip:unconfirmed Status: Not confirmed yet"), true)
})
