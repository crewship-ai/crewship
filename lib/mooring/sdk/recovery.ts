/** Mooring SDK · local recovery draft. The save lifecycle journals every
 * capture before sending it. After a crash, reload or lost lease, those rows
 * are offered for compare and copy only. This module has no save path: a
 * recovered draft never becomes a write, never adopts a lease and is never
 * saved over the current holder's version. */
import type { NativeDraft, NativeDraftJournal } from "../native-draft"

export { openNativeDraftJournal as openRecoveryJournal } from "../native-draft"
export type { NativeDraft as RecoveryDraft, NativeDraftJournal as RecoveryJournal, NativeRecovery } from "../native-draft"

export interface RecoveryComparison {
  serverVersion: string
  serverHash: string
  draftHash: string
  draftBaseVersion: string
  draftGeneration: string
  equal: boolean
}
export interface RecoveryOffer {
  readonly draft: Readonly<NativeDraft>
  readonly actions: readonly ["compare", "copy"]
  /** Compare against the server's current bytes. Reads nothing, writes nothing. */
  compare(server: { version: string; bytes: Uint8Array }): Promise<RecoveryComparison>
  /** A detached copy for the person to paste deliberately. */
  copy(): Uint8Array
}

async function sha256(bytes: Uint8Array): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new Uint8Array(bytes))
  return Array.from(new Uint8Array(digest), b => b.toString(16).padStart(2, "0")).join("")
}
export async function compareRecovery(server: { version: string; bytes: Uint8Array }, draft: NativeDraft): Promise<RecoveryComparison> {
  const [serverHash, draftHash] = await Promise.all([sha256(server.bytes), sha256(draft.bytes)])
  return { serverVersion: server.version, serverHash, draftHash, draftBaseVersion: draft.baseVersion, draftGeneration: draft.generation, equal: serverHash === draftHash }
}

/** Drafts other mounts left behind, as compare/copy offers. Recovery rows stay
 * in the journal; dismissing an offer is the app's UI state, not a deletion. */
export async function recoveryOffers(journal: Pick<NativeDraftJournal, "recovery">): Promise<RecoveryOffer[]> {
  const rows = await journal.recovery()
  return rows.map(({ draft }) => {
    const kept: NativeDraft = Object.freeze({ ...draft, bytes: draft.bytes.slice() })
    return Object.freeze({
      draft: kept,
      actions: Object.freeze(["compare", "copy"] as const),
      compare: (server: { version: string; bytes: Uint8Array }) => compareRecovery(server, kept),
      copy: () => kept.bytes.slice(),
    })
  })
}
