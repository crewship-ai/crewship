// node --test e2e/acceptance/prd/report.test.mjs
import assert from "node:assert/strict"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { test } from "node:test"
import { parseOriginMap, evaluateRequired, formatGate, loadRequired, manualToRows, formatTable, parseArgs, readRows, resultOf, summarize, writeSummary } from "./report.mjs"

const row = (over = {}) => ({
  requirement: "B.project.version",
  title: "Agent revises after a comment",
  scenario: "project",
  test: "v2 after comment",
  result: "passed",
  appSha: "0123456789abcdef0123",
  evidence: "evidence/project/v2/result.json",
  evidenceFiles: ["evidence/project/v2/result.json"],
  mode: "full",
  origin: "https://example.test",
  reason: null,
  durationMs: 10,
  finishedAt: "2026-01-01T00:00:00.000Z",
  ...over,
})

test("parseArgs: --origin implies full, --quick overrides, unknown flags fail", () => {
  assert.equal(parseArgs(["--origin", "https://x.test"]).mode, "full")
  assert.equal(parseArgs(["--origin=https://x.test"]).origin, "https://x.test")
  assert.equal(parseArgs([]).mode, "quick")
  assert.equal(parseArgs(["--quick", "--origin", "https://x.test"]).mode, "quick")
  assert.throws(() => parseArgs(["--quick", "--full"]), /mutually exclusive/)
  assert.throws(() => parseArgs(["--orgin", "x"]), /unknown argument/)
  assert.throws(() => parseArgs(["--origin"]), /needs a value/)
  assert.deepEqual(parseArgs(["--quick", "--", "--headed", "-x"]).passthrough, ["--headed", "-x"])
})

test("resultOf maps every non-pass, non-skip status to failed", () => {
  assert.equal(resultOf("passed"), "passed")
  assert.equal(resultOf("skipped"), "skipped")
  for (const s of ["failed", "timedOut", "interrupted"]) assert.equal(resultOf(s), "failed")
})

test("summarize: zero rows or any failure is not ok", () => {
  assert.equal(summarize([]).ok, false)
  assert.equal(summarize([row(), row({ result: "skipped" })]).ok, true)
  assert.equal(summarize([row(), row({ result: "failed" })]).ok, false)
})

test("formatTable lists requirement, result with reason, short SHA and evidence; escapes pipes", () => {
  const t = formatTable([row(), row({ requirement: "A.lock", title: null, result: "failed", reason: "a | b", evidence: null })])
  const lines = t.split("\n")
  assert.equal(lines[0], "| Requirement | Scenario | Result | App SHA | Evidence |")
  assert.match(lines[2], /^\| A\.lock \| project › v2 after comment \| failed \(a \\\| b\) \| 0123456789ab \| — \|$/)
  assert.match(lines[3], /B\.project\.version: Agent revises after a comment .* passed .* evidence\/project\/v2\/result\.json/)
})

test("formatTable escapes backslashes so a trailing backslash cannot swallow the cell separator", () => {
  const t = formatTable([row({ reason: "path C:\\ | x\\", result: "failed", evidence: "end\\" })])
  const line = t.split("\n")[2]
  assert.ok(line.includes("failed (path C:\\\\ \\| x\\\\)"), line)
  assert.ok(line.endsWith("| end\\\\ |"), line)
  // Unescaped separators: every remaining "|" not preceded by an odd run of "\\".
  const separators = line.match(/(?<!\\)(?:\\\\)*\|/g) ?? []
  assert.equal(separators.length, 6)
})

test("readRows + writeSummary round-trip", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "prd-report-"))
  try {
    fs.mkdirSync(path.join(dir, "rows"))
    fs.writeFileSync(path.join(dir, "rows", "a.json"), JSON.stringify(row()))
    fs.writeFileSync(path.join(dir, "rows", "ignore.txt"), "x")
    const rows = readRows(path.join(dir, "rows"))
    assert.equal(rows.length, 1)
    const s = writeSummary(dir, rows, { mode: "full", origin: "https://example.test", appSha: "abc" })
    assert.equal(s.ok, true)
    assert.match(fs.readFileSync(path.join(dir, "summary.md"), "utf8"), /App SHA: abc · 1 passed, 0 failed, 0 skipped/)
    assert.equal(JSON.parse(fs.readFileSync(path.join(dir, "summary.json"), "utf8")).rows.length, 1)
  } finally {
    fs.rmSync(dir, { recursive: true, force: true })
  }
})

