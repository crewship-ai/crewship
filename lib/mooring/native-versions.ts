import {validNativeVersion} from "./native-draft"

/**
 * Native Word/Sheet version history (`GET .../craft/document/versions`).
 *
 * Local adapter: the Mooring SDK's `lib/mooring/sdk/versions` (PR #4723) is not
 * on main yet. When it lands, this parser and the client's `versions`/`restore`
 * methods should move onto it; the wire contract is the same server route.
 */
export const CRAFT_VERSION_HISTORY_FLAG = "craft_version_history"

export interface NativeVersionAuthor {actorId:string;kind:"human"|"agent"|"system";displayName:string}
export interface NativeVersion {version:string;createdAt:string;author:NativeVersionAuthor;restoredFrom?:string;current:boolean}
export interface NativeVersionList {currentVersion:string;versions:NativeVersion[];truncated:boolean}
/** The fenced restore binding, identical to a save's: holder session, generation and base version. */
export interface NativeRestoreRequest {
 workspaceId:string;pageId:string;applicationId:string;enginePin:string;sessionId:string;generation:string;baseVersion:string;targetVersion:string
}
export interface NativeRestoreReceipt {version:string;durability:"node";issuedBy:"crewship";phase:"committed"}

/** Effective value of one flag in `GET /api/v1/feature-flags`: the workspace override wins. */
export function nativeFlagEnabled(body:unknown,key:string):boolean {
 if(!Array.isArray(body))return false
 const flag=body.find(item=>object(item)&&item.key===key)
 if(!object(flag))return false
 if(typeof flag.override_enabled==="boolean")return flag.override_enabled
 return flag.enabled===true
}
const object=(v:unknown):v is Record<string,unknown>=>Boolean(v)&&typeof v==="object"&&!Array.isArray(v)
const text=(v:unknown,max=512):v is string=>typeof v==="string"&&v.length>0&&v.length<=max&&!v.includes("\0")
const positive=(v:unknown):v is string=>validNativeVersion(v)&&v!=="0"

/** Null for anything that is not exactly a history listing; a partial list is never shown. */
export function parseNativeVersionList(raw:unknown):NativeVersionList|null {
 if(!object(raw)||!positive(raw.currentVersion)||typeof raw.truncated!=="boolean"||!Array.isArray(raw.versions)||raw.versions.length===0||raw.versions.length>200)return null
 const versions:NativeVersion[]=[]
 for(const item of raw.versions){
  if(!object(item)||!positive(item.version)||!text(item.createdAt,64)||typeof item.current!=="boolean"||!object(item.author))return null
  const a=item.author
  if(!text(a.actorId)||!text(a.displayName)||a.kind!=="human"&&a.kind!=="agent"&&a.kind!=="system")return null
  if(item.restoredFrom!==undefined&&!positive(item.restoredFrom))return null
  versions.push({version:item.version,createdAt:item.createdAt,author:{actorId:a.actorId,kind:a.kind,displayName:a.displayName},current:item.current,...(item.restoredFrom?{restoredFrom:item.restoredFrom as string}:{})})
 }
 // Newest first, exactly one current entry and it is the newest.
 if(versions[0].version!==raw.currentVersion||versions.some((v,i)=>v.current!==(i===0)))return null
 for(let i=1;i<versions.length;i++)if(BigInt(versions[i].version)>=BigInt(versions[i-1].version))return null
 return {currentVersion:raw.currentVersion,versions,truncated:raw.truncated}
}

export function parseNativeRestoreReceipt(raw:unknown):NativeRestoreReceipt|null {
 if(!object(raw)||Object.keys(raw).length!==4||!positive(raw.version)||raw.durability!=="node"||raw.issuedBy!=="crewship"||raw.phase!=="committed")return null
 return raw as unknown as NativeRestoreReceipt
}
