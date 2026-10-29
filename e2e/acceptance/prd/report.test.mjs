// node --test e2e/acceptance/prd/report.test.mjs
import assert from "node:assert/strict"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { test } from "node:test"
import { formatTable, parseArgs, readRows, resultOf, summarize, writeSummary } from "./report.mjs"

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
