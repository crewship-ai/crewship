#!/usr/bin/env node
// Draft revocation proof in one command: `pnpm acceptance:draft-revocation`.
// Starts its own throwaway server behind revocation-proxy.mjs, seeds it, runs every
// case in real Chromium under Xvfb, stops everything and writes summary.md. Run it
// directly: the server stays outside the build queue, Playwright goes through it.
// Exit 0 only when every listed test passed. Docs: docs/development/prd-acceptance.md
import { execFileSync, spawn, spawnSync } from "node:child_process"
import * as fs from "node:fs"
import { createRequire } from "node:module"
import * as net from "node:net"
import * as os from "node:os"
import * as path from "node:path"
import { fileURLToPath } from "node:url"
import { formatTable, readRows, summarize, writeSummary } from "../prd/report.mjs"
import { EXIT, missingEngines, nativeSettings, verdict } from "./verdict.mjs"

const HELP = `Usage: pnpm acceptance:draft-revocation --binary PATH --native-env FILE... --output DIR [--chrome PATH] [--grep RE]

  --binary PATH     the crewship server to prove, built from the release candidate
  --native-env FILE env file with the CREWSHIP_CRAFT_NATIVE_* settings of Word and Sheet (repeatable);
                    both engines are required, other lines are ignored
  --output DIR      results: summary.md, summary.json, verdict.json, evidence/
  --chrome PATH     full Chromium for the hidden-tab cases (default: Playwright's own, else the
                    newest chromium-* under PLAYWRIGHT_BROWSERS_PATH)
  --grep RE         only the matching tests. Such a run exits 3, never 0

Exit: 0 every listed test passed; 1 a test failed or was skipped, or left no result; 2 setup failed; 3 partial.`

/** Through the shared queue where the host has one. It does not forward the environment. */
function queued(command, commandArgs, env, options) {
  if (spawnSync("sh", ["-c", "command -v crewship-heavy"]).status !== 0) return spawnSync(command, commandArgs, { ...options, env: { ...process.env, ...env } })
  return spawnSync("crewship-heavy", ["env", ...Object.entries(env).map(([key, value]) => `${key}=${value}`), command, ...commandArgs], options)
}

const here = path.dirname(fileURLToPath(import.meta.url))
const root = path.resolve(here, "../../..")
const SEEDED = { email: "demo@crewship.ai", password: "password123" } // scripts seed: --offline-demo
const NAME = `draft-revocation-${process.pid}`

function fail(message) {
  console.error(`acceptance:draft-revocation: ${message}`)
  process.exit(EXIT.setup)
}

function parse(argv) {
  const out = { nativeEnv: [] }
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i], value = () => { const v = argv[++i]; if (v === undefined) fail(`${flag} needs a value`); return v }
    if (flag === "--binary") out.binary = path.resolve(value())
    else if (flag === "--native-env") out.nativeEnv.push(value())
    else if (flag === "--output") out.output = path.resolve(value())
    else if (flag === "--chrome") out.chrome = path.resolve(value())
    else if (flag === "--grep") out.grep = value()
    else if (flag === "--help" || flag === "-h") { console.log(HELP); process.exit(EXIT.pass) }
    else fail(`unknown flag ${flag}\n\n${HELP}`)
  }
  return out
}

/** Full Chromium: the headless shell never hides a tab. */
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

const freePort = (port) => new Promise((resolve) => {
  const probe = net.createServer().once("error", () => resolve(false)).once("listening", () => probe.close(() => resolve(true))).listen(port, "127.0.0.1")
})
async function freePorts(count) {
  const ports = []
  for (let port = 8110; port < 8200 && ports.length < count; port++) if (await freePort(port)) ports.push(port)
  if (ports.length < count) fail("no free port between 8110 and 8199")
  return ports
}

const args = parse(process.argv.slice(2))
if (!args.binary || !args.output || !args.nativeEnv.length) fail(`--binary, --native-env and --output are required\n\n${HELP}`)
if (!fs.existsSync(args.binary)) fail(`no binary at ${args.binary}`)
for (const tool of ["xvfb-run", "sqlite3"]) if (spawnSync("sh", ["-c", `command -v ${tool}`]).status !== 0) fail(`${tool} is required`)
const settings = new Map()
for (const file of args.nativeEnv) for (const [key, value] of nativeSettings(fs.readFileSync(file, "utf8"))) settings.set(key, value)
const missing = missingEngines(settings)
if (missing.length) fail(`the native settings lack ${missing.join(", ")}`)
const browsers = process.env.PLAYWRIGHT_BROWSERS_PATH ?? path.join(os.homedir(), ".cache", "ms-playwright")
const cli = createRequire(path.join(root, "package.json")).resolve("@playwright/test/cli")
const chrome = args.chrome ?? findChrome(browsers, cli)
if (!chrome || !fs.existsSync(chrome)) fail(`no full Chromium under ${browsers}; pass --chrome`)
fs.mkdirSync(args.output, { recursive: true })

