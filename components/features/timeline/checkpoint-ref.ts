import type { JournalEntry } from "@/lib/types/journal"

/**
 * Resolving a `checkpoint.created` journal entry back to the checkpoint row
 * it announced.
 *
 * `cartographer.Create` (internal/cartographer/store.go) emits the entry with
 * the checkpoint id under **refs**, not payload:
 *
 *     Refs: {"checkpoint_id": id, "journal_cursor": …, "mission_id": …}
 *
 * The timeline used to read `payload.checkpoint_id` and fall back to
 * `entry.id`. Payload is always `{}` for these entries, so the fallback always
 * won — and `entry.id` is the JOURNAL row id, which no `/api/v1/checkpoints/…`
 * endpoint will ever match. A wrong id that looks plausible is worse than a
 * missing one, so these return null rather than guessing: callers disable the
 * action instead of firing a request that can only 404.
 */
function refString(entry: JournalEntry, key: string): string | undefined {
  const fromRefs = entry.refs?.[key]
  if (typeof fromRefs === "string" && fromRefs !== "") return fromRefs
  // Payload is checked second only so a future emitter that moves the field
  // doesn't silently break the timeline. It is not the documented location.
  const fromPayload = entry.payload?.[key]
  if (typeof fromPayload === "string" && fromPayload !== "") return fromPayload
  return undefined
}

/** checkpointIdOf returns the `cp_…` id an entry announced, or null. */
export function checkpointIdOf(entry: JournalEntry | null | undefined): string | null {
  if (!entry) return null
  return refString(entry, "checkpoint_id") ?? null
}

/**
 * checkpointLabelOf recovers the human label. The emitter does not put the
 * label in refs or payload — it only bakes it into the summary as
 * `checkpoint "green build" @ cursor …` — so the quoted span is parsed out
 * and the explicit fields still win if a future emitter adds them.
 */
export function checkpointLabelOf(entry: JournalEntry | null | undefined): string | undefined {
  if (!entry) return undefined
  const explicit = refString(entry, "label") ?? refString(entry, "name")
  if (explicit) return explicit
  const quoted = /^checkpoint "(.*)" @ cursor /.exec(entry.summary ?? "")
  if (quoted && quoted[1] !== "") return quoted[1]
  return undefined
}
