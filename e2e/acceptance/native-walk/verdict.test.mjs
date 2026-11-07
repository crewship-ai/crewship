// node --test e2e/acceptance/native-walk/verdict.test.mjs
import assert from "node:assert/strict"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { test } from "node:test"
import { EXIT, HISTORY_NOT_ENABLED, SCENARIOS, evidencePath, isHeadlessShell, loadEvidence, parseListed, verdict } from "./verdict.mjs"

const RC = "ce984daf1d5f9dad37e00228c269ad0ae06736dd"
const ORIGIN = "https://crewship-dev3.unifylab.cz"
const CHROME = "/cache/ms-playwright/chromium-1248/chrome-linux64/chrome"
const [WORD, SHEET] = SCENARIOS

const row = (s, over = {}) => ({ requirement: s.id, title: s.title, scenario: "native-walk", test: s.test, result: "passed", appSha: RC, origin: ORIGIN, mode: "full", reason: null, ...over })
const unmapped = (s) => ({ ...row(s), requirement: `UNMAPPED ${s.test}`, title: null, result: "failed", reason: "test declares no PRD requirement (use prdRequirement())" })

/** A walk that did everything right; versions 1 → 2..last as the steps advance. */
function walkEvidence(s, over = {}) {
  let v = 1
  const steps = s.steps.map((name) => {
    const before = String(v)
    if (!["open and take the editor", "home tab", "ctrl+s without a change", "reload", "final"].includes(name)) v++
    return { name, verifyPresses: 0, versionBefore: before, versionAfter: String(v), inputMs: 10, confirmationMs: 10, longestStallMs: 5, status: "Uloženo" }
  })
  const receipts = Array.from({ length: v - 1 }, (_, i) => String(i + 2))
  return {
    app: s.app, label: s.app === "wordcraft" ? "Word" : "Sheet", appSha: RC, origin: ORIGIN,
    chromium: { path: CHROME, version: "156.0.8078.4", headless: false },
    failure: null, violations: [], steps,
    versions: { start: "1", receipts, head: String(v) },
    history: { result: "not-enabled", listingStatus: 404, error: HISTORY_NOT_ENABLED, listing: null, truncated: null },
    ...over,
  }
}

const good = (over = {}) => ({
  playwrightStatus: 0,
  listed: SCENARIOS.map((s) => s.test),
  rows: SCENARIOS.map((s) => row(s)),
  run: { origin: ORIGIN, mode: "full", appSha: RC, appDirty: false },
  finished: { status: "passed" },
  rc: RC, origin: ORIGIN, chrome: CHROME, narrowed: false,
  evidence: Object.fromEntries(SCENARIOS.map((s) => [s.id, walkEvidence(s)])),
  ...over,
})
const withEvidence = (id, change) => {
  const input = good()
  input.evidence[id] = change(input.evidence[id])
  return input
}
const historyOk = (e) => ({ ...e, history: { listingStatus: 200, error: null, truncated: false, listing: [e.versions.start, ...e.versions.receipts] } })

test("a complete run passes and lists the disabled history as NOT VERIFIED", () => {
  const v = verdict(good())
  assert.equal(v.verdict, "PASS")
  assert.equal(v.exit, EXIT.pass)
  assert.deepEqual(v.reasons, [])
  assert.deepEqual(v.notVerified.map((n) => n.id), ["walk.word", "walk.sheet"])
  assert.match(v.notVerified[0].item, /not compared/)
})

test("a listing that answers 200 with exactly the observed versions passes with nothing NOT VERIFIED", () => {
  const input = good()
  for (const s of SCENARIOS) input.evidence[s.id] = historyOk(input.evidence[s.id])
  const v = verdict(input)
  assert.equal(v.verdict, "PASS")
  assert.deepEqual(v.notVerified, [])
})

