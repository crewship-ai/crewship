// node --test e2e/acceptance/native-walk/sheet-versions.test.mjs
import assert from "node:assert/strict"
import { test } from "node:test"
import { changeKind, classifyVersions, lostReplyProblems, onlyCursorAfter, withoutActiveCell } from "./sheet-versions.mjs"

// The shape the Sheet engine saves (lib/mooring/gridcraft-empty.ts), cut to what matters here.
const book = (cells, row, col = 0, extra = {}) => JSON.stringify({
  sheets: [{ name: "Sheet1", cells: { bands: { 0: { rows: Object.fromEntries(cells.map((v, r) => [r, { 0: { value: { t: "Text", v }, formula: null, style: 0 } }])) } }, len: cells.length }, view_active: { row, col }, view_top_left: { row: 0, col: 0 }, ...extra }],
  active_sheet: 0,
})

test("a move of the active cell alone is cursor-only", () => {
  assert.equal(changeKind(book(["A"], 0), book(["A"], 1)), "cursor-only")
  assert.equal(changeKind(book(["A"], 0), book(["A"], 3, 2)), "cursor-only")
})

test("a new or changed cell is a content change, also when the active cell moves with it", () => {
  assert.equal(changeKind(book(["A"], 1), book(["A", "B"], 1)), "content")
  assert.equal(changeKind(book(["A"], 0), book(["A", "B"], 1)), "content")
  assert.equal(changeKind(book(["A"], 0), book(["X"], 1)), "content")
})

test("any other view field is content, not the active cell", () => {
  assert.equal(changeKind(book(["A"], 0), book(["A"], 0, 0, { view_top_left: { row: 5, col: 0 } })), "content")
  assert.equal(changeKind(book(["A"], 0), book(["A"], 1, 0, { zoom: 120 })), "content")
})

test("same bytes are identical; unknown or non-workbook text is content", () => {
  assert.equal(changeKind(book(["A"], 0), book(["A"], 0)), "identical")
  assert.equal(changeKind(undefined, book(["A"], 0)), "content")
  assert.equal(changeKind("not json", "not json either"), "content")
  assert.equal(withoutActiveCell('{"body":[]}'), null)
})

test("cursor-only versions after a content save are counted, not rejected", () => {
  const initial = { version: "1", text: book([], 0) }
  const writes = [
    { version: "2", base: "1", text: book(["A"], 0) },
    { version: "3", base: "2", text: book(["A"], 1) },
    { version: "4", base: "3", text: book(["A", "B"], 2) },
  ]
  const r = classifyVersions(initial, writes)
  assert.deepEqual(r.kinds, { 2: "content", 3: "cursor-only", 4: "content" })
  assert.deepEqual(r.cursorOnly, [{ version: "3", base: "2" }])
  assert.deepEqual(r.problems, [])
  assert.equal(onlyCursorAfter(r.kinds, "2", "3"), true)
  assert.equal(onlyCursorAfter(r.kinds, "2", "4"), false)
})

test("a cursor-only version that does not follow a content save is a problem", () => {
  const initial = { version: "1", text: book(["A"], 0) }
  const chain = classifyVersions(initial, [{ version: "2", base: "1", text: book(["A"], 1) }])
  assert.match(chain.problems.join(), /v2 changes only the active cell and does not follow a content change/)
  const twice = classifyVersions(initial, [
    { version: "2", base: "1", text: book(["A", "B"], 1) },
    { version: "3", base: "2", text: book(["A", "B"], 2) },
    { version: "4", base: "3", text: book(["A", "B"], 3) },
  ])
  assert.deepEqual(twice.problems, ["v4 changes only the active cell and does not follow a content change (v3 is cursor-only)"])
})

test("after a lost reply: a move of the active cell on the lost version passes", () => {
  const kinds = { 8: "content", 9: "content", 10: "cursor-only" }
  const lost = { version: "9", contentHash: "sha256:lostlostlost" }
  const after = [{ kind: "reconcile", baseVersion: "8", contentHash: "sha256:lostlostlost", version: "9" }, { kind: "save", baseVersion: "9", contentHash: "sha256:cursorcursor", version: "10" }]
  assert.deepEqual(lostReplyProblems(lost, after, kinds), [])
})

test("after a lost reply: a re-sent attempt, a wrong base or a content change is flagged", () => {
  const lost = { version: "9", contentHash: "sha256:lostlostlost" }
  const resent = lostReplyProblems(lost, [{ kind: "save", baseVersion: "9", contentHash: "sha256:lostlostlost", version: "9", unchanged: true }], { 9: "content" })
  assert.match(resent.join(), /re-sent the lost attempt/)
  const duplicate = lostReplyProblems(lost, [{ kind: "save", baseVersion: "8", contentHash: "sha256:lostlostlost", version: "10" }], { 10: "content" })
  assert.match(duplicate.join(), /re-sent the lost attempt/)
  assert.match(duplicate.join(), /builds on v8, not the lost save's v9/)
  assert.match(duplicate.join(), /save v10 after the lost reply is a content change/)
  const content = lostReplyProblems(lost, [{ kind: "save", baseVersion: "9", contentHash: "sha256:newcontent", version: "10" }], { 10: "content" })
  assert.deepEqual(content, ["save v10 after the lost reply is a content change, not a move of the active cell"])
})
