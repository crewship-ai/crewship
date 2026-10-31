// Pure helpers shared by the PRD acceptance reporter and runner: reading
// result rows, rendering the requirement table and parsing runner arguments.
// Tested with `node --test e2e/acceptance/prd/report.test.mjs`.
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
  /** @type {{ origin: string | null, mode: "quick" | "full" | null, output: string | null, grep: string | null, list: boolean, cleanup: boolean, dryRun: boolean, olderThan: number | undefined, help: boolean, passthrough: string[] }} */
  const out = { origin: null, mode: null, output: null, grep: null, list: false, cleanup: false, dryRun: false, olderThan: undefined, help: false, passthrough: [] }
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
