#!/usr/bin/env node
// Native walk in one command: `pnpm acceptance:native-walk --origin URL --rc SHA --output DIR`.
// Runs the Word and the Sheet walk once each in full headed Chromium under Xvfb against a
// deployed origin, through the build queue where the host has one, and writes verdict.json.
// Exit 0 only on PASS (verdict.mjs). Docs: docs/development/prd-acceptance.md, "Native walk".
import { spawnSync } from "node:child_process"
import { randomUUID } from "node:crypto"
import * as fs from "node:fs"
import { createRequire } from "node:module"
import * as os from "node:os"
import * as path from "node:path"
import { fileURLToPath } from "node:url"
import { formatTable, readRows, summarize, writeSummary } from "../prd/report.mjs"
import { checkOrigin, jobArgs, runEnv } from "./runner-env.mjs"
import { EXIT, SCENARIOS, fullChromium, loadEvidence, parseListed, verdict } from "./verdict.mjs"

const HELP = `Usage: pnpm acceptance:native-walk --origin URL --rc SHA --output DIR [--env-file FILE] [--chrome PATH] [--grep RE]

  --origin URL     deployed origin under test, e.g. https://crewship-dev3.unifylab.cz
  --rc SHA         full 40-character commit the origin must serve (GET /api/v1/system/version), clean
  --output DIR     new or empty results directory: verdict.json, summary.md, summary.json, run.json,
                   finished.json, rows/, evidence/native-walk/<test>/walk.json, playwright/
  --env-file FILE  PRD account file (PRD_OWNER_EMAIL, PRD_OWNER_PASSWORD, ...), sourced inside the
                   queued job so no credential is on a command line. Without it the PRD_* variables
                   of this environment are used
  --chrome PATH    full Chromium (default: Playwright's own, else the newest chromium-* under
                   PLAYWRIGHT_BROWSERS_PATH). The headless shell is refused
  --grep RE        only the matching walks. Such a run exits 3, never 0

Exit: 0 PASS; 1 FAIL (a walk, a row, the evidence or the run itself); 2 SETUP (arguments, browser,
credentials, or the origin does not serve --rc clean); 3 PARTIAL (--grep).`

const here = path.dirname(fileURLToPath(import.meta.url))
const root = path.resolve(here, "../../..")

function fail(message) {
  console.error(`acceptance:native-walk: ${message}`)
  process.exit(EXIT.setup)
}

function parse(argv) {
  const out = {}
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i], value = () => { const v = argv[++i]; if (v === undefined || v.startsWith("--")) fail(`${flag} needs a value`); return v }
    if (flag === "--origin") out.origin = value()
    else if (flag === "--rc") out.rc = value()
    else if (flag === "--output") out.output = path.resolve(value())
    else if (flag === "--env-file") out.envFile = path.resolve(value())
    else if (flag === "--chrome") out.chrome = path.resolve(value())
    else if (flag === "--grep") out.grep = value()
    else if (flag === "--help" || flag === "-h") { console.log(HELP); process.exit(EXIT.pass) }
    else fail(`unknown flag ${flag}\n\n${HELP}`)
  }
  return out
}

function findChrome(browsers, cli) {
  const own = spawnSync(process.execPath, ["-e", `import("${cli.replace("@playwright/test/cli.js", "playwright-core/index.mjs")}").then(m=>console.log(m.chromium.executablePath()))`], { encoding: "utf8", env: { ...process.env, PLAYWRIGHT_BROWSERS_PATH: browsers } }).stdout?.trim()
  if (own && fs.existsSync(own)) return own
  const installed = fs.existsSync(browsers) ? fs.readdirSync(browsers).filter((name) => /^chromium-\d+$/.test(name)).sort((a, b) => Number(b.slice(9)) - Number(a.slice(9))) : []
  for (const name of installed) {
    const chrome = path.join(browsers, name, "chrome-linux64", "chrome")
    if (fs.existsSync(chrome)) return chrome
  }
  return undefined
}