// Each case changes one thing of a passing run and must not pass.
const cases = [
  // The real run of 7 Nov (rc-ce984daf1/walk): both walks passed, but the rows were UNMAPPED and failed.
  { name: "the real case: two passed walks, two UNMAPPED failed rows, exit 1", input: good({ playwrightStatus: 1, finished: { status: "failed" }, rows: SCENARIOS.map(unmapped) }), want: "FAIL", reason: /unexpected row UNMAPPED Native walk Word/ },
  { name: "two passed walk rows next to two UNMAPPED failed rows", input: good({ rows: [...SCENARIOS.map((s) => row(s)), ...SCENARIOS.map(unmapped)] }), want: "FAIL", reason: /unexpected row UNMAPPED/ },
  { name: "output says \"2 passed\" but playwright exited 1", input: { ...good({ playwrightStatus: 1 }), log: "  2 passed (3.4m)\n" }, want: "FAIL", reason: /playwright exited 1/ },
  { name: "playwright was killed (no exit status)", input: good({ playwrightStatus: null }), want: "FAIL", reason: /playwright exited null/ },
  { name: "the run did not finish", input: good({ finished: null }), want: "FAIL", reason: /no finished.json/ },
  { name: "playwright reported the run interrupted", input: good({ finished: { status: "interrupted" } }), want: "FAIL", reason: /interrupted/ },
  { name: "run.json is missing", input: good({ run: null }), want: "SETUP", reason: /run.json is missing/ },
  { name: "the origin serves another SHA", input: good({ run: { origin: ORIGIN, mode: "full", appSha: "0".repeat(40), appDirty: false } }), want: "SETUP", reason: /not RC/ },
  { name: "the app build is dirty", input: good({ run: { origin: ORIGIN, mode: "full", appSha: RC, appDirty: true } }), want: "SETUP", reason: /not clean/ },
  { name: "the dirty flag is unknown", input: good({ run: { origin: ORIGIN, mode: "full", appSha: RC, appDirty: null } }), want: "SETUP", reason: /not clean/ },
  { name: "the run recorded another origin", input: good({ run: { origin: "https://crewship-dev2.unifylab.cz", mode: "full", appSha: RC, appDirty: false } }), want: "SETUP", reason: /run origin/ },
  { name: "the run was in quick mode", input: good({ run: { origin: ORIGIN, mode: "quick", appSha: RC, appDirty: false } }), want: "SETUP", reason: /mode quick/ },
  { name: "--rc is a short SHA", input: good({ rc: RC.slice(0, 12) }), want: "SETUP", reason: /full 40-character/ },
  { name: "nothing was listed", input: good({ listed: [] }), want: "FAIL", reason: /listed 0 times/ },
  { name: "only one walk was listed without --grep", input: good({ listed: [WORD.test] }), want: "FAIL", reason: /walk.sheet: "Native walk Sheet" listed 0 times/ },
  { name: "an extra test was listed", input: good({ listed: [...SCENARIOS.map((s) => s.test), "Native walk Slides"] }), want: "FAIL", reason: /unexpected test listed/ },
  { name: "a walk left no row", input: good({ rows: [row(WORD)] }), want: "FAIL", reason: /walk.sheet: left 0 result rows/ },
  { name: "no rows at all", input: good({ rows: [] }), want: "FAIL", reason: /left 0 result rows/ },
  { name: "a walk left two rows", input: good({ rows: [...SCENARIOS.map((s) => row(s)), row(SHEET)] }), want: "FAIL", reason: /walk.sheet: left 2 result rows/ },
  { name: "a walk failed", input: good({ rows: [row(WORD), row(SHEET, { result: "failed", reason: "violation" })] }), want: "FAIL", reason: /walk.sheet: failed \(violation\)/ },
  { name: "a walk was skipped", input: good({ rows: [row(WORD), row(SHEET, { result: "skipped" })] }), want: "FAIL", reason: /1 skipped/ },
  { name: "a row names another test", input: good({ rows: [row(WORD), row(SHEET, { test: WORD.test })] }), want: "FAIL", reason: /row is for test/ },
  { name: "a row is on another SHA", input: good({ rows: [row(WORD), row(SHEET, { appSha: "unknown" })] }), want: "FAIL", reason: /row app SHA unknown/ },
  { name: "a row is from another origin", input: good({ rows: [row(WORD), row(SHEET, { origin: "http://localhost:8080" })] }), want: "FAIL", reason: /row origin/ },
  { name: "evidence is missing", input: withEvidence("walk.word", () => null), want: "FAIL", reason: /walk.word: no evidence file/ },
  { name: "evidence is unreadable", input: withEvidence("walk.word", () => ({ unreadable: "Unexpected end of JSON input" })), want: "FAIL", reason: /unreadable evidence/ },
  { name: "evidence of the other app", input: withEvidence("walk.sheet", () => walkEvidence(SHEET, { app: "wordcraft" })), want: "FAIL", reason: /evidence is for wordcraft/ },
  { name: "evidence on another SHA", input: withEvidence("walk.word", (e) => ({ ...e, appSha: "unknown" })), want: "FAIL", reason: /evidence app SHA/ },
  { name: "evidence from another origin", input: withEvidence("walk.word", (e) => ({ ...e, origin: "https://x.test" })), want: "FAIL", reason: /evidence origin/ },
  { name: "the walk ran in the headless shell", input: withEvidence("walk.word", (e) => ({ ...e, chromium: { path: "/b/chromium_headless_shell-1248/chrome-linux/headless_shell", version: "1", headless: false } })), want: "FAIL", reason: /headless shell/ },
  { name: "the walk ran headless", input: withEvidence("walk.word", (e) => ({ ...e, chromium: { ...e.chromium, headless: true } })), want: "FAIL", reason: /headed/ },
  { name: "the walk ran another Chromium", input: withEvidence("walk.word", (e) => ({ ...e, chromium: { ...e.chromium, path: "/other/chrome" } })), want: "FAIL", reason: /not \/cache/ },
  { name: "the walk aborted", input: withEvidence("walk.word", (e) => ({ ...e, failure: "Error: walk stalled" })), want: "FAIL", reason: /walk aborted/ },
  { name: "a violation was recorded", input: withEvidence("walk.word", (e) => ({ ...e, violations: [{ step: "reload", rule: "blank" }] })), want: "FAIL", reason: /violation at "reload"/ },
  { name: "a step is missing", input: withEvidence("walk.word", (e) => ({ ...e, steps: e.steps.filter((s) => s.name !== "lost reply") })), want: "FAIL", reason: /step "lost reply" recorded 0 times/ },
  { name: "a step is repeated", input: withEvidence("walk.sheet", (e) => ({ ...e, steps: [...e.steps, e.steps.at(-1)] })), want: "FAIL", reason: /step "final" recorded 2 times/ },
  { name: "an unknown step", input: withEvidence("walk.sheet", (e) => ({ ...e, steps: [...e.steps, { ...e.steps.at(-1), name: "extra" }] })), want: "FAIL", reason: /unexpected step "extra"/ },
  { name: "\"Ověřit výsledek\" was pressed", input: withEvidence("walk.word", (e) => ({ ...e, steps: e.steps.map((s) => (s.name === "lost reply" ? { ...s, verifyPresses: 1 } : s)) })), want: "FAIL", reason: /pressed 1 times/ },
  { name: "a step did not record its presses", input: withEvidence("walk.word", (e) => ({ ...e, steps: e.steps.map((s, i) => (i === 0 ? { ...s, verifyPresses: undefined } : s)) })), want: "FAIL", reason: /pressed undefined times/ },
  { name: "no version record", input: withEvidence("walk.word", (e) => ({ ...e, versions: undefined })), want: "FAIL", reason: /no version record/ },
  { name: "a gap in the receipts", input: withEvidence("walk.word", (e) => ({ ...e, versions: { ...e.versions, receipts: e.versions.receipts.filter((r) => r !== "4") } })), want: "FAIL", reason: /not contiguous/ },
  { name: "a duplicate receipt", input: withEvidence("walk.word", (e) => ({ ...e, versions: { ...e.versions, receipts: ["2", "2", ...e.versions.receipts.slice(1)] } })), want: "FAIL", reason: /not contiguous/ },
  { name: "receipts that do not start after the starting head", input: withEvidence("walk.word", (e) => ({ ...e, versions: { ...e.versions, start: "0" } })), want: "FAIL", reason: /not contiguous from v0/ },
  { name: "the final head is not the last receipt", input: withEvidence("walk.word", (e) => ({ ...e, versions: { ...e.versions, head: String(Number(e.versions.head) + 1) } })), want: "FAIL", reason: /is not the final head/ },
  { name: "no receipt at all", input: withEvidence("walk.word", (e) => ({ ...e, versions: { ...e.versions, receipts: [] } })), want: "FAIL", reason: /no save receipt/ },
  { name: "an unreadable receipt (the lost save answered no version)", input: withEvidence("walk.word", (e) => ({ ...e, versions: { ...e.versions, receipts: [...e.versions.receipts.slice(0, -1), "?"] } })), want: "FAIL", reason: /are not versions/ },
  { name: "ctrl+s without a change made a version", input: withEvidence("walk.word", (e) => ({ ...e, steps: e.steps.map((s) => (s.name === "ctrl+s without a change" ? { ...s, versionAfter: String(Number(s.versionBefore) + 1) } : s)) })), want: "FAIL", reason: /ctrl\+s without a change went from/ },
  { name: "the lost reply made two versions", input: withEvidence("walk.sheet", (e) => ({ ...e, steps: e.steps.map((s) => (s.name === "lost reply" ? { ...s, versionAfter: String(Number(s.versionBefore) + 2) } : s)) })), want: "FAIL", reason: /not exactly one version/ },
  { name: "the lost reply made no version", input: withEvidence("walk.sheet", (e) => ({ ...e, steps: e.steps.map((s) => (s.name === "lost reply" ? { ...s, versionAfter: s.versionBefore } : s)) })), want: "FAIL", reason: /not exactly one version/ },
  { name: "no history record", input: withEvidence("walk.word", (e) => ({ ...e, history: undefined })), want: "FAIL", reason: /no history record/ },
  { name: "a 404 with another message", input: withEvidence("walk.word", (e) => ({ ...e, history: { listingStatus: 404, error: "page not found" } })), want: "FAIL", reason: /answered 404: page not found/ },
  { name: "a 500 from the listing", input: withEvidence("walk.word", (e) => ({ ...e, history: { listingStatus: 500, error: "native version history unavailable" } })), want: "FAIL", reason: /answered 500/ },
  { name: "a 403 labelled not-enabled by the spec", input: withEvidence("walk.word", (e) => ({ ...e, history: { result: "not-enabled", listingStatus: 403, error: HISTORY_NOT_ENABLED } })), want: "FAIL", reason: /answered 403/ },
  { name: "a 200 listing that misses a version", input: withEvidence("walk.word", (e) => { const h = historyOk(e); return { ...h, history: { ...h.history, listing: h.history.listing.slice(1) } } }), want: "FAIL", reason: /is not the observed/ },
  { name: "a 200 listing that is truncated", input: withEvidence("walk.word", (e) => { const h = historyOk(e); return { ...h, history: { ...h.history, truncated: true } } }), want: "FAIL", reason: /truncated=true/ },
]
for (const c of cases) {
  test(`not a pass: ${c.name}`, () => {
    const v = verdict(c.input)
    assert.equal(v.verdict, c.want, v.reasons.join("; "))
    assert.equal(v.exit, c.want === "SETUP" ? EXIT.setup : EXIT.failed)
    assert.ok(v.reasons.some((r) => c.reason.test(r)), `no reason matches ${c.reason}: ${v.reasons.join("; ")}`)
  })
}

