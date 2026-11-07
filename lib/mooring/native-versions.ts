import {validNativeVersion} from "./native-draft"

/**
 * Native Word/Sheet version history (`GET .../craft/document/versions`).
 *
 * This module is the one parser for that wire contract. The experimental
 * Mooring SDK (`lib/mooring/sdk/versions`, `nativeCraftVersions`) adapts the
 * client's `versions`/`restore` methods built on it instead of parsing again.
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

/** One saved version with its bytes (`GET .../craft/document/versions/{version}`), read-only. */
export interface NativeVersionDocument {
 version:string;currentVersion:string;current:boolean;createdAt:string;author:NativeVersionAuthor;restoredFrom?:string
 applicationId:"wordcraft"|"gridcraft";enginePin:string;editorBundle:string;editorURL:string;bytes:Uint8Array
}
const pinned=(v:unknown):v is string=>typeof v==="string"&&/^[a-f0-9]{64}$/.test(v)

/** Null for anything that is not exactly the requested version; never a partial document. */
export function parseNativeVersionDocument(raw:unknown,expected:string):NativeVersionDocument|null {
 if(!object(raw)||!positive(raw.version)||raw.version!==expected||!positive(raw.currentVersion)||typeof raw.current!=="boolean"||raw.current!==(raw.version===raw.currentVersion)||BigInt(raw.version)>BigInt(raw.currentVersion))return null
 if(!text(raw.createdAt,64)||!object(raw.author)||raw.restoredFrom!==undefined&&!positive(raw.restoredFrom))return null
 const a=raw.author
 if(!text(a.actorId)||!text(a.displayName)||a.kind!=="human"&&a.kind!=="agent"&&a.kind!=="system")return null
 if(raw.applicationId!=="wordcraft"&&raw.applicationId!=="gridcraft"||!pinned(raw.enginePin)||!pinned(raw.editorBundle)||typeof raw.editorURL!=="string"||!raw.editorURL.endsWith(`/b/${raw.editorBundle}/index.html`))return null
 if(typeof raw.bytes!=="string"||!raw.bytes.length||raw.bytes.length>44739244||raw.bytes.length%4!==0)return null
 let bytes:Uint8Array
 try{
  const binary=atob(raw.bytes)
  if(!binary.length||binary.length>32*1024*1024||btoa(binary)!==raw.bytes)return null
  bytes=Uint8Array.from(binary,c=>c.charCodeAt(0))
 }catch{return null}
 return {version:raw.version,currentVersion:raw.currentVersion,current:raw.current,createdAt:raw.createdAt,author:{actorId:a.actorId,kind:a.kind,displayName:a.displayName},...(raw.restoredFrom?{restoredFrom:raw.restoredFrom as string}:{}),applicationId:raw.applicationId,enginePin:raw.enginePin,editorBundle:raw.editorBundle,editorURL:raw.editorURL,bytes}
}

export type NativeDiffLine={kind:"same"|"added"|"removed";text:string}
/**
 * Line comparison of two native documents for people, read-only. JSON is
 * pretty-printed first so a one-line document still compares line by line.
 * Null when the inputs are too large for the bounded table; the caller then
 * shows both texts side by side without a line diff.
 */
export function nativeVersionText(bytes:Uint8Array):string {
 const decoded=new TextDecoder("utf-8",{fatal:true}).decode(bytes)
 try{return JSON.stringify(JSON.parse(decoded),null,1)}catch{return decoded}
}
export function nativeLineDiff(before:string,after:string,maxCells=4_000_000):NativeDiffLine[]|null {
 const a=before.split("\n"),b=after.split("\n")
 let start=0
 while(start<a.length&&start<b.length&&a[start]===b[start])start++
 let endA=a.length,endB=b.length
 while(endA>start&&endB>start&&a[endA-1]===b[endB-1]){endA--;endB--}
 const x=a.slice(start,endA),y=b.slice(start,endB)
 if((x.length+1)*(y.length+1)>maxCells)return null
 const width=y.length+1,table=new Uint32Array((x.length+1)*width)
 for(let i=x.length-1;i>=0;i--)for(let j=y.length-1;j>=0;j--)table[i*width+j]=x[i]===y[j]?table[(i+1)*width+j+1]+1:Math.max(table[(i+1)*width+j],table[i*width+j+1])
 const out:NativeDiffLine[]=a.slice(0,start).map(text=>({kind:"same",text}))
 let i=0,j=0
 while(i<x.length||j<y.length){
  if(i<x.length&&j<y.length&&x[i]===y[j]){out.push({kind:"same",text:x[i]});i++;j++}
  else if(j<y.length&&(i===x.length||table[i*width+j+1]>=table[(i+1)*width+j])){out.push({kind:"added",text:y[j]});j++}
  else{out.push({kind:"removed",text:x[i]});i++}
 }
 for(const text of a.slice(endA))out.push({kind:"same",text})
 return out
}