test("parseArgs: --cleanup, --dry-run and --older-than", () => {
  const a = parseArgs(["--origin", "https://x.test", "--cleanup", "--dry-run", "--older-than", "15"])
  assert.equal(a.cleanup, true)
  assert.equal(a.dryRun, true)
  assert.equal(a.olderThan, 15)
  assert.throws(() => parseArgs(["--cleanup", "--older-than", "soon"]), /minutes/)
})

test("cleanup patterns need both slug and name to look like the harness's", async () => {
  const { isHarnessPage, isHarnessWorkspace } = await import("./cleanup.mjs")
  assert.equal(isHarnessPage({ slug: "prd-quota-word-lx2k", name: "PRD quota word" }), true)
  assert.equal(isHarnessPage({ slug: "prd-lease-wordcraft-1", name: "PRD lease Word" }), true)
  assert.equal(isHarnessPage({ slug: "qa-close-ab-x-1", name: "QA close-ab probe" }), false)
  assert.equal(isHarnessPage({ slug: "prd-notes", name: "My notes" }), false)
  assert.equal(isHarnessPage({ slug: "pricing-site", name: "PRD pricing" }), false)
  assert.equal(isHarnessWorkspace({ slug: "prd-qa-sheet-soak-6bcf1c9a8f", name: "PRD acceptance Sheet soak" }), true)
  assert.equal(isHarnessWorkspace({ slug: "qa-c2-rights-be389ab9e6514a46", name: "QA Codex2 Craft rights" }), false)
  assert.equal(isHarnessWorkspace({ slug: "prd-qa-sheet-soak-6bcf1c9a8f", name: "Someone's workspace" }), false)
})

// ---- Required-ID gate (ZADANI G3 rule 3) ----------------------------------

const RC = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const CRAFT = "https://craft.example.test"
const ART = "https://artifacts.example.test"
const required = {
  origins: { craft: CRAFT, artifacts: ART },
  rows: [
    { id: "A.one", origin: "craft" },
    { id: "A.hour", origin: "craft", allowManual: true },
    { id: "A.skip", origin: "craft", allowSkipped: true },
    { id: "B.one", origin: "artifacts" },
  ],
}
const good = () => [
  row({ requirement: "A.one", origin: CRAFT, appSha: RC }),
  row({ requirement: "A.hour", origin: CRAFT, appSha: RC }),
  row({ requirement: "A.skip", origin: CRAFT, appSha: RC, result: "skipped", reason: "native editor" }),
  row({ requirement: "B.one", origin: ART, appSha: RC }),
]
const kinds = (r) => r.problems.map((p) => `${p.id}:${p.kind}`).sort()

test("gate: complete run passes", () => {
  const r = evaluateRequired(good(), required, { rc: RC })
  assert.deepEqual(r.problems, [])
  assert.equal(r.ok, true)
})

test("gate: missing ID fails", () => {
  const r = evaluateRequired(good().filter((x) => x.requirement !== "B.one"), required, { rc: RC })
  assert.equal(r.ok, false)
  assert.deepEqual(kinds(r), ["B.one:missing"])
})

test("gate: skipped fails except on the allowed row; failed always fails", () => {
  const rows = good().map((x) => (x.requirement === "A.one" ? { ...x, result: "skipped" } : x))
  assert.deepEqual(kinds(evaluateRequired(rows, required, { rc: RC })), ["A.one:result"])
  const f = good().map((x) => (x.requirement === "A.skip" ? { ...x, result: "failed" } : x))
  assert.deepEqual(kinds(evaluateRequired(f, required, { rc: RC })), ["A.skip:result"])
})

test("gate: wrong appSha fails; a row on the RC wins over an older row on another SHA", () => {
  const bad = good().map((x) => (x.requirement === "B.one" ? { ...x, appSha: "b".repeat(40) } : x))
  assert.deepEqual(kinds(evaluateRequired(bad, required, { rc: RC })), ["B.one:appSha"])
  const merged = [...bad, row({ requirement: "B.one", origin: ART, appSha: RC, finishedAt: "2025-01-01T00:00:00.000Z" })]
  assert.equal(evaluateRequired(merged, required, { rc: RC }).ok, true)
})

