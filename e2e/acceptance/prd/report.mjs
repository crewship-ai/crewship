// Pure helpers shared by the PRD acceptance reporter and runner: reading
// result rows, rendering the requirement table and parsing runner arguments.
// Tested with `node --test e2e/acceptance/prd/report.test.mjs`.
import { execFileSync } from "node:child_process"
import * as fs from "node:fs"
import * as path from "node:path"

/**
 * @typedef {"passed" | "failed" | "skipped"} PrdResult
 * @typedef {{
 *   requirement: string, title: string | null, scenario: string, test: string,
 *   result: PrdResult, appSha: string, evidence: string | null, evidenceFiles: string[],
 *   mode: string, origin: string, reason: string | null, durationMs: number, finishedAt: string
 * }} PrdRow
 */

/** @param {string} status Playwright TestResult.status */
export function resultOf(status) {
  if (status === "passed") return "passed"
  if (status === "skipped") return "skipped"
  return "failed"
}

/** File-name-safe slug. @param {string} s */
export function slug(s) {
  return s.toLowerCase().replace(/[^a-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 80) || "x"
}

/** @param {string} rowsDir @returns {PrdRow[]} */
export function readRows(rowsDir) {
  if (!fs.existsSync(rowsDir)) return []
  return fs
    .readdirSync(rowsDir)
    .filter((f) => f.endsWith(".json"))
    .sort()
    .map((f) => JSON.parse(fs.readFileSync(path.join(rowsDir, f), "utf8")))
}

const ORDER = { failed: 0, skipped: 1, passed: 2 }

/** Sort by requirement, then worst result first. @param {PrdRow[]} rows */
export function sortRows(rows) {
  return [...rows].sort(
    (a, b) => a.requirement.localeCompare(b.requirement) || ORDER[a.result] - ORDER[b.result] || a.test.localeCompare(b.test),
  )
}

/** @param {PrdRow[]} rows */
export function summarize(rows) {
  const counts = { passed: 0, failed: 0, skipped: 0 }
  for (const r of rows) counts[r.result]++
  // Zero rows is never success: a suite that ran nothing proves nothing.
  return { ...counts, total: rows.length, ok: rows.length > 0 && counts.failed === 0 }
}

// Escape backslashes before pipes, so a value ending in "\\" cannot turn the
// cell separator that follows it into an escaped literal pipe.
function cell(v) {
  return String(v ?? "")
    .replace(/\\/g, "\\\\")
    .replace(/\|/g, "\\|")
    .replace(/\s+/g, " ")
    .trim()
}

/** Markdown table: requirement | result | app SHA | evidence. @param {PrdRow[]} rows */
export function formatTable(rows) {
  const head = ["Requirement", "Scenario", "Result", "App SHA", "Evidence"]
  const lines = [`| ${head.join(" | ")} |`, `| ${head.map(() => "---").join(" | ")} |`]
  for (const r of sortRows(rows)) {
    const result = r.reason && r.result !== "passed" ? `${r.result} (${r.reason})` : r.result
    const req = r.title ? `${r.requirement}: ${r.title}` : r.requirement
    lines.push(`| ${[req, `${r.scenario} › ${r.test}`, result, r.appSha.slice(0, 12), r.evidence ?? "—"].map(cell).join(" | ")} |`)
  }
  return lines.join("\n")
}

/** Writes summary.json and summary.md next to rows/. @param {string} outputDir @param {PrdRow[]} rows @param {any} run */
export function writeSummary(outputDir, rows, run) {
  const summary = summarize(rows)
  fs.writeFileSync(path.join(outputDir, "summary.json"), JSON.stringify({ run, summary, rows: sortRows(rows) }, null, 2) + "\n")
  const md = [
    `# PRD acceptance — ${run?.mode ?? "?"} mode`,
    "",
    `Origin: ${run?.origin ?? "?"} · App SHA: ${run?.appSha ?? "unknown"} · ${summary.passed} passed, ${summary.failed} failed, ${summary.skipped} skipped`,
    "",
    formatTable(rows),
    "",
  ].join("\n")
  fs.writeFileSync(path.join(outputDir, "summary.md"), md)
  return summary
}

/**
 * Runner arguments. Unknown flags are an error so a typo cannot silently
 * fall back to quick mode. Anything after `--` goes to Playwright verbatim.
 * @param {string[]} argv
 */
export function parseArgs(argv) {
  /** @type {{ originMap: string | null, rc: string | null, manual: string | null, carryover: string | null, merge: string[], required: string | null, origin: string | null, mode: "quick" | "full" | null, output: string | null, grep: string | null, list: boolean, cleanup: boolean, dryRun: boolean, olderThan: number | undefined, help: boolean, passthrough: string[] }} */
  const out = { originMap: null, rc: null, manual: null, carryover: null, merge: [], required: null, origin: null, mode: null, output: null, grep: null, list: false, cleanup: false, dryRun: false, olderThan: undefined, help: false, passthrough: [] }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    const [flag, inline] = a.startsWith("--") && a.includes("=") ? [a.slice(0, a.indexOf("=")), a.slice(a.indexOf("=") + 1)] : [a, null]
    const take = () => {
      if (inline !== null) return inline
      const v = argv[++i]
      if (v === undefined || v.startsWith("--")) throw new Error(`${flag} needs a value`)
      return v
    }
    switch (flag) {
      case "--":
        out.passthrough = argv.slice(i + 1)
        i = argv.length
        break
      case "--origin":
        out.origin = take()
        break
      case "--quick":
      case "--full": {
        const m = flag === "--quick" ? "quick" : "full"
        if (out.mode && out.mode !== m) throw new Error("--quick and --full are mutually exclusive")
        out.mode = m
        break
      }
      case "--origin-map":
        out.originMap = take()
        break
      case "--rc":
        out.rc = take()
        break
      case "--manual":
        out.manual = take()
        break
      case "--carryover":
        out.carryover = take()
        break
      case "--merge":
        out.merge.push(take())
        break
      case "--required":
        out.required = take()
        break
      case "--output":
        out.output = take()
        break
      case "--grep":
        out.grep = take()
        break
      case "--list":
        out.list = true
        break
      case "--cleanup":
        out.cleanup = true
        break
      case "--dry-run":
        out.dryRun = true
        break
      case "--older-than": {
        const n = Number(take())
        if (!Number.isFinite(n) || n < 0) throw new Error("--older-than needs a number of minutes")
        out.olderThan = n
        break
      }
      case "-h":
      case "--help":
        out.help = true
        break
      default:
        throw new Error(`unknown argument ${a}`)
    }
  }
  // --origin without a mode means full mode against that deployment.
  if (!out.mode) out.mode = out.origin ? "full" : "quick"
  return out
}

