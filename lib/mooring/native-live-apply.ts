import type { NativeExecuteCommand, NativeExecuteResult } from "./native-editor-port"

/** What the host needs from the editor port and the save controller to show an agent's saved version. */
export interface LiveApplyTargets {
  port: { execute(commands: NativeExecuteCommand[], options: { baseRevision?: string }, signal: AbortSignal): Promise<NativeExecuteResult> }
  save: { adoptVersion(version: string, contentHash: string): boolean }
  isCurrent?(): boolean
  beginReplay?(): (() => void) | undefined
  replayed?(result: NativeExecuteResult, version: string, adopted: boolean): void
}

export interface LiveApplyInput {
  version: string
  contentHash: string
  /** The value commands the agent's batch ran, in order; absent when the batch is not replayable. */
  commands?: NativeExecuteCommand[]
  /** The editor revision the host last saw, so a document that moved is not replayed onto. */
  baseRevision?: string
  /** Opens the whole saved version in the editor: today's path, and the answer to anything unclear. */
  reopen(): Promise<void>
  signal: AbortSignal
}

export type LiveApplyMode = "replayed" | "reopened"

/**
 * Brings the editor to the agent's saved version. First choice: replay the agent's value
 * commands in the open editor (no reload of a 30 MB document, the person's view and selection
 * stay) and make that version the baseline for the next save. Whenever that cannot be done
 * cleanly, or anything about it is unclear (no commands, the editor refuses because the person
 * is typing or the revision moved, a protocol error, the baseline cannot be adopted), the whole
 * version is opened as before. The editor never ends up half-updated: a replay that failed part
 * way is followed by the full open, which replaces the document.
 */
export async function applyAgentVersion(targets: LiveApplyTargets, input: LiveApplyInput): Promise<{ mode: LiveApplyMode; reason?: string }> {
  const { commands } = input
  if (!commands || commands.length === 0) { await input.reopen(); return { mode: "reopened", reason: "no_commands" } }
  const release = targets.beginReplay?.()
  if (targets.beginReplay && !release) { await input.reopen(); return { mode: "reopened", reason: "save_unsettled" } }
  let reason: string
  try {
    const result = await targets.port.execute(commands, input.baseRevision === undefined ? {} : { baseRevision: input.baseRevision }, input.signal)
    const adopted = targets.save.adoptVersion(input.version, input.contentHash)
    targets.replayed?.(result, input.version, adopted)
    if (adopted) return { mode: "replayed" }
    reason = "baseline_refused"
  } catch (error) {
    if (input.signal.aborted) throw error
    reason = error instanceof Error && error.message ? error.message.slice(0, 120) : "execute_failed"
  } finally { release?.() }
  await input.reopen()
  return { mode: "reopened", reason }
}
