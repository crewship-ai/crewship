import {captureLocalDraftPage,purgeLocalDraftPage} from "../artifact-drafts"
import {nativeDraftReadRevoked,type LocalDraftScope} from "../local-draft-seal"

/** A refused native admission proves nothing by itself (`craftNativeHTTPError`
 * answers 403 for any fault). Only `draft_read_revoked` from `draft-key` does, so
 * ask it, and erase what this browser held when asking. Any other answer, a stall
 * or no network keeps the draft. No deadline here: a proof that arrives late is
 * still proof, and the captured revisions keep a later draft safe. `retire` may
 * answer false when the editor has verified read again since the question.
 * Returns whether read loss was proven and acted on. */
export async function verifyNativeReadLoss(scope:LocalDraftScope,retire?:()=>Promise<boolean|void>):Promise<boolean>{
 const captured=await captureLocalDraftPage(scope)
 if(!captured.local.length&&!captured.artifacts.length&&!captured.documents.length&&!retire)return false
 if(!await nativeDraftReadRevoked(scope).catch(()=>false))return false
 if(await retire?.()===false)return false
 await purgeLocalDraftPage(scope,captured)
 return true
}
