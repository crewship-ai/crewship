// node --test e2e/acceptance/native-walk/verdict.test.mjs
import assert from "node:assert/strict"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { spawnSync } from "node:child_process"
import { test } from "node:test"
import { ACCOUNT_VARS, checkOrigin, jobArgs, runEnv } from "./runner-env.mjs"
import { EXIT, HISTORY_NOT_ENABLED, SCENARIOS, evidencePath, fullChromium, isHeadlessShell, loadEvidence, parseListed, sameVersions, verdict } from "./verdict.mjs"

const RC = "ce984daf1d5f9dad37e00228c269ad0ae06736dd"
const ORIGIN = "https://crewship-dev3.unifylab.cz"
const CHROME = "/cache/ms-playwright/chromium-1248/chrome-linux64/chrome"
const RUN = "6f1c1f7e-4a57-4c1e-9d3b-2f5f0d1a9e01"
const OTHER_RUN = "0b9a2c11-0000-4000-8000-000000000000"
const [WORD, SHEET] = SCENARIOS

const row = (s, over = {}) => ({ requirement: s.id, title: s.title, scenario: "native-walk", test: s.test, result: "passed", appSha: RC, origin: ORIGIN, mode: "full", reason: null, runId: RUN, ...over })
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
  const lostAt = steps.find((x) => x.name === "lost reply").versionAfter
  // Every receipt is a committed save the browser saw, except the dropped reply.
  const ops = receipts.filter((r) => r !== lostAt).map((version, i) => ({ n: i + 1, kind: "save", status: 200, version, baseVersion: String(Number(version) - 1) }))
  ops.push({ n: ops.length + 1, kind: "reconcile", status: 200, version: lostAt })
  return {
    app: s.app, label: s.app === "wordcraft" ? "Word" : "Sheet", appSha: RC, origin: ORIGIN, runId: RUN,
    chromium: { path: CHROME, version: "156.0.8078.4", headless: false },
    failure: null, violations: [], steps, ops, savesAfterProof: 0, releaseStatus: "ok", pageErrors: [],
    build: { start: { sha: RC, dirty: false, version: "dev", error: null }, end: { sha: RC, dirty: false, version: "dev", error: null } },
    versions: { start: "1", receipts, head: String(v), headAfterReload: String(v), lostVersions: [lostAt] },
    history: { result: "not-enabled", listingStatus: 404, error: HISTORY_NOT_ENABLED, listing: null, truncated: null },
    ...over,
  }
}