// ---- Required-ID gate (docs/development/prd-acceptance.md "RC gate") -------

export const DEFAULT_REQUIRED = path.join(path.dirname(new URL(import.meta.url).pathname), "required-ids.json")

/**
 * @typedef {{ id: string, origin: string, allowSkipped?: boolean, allowManual?: boolean, manualChecks?: string[], alwaysRerun?: boolean }} RequiredRow
 * @typedef {{ origins: Record<string, string>, sharedPaths?: string[], rows: RequiredRow[] }} Required
 * @typedef {{ id: string, fromSha: string, paths: string[], engines?: { from: string, rc: string } }} Carryover
 */

/** "craft=https://a,artifacts=https://b" (flag or PRD_RC_ORIGINS) to a map. @param {string | null | undefined} spec */
export function parseOriginMap(spec) {
  /** @type {Record<string, string>} */
  const map = {}
  for (const part of String(spec ?? "").split(",").map((s) => s.trim()).filter(Boolean)) {
    const i = part.indexOf("=")
    if (i <= 0 || i === part.length - 1) throw new Error(`origin map entry "${part}" must be key=url`)
    map[part.slice(0, i)] = part.slice(i + 1)
  }
  return map
}

/**
 * @param {string} [file] @param {string | null} [originMap] "craft=URL,artifacts=URL"; defaults to $PRD_RC_ORIGINS
 * @returns {Required}
 */
export function loadRequired(file = DEFAULT_REQUIRED, originMap = null) {
  const doc = JSON.parse(fs.readFileSync(file, "utf8"))
  if (!doc || !Array.isArray(doc.rows) || doc.rows.length === 0) throw new Error(`${file}: expected { rows[] } with at least one row`)
  doc.origins = { ...(doc.origins ?? {}), ...parseOriginMap(originMap ?? process.env.PRD_RC_ORIGINS) }
  for (const r of doc.rows) {
    if (!r.id) throw new Error(`${file}: row without id`)
    if (!doc.origins[r.origin]) throw new Error(`origin "${r.origin}" of ${r.id} is not configured: pass --origin-map ${r.origin}=<url>,... or set PRD_RC_ORIGINS`)
  }
  return doc
}

