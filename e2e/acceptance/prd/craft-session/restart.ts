// Restarting the server under test from a scenario.
//
// Opt-in through the environment, so the repository names no host (the same
// variables as the other restart rows of the suite):
//   PRD_RESTART_COMMAND    command that restarts the server and returns when it
//                          was asked to, for example `crew restart dev3`
//   PRD_RESTART_HANDSHAKE  directory for hosts where that command cannot run
//                          inside the test process (`crew` refuses to run under
//                          crewship-heavy): the test writes <dir>/restart.request
//                          and waits for <dir>/restart.done, which a watcher in
//                          the operator's own shell answers
//   PRD_RESTART_NOTE_FILE  optional file; one line (time, reason) is appended
//                          before every restart
// The restart is proven by the server itself: GET /healthz reports its uptime,
// and a NEW process answers with a lower one. Without a command or a handshake
// the restart rows fail in full mode; they never pass without a restart.
import { execFileSync } from "node:child_process"
import * as fs from "node:fs"
import * as path from "node:path"
import { request as playwrightRequest } from "@playwright/test"

export interface RestartRecord {
  command: string
  startedAt: string
  backAt: string
  tookMs: number
  uptimeBefore: number | null
  uptimeAfter: number
}

const pause = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

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

/** Whether this run can restart the server at all. */
export function canRestart(): boolean {
  return Boolean(process.env.PRD_RESTART_COMMAND?.trim() || process.env.PRD_RESTART_HANDSHAKE?.trim())
}

/** Restarts the server under test and waits until a new process answers /healthz. */
export async function restartServer(origin: string, reason: string): Promise<RestartRecord> {
  const command = process.env.PRD_RESTART_COMMAND?.trim().split(/\s+/)
  const handshake = process.env.PRD_RESTART_HANDSHAKE?.trim()
  if (!command?.length && !handshake) throw new Error("PRD_RESTART_COMMAND or PRD_RESTART_HANDSHAKE is not set: the restart rows need a way to restart the server under test (for example PRD_RESTART_COMMAND='crew restart dev3')")
  const uptimeBefore = await uptimeSeconds(origin)
  const label = command?.length ? command.join(" ") : `handshake ${handshake}`
  const note = process.env.PRD_RESTART_NOTE_FILE?.trim()
  if (note) fs.appendFileSync(note, `- ${new Date().toISOString()} restart (${label}): ${reason}\n`)
  const started = Date.now()
  const startedAt = new Date(started).toISOString()
  if (command?.length) {
    try {
      execFileSync(command[0], command.slice(1), { stdio: ["ignore", "pipe", "pipe"], timeout: 5 * 60_000, encoding: "utf8" })
    } catch (err) {
      const e = err as { status?: number | null; stdout?: string; stderr?: string }
      throw new Error(`${label} exited ${e.status ?? "abnormally"}: ${`${e.stdout ?? ""} ${e.stderr ?? ""}`.replace(/\s+/g, " ").trim().slice(0, 800)}`)
    }
  } else {
    const request = path.join(handshake!, "restart.request")
    const done = path.join(handshake!, "restart.done")
    fs.rmSync(done, { force: true })
    fs.writeFileSync(request, JSON.stringify({ at: startedAt, reason }) + "\n")
    const waitUntil = Date.now() + 10 * 60_000
    while (!fs.existsSync(done)) {
      if (Date.now() > waitUntil) throw new Error(`nobody answered ${request} within 10 minutes (is the restart watcher running?)`)
      await pause(500)
    }
    const answer = fs.readFileSync(done, "utf8").trim()
    fs.rmSync(done, { force: true })
    if (!answer.startsWith("ok")) throw new Error(`restart handshake answered: ${answer.slice(0, 400)}`)
  }
  const deadline = Date.now() + 3 * 60_000
  for (;;) {
    const up = await uptimeSeconds(origin)
    if (up !== null && (uptimeBefore === null || up < uptimeBefore)) {
      return { command: label, startedAt, backAt: new Date().toISOString(), tookMs: Date.now() - started, uptimeBefore, uptimeAfter: up }
    }
    if (Date.now() > deadline) throw new Error("no restarted server answered /healthz within 3 minutes")
    await pause(1_000)
  }
}
