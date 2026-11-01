/** Mooring SDK · single-editor save. A re-export of the existing lifecycle
 * (`../single-editor-save`) with the vocabulary app authors need. Behavior is
 * the host's: every write carries the lease generation and base version, the
 * draft is journaled before it is sent, a lost reply is `uncertain` and is
 * proven with `reconcile()` (identical bytes, never a blind resend), and a 409
 * leaves the draft in place. */
import type { NativeSaveState } from "../single-editor-save"

export { createSingleEditorSave, NativeSaveError as SaveError } from "../single-editor-save"
export type {
  NativeSaveFailure as SaveFailure,
  NativeSaveReceipt as SaveReceipt,
  NativeSaveRequest as SaveRequest,
  NativeSaveState as SaveState,
  SingleEditorSaveOptions as SaveOptions,
} from "../single-editor-save"

export type SavePhase = NativeSaveState["phase"]
export type SaveController = ReturnType<typeof import("../single-editor-save").createSingleEditorSave>
export const SAVE_PHASES: readonly SavePhase[] = Object.freeze(["idle", "dirty", "saving", "saved", "draft", "uncertain", "read-only", "stopped", "revoked"] as const)

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
    case "revoked": return "Přístup ke čtení byl odebrán"
    case "idle": return "Připraveno k úpravám"
    default: return "Neuložený koncept"
  }
}
/** True only when nothing local is unconfirmed: the app may close silently. */
export function saveSettled(state: NativeSaveState): boolean {
  return (state.phase === "saved" || state.phase === "idle") && !state.dirty
}
