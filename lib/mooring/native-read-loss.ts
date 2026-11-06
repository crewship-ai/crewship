import {captureLocalDraftPage,purgeLocalDraftPage} from "../artifact-drafts"
import {forgetLocalDraftPage,nativeDraftRead,type LocalDraftScope} from "../local-draft-seal"

const confirmations=new Map<string,number>()
const key=(scope:LocalDraftScope)=>JSON.stringify([scope.human,scope.workspace,scope.page])
/** Any later proof of current read supersedes every question already asked, for
 * this Page in this tab, across editor remounts. Only ever counts up. */
export function confirmNativeRead(scope:LocalDraftScope){confirmations.set(key(scope),(confirmations.get(key(scope))??0)+1)}

/** A refused native admission proves nothing by itself (`craftNativeHTTPError`
 * answers 403 for any fault). Only `draft_read_revoked` from `draft-key` does, so
 * ask it, and erase what this browser held when asking. Any other answer, a stall
 * or no network keeps the draft. No deadline here: a proof that arrives late is
 * still proof, unless read was confirmed after it was asked. Then it does nothing
 * at all: no cipher closes and no journal is revoked. Captured revisions keep a
 * later draft safe. Returns whether read loss was proven and acted on. */
export async function verifyNativeReadLoss(scope:LocalDraftScope,retire?:()=>Promise<void>,signal?:AbortSignal):Promise<boolean>{
 const asked=confirmations.get(key(scope))??0
 const captured=await captureLocalDraftPage(scope)
 if(!captured.local.length&&!captured.artifacts.length&&!captured.documents.length&&!retire)return false
 const answer=await nativeDraftRead(scope,signal).catch(()=>"unknown" as const)
 if(signal?.aborted)return false
 if(answer==="readable")confirmNativeRead(scope)
 if(answer!=="revoked")return false
 if((confirmations.get(key(scope))??0)!==asked)return false
 forgetLocalDraftPage(scope)
 await retire?.()
 await purgeLocalDraftPage(scope,captured)
 return true
}
