// What a restart request's answer means. Pure, so restart-outcome.test.mjs covers it.
//
// The answer of the restart control is not the proof of a restart: the server's own
// /healthz uptime is (restart.ts). `crew restart` can refuse AFTER it restarted the
// server: on 2026-11-06 it answered "protected process identity changed during runtime
// control" because an unrelated Codex process ended during the 17 s restart, and the Word
// hour row reported that as its failure although a new server was up.
export type RestartOutcome =
  /** A new process answers. `refusedBy` keeps a control complaint for the row's evidence. */
  | { kind: "restarted"; refusedBy?: string }
  /** The control refused and the old process still runs: no restart happened. Environment, not product. */
  | { kind: "refused"; reason: string }
  /** The control accepted; wait for the new process. */
  | { kind: "waiting" }

export function restartOutcome(answer: string, uptimeBefore: number | null, uptimeNow: number | null): RestartOutcome {
  const accepted = answer.trim().startsWith("ok")
  const newProcess = uptimeNow !== null && (uptimeBefore === null || uptimeNow < uptimeBefore)
  if (newProcess) return accepted ? { kind: "restarted" } : { kind: "restarted", refusedBy: answer.trim().slice(0, 400) }
  return accepted ? { kind: "waiting" } : { kind: "refused", reason: answer.trim().slice(0, 400) }
}

/** The line a row's steps carry when the control complained about a restart that did happen. */
export function restartNote(record: { controlRefused?: string }): string {
  return record.controlRefused ? `restart: the server did restart, the control refused: ${record.controlRefused}` : "restart: done"
}
