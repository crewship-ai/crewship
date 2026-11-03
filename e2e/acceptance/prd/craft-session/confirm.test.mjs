// node --test e2e/acceptance/prd/craft-session/confirm.test.mjs
// (also run by e2e/acceptance/prd/report.test.mjs, the PRD runner step in CI)
import assert from "node:assert/strict"
import { test } from "node:test"
import { baselineBefore, cellInTable, confirmCycle, holdsMarker, labelVersion } from "./confirm.ts"

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
