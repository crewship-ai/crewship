/** The note the server stores after a message it saved but did not run (history id notrun-<message id>). */
export const NOT_RUN_PREFIX = "notrun-"

/**
 * The saved message that Send again can run: the last not-run note, while no agent reply follows it.
 */
export function resendableNotRun(turns: readonly { id: string; role: string }[]): string | undefined {
  for (let i = turns.length - 1; i >= 0; i--) {
    const turn = turns[i]
    if (turn.role === "assistant") return undefined
    if (turn.role === "system" && turn.id.startsWith(NOT_RUN_PREFIX) && turn.id.length > NOT_RUN_PREFIX.length) return turn.id.slice(NOT_RUN_PREFIX.length)
  }
  return undefined
}

/** Shown when the server's not-run event came without words of its own. */
export const NOT_RUN_FALLBACK = "Your message is saved but did not run. Send it again when the agent can run."

/**
 * The saved message an event says was not run: agent_busy, error and crew_provisioning carry
 * {message_id, status: "not_run"} or a not_run_reason. Without a message id the server kept nothing.
 */
export function notRunMessage(metadata: Record<string, unknown> | undefined): string | undefined {
  const id = metadata?.message_id
  if (typeof id !== "string" || !id || id.length > 128) return undefined
  return metadata?.status === "not_run" || typeof metadata?.not_run_reason === "string" ? id : undefined
}
