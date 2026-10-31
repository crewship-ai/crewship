#!/usr/bin/env node
// PRD acceptance runner: `pnpm acceptance:prd --origin <url>` (full mode) or
// `pnpm acceptance:prd --quick`. Runs every scenario in this directory and
// prints requirement | result | app SHA | evidence. Exit 0 only when at least
// one row was recorded and none failed. Docs: docs/development/prd-acceptance.md
import { spawnSync } from "node:child_process"
import * as fs from "node:fs"
import { createRequire } from "node:module"
import * as path from "node:path"
import { fileURLToPath } from "node:url"
import { cleanup } from "./cleanup.mjs"
import { formatTable, parseArgs, readRows, summarize, writeSummary } from "./report.mjs"

const HELP = `Usage: pnpm acceptance:prd [--origin URL] [--quick|--full] [--output DIR] [--grep RE] [--list] [-- <playwright args>]

  --origin URL   origin under test; implies --full unless --quick is given
  --quick        CI mode against a local test server (default origin $PLAYWRIGHT_BASE_URL or http://localhost:8080);
                 scenarios needing a real agent or extra accounts are skipped
  --full         full mode against a deployed origin (PRD_ORIGIN or --origin required)
  --output DIR   results directory (default test-results/prd-acceptance/<mode>-<timestamp>)
  --grep RE      only scenarios/tests matching RE
  --list         list the scenarios without running them
  --cleanup      delete leftovers of earlier runs and exit: workspaces prd-qa-* named "PRD acceptance …",
                 and pages/Designs/Projects whose slug starts with prd- AND name with "PRD " (in the
                 workspaces of PRD_AGENT_WORKSPACE_ID / PRD_WORKSPACE_ID). Nothing else is touched.
  --dry-run      with --cleanup: only list what would be deleted
  --older-than M with --cleanup: leave objects younger than M minutes (default 60)

Accounts come only from the environment: PRD_OWNER_EMAIL/PRD_OWNER_PASSWORD (quick mode falls
back to E2E_EMAIL/E2E_PASSWORD), optional PRD_MEMBER_*, PRD_READER_*, PRD_WORKSPACE_ID,
PRD_AGENT_SLUG and PRD_AGENT_WORKSPACE_ID.`

const here = path.dirname(fileURLToPath(import.meta.url))
const root = path.resolve(here, "../../..")

let args
try {
  args = parseArgs(process.argv.slice(2))
} catch (err) {
  console.error(`acceptance:prd: ${err.message}\n\n${HELP}`)
  process.exit(2)
}
if (args.help) {
  console.log(HELP)
  process.exit(0)
}

const origin = args.origin ?? process.env.PRD_ORIGIN ?? null
if (args.mode === "full" && !origin) {
  console.error("acceptance:prd: full mode needs --origin <url> or PRD_ORIGIN")
  process.exit(2)
}
if (args.cleanup) {
  const owner = { email: process.env.PRD_OWNER_EMAIL, password: process.env.PRD_OWNER_PASSWORD }
  if (!origin || !owner.email || !owner.password) {
    console.error("acceptance:prd: --cleanup needs --origin (or PRD_ORIGIN) and PRD_OWNER_EMAIL/PRD_OWNER_PASSWORD")
    process.exit(2)
  }
  const workspaceIds = [process.env.PRD_AGENT_WORKSPACE_ID, process.env.PRD_WORKSPACE_ID].filter(Boolean)
  console.log(`${args.dryRun ? "Dry run: " : ""}cleaning harness leftovers on ${origin}`)
  const r = await cleanup({ origin, owner, workspaceIds, olderThanMin: args.olderThan, dryRun: args.dryRun })
  console.log(`${r.workspacesDeleted} workspaces and ${r.pagesDeleted} pages deleted, ${r.skipped.length} skipped, ${r.failed.length} failed`)
  for (const f of r.failed) console.error(`  failed ${f.kind} ${f.id}: HTTP ${f.status}`)
  process.exit(r.failed.length ? 1 : 0)
}
const stamp = new Date().toISOString().replace(/[:.]/g, "-")
const outputDir = path.resolve(args.output ?? process.env.PRD_OUTPUT_DIR ?? path.join(root, "test-results", "prd-acceptance", `${args.mode}-${stamp}`))
fs.mkdirSync(outputDir, { recursive: true })

const env = { ...process.env, PRD_MODE: args.mode, PRD_OUTPUT_DIR: outputDir }
if (origin) env.PRD_ORIGIN = origin

const cli = createRequire(path.join(root, "package.json")).resolve("@playwright/test/cli")
const pwArgs = [cli, "test", "--config", path.join(root, "playwright.prd-acceptance.config.ts")]
if (args.grep) pwArgs.push("--grep", args.grep)
if (args.list) pwArgs.push("--list")
pwArgs.push(...args.passthrough)

const child = spawnSync(process.execPath, pwArgs, { cwd: root, env, stdio: "inherit" })
if (args.list) process.exit(child.status ?? 1)

const rows = readRows(path.join(outputDir, "rows"))
let run = null
try {
  run = JSON.parse(fs.readFileSync(path.join(outputDir, "run.json"), "utf8"))
} catch {
  // Global setup failed before recording the run; the table says so via zero rows.
}
writeSummary(outputDir, rows, run)
const summary = summarize(rows)

console.log(`\nPRD acceptance (${args.mode}) — origin ${run?.origin ?? origin ?? "?"}, app SHA ${run?.appSha ?? "unknown"}`)
console.log(rows.length ? formatTable(rows) : "No result rows were recorded (global setup failed or nothing matched).")
console.log(`\n${summary.passed} passed, ${summary.failed} failed, ${summary.skipped} skipped. Evidence: ${outputDir}`)

process.exit(child.status === 0 && summary.ok ? 0 : 1)