test("gate: wrong origin fails", () => {
  const bad = good().map((x) => (x.requirement === "A.one" ? { ...x, origin: ART + "/" } : x))
  assert.deepEqual(kinds(evaluateRequired(bad, required, { rc: RC })), ["A.one:origin"])
  const ok = good().map((x) => (x.requirement === "A.one" ? { ...x, origin: CRAFT + "/" } : x))
  assert.equal(evaluateRequired(ok, required, { rc: RC }).ok, true)
})

test("gate: quick mode fails; manual needs evidence and an allowing row", () => {
  const quick = good().map((x) => (x.requirement === "A.one" ? { ...x, mode: "quick" } : x))
  assert.deepEqual(kinds(evaluateRequired(quick, required, { rc: RC })), ["A.one:mode"])
  const manual = (requirement, evidence) => good().map((x) => (x.requirement === requirement ? { ...x, mode: "manual", evidence } : x))
  assert.deepEqual(kinds(evaluateRequired(manual("A.hour", null), required, { rc: RC })), ["A.hour:evidence"])
  assert.equal(evaluateRequired(manual("A.hour", "https://rec.test/v/1"), required, { rc: RC }).ok, true)
  assert.deepEqual(kinds(evaluateRequired(manual("A.one", "https://rec.test/v/1"), required, { rc: RC })), ["A.one:mode"])
})

test("gate: rows from several runs merge, latest per ID wins, no rc means any single SHA", () => {
  const runA = good().filter((x) => x.requirement !== "B.one")
  const runB = [row({ requirement: "B.one", origin: ART, appSha: RC })]
  assert.equal(evaluateRequired([...runA, ...runB], required, { rc: RC }).ok, true)
  const older = row({ requirement: "A.one", origin: CRAFT, appSha: RC, result: "failed", finishedAt: "2025-01-01T00:00:00.000Z" })
  assert.equal(evaluateRequired([older, ...good()], required, { rc: RC }).ok, true)
  assert.throws(() => evaluateRequired(good(), required, {}), /--rc/)
})

test("gate: manual rows are converted and recordedAt orders them", () => {
  const rows = manualToRows([{ id: "A.hour", result: "passed", appSha: RC, evidence: "https://rec.test/v/1", recordedAt: "2026-10-31T10:00:00Z" }], required)
  assert.equal(rows[0].mode, "manual")
  assert.equal(rows[0].origin, CRAFT)
  assert.equal(rows[0].finishedAt, "2026-10-31T10:00:00Z")
  const withoutEvidence = manualToRows([{ id: "A.hour", result: "passed", appSha: RC, recordedAt: "2026-10-31T10:00:00Z" }], required)
  const all = good().filter((x) => x.requirement !== "A.hour").concat(withoutEvidence)
  assert.deepEqual(kinds(evaluateRequired(all, required, { rc: RC })), ["A.hour:evidence"])
  assert.throws(() => manualToRows([{ result: "passed" }], required), /id/)
})

test("gate: formatGate lists every problem; shipped required-ids.json is well formed", () => {
  const r = evaluateRequired([], required, { rc: RC })
  const text = formatGate(r)
  for (const id of ["A.one", "A.hour", "A.skip", "B.one"]) assert.match(text, new RegExp(id.replace(".", "\\.")))
  assert.match(text, /FAIL/)
  const real = loadRequired(new URL("./required-ids.json", import.meta.url).pathname, `craft=${CRAFT},artifacts=${ART}`)
  assert.throws(() => loadRequired(new URL("./required-ids.json", import.meta.url).pathname, ""), /not configured/)
  const ids = real.rows.map((x) => x.id)
  assert.equal(new Set(ids).size, ids.length)
  assert.equal(real.rows.filter((x) => x.allowSkipped).map((x) => x.id).join(), "A.craft.lease.fail-closed")
  for (const id of ["B.project.from-chat", "A.lock", "quota.aggregate.workspace", "drafts.identity-switch", "A.craft.session.hour-word"]) assert.ok(ids.includes(id), id)
  assert.equal(ids.filter((i) => i.startsWith("drafts.crash.")).length, 11)
  for (const x of real.rows) assert.ok(real.origins[x.origin], x.id)
})

