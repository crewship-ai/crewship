// Operator actions a PRD scenario may need from the deployment under test:
// restarting the server and driving the crewship CLI as an instance admin.
// Both are opt-in through the environment so the suite names no host:
//   PRD_RESTART_COMMAND   command that restarts the server under test and
//                         returns when it was asked to (e.g. "crew restart dev2")
//   PRD_RESTART_HANDSHAKE directory for hosts where the restart command cannot
//                         run inside the test process (`crew` refuses to run
//                         under crewship-heavy and needs the operator's terminal
//                         environment). The test writes <dir>/restart.request
//                         and waits for <dir>/restart.done; restart-watcher.sh
//                         in this folder answers it from the operator's shell.
//   PRD_RESTART_NOTE_FILE optional file; one line (time, reason) is appended
//                         before every restart
import { execFileSync } from "node:child_process"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { request as playwrightRequest } from "@playwright/test"

export const pause = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

/** Restart command from the environment, or null. */
export function restartCommand(): string[] | null {
  const raw = process.env.PRD_RESTART_COMMAND?.trim()
  return raw ? raw.split(/\s+/) : null
}

/** Directory of the request/done handshake, or null. */
export function restartHandshake(): string | null {
  return process.env.PRD_RESTART_HANDSHAKE?.trim() || null
}

/** Whether this run can restart the server at all. */
export function canRestart(): boolean {
  return restartCommand() !== null || restartHandshake() !== null
}

/** Seconds the server says it has been up (GET /healthz), or null while it is down. */
async function uptimeSeconds(origin: string): Promise<number | null> {
  const ctx = await playwrightRequest.newContext({ baseURL: origin })
  try {
    const res = await ctx.get("/healthz", { timeout: 5_000 })
    if (!res.ok()) return null
    const body = (await res.json().catch(() => null)) as { status?: string; uptime?: string } | null
    if (body?.status !== "ok" || typeof body.uptime !== "string") return null
    // Go duration such as "53m49.08s" or "1h2m3s".
    let total = 0
    for (const m of body.uptime.matchAll(/([\d.]+)(h|m|s)/g)) total += Number(m[1]) * { h: 3600, m: 60, s: 1 }[m[2] as "h" | "m" | "s"]
    return total
  } catch {
    return null
  } finally {
    await ctx.dispose()
  }
}

/**
 * Restarts the server under test and waits until a NEW process answers
 * /healthz (its uptime is lower than before). Returns the elapsed time.
 */
export async function restartServer(origin: string, reason: string): Promise<{ command: string; ms: number; uptimeBefore: number | null; uptimeAfter: number }> {
  const cmd = restartCommand()
  const handshake = restartHandshake()
  if (!cmd && !handshake) throw new Error("PRD_RESTART_COMMAND or PRD_RESTART_HANDSHAKE is not set")
  const uptimeBefore = await uptimeSeconds(origin)
  const label = cmd ? cmd.join(" ") : `handshake ${handshake}`
  const note = process.env.PRD_RESTART_NOTE_FILE?.trim()
  if (note) fs.appendFileSync(note, `- ${new Date().toISOString()} restart (${label}): ${reason}\n`)
  const t0 = Date.now()
  if (cmd) {
    try {
      execFileSync(cmd[0], cmd.slice(1), { stdio: ["ignore", "pipe", "pipe"], timeout: 5 * 60_000, encoding: "utf8" })
    } catch (err) {
      const e = err as { status?: number | null; stdout?: string; stderr?: string }
      throw new Error(`${cmd.join(" ")} exited ${e.status ?? "abnormally"}: ${`${e.stdout ?? ""} ${e.stderr ?? ""}`.replace(/\s+/g, " ").trim().slice(0, 800)}`)
    }
  } else {
    const request = path.join(handshake!, "restart.request")
    const done = path.join(handshake!, "restart.done")
    fs.rmSync(done, { force: true })
    fs.writeFileSync(request, JSON.stringify({ at: new Date().toISOString(), reason }) + "\n")
    const waitUntil = Date.now() + 10 * 60_000
    while (!fs.existsSync(done)) {
      if (Date.now() > waitUntil) throw new Error(`nobody answered ${request} within 10 minutes (is restart-watcher.sh running?)`)
      await pause(500)
    }
    const answer = fs.readFileSync(done, "utf8").trim()
    fs.rmSync(done, { force: true })
    if (!answer.startsWith("ok")) throw new Error(`restart handshake answered: ${answer.slice(0, 400)}`)
  }
  const deadline = Date.now() + 3 * 60_000
  for (;;) {
    const up = await uptimeSeconds(origin)
    if (up !== null && (uptimeBefore === null || up < uptimeBefore)) return { command: label, ms: Date.now() - t0, uptimeBefore, uptimeAfter: up }
    if (Date.now() > deadline) throw new Error("no restarted server answered /healthz within 3 minutes")
    await pause(1_000)
  }
}

/**
 * The crewship CLI as an instance admin with its own HOME (no shared config or
 * token). `args` follow the global flags; the password goes in over stdin.
 */
export class AdminCli {
  private readonly home = fs.mkdtempSync(path.join(os.tmpdir(), "prd-cli-"))
  constructor(private readonly bin: string, private readonly origin: string) {}

  private run(args: string[], input?: string): string {
    return execFileSync(this.bin, ["--server", this.origin, ...args], {
      encoding: "utf8",
      input,
      timeout: 60_000,
      env: { PATH: process.env.PATH ?? "", HOME: this.home, CREWSHIP_SERVER: this.origin } as unknown as NodeJS.ProcessEnv,
    })
  }

  login(email: string, password: string): void {
    this.run(["login", "--email", email, "--password-stdin"], `${password}\n`)
  }

  json<T>(args: string[]): T {
    return JSON.parse(this.run(["--format", "json", ...args])) as T
  }

  dispose(): void {
    fs.rmSync(this.home, { recursive: true, force: true })
  }
}

export interface InstanceStorage {
  workspace_limit_bytes: number
  instance_limit_bytes: number
  used_bytes: number
  reserved_bytes: number
  level: string
}
