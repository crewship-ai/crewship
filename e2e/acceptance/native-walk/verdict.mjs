// When a native walk run counts. Pure apart from loadEvidence; tested with
// `node --test e2e/acceptance/native-walk/verdict.test.mjs`.
//
// The verdict never reads Playwright's text output: "2 passed" with exit 1, two
// passed walks next to unmapped failed rows, or a run against another build is
// not a pass. Every reason a run is not a PASS is listed in `reasons`.
import * as fs from "node:fs"
import * as path from "node:path"

export const EXIT = { pass: 0, failed: 1, setup: 2, partial: 3 }

/** The 404 body of GET …/craft/document/versions while the craft_version_history flag is off. */
export const HISTORY_NOT_ENABLED = "native version history is not enabled"

export const SPEC_FILE = "native-walk.acceptance.ts"
export const EVIDENCE_FILE = "walk.json"

const AFTER_INPUT = ["autosave", "ctrl+s with a change", "ctrl+s without a change", "reload", "keep working after the reload", "lost reply", "next save after the lost reply", "final"]

/** The two walks, in the order they run. The spec takes its titles and ids from here. */
export const SCENARIOS = [
  {
    id: "walk.word",
    app: "wordcraft",
    test: "Native walk Word",
    title: "A short Word working cycle in full Chromium: typing, ribbon styles, autosave, ctrl+s, reload, a lost save reply and release, without pressing \"Ověřit výsledek\"",
    steps: ["open and take the editor", "home tab", "type text", "enter, more text", "heading from the ribbon", "style from the ribbon", "bold", ...AFTER_INPUT],
  },
  {
    id: "walk.sheet",
    app: "gridcraft",
    test: "Native walk Sheet",
    title: "A short Sheet working cycle in full Chromium: cell entry, autosave, ctrl+s, reload, a lost save reply and release, without pressing \"Ověřit výsledek\"",
    steps: ["open and take the editor", "type in a cell, enter", "next cell after enter", "third cell after enter", ...AFTER_INPUT],
  },
]