const normOrigin = (o) => String(o ?? "").replace(/\/+$/, "").toLowerCase()
const shaMatches = (a, b) => {
  const x = String(a ?? "").toLowerCase()
  const y = String(b ?? "").toLowerCase()
  return x.length >= 7 && y.length >= 7 && (x.startsWith(y) || y.startsWith(x))
}
const time = (r) => Date.parse(r.finishedAt) || 0

/**
 * Manual rows ({ id, result, appSha, evidence, recordedAt, checks }) as ordinary rows
 * with mode "manual". Origin defaults to the row's canonical origin.
 * @param {any[]} manual @param {Required} required
 */
export function manualToRows(manual, required) {
  if (!Array.isArray(manual)) throw new Error("manual rows must be a JSON array")
  return manual.map((m) => {
    if (!m || !m.id) throw new Error("manual row needs an id")
    if (!["passed", "failed", "skipped"].includes(m.result)) throw new Error(`manual row ${m.id}: result must be passed|failed|skipped`)
    const req = required.rows.find((r) => r.id === m.id)
    return {
      requirement: m.id, title: null, scenario: "manual", test: "manual walkthrough", result: m.result,
      appSha: m.appSha ?? "unknown", evidence: m.evidence || null, evidenceFiles: m.evidence ? [m.evidence] : [],
      mode: "manual", origin: m.origin ?? (req ? required.origins[req.origin] : "unknown"),
      reason: null, durationMs: 0, finishedAt: m.recordedAt ?? "", checks: m.checks ?? {},
    }
  })
}

/** `git diff --stat from to -- paths`; empty output means the paths did not change. */
export function gitDiffStat(from, to, paths) {
  return execFileSync("git", ["diff", "--stat", from, to, "--", ...paths], { encoding: "utf8" })
}

/**
 * Checks merged rows against the required list. Per ID the latest row on the
 * RC SHA wins (ties: worst result); rows on other SHAs only count when no row
 * on the RC exists, and then fail as "appSha" unless a carryover record
 * covers them. A carried row keeps the SHA it was proven on and gets
 * `carriedFrom` with the diff against the RC over its scenario paths plus the
 * shared paths (authorization, saving, migrations, runtime config); the diff
 * must be empty and the engine pins equal. Save-class rows never carry over.
 * @param {PrdRow[]} rows @param {Required} required
 * @param {{ rc?: string | null, carryover?: Carryover[], diff?: (from: string, to: string, paths: string[]) => string }} opts
 */
export function evaluateRequired(rows, required, opts) {
  const rc = opts?.rc
  if (!rc) throw new Error("--rc <SHA> is required for the RC gate")
  /** @type {{ id: string, kind: string, detail: string }[]} */
  const problems = []
  const checked = []
  for (const req of required.rows) {
    const mine = rows.filter((r) => r.requirement === req.id)
    const onRc = mine.filter((r) => shaMatches(r.appSha, rc))
    const pool = onRc.length ? onRc : mine
    let row = [...pool].sort((a, b) => time(b) - time(a) || ORDER[a.result] - ORDER[b.result])[0]
    const fail = (kind, detail) => problems.push({ id: req.id, kind, detail })
    if (!row) {
      fail("missing", "no result row recorded")
      continue
    }
    const carry = onRc.length ? null : (opts.carryover ?? []).find((c) => c.id === req.id)
    let checkedRow = row
    if (!shaMatches(row.appSha, rc) && carry) {
      const from = mine.filter((r) => shaMatches(r.appSha, carry.fromSha)).sort((a, b) => time(b) - time(a) || ORDER[a.result] - ORDER[b.result])[0]
      if (!from) fail("appSha", `no row on carryover SHA ${String(carry.fromSha).slice(0, 12)}`)
      else {
        const paths = [...(carry.paths ?? []), ...(required.sharedPaths ?? [])]
        const why = req.alwaysRerun ? "save-class row must rerun on the RC"
          : !(carry.paths ?? []).length ? "carryover lists no scenario paths"
          : !carry.engines?.from || carry.engines.from !== carry.engines.rc ? "engine pins missing or different"
          : null
        const diffStat = why ? null : (opts.diff ?? gitDiffStat)(carry.fromSha, rc, paths)
        checkedRow = { ...from, carriedFrom: { fromSha: carry.fromSha, rc, paths, engines: carry.engines ?? null, diffStat } }
        if (why) fail("carryover", why)
        else if (diffStat.trim()) fail("carryover", `paths changed since ${carry.fromSha.slice(0, 12)}: ${diffStat.trim().split("\n").pop()}`)
      }
    } else if (!shaMatches(row.appSha, rc)) fail("appSha", `${String(row.appSha).slice(0, 12)} is not RC ${rc.slice(0, 12)}`)
    checked.push(checkedRow)
    const want = required.origins[req.origin]
    if (normOrigin(row.origin) !== normOrigin(want)) fail("origin", `${row.origin} is not ${want}`)
    row = checkedRow
    if (row.result === "skipped" && req.allowSkipped) {
      // Pre-approved skip (native editor servers only); nothing else to check.
    } else if (row.result !== "passed") {
      fail("result", row.result + (row.reason ? ` (${row.reason})` : ""))
    }
    if (row.mode === "manual") {
      if (!req.allowManual) fail("mode", "manual is not allowed for this row")
      else if (!String(row.evidence ?? "").trim()) fail("evidence", "manual row has no link to the recording")
      else {
        const missing = (req.manualChecks ?? []).filter((c) => row.checks?.[c] !== true)
        if (missing.length) fail("evidence", `manual record lacks checks: ${missing.join(", ")}`)
      }
    } else if (row.mode !== "full") {
      fail("mode", `mode ${row.mode}, need full`)
    }
  }
  return { ok: problems.length === 0, rc, required: required.rows.length, problems, rows: checked }
}

