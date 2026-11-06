import {captureLocalDraftPage,purgeLocalDraftPage} from "../artifact-drafts"
import {createLocalDraftCipher,LocalDraftReadRevoked,type LocalDraftScope} from "../local-draft-seal"
import {NATIVE_REAUTHORIZE_DEADLINE_MS} from "./native-revocation-bounds"

/** A refused native admission proves nothing by itself (`craftNativeHTTPError`
 * answers 403 for any fault). Only `draft_read_revoked` from `draft-key` does, so
 * ask it, and erase what this browser held when asking. Any other answer, a stall
 * or no network keeps the draft. Returns whether read loss was proven. */
export async function verifyNativeReadLoss(scope:LocalDraftScope,retire?:()=>Promise<void>):Promise<boolean>{
 const captured=await captureLocalDraftPage(scope)
 if(!captured.local.length&&!captured.artifacts.length&&!captured.documents.length&&!retire)return false
 let timer:ReturnType<typeof setTimeout>|undefined
 const revoked=await Promise.race([
  createLocalDraftCipher(scope).then(cipher=>{cipher.close();return false},(error:unknown)=>error instanceof LocalDraftReadRevoked),
  new Promise<boolean>(resolve=>{timer=setTimeout(()=>resolve(false),NATIVE_REAUTHORIZE_DEADLINE_MS)}),
 ]).finally(()=>clearTimeout(timer))
 if(!revoked)return false
 await retire?.()
 await purgeLocalDraftPage(scope,captured)
 return true
}