/** Same rule as e2e/acceptance/prd/report.mjs slug(), which names the evidence directory. */
const slug = (s) => s.toLowerCase().replace(/[^a-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 80) || "x"

/** Evidence of one walk, relative to the output directory. */
export const evidencePath = (scenario) => path.join("evidence", slug(SPEC_FILE.replace(/\.acceptance\.ts$/, "")), slug(scenario.test), EVIDENCE_FILE)

/** id -> parsed walk.json, null when absent, { unreadable } when it does not parse. */
export function loadEvidence(outputDir) {
  return Object.fromEntries(SCENARIOS.map((s) => {
    const file = path.join(outputDir, evidencePath(s))
    if (!fs.existsSync(file)) return [s.id, null]
    try {
      return [s.id, JSON.parse(fs.readFileSync(file, "utf8"))]
    } catch (err) {
      return [s.id, { unreadable: String(err?.message ?? err) }]
    }
  }))
}

/** Test titles from `playwright test --list`. */
export function parseListed(stdout) {
  return String(stdout ?? "").split("\n").filter((line) => line.includes(SPEC_FILE) && line.includes(" › ")).map((line) => line.slice(line.lastIndexOf(" › ") + 3).trim())
}

/** The headless shell has no real compositor path to measure; only full Chromium counts.
 * Pass the resolved path (fs.realpathSync) and, where known, the `--version` output. */
export function isHeadlessShell(executable, versionOutput = "") {
  return /headless[_-]?shell/i.test(String(executable ?? "")) || /headless/i.test(String(versionOutput ?? ""))
}

/**
 * The Chromium a walk may use: the symlink-resolved file, refused when it is the headless
 * shell by path or by what `--version` prints. `version(path)` returns that output or null.
 * @returns {{ path: string, version: string | null, refused: string | null }}
 */
export function fullChromium(found, version) {
  const real = fs.realpathSync(found)
  if (isHeadlessShell(real)) return { path: real, version: null, refused: `${real} is the headless shell; the walk needs full Chromium` }
  const out = version(real)
  if (!out) return { path: real, version: null, refused: `${real} --version failed` }
  if (isHeadlessShell(real, out)) return { path: real, version: out, refused: `${real} reports "${out}", the headless shell; the walk needs full Chromium` }
  return { path: real, version: out, refused: null }
}

/** Element-wise equal version lists: same length, every element a string of digits. */
export function sameVersions(a, b) {
  return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((v, i) => typeof v === "string" && /^\d+$/.test(v) && v === b[i])
}

const normOrigin = (o) => String(o ?? "").replace(/\/+$/, "").toLowerCase()
const big = (v) => { try { return typeof v === "string" && /^\d+$/.test(v) ? BigInt(v) : null } catch { return null } }

/**
 * Version rules recomputed from the evidence, independent of the history listing:
 * the save receipts are exactly the committed saves in the recorded ops plus the
 * dropped reply, they run from the starting head +1 without a gap or a repeat, the
 * head after the final step and after its reload is the last receipt, nothing saved
 * after the proof, ctrl+s without a change keeps the version and the lost reply
 * makes exactly one.
 */
export function versionProblems(evidence) {
  const v = evidence?.versions
  if (!v || typeof v !== "object") return ["no version record in the evidence"]
  const problems = []
  const start = big(v.start), head = big(v.head)
  const receipts = Array.isArray(v.receipts) ? v.receipts.map(big) : null
  if (start === null) problems.push(`starting head ${JSON.stringify(v.start)} is not a version`)
  if (head === null) problems.push(`final head ${JSON.stringify(v.head)} is not a version`)
  if (!receipts || receipts.some((r) => r === null)) problems.push(`save receipts ${JSON.stringify(v.receipts)} are not versions`)
  else if (!receipts.length) problems.push("no save receipt")
  else if (start !== null) {
    const bad = receipts.findIndex((r, i) => r !== start + BigInt(i) + 1n)
    if (bad >= 0) problems.push(`save receipts ${v.receipts.join(",")} are not contiguous from v${v.start}: receipt ${bad + 1} is v${v.receipts[bad]}, expected v${start + BigInt(bad) + 1n}`)
    if (head !== null && receipts.at(-1) !== head) problems.push(`last receipt v${v.receipts.at(-1)} is not the final head v${v.head}`)
    if (v.headAfterReload !== v.receipts.at(-1)) problems.push(`head after the final reload v${v.headAfterReload} is not the last receipt v${v.receipts.at(-1)}`)
  }
  const ops = Array.isArray(evidence.ops) ? evidence.ops : null
  if (!ops) problems.push("evidence records no save ops")
  else if (receipts && !receipts.some((r) => r === null)) {
    const fromOps = [...ops.filter((o) => o?.kind === "save" && o?.status === 200 && o?.version !== undefined && o?.version !== null).map((o) => String(o.version)), ...(Array.isArray(v.lostVersions) ? v.lostVersions.map(String) : [])]
      .sort((a, b) => (big(a) ?? -1n) < (big(b) ?? -1n) ? -1 : (big(a) ?? -1n) > (big(b) ?? -1n) ? 1 : 0)
    if (!sameVersions(fromOps, v.receipts)) problems.push(`the recorded saves ${fromOps.join(",")} are not the receipts ${v.receipts.join(",")}`)
  }
  if (evidence.savesAfterProof !== 0) problems.push(`saves after the version proof: ${JSON.stringify(evidence.savesAfterProof)}`)
  const final = Array.isArray(evidence.steps) ? evidence.steps.find((s) => s?.name === "final") : undefined
  if (!final || final.versionAfter !== v.receipts?.at?.(-1)) problems.push(`the final step ended on v${final?.versionAfter}, not the last receipt v${Array.isArray(v.receipts) ? v.receipts.at(-1) : "?"}`)
  const step = (name) => (Array.isArray(evidence.steps) ? evidence.steps.find((s) => s?.name === name) : undefined)
  const unchanged = step("ctrl+s without a change")
  if (unchanged && (big(unchanged.versionBefore) === null || unchanged.versionBefore !== unchanged.versionAfter)) problems.push(`ctrl+s without a change went from v${unchanged.versionBefore} to v${unchanged.versionAfter}`)
  const lost = step("lost reply")
  const lb = big(lost?.versionBefore), la = big(lost?.versionAfter)
  if (lost && (lb === null || la === null || la !== lb + 1n)) problems.push(`the lost reply went from v${lost.versionBefore} to v${lost.versionAfter}, not exactly one version`)
  return problems
}

/**
 * The history listing: 200 must list exactly the observed versions; a 404 with
 * the flag-off message is NOT VERIFIED (the flag is not accepted and stays off);
 * anything else fails. Derived from the recorded status and body, not from the
 * spec's own label.
 */
export function historyCheck(evidence) {
  const h = evidence?.history
  if (!h || typeof h !== "object") return { problem: "no history record in the evidence" }
  if (h.listingStatus === 404 && h.error === HISTORY_NOT_ENABLED) return { notVerified: `version history listing answered 404 "${HISTORY_NOT_ENABLED}" (flag craft_version_history off): the listing was not compared` }
  if (h.listingStatus !== 200) return { problem: `version history listing answered ${h.listingStatus}: ${String(h.error ?? "").slice(0, 160)}` }
  const v = evidence.versions ?? {}
  const observed = [v.start, ...(Array.isArray(v.receipts) ? v.receipts : [])]
  if (h.truncated !== false) return { problem: `version history listing truncated=${JSON.stringify(h.truncated)}` }
  if (!sameVersions(h.listing, observed)) return { problem: `version history listing ${JSON.stringify(h.listing)} is not the observed ${observed.join(",")}` }
  return {}
}

/** Everything one walk's evidence must show. */
export function checkEvidence(scenario, evidence, { rc, origin, chrome, runId }) {
  if (evidence === null || evidence === undefined) return { problems: [`no evidence file ${evidencePath(scenario)}`], notVerified: [] }
  if (evidence.unreadable) return { problems: [`unreadable evidence ${evidencePath(scenario)}: ${evidence.unreadable}`], notVerified: [] }
  const problems = []
  if (evidence.app !== scenario.app) problems.push(`evidence is for ${evidence.app}, not ${scenario.app}`)
  if (evidence.appSha !== rc) problems.push(`evidence app SHA ${evidence.appSha} is not the RC`)
  if (!runId || evidence.runId !== runId) problems.push(`evidence is of run ${evidence.runId}, not ${runId}`)
  // Read from the origin at the start and the end of this walk, not only in global setup.
  for (const when of ["start", "end"]) {
    const b = evidence.build?.[when]
    if (!b) problems.push(`no deployed build recorded at the ${when} of the walk`)
    else if (b.sha !== rc || b.dirty !== false) problems.push(`the origin served ${b.sha} (dirty ${JSON.stringify(b.dirty)}) at the ${when} of the walk, not RC ${rc} clean`)
  }
  if (normOrigin(evidence.origin) !== normOrigin(origin)) problems.push(`evidence origin ${evidence.origin} is not ${origin}`)
  const c = evidence.chromium ?? {}
  if (!c.version) problems.push("evidence records no Chromium version")
  if (c.headless !== false) problems.push("the walk did not record a headed browser")
  if (isHeadlessShell(c.path, c.version)) problems.push(`the walk ran in the headless shell ${c.path}`)
  if (chrome && c.path !== chrome) problems.push(`the walk ran ${c.path}, not ${chrome}`)
  if (evidence.failure !== null) problems.push(`walk aborted: ${String(evidence.failure).slice(0, 200)}`)
  if (evidence.releaseStatus !== "ok") problems.push(`release: ${JSON.stringify(evidence.releaseStatus)}`)
  if (!Array.isArray(evidence.pageErrors)) problems.push("evidence records no page error list")
  else for (const e of evidence.pageErrors) problems.push(`page error at "${e?.step}": ${String(e?.message).slice(0, 160)}`)
  if (!Array.isArray(evidence.violations)) problems.push("evidence records no violation list")
  else for (const v of evidence.violations) problems.push(`violation at "${v?.step}": ${v?.rule}`)
  const steps = Array.isArray(evidence.steps) ? evidence.steps : []
  for (const name of scenario.steps) {
    const n = steps.filter((s) => s?.name === name).length
    if (n !== 1) problems.push(`step "${name}" recorded ${n} times`)
  }
  for (const s of steps) {
    if (!scenario.steps.includes(s?.name)) problems.push(`unexpected step "${s?.name}"`)
    if (s?.verifyPresses !== 0) problems.push(`step "${s?.name}": "Ověřit výsledek" pressed ${JSON.stringify(s?.verifyPresses)} times`)
  }
  problems.push(...versionProblems(evidence))
  const history = historyCheck(evidence)
  if (history.problem) problems.push(history.problem)
  return { problems, notVerified: history.notVerified ? [history.notVerified] : [] }
}

/**
 * @param {{
 *   playwrightStatus: number | null, listed: string[], rows: Array<Record<string, any>>,
 *   run: Record<string, any> | null, finished: Record<string, any> | null, reporter: Record<string, any> | null,
 *   rc: string, origin: string, chrome?: string, runId: string, narrowed: boolean,
 *   evidence: Record<string, any>,
 * }} input
 * @returns {{ exit: number, verdict: "PASS" | "FAIL" | "SETUP" | "PARTIAL", reasons: string[], notVerified: { id: string, item: string }[], expected: string[] }}
 */
export function verdict({ playwrightStatus, listed, rows, run, finished, reporter, rc, origin, chrome, runId, narrowed, evidence }) {
  const setup = [], failed = [], notVerified = []
  if (!/^[0-9a-f]{40}$/.test(String(rc ?? ""))) setup.push(`--rc ${JSON.stringify(rc)} is not a full 40-character SHA`)
  if (!runId) setup.push("no run id")
  if (!run) setup.push("run.json is missing: global setup did not record the run")
  else {
    if (run.appSha !== rc) setup.push(`the origin serves app SHA ${run.appSha}, not RC ${rc}`)
    if (run.appDirty !== false) setup.push(`the app build is not clean (appDirty ${JSON.stringify(run.appDirty)})`)
    if (normOrigin(run.origin) !== normOrigin(origin)) setup.push(`run origin ${run.origin} is not ${origin}`)
    if (run.mode !== "full") setup.push(`run mode ${run.mode}, need full`)
    if (run.runId !== runId) setup.push(`run.json is of run ${run.runId}, not ${runId}`)
  }
  if (playwrightStatus !== 0) failed.push(`playwright exited ${playwrightStatus}`)
  if (!finished) failed.push("the run did not finish: no finished.json")
  else {
    if (finished.status !== "passed") failed.push(`playwright reported the run ${finished.status}`)
    if (!runId || finished.runId !== runId) failed.push(`finished.json is of run ${finished.runId}, not ${runId}`)
    // Playwright starts before global setup records run.json and ends after it.
    const began = Date.parse(finished.startedAt), ended = Date.parse(finished.finishedAt), setupAt = Date.parse(run?.startedAt)
    if (!(began <= setupAt && setupAt <= ended)) failed.push(`finished.json (${finished.startedAt} to ${finished.finishedAt}) does not span run.json startedAt ${run?.startedAt}`)
  }

  const titles = Array.isArray(listed) ? listed : []
  // The PRD reporter's own completion: Playwright swallows reporter exceptions.
  if (!reporter) failed.push("the PRD reporter did not record its completion: no reporter.json")
  else {
    if (!runId || reporter.runId !== runId) failed.push(`reporter.json is of run ${reporter.runId}, not ${runId}`)
    if (reporter.rows !== (rows ?? []).length) failed.push(`reporter.json counts ${reporter.rows} rows, ${(rows ?? []).length} were read`)
    if (reporter.rows !== titles.length) failed.push(`reporter.json counts ${reporter.rows} rows for ${titles.length} listed tests`)
  }
  for (const t of titles) if (!SCENARIOS.some((s) => s.test === t)) failed.push(`unexpected test listed: ${t}`)
  const expected = SCENARIOS.filter((s) => (narrowed ? titles.includes(s.test) : true))
  if (!expected.length) failed.push("no test was listed")
  for (const s of expected) {
    const n = titles.filter((t) => t === s.test).length
    if (n !== 1) failed.push(`${s.id}: "${s.test}" listed ${n} times`)
  }

  const ids = new Set(expected.map((s) => s.id))
  for (const r of rows ?? []) {
    if (!ids.has(r?.requirement)) failed.push(`unexpected row ${r?.requirement} (${r?.result}${r?.reason ? `: ${r.reason}` : ""})`)
  }
  const skipped = (rows ?? []).filter((r) => r?.result === "skipped").length
  if (skipped) failed.push(`${skipped} skipped`)
  for (const s of expected) {
    const mine = (rows ?? []).filter((r) => r?.requirement === s.id)
    if (mine.length !== 1) failed.push(`${s.id}: left ${mine.length} result rows, need exactly one`)
    for (const r of mine) {
      if (r.result !== "passed") failed.push(`${s.id}: ${r.result}${r.reason ? ` (${r.reason})` : ""}`)
      if (r.test !== s.test) failed.push(`${s.id}: row is for test "${r.test}"`)
      if (r.appSha !== rc) failed.push(`${s.id}: row app SHA ${r.appSha} is not the RC`)
      if (normOrigin(r.origin) !== normOrigin(origin)) failed.push(`${s.id}: row origin ${r.origin} is not ${origin}`)
      if (r.mode !== "full") failed.push(`${s.id}: row mode ${r.mode}, need full`)
      if (!runId || r.runId !== runId) failed.push(`${s.id}: row is of run ${r.runId}, not ${runId}`)
    }
    const e = checkEvidence(s, evidence?.[s.id], { rc, origin, chrome, runId })
    failed.push(...e.problems.map((p) => `${s.id}: ${p}`))
    notVerified.push(...e.notVerified.map((item) => ({ id: s.id, item })))
  }

  const expectedIds = expected.map((s) => s.id)
  if (setup.length) return { exit: EXIT.setup, verdict: "SETUP", reasons: [...setup, ...failed], notVerified, expected: expectedIds }
  if (failed.length) return { exit: EXIT.failed, verdict: "FAIL", reasons: failed, notVerified, expected: expectedIds }
  if (narrowed) return { exit: EXIT.partial, verdict: "PARTIAL", reasons: [`only ${expectedIds.join(", ")} ran`], notVerified, expected: expectedIds }
  return { exit: EXIT.pass, verdict: "PASS", reasons: [], notVerified, expected: expectedIds }
}