const RUN_JSON = { origin: ORIGIN, mode: "full", appSha: RC, appDirty: false, runId: RUN, startedAt: "2026-11-07T07:41:59.585Z" }
const FINISHED = { runId: RUN, status: "passed", startedAt: "2026-11-07T07:41:58.000Z", finishedAt: "2026-11-07T07:45:16.000Z" }
const good = (over = {}) => ({
  playwrightStatus: 0,
  listed: SCENARIOS.map((s) => s.test),
  rows: SCENARIOS.map((s) => row(s)),
  run: { ...RUN_JSON },
  finished: { ...FINISHED },
  reporter: { runId: RUN, rows: 2 },
  rc: RC, origin: ORIGIN, chrome: CHROME, runId: RUN, narrowed: false,
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
  { name: "the real case: two passed walks, two UNMAPPED failed rows, exit 1", input: good({ playwrightStatus: 1, finished: { ...FINISHED, status: "failed" }, rows: SCENARIOS.map(unmapped) }), want: "FAIL", reason: /unexpected row UNMAPPED Native walk Word/ },
  { name: "two passed walk rows next to two UNMAPPED failed rows", input: good({ rows: [...SCENARIOS.map((s) => row(s)), ...SCENARIOS.map(unmapped)] }), want: "FAIL", reason: /unexpected row UNMAPPED/ },
  { name: "output says \"2 passed\" but playwright exited 1", input: { ...good({ playwrightStatus: 1 }), log: "  2 passed (3.4m)\n" }, want: "FAIL", reason: /playwright exited 1/ },
  { name: "playwright was killed (no exit status)", input: good({ playwrightStatus: null }), want: "FAIL", reason: /playwright exited null/ },
  { name: "the run did not finish", input: good({ finished: null }), want: "FAIL", reason: /no finished.json/ },
  { name: "playwright reported the run interrupted", input: good({ finished: { ...FINISHED, status: "interrupted" } }), want: "FAIL", reason: /interrupted/ },
  { name: "run.json is missing", input: good({ run: null }), want: "SETUP", reason: /run.json is missing/ },
  { name: "the origin serves another SHA", input: good({ run: { ...RUN_JSON, appSha: "0".repeat(40) } }), want: "SETUP", reason: /not RC/ },
  { name: "the app build is dirty", input: good({ run: { ...RUN_JSON, appDirty: true } }), want: "SETUP", reason: /not clean/ },
  { name: "the dirty flag is unknown", input: good({ run: { ...RUN_JSON, appDirty: null } }), want: "SETUP", reason: /not clean/ },
  { name: "the run recorded another origin", input: good({ run: { ...RUN_JSON, origin: "https://crewship-dev2.unifylab.cz" } }), want: "SETUP", reason: /run origin/ },
  { name: "the run was in quick mode", input: good({ run: { ...RUN_JSON, mode: "quick" } }), want: "SETUP", reason: /mode quick/ },
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
  // Review of b9a636cdf (Codex), one regression per finding the verdict can catch.
  // P1-1: the proof is taken after the final step, its reload included.
  { name: "P1-1 the final step ended past the last receipt", input: withEvidence("walk.word", (e) => ({ ...e, steps: e.steps.map((s) => (s.name === "final" ? { ...s, versionAfter: String(Number(s.versionAfter) + 1) } : s)) })), want: "FAIL", reason: /the final step ended on v\d+, not the last receipt/ },
  { name: "P1-1 the head after the final reload is not the last receipt", input: withEvidence("walk.word", (e) => ({ ...e, versions: { ...e.versions, headAfterReload: String(Number(e.versions.head) + 1) } })), want: "FAIL", reason: /head after the final reload/ },
  { name: "P1-1 a save after the version proof", input: withEvidence("walk.sheet", (e) => ({ ...e, savesAfterProof: 1 })), want: "FAIL", reason: /saves after the version proof: 1/ },
  { name: "P1-1 no record of saves after the proof", input: withEvidence("walk.sheet", (e) => ({ ...e, savesAfterProof: null })), want: "FAIL", reason: /saves after the version proof: null/ },
  { name: "P1-1 a recorded save missing from the receipts", input: withEvidence("walk.sheet", (e) => ({ ...e, ops: [...e.ops, { kind: "save", status: 200, version: String(Number(e.versions.head) + 1) }] })), want: "FAIL", reason: /recorded saves .* are not the receipts/ },
  { name: "P1-1 no ops in the evidence", input: withEvidence("walk.sheet", (e) => ({ ...e, ops: undefined })), want: "FAIL", reason: /no save ops/ },
  // P1-2: release and page errors are structured and fail the run.
  { name: "P1-2 the release failed", input: withEvidence("walk.word", (e) => ({ ...e, releaseStatus: "failed: TimeoutError" })), want: "FAIL", reason: /release: "failed: TimeoutError"/ },
  { name: "P1-2 the walk never released", input: withEvidence("walk.word", (e) => ({ ...e, releaseStatus: null })), want: "FAIL", reason: /release: null/ },
  { name: "P1-2 a page error", input: withEvidence("walk.sheet", (e) => ({ ...e, pageErrors: [{ at: "t", step: "reload", message: "TypeError: x is undefined" }] })), want: "FAIL", reason: /page error at "reload": TypeError/ },
  { name: "P1-2 no page error list", input: withEvidence("walk.sheet", (e) => ({ ...e, pageErrors: undefined })), want: "FAIL", reason: /no page error list/ },
  // P1-3: a marker the discovery (`--list`) would leave carries no run id.
  { name: "P1-3 finished.json without a run id", input: good({ finished: { status: "passed", startedAt: FINISHED.startedAt, finishedAt: FINISHED.finishedAt } }), want: "FAIL", reason: /finished.json is of run undefined/ },
  { name: "P1-3 finished.json written before the run started", input: good({ finished: { ...FINISHED, startedAt: "2026-11-07T07:30:00.000Z", finishedAt: "2026-11-07T07:30:05.000Z" } }), want: "FAIL", reason: /does not span run.json startedAt/ },
  // P1-4: every artefact belongs to this invocation.
  { name: "P1-4 run.json of another run", input: good({ run: { ...RUN_JSON, runId: OTHER_RUN } }), want: "SETUP", reason: /run.json is of run 0b9a2c11/ },
  { name: "P1-4 finished.json of another run", input: good({ finished: { ...FINISHED, runId: OTHER_RUN } }), want: "FAIL", reason: /finished.json is of run 0b9a2c11/ },
  { name: "P1-4 a row of another run", input: good({ rows: [row(WORD), row(SHEET, { runId: OTHER_RUN })] }), want: "FAIL", reason: /walk.sheet: row is of run 0b9a2c11/ },
  { name: "P1-4 a row without a run id (an older PRD reporter)", input: good({ rows: [row(WORD), row(SHEET, { runId: undefined })] }), want: "FAIL", reason: /walk.sheet: row is of run undefined/ },
  { name: "P1-4 evidence of another run on the same RC and origin", input: withEvidence("walk.word", (e) => ({ ...e, runId: OTHER_RUN })), want: "FAIL", reason: /evidence is of run 0b9a2c11/ },
  { name: "P1-4 no run id given to the verdict", input: good({ runId: undefined }), want: "SETUP", reason: /no run id/ },
  { name: "P1-4 finished.json with unreadable times", input: good({ finished: { ...FINISHED, finishedAt: "soon" } }), want: "FAIL", reason: /does not span/ },
  // P2-1: what the browser says it is counts, not only its path.
  { name: "P2-1 the evidence browser reports the headless shell", input: withEvidence("walk.word", (e) => ({ ...e, chromium: { ...e.chromium, version: "HeadlessChrome/156.0.8078.4" } })), want: "FAIL", reason: /headless shell/ },
  // P2-2: listings compare as arrays of digit strings, never as joined text.
  { name: "P2-2 a listing whose joined text matches but whose elements do not", input: withEvidence("walk.word", (e) => { const h = historyOk(e); const l = h.history.listing; return { ...h, history: { ...h.history, listing: [`${l[0]},${l[1]}`, ...l.slice(2)] } } }), want: "FAIL", reason: /is not the observed/ },
  { name: "P2-2 a listing of numbers instead of version strings", input: withEvidence("walk.word", (e) => { const h = historyOk(e); return { ...h, history: { ...h.history, listing: h.history.listing.map(Number) } } }), want: "FAIL", reason: /is not the observed/ },
  // Codex round 2 on 0aca329e2.
  // P1: Playwright swallows reporter exceptions, so the PRD reporter must prove it completed.
  { name: "R2 no reporter.json (a PRD reporter callback threw)", input: good({ reporter: null }), want: "FAIL", reason: /did not record its completion/ },
  { name: "R2 reporter.json of another run", input: good({ reporter: { runId: OTHER_RUN, rows: 2 } }), want: "FAIL", reason: /reporter.json is of run 0b9a2c11/ },
  { name: "R2 reporter.json counts fewer rows than were read", input: good({ reporter: { runId: RUN, rows: 1 } }), want: "FAIL", reason: /counts 1 rows, 2 were read/ },
  { name: "R2 reporter.json counts fewer rows than listed tests", input: good({ reporter: { runId: RUN, rows: 1 }, rows: [row(WORD)] }), want: "FAIL", reason: /counts 1 rows for 2 listed tests/ },
  // P1: the deployed build is read at the start and the end of each walk.
  { name: "R2 another build at the start of a walk", input: withEvidence("walk.word", (e) => ({ ...e, build: { ...e.build, start: { sha: "1".repeat(40), dirty: false } } })), want: "FAIL", reason: /served 1{40} \(dirty false\) at the start/ },
  { name: "R2 another build at the end of a walk (deployed between Word and Sheet)", input: withEvidence("walk.sheet", (e) => ({ ...e, build: { ...e.build, end: { sha: "1".repeat(40), dirty: false } } })), want: "FAIL", reason: /walk.sheet: the origin served 1{40} .* at the end/ },
  { name: "R2 the build could not be read at the end", input: withEvidence("walk.word", (e) => ({ ...e, build: { ...e.build, end: { sha: "unknown", dirty: null, error: "GET /api/v1/system/version answered 401" } } })), want: "FAIL", reason: /served unknown .* at the end/ },
  { name: "R2 a dirty build at the start", input: withEvidence("walk.word", (e) => ({ ...e, build: { ...e.build, start: { sha: RC, dirty: true } } })), want: "FAIL", reason: /dirty true\) at the start/ },
  { name: "R2 no build record", input: withEvidence("walk.word", (e) => ({ ...e, build: undefined })), want: "FAIL", reason: /no deployed build recorded at the start/ },
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
  const v = verdict(good({ narrowed: true, listed: [WORD.test], rows: [row(WORD)], reporter: { runId: RUN, rows: 1 } }))
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

test("P2-1 a symlink named chrome that resolves to the headless shell is refused, by path or by --version", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "native-walk-chrome-"))
  const shell = path.join(dir, "chrome-headless-shell"), full = path.join(dir, "chrome-real")
  fs.writeFileSync(shell, ""); fs.writeFileSync(full, "")
  fs.symlinkSync(shell, path.join(dir, "chrome"))
  const viaLink = fullChromium(path.join(dir, "chrome"), () => "Chromium 156.0.8078.4")
  assert.equal(viaLink.path, fs.realpathSync(shell))
  assert.match(viaLink.refused, /headless shell/)
  assert.match(fullChromium(full, () => "HeadlessChrome 156.0.8078.4").refused, /reports "HeadlessChrome/)
  assert.match(fullChromium(full, () => null).refused, /--version failed/)
  assert.deepEqual(fullChromium(full, () => "Chromium 156.0.8078.4"), { path: fs.realpathSync(full), version: "Chromium 156.0.8078.4", refused: null })
  fs.rmSync(dir, { recursive: true, force: true })
})
test("P2-2 version lists are equal only element by element", () => {
  assert.ok(sameVersions(["1", "2"], ["1", "2"]))
  assert.ok(!sameVersions(["1,2"], ["1", "2"]))
  assert.ok(!sameVersions([1, 2], ["1", "2"]))
  assert.ok(!sameVersions(["1", "2", "3"], ["1", "2"]))
  assert.ok(!sameVersions(["v1"], ["v1"]))
  assert.ok(!sameVersions(null, []))
})