const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), "draft-revocation-"))
const throwaway = (...rest) => execFileSync(path.join(root, "scripts/throwaway-server.sh"), rest, { cwd: root, env: { ...process.env, CREWSHIP_THROWAWAY_STATE: stateDir }, stdio: ["ignore", "pipe", "inherit"], encoding: "utf8" })
const [origin, server, control] = await freePorts(3)
const nativeFlags = [...settings].flatMap(([key, value]) => ["--env", `${key}=${value}`])
let proxy, finished = false
function stop() {
  if (finished) return
  finished = true
  proxy?.kill()
  try { throwaway("stop", NAME) } catch { console.error(`acceptance:draft-revocation: the throwaway server did not stop cleanly; state in ${stateDir}`); return }
  fs.rmSync(stateDir, { recursive: true, force: true })
}
for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => { stop(); process.exit(EXIT.setup) })

const log = fs.openSync(path.join(args.output, "setup.log"), "a")
try {
  console.log(`Starting a throwaway server on ${server}, behind a proxy on ${origin}`)
  throwaway("start", NAME, "--binary", args.binary, "--port", String(server), "--page-projects",
    "--env", `CREWSHIP_NEXTJS_URL=http://127.0.0.1:${origin}`, "--env", "CREWSHIP_SKIP_SIDECAR=1", "--env", "CREWSHIP_RATELIMIT_DISABLED=1", ...nativeFlags)
  const instance = path.join(stateDir, NAME)
  const seedEnv = { ...process.env, CREWSHIP_SERVER: `http://127.0.0.1:${server}`, CREWSHIP_CONFIG: path.join(instance, "cli-config.yaml") }
  for (let attempt = 1; ; attempt++) {
    const seeded = spawnSync(args.binary, ["seed", "--offline-demo", "--server", seedEnv.CREWSHIP_SERVER], { env: seedEnv, stdio: ["ignore", log, log] })
    if (seeded.status === 0) break
    if (attempt === 3) throw new Error("seeding failed; see setup.log")
    await new Promise((resolve) => setTimeout(resolve, 5_000))
  }
  proxy = spawn(process.execPath, [path.join(here, "revocation-proxy.mjs"), String(origin), String(server), String(control)], { stdio: ["ignore", log, log] })
  await new Promise((resolve) => setTimeout(resolve, 1_000))

  const env = {
    PATH: process.env.PATH, HOME: os.homedir(), PLAYWRIGHT_BROWSERS_PATH: browsers, CI: "1",
    PRD_MODE: "quick", PRD_ORIGIN: `http://127.0.0.1:${origin}`, PRD_CRAFT_NATIVE: "on", PRD_OUTPUT_DIR: args.output,
    E2E_EMAIL: SEEDED.email, E2E_PASSWORD: SEEDED.password, CREWSHIP_INSTANCE_ID: NAME,
    REVOCATION_DB: path.join(instance, "data", "crewship.db"), REVOCATION_CHROME: chrome,
    REVOCATION_PROXY_CONTROL: `http://127.0.0.1:${control}`, REVOCATION_DIRECT: `http://127.0.0.1:${server}`,
    ...(process.env.REVOCATION_HIDDEN_MS ? { REVOCATION_HIDDEN_MS: process.env.REVOCATION_HIDDEN_MS } : {}),
  }
  const config = path.join(root, "playwright.draft-revocation.config.ts")
  const select = args.grep ? ["--grep", args.grep] : []
  const list = queued(process.execPath, [cli, "test", "--config", config, "--list", ...select], env, { cwd: root, encoding: "utf8" })
  const listed = Number(/Total: (\d+) tests?/.exec(list.stdout ?? "")?.[1] ?? 0)
  const run = queued("xvfb-run", ["-a", "-s", "-screen 0 1600x1200x24", process.execPath, cli, "test", "--config", config, ...select], env, { cwd: root, stdio: "inherit" })

  const rows = readRows(path.join(args.output, "rows"))
  let record = null
  try { record = JSON.parse(fs.readFileSync(path.join(args.output, "run.json"), "utf8")) } catch { /* Global setup failed; the row count says so. */ }
  writeSummary(args.output, rows, record)
  const result = verdict({ playwrightStatus: run.status, listed, rows, narrowed: Boolean(args.grep) })
  const counts = summarize(rows)
  fs.writeFileSync(path.join(args.output, "verdict.json"), JSON.stringify({ ...result, listed, ...counts, appSha: record?.appSha ?? null, chrome: path.relative(browsers, chrome) }, null, 2) + "\n")
  console.log(rows.length ? formatTable(rows) : "No result rows were recorded.")
  console.log(`\n${result.verdict}: ${counts.passed} passed, ${counts.failed} failed, ${counts.skipped} skipped of ${listed} listed, app SHA ${record?.appSha ?? "unknown"}. ${result.reasons.join("; ")}\nEvidence: ${args.output}`)
  stop()
  process.exit(result.exit)
} catch (cause) {
  stop()
  fail(cause.message)
}