test("a narrowed green run is PARTIAL, never 0", () => {
  const v = verdict(good({ narrowed: true, listed: [WORD.test], rows: [row(WORD)] }))
  assert.equal(v.verdict, "PARTIAL")
  assert.equal(v.exit, EXIT.partial)
  assert.deepEqual(v.expected, ["walk.word"])
})
test("a narrowed run over both walks is still PARTIAL", () => {
  assert.equal(verdict(good({ narrowed: true })).exit, EXIT.partial)
})
test("a narrowed run that failed is FAIL, not PARTIAL", () => {
  assert.equal(verdict(good({ narrowed: true, listed: [WORD.test], rows: [row(WORD, { result: "failed" })] })).verdict, "FAIL")
})
test("a narrowed run that matched nothing is FAIL", () => {
  assert.equal(verdict(good({ narrowed: true, listed: [], rows: [] })).verdict, "FAIL")
})

test("listed titles come from `playwright test --list` lines of the spec only", () => {
  const out = "Listing tests:\n  native-walk.acceptance.ts:83:3 › Native walk Word\n  native-walk.acceptance.ts:83:3 › Native walk Sheet\nTotal: 2 tests in 1 file\n"
  assert.deepEqual(parseListed(out), ["Native walk Word", "Native walk Sheet"])
  assert.deepEqual(parseListed(""), [])
})
test("the headless shell is recognised by its path", () => {
  assert.ok(isHeadlessShell("/c/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell"))
  assert.ok(isHeadlessShell("/c/chromium_headless_shell-1169/chrome-linux/headless_shell"))
  assert.ok(!isHeadlessShell(CHROME))
})
test("evidence is read from the path the PRD fixture writes, and a broken file is unreadable, not absent", () => {
  assert.equal(evidencePath(WORD), path.join("evidence", "native-walk", "native-walk-word", "walk.json"))
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "native-walk-verdict-"))
  fs.mkdirSync(path.dirname(path.join(dir, evidencePath(WORD))), { recursive: true })
  fs.writeFileSync(path.join(dir, evidencePath(WORD)), "{\"app\":")
  const loaded = loadEvidence(dir)
  assert.ok(loaded["walk.word"].unreadable)
  assert.equal(loaded["walk.sheet"], null)
  fs.rmSync(dir, { recursive: true, force: true })
})