/** Plain-text gate report. @param {ReturnType<typeof evaluateRequired>} r */
export function formatGate(r) {
  const head = `RC gate for ${r.rc}: ${r.ok ? "PASS" : "FAIL"} (${r.required - new Set(r.problems.map((p) => p.id)).size}/${r.required} required IDs satisfied)`
  if (r.ok) return head
  return [head, ...r.problems.map((p) => `  ${p.kind.padEnd(8)} ${p.id}: ${p.detail}`)].join("\n")
}

/** Reads rows from a run directory, a summary.json or a rows/ directory. @param {string} p */
export function readRunRows(p) {
  if (fs.statSync(p).isFile()) return JSON.parse(fs.readFileSync(p, "utf8")).rows ?? []
  const summary = path.join(p, "summary.json")
  if (fs.existsSync(summary)) return JSON.parse(fs.readFileSync(summary, "utf8")).rows ?? []
  return readRows(fs.existsSync(path.join(p, "rows")) ? path.join(p, "rows") : p)
}

/** Merges runs and the optional manual file, evaluates, returns { rows, result }. */
export function gateFromInputs({ rc, required: requiredFile, originMap, runs, manual, carryover }) {
  const required = loadRequired(requiredFile ?? DEFAULT_REQUIRED, originMap)
  const rows = runs.flatMap(readRunRows)
  if (manual) rows.push(...manualToRows(JSON.parse(fs.readFileSync(manual, "utf8")), required))
  const carried = carryover ? JSON.parse(fs.readFileSync(carryover, "utf8")) : []
  return { rows, result: evaluateRequired(rows, required, { rc, carryover: carried }) }
}

// Standalone: node report.mjs --rc SHA --merge DIR [--merge DIR ...] [--manual manual-rows.json] [--carryover carryover.json] [--required FILE] [--output DIR]
if (process.argv[1] && path.resolve(process.argv[1]) === new URL(import.meta.url).pathname) {
  try {
    const a = parseArgs(process.argv.slice(2))
    if (!a.rc || a.merge.length === 0) throw new Error("usage: report.mjs --rc <SHA> --merge <run dir|summary.json> [--merge ...] [--manual file] --origin-map craft=URL,artifacts=URL [--output DIR]")
    const { result } = gateFromInputs({ rc: a.rc, required: a.required, originMap: a.originMap, runs: a.merge, manual: a.manual, carryover: a.carryover })
    console.log(formatGate(result))
    if (a.output) {
      fs.mkdirSync(a.output, { recursive: true })
      fs.writeFileSync(path.join(a.output, "rc-gate.json"), JSON.stringify(result, null, 2) + "\n")
      fs.writeFileSync(path.join(a.output, "rc-gate.md"), `# RC gate ${a.rc}\n\n\`\`\`\n${formatGate(result)}\n\`\`\`\n\n${formatTable(result.rows)}\n`)
    }
    process.exit(result.ok ? 0 : 1)
  } catch (err) {
    console.error(`report.mjs: ${err.message}`)
    process.exit(2)
  }
}