const quote = (v) => `'${String(v).replace(/'/g, `'\\''`)}'`

/** PRD_* account variables of this environment in a private file, removed after the run. */
function envFileFromEnvironment() {
  const keys = Object.keys(process.env).filter((k) => /^PRD_(OWNER|MEMBER|READER)_(EMAIL|PASSWORD)$/.test(k) || ["PRD_WORKSPACE_ID", "PRD_KEEP_WORKSPACES"].includes(k))
  if (!process.env.PRD_OWNER_EMAIL || !process.env.PRD_OWNER_PASSWORD) return null
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "native-walk-env-"))
  const file = path.join(dir, "accounts.env")
  fs.writeFileSync(file, keys.map((k) => `${k}=${quote(process.env[k])}`).join("\n") + "\n", { mode: 0o600 })
  return { file, remove: () => fs.rmSync(dir, { recursive: true, force: true }) }
}

/** Through the shared queue where the host has one; it forwards no environment. */
function queued(envFile, env, command, commandArgs, options) {
  const job = jobArgs(envFile, env, command, commandArgs)
  if (spawnSync("sh", ["-c", "command -v crewship-heavy"]).status !== 0) return spawnSync(job[0], job.slice(1), options)
  return spawnSync("crewship-heavy", job, options)
}

const readJson = (file) => { try { return JSON.parse(fs.readFileSync(file, "utf8")) } catch { return null } }

const args = parse(process.argv.slice(2))
if (!args.origin || !args.rc || !args.output) fail(`--origin, --rc and --output are required\n\n${HELP}`)
const checked = checkOrigin(args.origin)
if (checked.error) fail(checked.error)
const origin = checked.origin
const rc = args.rc.toLowerCase()
if (!/^[0-9a-f]{40}$/.test(rc)) fail(`--rc must be the full 40-character commit, got ${args.rc}`)
if (fs.existsSync(args.output) && fs.readdirSync(args.output).length) fail(`${args.output} is not empty: a stale run.json or row must not count for this run`)
if (args.envFile && !fs.existsSync(args.envFile)) fail(`no env file at ${args.envFile}`)
if (spawnSync("sh", ["-c", "command -v xvfb-run"]).status !== 0) fail("xvfb-run is required: the walk runs headed")

const browsers = process.env.PLAYWRIGHT_BROWSERS_PATH ?? path.join(os.homedir(), ".cache", "ms-playwright")
const cli = createRequire(path.join(root, "package.json")).resolve("@playwright/test/cli")
const found = args.chrome ?? findChrome(browsers, cli)
if (!found || !fs.existsSync(found)) fail(`no full Chromium under ${browsers}; pass --chrome`)
// A symlink named chrome may point at the headless shell: fullChromium judges the real file.
const chromium = fullChromium(found, (file) => { const p = spawnSync(file, ["--version"], { encoding: "utf8" }); return p.status === 0 ? p.stdout.trim() : null })
if (chromium.refused) fail(chromium.refused)
const chrome = chromium.path, chromeVersion = chromium.version

const accounts = args.envFile ? { file: args.envFile, remove: () => {} } : envFileFromEnvironment()
if (!accounts) fail("no owner account: pass --env-file or set PRD_OWNER_EMAIL and PRD_OWNER_PASSWORD")
fs.mkdirSync(args.output, { recursive: true })

// Binds run.json, every row, each walk.json and finished.json to this invocation.
const runId = randomUUID()
const env = runEnv({ path: process.env.PATH, home: os.homedir(), browsers, origin, output: args.output, chrome })
const config = path.join(root, "playwright.native-walk.config.ts")
const select = args.grep ? ["--grep", args.grep] : []
for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => { accounts.remove(); process.exit(EXIT.setup) })
// Discovery runs without PRD_RUN_ID, so it loads no finish reporter.
const list = queued(null, env, process.execPath, [cli, "test", "--config", config, "--list", ...select], { cwd: root, encoding: "utf8" })
const listedOut = list.stdout ?? ""
if (list.status !== 0) { accounts.remove(); fail(`playwright --list exited ${list.status}: ${(list.stderr ?? "").trim().slice(-300)}`) }
// Whatever discovery left in the output directory does not belong to the run.
for (const name of fs.readdirSync(args.output)) fs.rmSync(path.join(args.output, name), { recursive: true, force: true })
console.log(`Native walk ${runId} on ${origin}, RC ${rc}, ${chromeVersion} (${chrome})`)
const run = queued(accounts.file, runEnv({ path: process.env.PATH, home: os.homedir(), browsers, origin, output: args.output, chrome, runId }), "xvfb-run", ["-a", "-s", "-screen 0 1600x1200x24", process.execPath, cli, "test", "--config", config, ...select], { cwd: root, stdio: "inherit" })
accounts.remove()

const listed = parseListed(listedOut)
const rows = readRows(path.join(args.output, "rows"))
const record = readJson(path.join(args.output, "run.json"))
const finished = readJson(path.join(args.output, "finished.json"))
const reporter = readJson(path.join(args.output, "reporter.json"))
writeSummary(args.output, rows, record)
const result = verdict({ playwrightStatus: run.status, listed, rows, run: record, finished, reporter, rc, origin, chrome, runId, narrowed: Boolean(args.grep), evidence: loadEvidence(args.output) })
const counts = summarize(rows)
fs.writeFileSync(path.join(args.output, "verdict.json"), JSON.stringify({
  ...result, runId, rc, origin, appSha: record?.appSha ?? null, appDirty: record?.appDirty ?? null, playwrightStatus: run.status,
  listed, scenarios: SCENARIOS.map((s) => s.id), passed: counts.passed, failed: counts.failed, skipped: counts.skipped, rows: counts.total,
  chrome: { path: chrome, version: chromeVersion }, grep: args.grep ?? null,
}, null, 2) + "\n")
console.log(rows.length ? formatTable(rows) : "No result rows were recorded.")
for (const n of result.notVerified) console.log(`NOT VERIFIED ${n.id}: ${n.item}`)
console.log(`\n${result.verdict}: ${counts.passed} passed, ${counts.failed} failed, ${counts.skipped} skipped of ${listed.length} listed, app SHA ${record?.appSha ?? "unknown"}. ${result.reasons.join("; ")}\nEvidence: ${args.output}`)
process.exit(result.exit)
