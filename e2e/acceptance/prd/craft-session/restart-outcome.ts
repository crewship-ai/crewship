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
  /** The control refused and nothing proves a new process. Environment, not product. */
  | { kind: "refused"; reason: string }
  /** Wait: the control accepted, or the server does not answer and proves nothing yet. */
  /** No uptime from before the restart: nothing can prove a new process, so the row proves no restart. */
  | { kind: "inconclusive" }
  | { kind: "waiting" }

export function restartOutcome(answer: string, uptimeBefore: number | null, uptimeNow: number | null): RestartOutcome {
  const accepted = answer.trim().startsWith("ok")
  const complaint = answer.trim().slice(0, 400)
  if (uptimeBefore === null) return { kind: "inconclusive" }
  if (uptimeNow === null) return { kind: "waiting" }
  if (uptimeNow < uptimeBefore) return accepted ? { kind: "restarted" } : { kind: "restarted", refusedBy: complaint }
  return accepted ? { kind: "waiting" } : { kind: "refused", reason: complaint }
}

/** The line a row's steps carry when the control complained about a restart that did happen. */
export function restartNote(record: { controlRefused?: string }): string {
  return record.controlRefused ? `restart: the server did restart, the control refused: ${record.controlRefused}` : "restart: done"
}