// Runner regressions (Codex round 2): the job environment and the origin refusal.
function jobEnv(fileText, own) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "native-walk-job-"))
  const file = path.join(dir, "accounts.env")
  fs.writeFileSync(file, fileText, { mode: 0o600 })
  const job = jobArgs(file, own, "env", [])
  const r = spawnSync(job[0], job.slice(1), { encoding: "utf8", env: { PATH: process.env.PATH } })
  fs.rmSync(dir, { recursive: true, force: true })
  assert.equal(r.status, 0, r.stderr)
  return { env: Object.fromEntries(r.stdout.split("\n").filter((l) => l.includes("=")).map((l) => [l.slice(0, l.indexOf("=")), l.slice(l.indexOf("=") + 1)])), argv: job }
}
const own = runEnv({ path: process.env.PATH, home: "/home/walk", browsers: "/b", origin: ORIGIN, output: "/out", chrome: CHROME, runId: RUN })
const PASSWORD = "p w'$x\"y"

test("R2 an account file that exports PRD_TRACE=1 still yields PRD_TRACE=0", () => {
  assert.equal(own.PRD_TRACE, "0")
  const { env } = jobEnv(`PRD_OWNER_EMAIL=o@example.test\nexport PRD_TRACE=1\nPRD_TRACE=1\n`, own)
  assert.equal(env.PRD_TRACE, "0")
})
test("R2 a variable outside the account list does not reach the run, exported or not, and cannot move the run", () => {
  const { env } = jobEnv(`export NOISE=1\nOTHER=2\nexport PRD_ORIGIN=https://evil.test\nexport HOME=/evil\nexport NODE_OPTIONS=--require=/evil.js\nPRD_OWNER_EMAIL=o@example.test\n`, own)
  assert.equal(env.NOISE, undefined)
  assert.equal(env.OTHER, undefined)
  assert.equal(env.NODE_OPTIONS, undefined)
  assert.equal(env.PRD_ORIGIN, ORIGIN)
  assert.equal(env.HOME, "/home/walk")
  assert.equal(env.PRD_OWNER_EMAIL, "o@example.test")
})
test("R2 account values arrive intact and never in an argv", () => {
  const { env, argv } = jobEnv(`PRD_OWNER_EMAIL=o@example.test\nPRD_OWNER_PASSWORD='p w'\\''$x"y'\n`, own)
  assert.equal(env.PRD_OWNER_PASSWORD, PASSWORD)
  assert.ok(!argv.some((a) => a.includes("p w")))
  assert.ok(ACCOUNT_VARS.includes("PRD_OWNER_PASSWORD"))
})
test("R2 a rejected origin with userinfo and a token query prints neither", () => {
  const r = checkOrigin("https://walker:hunter2@crewship-dev3.unifylab.cz/private?token=abc123#frag")
  assert.ok(r.error)
  for (const secret of ["walker", "hunter2", "token", "abc123", "private", "frag"]) assert.ok(!r.error.includes(secret), r.error)
  assert.match(r.error, /https:\/\/crewship-dev3.unifylab.cz/)
  const bad = checkOrigin("not a url hunter2")
  assert.ok(bad.error && !bad.error.includes("hunter2"))
  const ftp = checkOrigin("ftp://walker:hunter2@host")
  assert.ok(ftp.error && !ftp.error.includes("hunter2") && !ftp.error.includes("walker"))
  assert.deepEqual(checkOrigin(`${ORIGIN}/`), { origin: ORIGIN })
})
