/** Experimental Mooring SDK · single-editor save vocabulary. The lifecycle
 * itself is the host's `createSingleEditorSave` (`lib/mooring/single-editor-save`);
 * the host constructs it with its journal and admitted transport and hands the
 * factory or controller to the application. This module only names its states
 * and labels them. Behavior is the host's: every write carries the lease
 * generation and base version, the draft is journaled before it is sent, a lost
 * reply is `uncertain` and is proven with `reconcile()` (identical bytes, never
 * a blind resend), and a 409 leaves the draft in place. */
import type { createSingleEditorSave, NativeSaveState } from "../single-editor-save"

/** The host's typed refusal (`conflict`, `quota`, `write_revoked`, `read_revoked`, `absent`). */
export { NativeSaveError as SaveError } from "../single-editor-save"
export type {
  NativeSaveFailure as SaveFailure,
  NativeSaveReceipt as SaveReceipt,
  NativeSaveRequest as SaveRequest,
  NativeSaveState as SaveState,
  SingleEditorSaveOptions as SaveOptions,
} from "../single-editor-save"

export type SavePhase = NativeSaveState["phase"]
/** The controller the host's lifecycle factory returns. */
export type SaveController = ReturnType<typeof createSingleEditorSave>
/** The phases the current host lifecycle emits. A read revocation currently
 * ends in `stopped`, the same phase as a close or a sign-out retirement. */
export const SAVE_PHASES: readonly SavePhase[] = Object.freeze(["idle", "dirty", "saving", "saved", "draft", "uncertain", "read-only", "stopped"] as const)

/** Default Czech labels, matching the native Craft host. Only `saved` with no
 * dirty input may be shown as saved; every other phase keeps the draft. */
export function saveLabel(state: NativeSaveState): string {
  if (state.phase === "saved" && !state.dirty) return "Uloženo"
  switch (state.phase) {
    case "uncertain": return "Výsledek nepotvrzen"
    case "saving": return "Ukládám…"
    case "draft": return state.reason ? "Koncept zůstává v tomto editoru" : "Neuložený koncept"
    case "read-only": return "Pouze čtení"
    case "stopped": return "Úpravy ukončeny"
    case "idle": return "Připraveno k úpravám"
    default: return "Neuložený koncept"
  }
}
/** True only when nothing local is unconfirmed: the app may close silently. */
export function saveSettled(state: NativeSaveState): boolean {
  return (state.phase === "saved" || state.phase === "idle") && !state.dirty
}