test("parseArgs: --rc, --manual, --merge, --required", () => {
  const a = parseArgs(["--origin", "https://x.test", "--rc", RC, "--manual", "m.json", "--merge", "a", "--merge", "b", "--required", "r.json", "--origin-map", "craft=https://c.test"])
  assert.equal(a.rc, RC)
  assert.equal(a.manual, "m.json")
  assert.deepEqual(a.merge, ["a", "b"])
  assert.equal(a.required, "r.json")
  assert.deepEqual(parseOriginMap(a.originMap), { craft: "https://c.test" })
  assert.throws(() => parseOriginMap("craft"), /key=url/)
})

// ---- Manual records and carried-over evidence (G3 rules 3–4, review 31. 10.) ----

const OLD = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const strict = {
  origins: { craft: CRAFT, artifacts: ART },
  sharedPaths: ["internal/mooring", "internal/database/migrations"],
  rows: [
    { id: "A.hour", origin: "craft", allowManual: true, manualChecks: ["restart", "content-after-restart"] },
    { id: "A.save", origin: "craft", alwaysRerun: true },
    { id: "B.one", origin: "artifacts" },
  ],
}
const strictGood = () => [
  ...manualToRows([{ id: "A.hour", result: "passed", appSha: RC, evidence: "https://rec.test/v/1", recordedAt: "2026-10-31T10:00:00Z", checks: { restart: true, "content-after-restart": true } }], strict),
  row({ requirement: "A.save", origin: CRAFT, appSha: RC }),
  row({ requirement: "B.one", origin: ART, appSha: RC }),
]

test("gate: a manual row must record every check its ID requires", () => {
  assert.deepEqual(evaluateRequired(strictGood(), strict, { rc: RC }).problems, [])
  const partial = strictGood().map((x) => (x.requirement === "A.hour" ? { ...x, checks: { restart: true } } : x))
  assert.deepEqual(kinds(evaluateRequired(partial, strict, { rc: RC })), ["A.hour:evidence"])
  const falsy = strictGood().map((x) => (x.requirement === "A.hour" ? { ...x, checks: { restart: true, "content-after-restart": false } } : x))
  assert.deepEqual(kinds(evaluateRequired(falsy, strict, { rc: RC })), ["A.hour:evidence"])
})

test("gate: carried-over evidence keeps its own SHA and needs an empty diff over scenario and shared paths", () => {
  const rows = strictGood().map((x) => (x.requirement === "B.one" ? { ...x, appSha: OLD } : x))
  const carry = [{ id: "B.one", fromSha: OLD, paths: ["e2e/acceptance/prd/project.acceptance.ts"], engines: { from: "word@1 sheet@2", rc: "word@1 sheet@2" } }]
  /** @type {any[]} */
  const calls = []
  const empty = (from, to, paths) => (calls.push({ from, to, paths }), "")
  const ok = evaluateRequired(rows, strict, { rc: RC, carryover: carry, diff: empty })
  assert.deepEqual(ok.problems, [])
  const b = ok.rows.find((r) => r.requirement === "B.one")
  assert.equal(b.appSha, OLD, "the carried row keeps the SHA it was proven on")
  assert.equal(b.carriedFrom.fromSha, OLD)
  assert.equal(b.carriedFrom.diffStat, "")
  assert.deepEqual(calls[0].paths, ["e2e/acceptance/prd/project.acceptance.ts", "internal/mooring", "internal/database/migrations"])
  assert.equal(calls[0].to, RC)

  const changed = () => " internal/mooring/adapter.go | 2 +-\n"
  assert.deepEqual(kinds(evaluateRequired(rows, strict, { rc: RC, carryover: carry, diff: changed })), ["B.one:carryover"])
  const engines = [{ ...carry[0], engines: { from: "word@1", rc: "word@2" } }]
  assert.deepEqual(kinds(evaluateRequired(rows, strict, { rc: RC, carryover: engines, diff: empty })), ["B.one:carryover"])
  const noEngines = [{ id: "B.one", fromSha: OLD, paths: ["e2e/x"] }]
  assert.deepEqual(kinds(evaluateRequired(rows, strict, { rc: RC, carryover: noEngines, diff: empty })), ["B.one:carryover"])
  const noPaths = [{ ...carry[0], paths: [] }]
  assert.deepEqual(kinds(evaluateRequired(rows, strict, { rc: RC, carryover: noPaths, diff: empty })), ["B.one:carryover"])
  assert.deepEqual(kinds(evaluateRequired(rows, strict, { rc: RC })), ["B.one:appSha"], "without a carryover record an old SHA still fails")
})

test("gate: save-class rows can never be carried over", () => {
  const rows = strictGood().map((x) => (x.requirement === "A.save" ? { ...x, appSha: OLD } : x))
  const carry = [{ id: "A.save", fromSha: OLD, paths: ["e2e/x"], engines: { from: "e", rc: "e" } }]
  assert.deepEqual(kinds(evaluateRequired(rows, strict, { rc: RC, carryover: carry, diff: () => "" })), ["A.save:carryover"])
})

test("gate: shipped required-ids.json marks save rows, hour checklists and shared paths", () => {
  const real = loadRequired(new URL("./required-ids.json", import.meta.url).pathname, `craft=${CRAFT},artifacts=${ART}`)
  assert.ok((real.sharedPaths ?? []).length > 0)
  for (const p of ["internal/database/migrations", "internal/mooring", "lib/mooring", ".env.production"]) assert.ok(real.sharedPaths.includes(p), p)
  const save = /save|roundtrip|soak|\.lease(\.|-|$)|^A\.lock$|outage|lost-ack|durability|work-kept|conflict|session\.hour|restart|write-revoked|stale/
  for (const x of real.rows.filter((r) => save.test(r.id))) assert.equal(x.alwaysRerun, true, x.id)
  // Every Craft row drives the native lease or save path (#4658, #4662): none carries over.
  for (const x of real.rows.filter((r) => r.origin === "craft")) assert.equal(x.alwaysRerun, true, x.id)
  for (const id of ["A.lock", "A.craft.agent.word.lease", "A.craft.agent.sheet.lease", "A.craft.agent.free-edit-word"]) assert.equal(real.rows.find((r) => r.id === id)?.alwaysRerun, true, id)
  for (const id of ["A.craft.session.hour-word", "A.craft.session.hour-sheet"]) {
    const r = real.rows.find((x) => x.id === id)
    for (const c of ["enter", "font", "styles", "table", "image", "reload", "restart", "content-after-restart"]) assert.ok(r.manualChecks?.includes(c), `${id} ${c}`)
  }
  assert.deepEqual(real.rows.filter((x) => x.allowManual).map((x) => x.id), ["A.craft.session.hour-word", "A.craft.session.hour-sheet"])
})

test("manualToRows keeps the recorded checks", () => {
  const [r] = manualToRows([{ id: "A.hour", result: "passed", appSha: RC, evidence: "x", checks: { restart: true } }], strict)
  assert.deepEqual(r.checks, { restart: true })
})

test("gate: a carried row is checked against its own origin, not the newer row's (Codex #4689 B1)", () => {
  const rows = [
    ...strictGood().filter((x) => x.requirement !== "B.one"),
    row({ requirement: "B.one", origin: "https://elsewhere.test", appSha: OLD, finishedAt: "2025-01-01T00:00:00.000Z" }),
    row({ requirement: "B.one", origin: ART, appSha: "c".repeat(40), result: "failed", finishedAt: "2026-01-01T00:00:00.000Z" }),
  ]
  const carry = [{ id: "B.one", fromSha: OLD, paths: ["e2e/x"], engines: { from: "e", rc: "e" } }]
  assert.deepEqual(kinds(evaluateRequired(rows, strict, { rc: RC, carryover: carry, diff: () => "" })), ["B.one:origin"])
})

test("gate: shipped list marks every saving row alwaysRerun and shares the API layer (Codex #4689 B2, B3)", () => {
  const real = loadRequired(new URL("./required-ids.json", import.meta.url).pathname, `craft=${CRAFT},artifacts=${ART}`)
  for (const id of ["B.design.save-reload", "B.durability.outage", "B.durability.lost-ack", "B.durability.lost-ack.contract", "quota.project.work-kept", "B.project.conflict", "B.project.restore-restart"]) assert.equal(real.rows.find((x) => x.id === id)?.alwaysRerun, true, id)
  for (const p of ["internal/api", "internal/server", "internal/pages", "internal/design"]) assert.ok(real.sharedPaths.includes(p), p)
})
