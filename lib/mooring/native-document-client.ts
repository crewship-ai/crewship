import {GZIP_THRESHOLD,base64Parts,gzipBlob} from "./native-compact"
import { engineBuildToken } from "./native-editor-port"
import { apiFetch,tryRefresh,type RefreshResult } from "../api-fetch"
import {getAuthMode} from "../server-base"
import {MAX_NATIVE_BYTES,validNativeDraft,validNativeVersion,type NativeFileFormat} from "./native-draft"
import {NativeSaveError,type NativeSaveRequest} from "./single-editor-save"
import {nativeAppOffFrom} from "./native-app-off"
import {NATIVE_REAUTHORIZE_DEADLINE_MS} from "./native-revocation-bounds"
import {parseNativeRestoreReceipt,parseNativeVersionDocument,parseNativeVersionList,type NativeRestoreReceipt,type NativeRestoreRequest,type NativeVersionDocument,type NativeVersionList} from "./native-versions"

export interface NativeDocumentClientOptions {
 workspaceId:string;pageId:string;fetch?:typeof fetch
 /** Transport fixtures supply their own credential rotation; production uses tryRefresh. */
 refresh?:()=>Promise<RefreshResult>;bearer?:()=>boolean
}
export interface NativeDocumentSnapshot {
 version:string;bytes:Uint8Array;legacySource?:boolean;applicationId:"wordcraft"|"gridcraft";enginePin:string;editorBundle:string;editorURL:string
 /** The engine build the server runs for this Sheet (engineBuild); absent when the package does not name it. */
 engineBuild?:string
 /** The stored file format for a workbook Sheet. */
 format?:NativeFileFormat;fileName?:string
 /** `level` (near 80 %, critical 95 %, full) when the server reports it; older servers send only `warning`. */
 quota:{usedBytes:string;limitBytes:string;warning:boolean;retention:string;level?:"none"|"near"|"critical"|"full"}
}
export interface NativeShareRequest {
 workspaceId:string;pageId:string;applicationId:string;enginePin:string;sessionId:string;generation:string;baseVersion:string;targetVersion:string
}
export interface NativeShareReceipt {version:string;durability:'node';issuedBy:'crewship';phase:'shared'}
/** An agent's candidate prepared while a person holds the lease (#4480). */
export interface NativeProposal {id:string;authorName:string;baseVersion:string;summary:string;createdAt:string;state:'open'|'accepted'|'rejected'|'stale';resultVersion?:string}
const PROPOSAL_ID=/^ncp_[a-f0-9]{24}$/
function nativeProposal(raw:unknown):NativeProposal|null{
 if(!raw||typeof raw!=='object'||Array.isArray(raw))return null
 const v=raw as Record<string,unknown>,author=v.author as Record<string,unknown>|undefined
 if(typeof v.id!=='string'||!PROPOSAL_ID.test(v.id)||!Number.isSafeInteger(v.base_version)||(v.base_version as number)<1||typeof v.summary!=='string'||typeof v.created_at!=='string'||!['open','accepted','rejected','stale'].includes(v.state as string)||!author||author.kind!=='agent'||typeof author.display_name!=='string')return null
 return {id:v.id,authorName:author.display_name,baseVersion:String(v.base_version),summary:v.summary,createdAt:v.created_at,state:v.state as NativeProposal['state'],...(Number.isSafeInteger(v.result_version)?{resultVersion:String(v.result_version)}:{})}
}
export class LegacyWorkbookError extends Error {
 constructor(readonly code?: string) { super("legacy_workbook_unavailable") }
}
export interface NativeDocumentClient {
 open(signal: AbortSignal): Promise<{sessionId: string; applicationId: string}>
 read(signal:AbortSignal):Promise<NativeDocumentSnapshot>
 originalWorkbook?(signal:AbortSignal):Promise<Uint8Array>
 events(signal:AbortSignal,onEvent:(event:"lease"|"session_retired",value:Record<string,unknown>)=>void):Promise<void>
 reauthorize(signal:AbortSignal):Promise<boolean>
 lease(signal:AbortSignal):Promise<unknown>
 leaseAction(action:"acquire"|"release"|"request-handoff"|"handoff",body:{generation?:string;request_id?:string},signal:AbortSignal):Promise<unknown>
 share(request:NativeShareRequest,signal:AbortSignal):Promise<NativeShareReceipt>
 /** Undo of an agent's change under this session and its lease generation: the server's response as it is. */
 undo(operationId:string,generation:string,signal:AbortSignal):Promise<Response>
 proposals(signal:AbortSignal):Promise<NativeProposal[]>
 proposalBytes(id:string,signal:AbortSignal):Promise<Uint8Array>
 /** Accept is this admission's own fenced save; a "stale" result changed nothing. */
 decideProposal(id:string,decision:'accept'|'reject',generation:string|undefined,signal:AbortSignal):Promise<NativeProposal>
 /** Read-only history under this admission's Read authority; null while the workspace flag is off (404). */
 versions(signal:AbortSignal):Promise<NativeVersionList|null>
 /** One saved version's bytes under this admission's Read authority, read-only; never a lease or a write. */
 version(version:string,signal:AbortSignal):Promise<NativeVersionDocument>
 /** The Sheet as an .xlsx file under this admission's Read authority: the confirmed head, or a saved version. */
 exportXlsx(signal:AbortSignal,version?:string):Promise<NativeExport>
 /** The existing fenced restore: holder, generation and base version, like a save. Appends a new version. */
 restore(request:NativeRestoreRequest,signal:AbortSignal):Promise<NativeRestoreReceipt>
 save(request: NativeSaveRequest,signal: AbortSignal): Promise<unknown>
 reconcile(request: NativeSaveRequest,signal: AbortSignal): Promise<unknown>
 close(): void
}
export interface NativeExport {blob:Blob;filename:string;version:string}
export type NativeExportReason="bad_version"|"forbidden"|"not_found"|"too_large"|"not_sheet"|"failed"|"busy"
/** A refused or failed xlsx export; `busy` carries the server's Retry-After. */
export class NativeExportError extends Error {
 constructor(readonly reason:NativeExportReason,readonly retryAfterSeconds?:number){super(`native_export_${reason}`)}
}
const EXPORT_VERSION=/^[1-9]\d{0,17}$/
const EXPORT_STATUS:Record<number,NativeExportReason>={400:"bad_version",401:"forbidden",403:"forbidden",404:"not_found",413:"too_large",422:"not_sheet",500:"failed",503:"busy"}
/** The download name from Content-Disposition: filename* (UTF-8) before filename, never a path or a control character. */
export function nativeExportFilename(disposition:string|null):string{
 let name=""
 const exact=/filename\*\s*=\s*UTF-8''([^;]+)/i.exec(disposition??"")
 if(exact){try{name=decodeURIComponent((exact[1]??"").trim())}catch{name=""}}
 if(!name&&!exact){const plain=/filename\s*=\s*"([^"]*)"|filename\s*=\s*([^;\s]+)/i.exec(disposition??"");name=plain?.[1]??plain?.[2]??""}
 // eslint-disable-next-line no-control-regex
 name=name.replace(/[\\/\u0000-\u001f\u007f]/g,"_").trim()
 if(!name||/^\.+$/.test(name))return "Sheet.xlsx"
 return /\.xlsx$/i.test(name)?name:name+".xlsx"
}
// Only network I/O and explicitly temporary HTTP failures can reconnect.
// Invalid metadata, changed authority and callback failures remain terminal.
export class NativeMetadataTransportError extends Error {}
/** X-Craft-Touched: the addresses as JSON, every character outside ASCII escaped, so a sheet name of any
 * script fits a header; or unknown. */
function touchedHeader(touched:string[]|"unknown"):string{
 if(touched==="unknown")return touched
 const ascii=JSON.stringify(touched).replace(/[\u007f-\uffff]/g,c=>"\\u"+c.charCodeAt(0).toString(16).padStart(4,"0"))
 // Match the server's 64 KiB cap against the ASCII bytes that Fetch actually sends.
 return ascii.length<=64*1024?ascii:"unknown"
}
/** Create version 1 from an Excel upload and optionally link it to a chat atomically. */
export async function initializeNativeWorkbook(args:{workspaceId:string;pageId:string;file:Blob;chatId?:string;fetch?:typeof fetch;signal?:AbortSignal}):Promise<Response>{
 const {workspaceId,pageId,file,chatId,signal}=args
 if(!identifier(workspaceId)||!identifier(pageId)||!(file instanceof Blob)||!file.size||(chatId!==undefined&&!/^[A-Za-z0-9_-]{1,128}$/.test(chatId)))throw new Error("invalid_native_workbook")
 if(file.size>MAX_NATIVE_BYTES)throw new Error("native_workbook_too_large")
 const send=args.fetch??((input,init)=>apiFetch(input,{...init,skipRefresh:true}))
 return send(`/api/v1/pages/${encodeURIComponent("@"+pageId)}/craft/initialize?${new URLSearchParams({workspace_id:workspaceId})}`,{method:"POST",redirect:"error",cache:"no-store",signal,headers:{"Content-Type":"application/json",...(chatId===undefined?{}:{"X-Craft-Chat":chatId})},body:JSON.stringify({application_id:"gridcraft",bytes:nativeBase64(new Uint8Array(await file.arrayBuffer()))})})
}
/** The server's refusal code (`{code}`), or "". */
const refusalCode=async(response:Response)=>{try{const v:unknown=await response.clone().json();return v&&typeof v==="object"&&typeof (v as {code?:unknown}).code==="string"?(v as {code:string}).code:""}catch{return ""}}
const identifier=(value:unknown):value is string=>typeof value==="string"&&value.length>0&&value.length<=256&&!value.includes("\0")
const nativeBase64=(bytes:Uint8Array)=>{
 let binary="";for(let i=0;i<bytes.length;i+=32768)binary+=String.fromCharCode(...bytes.subarray(i,i+32768));return btoa(binary)
}
/** Admission tokens stay in this one mounted lifecycle, never in draft data or
 * iframe messages. Ambiguous mutations are never retried by this transport. */
export function createNativeDocumentClient(options: NativeDocumentClientOptions): NativeDocumentClient {
 options={...options}
 if(!identifier(options.workspaceId)||!identifier(options.pageId))throw new Error("invalid_native_scope")
 const transport:typeof fetch=options.fetch??((input,init)=>apiFetch(input,{...init,skipRefresh:true}))
 // A native mutation has one attempt. Redirects and automatic auth replay
 // would cross the private admission boundary or repeat an unknown commit.
 const send:typeof fetch=(input,init)=>transport(input,{...init,redirect:"error"})
 const base=`/api/v1/pages/${encodeURIComponent("@"+options.pageId)}/craft/`
 const query=new URLSearchParams({workspace_id:options.workspaceId}).toString()
 let active=true,token:string|undefined,sessionId:string|undefined,applicationId:string|undefined
 let opening:Promise<{sessionId:string;applicationId:string}>|undefined,reauthorizing:Promise<boolean>|undefined,reauthorizeAbort:AbortController|undefined
 const refresh=options.refresh??(options.fetch?undefined:tryRefresh),bearer=options.bearer??(()=>getAuthMode()==='bearer')
 let cookieRefreshedAt:number|undefined,rotation=0,eventRunning=false,eventRecovering=false,eventAbort:AbortController|undefined
 const rotationReason=new Error('native_credential_rotated')
 async function freshCookie(signal:AbortSignal){
  if(!refresh||bearer())return
  const now=Date.now()
  // Refresh well before the 15-minute access horizon, including the FIRST
  // admission. A clock correction must not postpone the next refresh forever.
  if(cookieRefreshedAt!==undefined&&now>=cookieRefreshedAt&&now-cookieRefreshedAt<5*60*1000)return
  const result=await refresh()
  if(!active||signal.aborted)throw new Error('native_client_unavailable')
  if(result==='retryable_failed')throw new NativeMetadataTransportError('native_auth_refresh_unconfirmed')
  if(result!=='ok')throw new Error('native_auth_refresh_unconfirmed')
  cookieRefreshedAt=Date.now()
 }
 async function reconnectPause(signal:AbortSignal,delay=100){
  await new Promise<void>((resolve,reject)=>{
   const done=()=>{signal.removeEventListener('abort',abort);resolve()},timer=setTimeout(done,delay)
   const abort=()=>{clearTimeout(timer);signal.removeEventListener('abort',abort);reject(signal.reason??new Error('native_stream_aborted'))}
   signal.addEventListener('abort',abort,{once:true});if(signal.aborted)abort()
  })
 }
 const ready=()=>{if(!active||!token||!sessionId||!applicationId)throw new Error("native_client_unavailable")}
 async function mutate(operation:"save"|"reconcile",request:NativeSaveRequest,signal:AbortSignal):Promise<unknown>{
  ready()
  if(!validNativeDraft(request)||request.baseVersion==="0"||request.workspaceId!==options.workspaceId||request.pageId!==options.pageId||request.sessionId!==sessionId||request.applicationId!==applicationId||!/^sha256:[a-f0-9]{64}$/.test(request.contentHash))throw new Error("invalid_native_snapshot_binding")
  const headers:Record<string,string>={"Content-Type":"application/json","X-Craft-Session":token!}
  if(operation==="save"&&request.touched!==undefined)headers["X-Craft-Touched"]=touchedHeader(request.touched)
  if(operation==="save"&&request.rebase==="keep-local")headers["X-Craft-Rebase"]="keep-local"
  let body:BodyInit
  // Reconcile asks about an attempt by its hash; the server never reads its bytes.
  if(operation==="reconcile")body=JSON.stringify({...request,bytes:undefined,format:undefined,rebase:undefined,touched:undefined})
  else if(request.bytes.byteLength*4/3<=GZIP_THRESHOLD)body=JSON.stringify({...request,bytes:nativeBase64(request.bytes),format:undefined,rebase:undefined,touched:undefined})
  else{
   // A large document: the JSON is assembled as Blob parts (no 40 MB string), compressed
   // by the browser's own stream (about 15:1 for a spreadsheet) and declared with its
   // inflated length, which the server enforces. A browser without CompressionStream
   // sends the same Blob uncompressed.
   const marker="\u0000BYTES\u0000",json=JSON.stringify({...request,bytes:marker,format:undefined,rebase:undefined,touched:undefined}),at=json.indexOf(JSON.stringify(marker))
   if(at<0)throw new Error("invalid_native_snapshot_binding")
   const plain=new Blob([json.slice(0,at),'"',...base64Parts(request.bytes),'"',json.slice(at+JSON.stringify(marker).length)],{type:"application/json"})
   const packed=await gzipBlob(plain)
   if(packed&&packed.length<plain.size){body=packed as unknown as BodyInit;headers["Content-Encoding"]="gzip";headers["X-Craft-Uncompressed-Length"]=String(plain.size)}
   else body=plain
  }
  const response=await send(`${base}document/${operation}?${query}`,{method:"POST",cache:"no-store",signal,headers,body})
  ready()
  // Only reconcile answers 410: the server retired that attempt; it never commits (#4899).
  if(operation==="reconcile"&&response.status===410)throw new NativeSaveError("absent")
  // Refused because the Sheet has a newer version: told apart, so the editor can replay it.
  if(response.status===409)throw new NativeSaveError(operation==="save"&&await refusalCode(response)==="moved"?"moved":"conflict")
  // The file itself was refused: not a workbook, encrypted, or another format.
  if(response.status===415)throw new NativeSaveError("unsupported")
  // The file would lose or change macros, signatures or customXml; nothing was stored.
  if(response.status===422&&operation==="save"&&await refusalCode(response)==="fidelity_loss")throw new NativeSaveError("fidelity_loss")
  if(response.status===413){
   // The server says "quota" for a full version history; any other 413 (the body cap, a proxy in
   // front of the server) means the document itself is too large to send, which is not a quota.
   let detail="",code="";try{const body:unknown=await response.clone().json();if(body&&typeof body==="object"&&typeof (body as {code?:unknown}).code==="string")code=(body as {code:string}).code;if(typeof body==="object"&&body!==null&&typeof (body as {detail?:unknown}).detail==="string")detail=(body as {detail:string}).detail;else if(typeof body==="object"&&body!==null&&typeof (body as {error?:unknown}).error==="string")detail=(body as {error:string}).error}catch{/* a proxy page */}
   throw new NativeSaveError(code==="quota"||/quota/i.test(detail)?"quota":"too_large")
  }
  // A refused bearer or write is insufficient evidence to erase readable data.
  // Only the independently authenticated lifecycle's explicit read loss purges.
  if(response.status===401||response.status===403)throw new NativeSaveError("write_revoked")
  if(!response.ok)throw new Error("native_snapshot_unconfirmed")
  const value:unknown=await response.json();ready();return value
 }
 const client:NativeDocumentClient={
  open(signal){
   if(!active)return Promise.reject(new Error("native_client_unavailable"))
   if(!opening)opening=(async()=>{
    await freshCookie(signal)
    if(!active||signal.aborted)throw new Error('native_client_unavailable')
    const response=await send(`${base}sessions?${query}`,{method:"POST",cache:"no-store",signal,headers:{"Content-Type":"application/json"},body:"{}"})
    if(!active||!response.ok)throw new Error("native_session_unavailable")
    const value:unknown=await response.json()
    if(!active||!value||typeof value!=="object")throw new Error("invalid_native_session")
    const v=value as Record<string,unknown>
    if(typeof v.session_token!=="string"||!/^[a-f0-9]{64}$/.test(v.session_token)||!identifier(v.session_id)||v.page_id!==options.pageId||(v.application_id!=="wordcraft"&&v.application_id!=="gridcraft"))throw new Error("invalid_native_session")
    token=v.session_token;sessionId=v.session_id;applicationId=v.application_id
    return {sessionId,applicationId}
   })()
   return opening
  },
  async undo(operationId,generation,signal){
   ready()
   if(typeof operationId!=='string'||!/^[A-Za-z0-9_.:-]{1,128}$/.test(operationId)||!validNativeVersion(generation))throw new Error('native_undo_invalid')
   const response=await send(`${base}document/undo?${query}`,{method:'POST',cache:'no-store',signal,headers:{'Content-Type':'application/json','X-Craft-Session':token!,'X-Craft-Generation':generation},body:JSON.stringify({operation_id:operationId})})
   ready();return response
  },
  async share(request,signal){
   ready()
   if(!request||Object.keys(request).length!==8||request.workspaceId!==options.workspaceId||request.pageId!==options.pageId||request.applicationId!==applicationId||request.sessionId!==sessionId||!/^[a-f0-9]{64}$/.test(request.enginePin)||![request.generation,request.baseVersion,request.targetVersion].every(v=>validNativeVersion(v)&&v!=='0'))throw new Error('invalid_native_share_binding')
   const response=await send(`${base}document/share?${query}`,{method:'POST',cache:'no-store',signal,headers:{'Content-Type':'application/json','X-Craft-Session':token!},body:JSON.stringify({...request})})
   ready()
   if(response.status===409)throw new NativeSaveError('conflict')
   if(response.status===401||response.status===403)throw new NativeSaveError('write_revoked')
   if(!response.ok)throw new Error('native_share_unconfirmed')
   const value:unknown=await response.json();ready()
   if(!value||typeof value!=='object'||Array.isArray(value))throw new Error('native_share_unconfirmed')
   const v=value as Record<string,unknown>
   if(Object.keys(v).length!==4||v.version!==request.targetVersion||v.durability!=='node'||v.issuedBy!=='crewship'||v.phase!=='shared')throw new Error('native_share_unconfirmed')
   return v as unknown as NativeShareReceipt
  },
  async lease(signal){
   ready();const response=await send(`${base}lease?${query}`,{method:"GET",cache:"no-store",signal,headers:{"X-Craft-Session":token!}})
   ready();if(!response.ok)throw new Error("native_lease_unavailable")
   const value:unknown=await response.json();ready();return value
  },
  async leaseAction(action,body,signal){
   ready()
   if(!["acquire","release","request-handoff","handoff"].includes(action)||!body||Object.keys(body).some(key=>key!=="generation"&&key!=="request_id")||(action==="release"||action==="handoff")&&(!validNativeVersion(body.generation)||body.generation==="0")||action==="handoff"&&!identifier(body.request_id))throw new Error("invalid_native_lease_action")
   const response=await send(`${base}lease/${action}?${query}`,{method:"POST",cache:"no-store",signal,headers:{"Content-Type":"application/json","X-Craft-Session":token!},body:JSON.stringify({...body})})
   ready()
   // The administrator turned the application off or it is draining (#5381).
   const off=await nativeAppOffFrom(response,applicationId);ready();if(off)throw off
   if(response.status===409)throw new NativeSaveError("conflict")
   if(response.status===401||response.status===403)throw new NativeSaveError("write_revoked")
   if(!response.ok)throw new Error("native_lease_unconfirmed")
   const value:unknown=await response.json();ready();return value
  },
  async reauthorize(signal){
   ready();if(reauthorizing)return reauthorizing
   const controller=new AbortController(),originalSignal=signal
   reauthorizeAbort=controller
   const abort=()=>controller.abort(originalSignal.reason)
   originalSignal.addEventListener('abort',abort,{once:true});if(originalSignal.aborted)abort()
   signal=controller.signal
   // No answer counts as a failed attempt: a stalled request must not hide a revocation.
   const deadline=setTimeout(()=>controller.abort(new NativeMetadataTransportError('native_reauthorization_deadline')),NATIVE_REAUTHORIZE_DEADLINE_MS)
   const operation=(async()=>{
   ready();await freshCookie(signal);ready()
   let response:Response
   try{response=await send(`${base}sessions/reauthorize?${query}`,{method:"POST",cache:"no-store",signal,headers:{"Content-Type":"application/json","X-Craft-Session":token!},body:"{}"})}
   catch(error){if(signal.aborted)throw error;throw new NativeMetadataTransportError("native_session_unavailable")}
   ready()
   if([408,429,500,502,503,504].includes(response.status))throw new NativeMetadataTransportError("native_session_unavailable")
   if(!response.ok)throw new Error("native_session_unavailable")
   const value:unknown=await response.json();ready()
   if(!value||typeof value!=="object"||typeof (value as Record<string,unknown>).renewed!=="boolean")throw new Error("invalid_native_session")
   const renewed=(value as {renewed:boolean}).renewed
   if(renewed){
    // The HTTP middleware correctly retains the old request's JWT horizon.
    // Replace ONLY that GET after fresh same-identity admission renewal.
    rotation++;if(!eventRecovering)eventAbort?.abort(rotationReason)
   }
   return renewed

   })()
   // close() also settles a coalesced recovery while credential refresh is
   // pending. Its late continuation still checks active/signal before I/O.
   const pending=new Promise<boolean>((resolve,reject)=>{
    const stopped=()=>reject(signal.reason??new Error('native_client_unavailable'))
    signal.addEventListener('abort',stopped,{once:true})
    operation.then(resolve,reject).finally(()=>signal.removeEventListener('abort',stopped))
    if(signal.aborted)stopped()
   })
   reauthorizing=pending
   try{return await pending}finally{
    clearTimeout(deadline)
    originalSignal.removeEventListener('abort',abort)
    if(reauthorizing===pending)reauthorizing=undefined
    if(reauthorizeAbort===controller)reauthorizeAbort=undefined
   }
  },
  async events(signal,onEvent){
   ready()
   if(eventRunning)throw new Error('native_stream_already_active')
   eventRunning=true
   let reconnecting=false,transportRecovery=false,retries=0,networkRetries=0
   try{
    while(active&&!signal.aborted){
     const controller=new AbortController(),startedRotation=rotation
     eventAbort=controller
     let verifiedLease=false,streamStarted:number|undefined
     const abort=()=>controller.abort(signal.reason)
     signal.addEventListener('abort',abort,{once:true});if(signal.aborted)abort()
     try{
      let response:Response
      try{response=await send(`${base}events?${query}`,{method:'GET',cache:'no-store',signal:controller.signal,headers:{Accept:'text/event-stream','X-Craft-Session':token!}})}
      catch(error){if(controller.signal.aborted)throw error;throw new NativeMetadataTransportError('native_stream_unavailable')}
      ready();if(controller.signal.aborted)throw controller.signal.reason
      // A reset may leave the old server stream alive for several seconds.
      // Network recovery uses its shared exponential budget; planned credential
      // rotation keeps the short bounded handoff grace.
      if(response.status===409&&reconnecting){
       await response.body?.cancel().catch(()=>{})
       if(transportRecovery)throw new NativeMetadataTransportError('native_stream_unavailable')
       if(retries<8){retries++;await reconnectPause(controller.signal);continue}
      }
      if([408,429,500,502,503,504].includes(response.status)){await response.body?.cancel().catch(()=>{});throw new NativeMetadataTransportError('native_stream_unavailable')}
      if(!response.ok||!response.headers.get('Content-Type')?.startsWith('text/event-stream')||!response.body)throw new Error('native_stream_unavailable')
      retries=0;reconnecting=false;transportRecovery=false;streamStarted=Date.now()
      const reader=response.body.getReader(),decoder=new TextDecoder('utf-8',{fatal:true});let buffer=''
      try{
       while(true){
        const {done,value}=await reader.read().catch(error=>{if(controller.signal.aborted)throw error;throw new NativeMetadataTransportError('native_stream_disconnected')});ready();if(controller.signal.aborted)throw controller.signal.reason
        if(value&&value.byteLength>65536)throw new Error('invalid_native_stream')
        buffer+=(done?decoder.decode():decoder.decode(value,{stream:true}));buffer=buffer.replace(/\r\n/g,'\n')
        if(buffer.length>65536)throw new Error('invalid_native_stream')
        let boundary:number
        while((boundary=buffer.indexOf('\n\n'))>=0){
         const block=buffer.slice(0,boundary);buffer=buffer.slice(boundary+2)
         if(!block||block.startsWith(':'))continue
         let event='',data=''
         for(const line of block.split('\n')){
          if(line.startsWith(':'))continue
          if(line.startsWith('event: ')&&!event)event=line.slice(7)
          else if(line.startsWith('data: ')&&!data)data=line.slice(6)
          else throw new Error('invalid_native_stream')
         }
         if(event!=='lease'&&event!=='session_retired')throw new Error('invalid_native_stream')
         const v:unknown=JSON.parse(data)
         if(!v||typeof v!=='object'||Array.isArray(v))throw new Error('invalid_native_stream')
         const record=v as Record<string,unknown>
         if(event==='session_retired'&&(record.delivery!=='stopped'||!['purge','retain'].includes(String(record.draft))||typeof record.reason!=='string'))throw new Error('invalid_native_stream')
         ready();if(event==='lease')verifiedLease=true;onEvent(event,record)
         if(event==='session_retired')return
        }
        if(done){if(buffer.trim())throw new Error('invalid_native_stream');throw new NativeMetadataTransportError('native_stream_disconnected')}
       }
      }finally{await reader.cancel().catch(()=>{});reader.releaseLock()}
     }catch(error){
      const aborted=error===rotationReason||typeof error==='object'&&error!==null&&'name'in error&&error.name==='AbortError'
      if(active&&!signal.aborted&&controller.signal.reason===rotationReason&&rotation!==startedRotation&&aborted){reconnecting=true;retries=0;continue}
      if(!(error instanceof NativeMetadataTransportError)||!active||signal.aborted||controller.signal.aborted)throw error
      // Recover only metadata for this exact admission. A lost save response
      // never reaches this path, and no acquire/session creation is replayed.
      // A valid initial lease alone is not evidence of a stable connection:
      // short-lived streams must not reset the retry budget indefinitely.
      if(verifiedLease&&streamStarted!==undefined&&Date.now()-streamStarted>=5000)networkRetries=0
      eventRecovering=true
      let authorized=false
      while(!authorized){
       if(networkRetries>=8)throw new Error('native_stream_unavailable')
       await reconnectPause(controller.signal,Math.min(100*2**networkRetries++,5000))
       ready()
       // The response is already cancelled; eventRecovering suppresses only
       // rotation aborts. close() must still cancel this authorization fetch.
       try{await client.reauthorize(controller.signal);ready();if(controller.signal.aborted)throw controller.signal.reason;authorized=true}
       catch(renewalError){if(!(renewalError instanceof NativeMetadataTransportError)||!active||signal.aborted||controller.signal.aborted)throw renewalError}
      }
      reconnecting=true;transportRecovery=true;retries=0
     }finally{eventRecovering=false;signal.removeEventListener('abort',abort);if(eventAbort===controller)eventAbort=undefined}
    }
    ready();throw new Error('native_stream_aborted')
   }finally{eventRunning=false}
  },
  async read(signal){
   ready()
   const read=()=>send(`${base}document?${query}`,{method:"GET",cache:"no-store",signal,headers:{"X-Craft-Session":token!}})
   let response=await read()
   ready()
   if(applicationId==="gridcraft"&&response.status===409){
    const reason=await response.json().catch(()=>null)
    if(reason?.code==="legacy_workbook"){
     ready()
     const result=await send(`${base}document/migrate-workbook?${query}`,{method:"POST",cache:"no-store",signal,headers:{"X-Craft-Session":token!}})
     ready()
     if(!result.ok){
      const refused=await result.json().catch(()=>null)
      throw new LegacyWorkbookError(typeof refused?.code==="string"?refused.code:result.status===403?"legacy_workbook_needs_editor":undefined)
     }
     response=await read()
     ready()
    }
   }
   // The admission stays valid: 403 here means no private read, not read loss.
   if(response.status===403)throw new Error("native_private_read_refused")
   if(!response.ok)throw new Error("native_document_unavailable")
   const value:unknown=await response.json();ready()
   if(!value||typeof value!=="object")throw new Error("invalid_native_document")
   const v=value as Record<string,unknown>,q=v.quota as Record<string,unknown>|undefined
   if(!validNativeVersion(v.version)||v.version==="0"||v.applicationId!==applicationId||typeof v.enginePin!=="string"||!/^[a-f0-9]{64}$/.test(v.enginePin)||typeof v.editorBundle!=="string"||!/^[a-f0-9]{64}$/.test(v.editorBundle)||typeof v.editorURL!=="string"||!v.editorURL.endsWith(`/b/${v.editorBundle}/index.html`)||typeof v.bytes!=="string"||!v.bytes.length||v.bytes.length>44739244||!q||!validNativeVersion(q.usedBytes)||!validNativeVersion(q.limitBytes)||q.limitBytes==="0"||typeof q.warning!=="boolean"||typeof q.retention!=="string")throw new Error("invalid_native_document")
   if(v.format!==undefined&&(applicationId!=="gridcraft"||v.format!=="xlsx"&&v.format!=="xlsm"))throw new Error("invalid_native_document")
   let bytes:Uint8Array
   try {const binary=atob(v.bytes);if(!binary.length||binary.length>32*1024*1024)throw new Error();bytes=Uint8Array.from(binary,c=>c.charCodeAt(0))}catch{throw new Error("invalid_native_document")}
   if(v.format!==undefined&&(bytes[0]!==0x50||bytes[1]!==0x4b))throw new Error("invalid_native_document")
   return {version:v.version,bytes,...(v.legacySource===true?{legacySource:true}:{}),...(v.format==="xlsx"||v.format==="xlsm"?{format:v.format}:{}),applicationId:applicationId as "wordcraft"|"gridcraft",enginePin:v.enginePin,...(engineBuildToken(v.engineBuild)?{engineBuild:v.engineBuild}:{}),editorBundle:v.editorBundle,editorURL:v.editorURL,quota:{usedBytes:q.usedBytes,limitBytes:q.limitBytes,warning:q.warning,retention:q.retention,...(q.level==="none"||q.level==="near"||q.level==="critical"||q.level==="full"?{level:q.level}:{})}}
  },
  async originalWorkbook(signal){
   ready()
   const response=await send(`${base}document/original-workbook?${query}`,{method:"GET",cache:"no-store",signal,headers:{"X-Craft-Session":token!}})
   ready()
   if(!response.ok||!response.headers.get("content-disposition")?.startsWith("attachment"))throw new Error("original_workbook_unavailable")
   const bytes=new Uint8Array(await response.arrayBuffer())
   ready()
   if(!bytes.length||bytes.length>32*1024*1024)throw new Error("invalid_original_workbook")
   return bytes
  },
  async proposals(signal){
   ready();const response=await send(`${base}proposals?${query}`,{method:'GET',cache:'no-store',signal,headers:{'X-Craft-Session':token!}})
   ready();if(!response.ok)throw new Error('native_proposals_unavailable')
   const value=await response.json() as {proposals?:unknown};ready()
   if(!value||!Array.isArray(value.proposals))throw new Error('native_proposals_unavailable')
   return value.proposals.map(nativeProposal).filter((p):p is NativeProposal=>p!==null)
  },
  async proposalBytes(id,signal){
   ready();if(!PROPOSAL_ID.test(id))throw new Error('invalid_native_proposal')
   const response=await send(`${base}proposals/${id}?${query}`,{method:'GET',cache:'no-store',signal,headers:{'X-Craft-Session':token!}})
   ready();if(!response.ok)throw new Error('native_proposal_unavailable')
   const value=await response.json() as {bytes?:unknown};ready()
   if(typeof value?.bytes!=='string'||!value.bytes.length||value.bytes.length>44739244)throw new Error('invalid_native_proposal')
   const binary=atob(value.bytes);return Uint8Array.from(binary,c=>c.charCodeAt(0))
  },
  async decideProposal(id,decision,generation,signal){
   ready()
   if(!PROPOSAL_ID.test(id)||decision==='accept'&&(!validNativeVersion(generation)||generation==='0'))throw new Error('invalid_native_proposal')
   const body=decision==='accept'?{sessionId,generation}:{}
   const response=await send(`${base}proposals/${id}/${decision}?${query}`,{method:'POST',cache:'no-store',signal,headers:{'Content-Type':'application/json','X-Craft-Session':token!},body:JSON.stringify(body)})
   ready()
   const value=await response.json().catch(()=>null) as Record<string,unknown>|null;ready()
   const proposal=nativeProposal(value&&'proposal' in value?value.proposal:value)
   if(response.status===409&&proposal?.state==='stale')return proposal
   if(response.status===409)throw new NativeSaveError('conflict')
   if(response.status===401||response.status===403)throw new NativeSaveError('write_revoked')
   if(!response.ok||!proposal)throw new Error('native_proposal_unconfirmed')
   return proposal
  },
  async versions(signal){
   ready();const response=await send(`${base}document/versions?${query}`,{method:'GET',cache:'no-store',signal,headers:{'X-Craft-Session':token!}})
   ready();if(response.status===404)return null
   if(!response.ok)throw new Error('native_versions_unavailable')
   const list=parseNativeVersionList(await response.json());ready()
   if(!list)throw new Error('invalid_native_versions')
   return list
  },
  async version(version,signal){
   ready();if(!validNativeVersion(version)||version==='0')throw new Error('invalid_native_version')
   const response=await send(`${base}document/versions/${version}?${query}`,{method:'GET',cache:'no-store',signal,headers:{'X-Craft-Session':token!}})
   ready();if(!response.ok)throw new Error('native_version_unavailable')
   const document=parseNativeVersionDocument(await response.json(),version);ready()
   if(!document)throw new Error('invalid_native_version')
   return document
  },
  async exportXlsx(signal,version){
   ready()
   if(version!==undefined&&!EXPORT_VERSION.test(version))throw new NativeExportError("bad_version")
   const q=version===undefined?query:`${query}&${new URLSearchParams({version})}`
   const response=await send(`${base}document/export.xlsx?${q}`,{method:"GET",cache:"no-store",signal,headers:{"X-Craft-Session":token!}})
   ready()
   if(!response.ok){
    const reason=EXPORT_STATUS[response.status]??"failed"
    const retry=Number(response.headers.get("Retry-After"))
    throw new NativeExportError(reason,reason==="busy"&&Number.isSafeInteger(retry)&&retry>0?retry:undefined)
   }
   const exported=response.headers.get("X-Craft-Version")??""
   if(!EXPORT_VERSION.test(exported))throw new NativeExportError("failed")
   const blob=await response.blob();ready()
   return {blob,filename:nativeExportFilename(response.headers.get("Content-Disposition")),version:exported}
  },
  async restore(request,signal){
   ready()
   if(!request||Object.keys(request).length!==8||request.workspaceId!==options.workspaceId||request.pageId!==options.pageId||request.applicationId!==applicationId||request.sessionId!==sessionId||!/^[a-f0-9]{64}$/.test(request.enginePin)||![request.generation,request.baseVersion,request.targetVersion].every(v=>validNativeVersion(v)&&v!=='0'))throw new Error('invalid_native_restore_binding')
   const response=await send(`${base}document/restore?${query}`,{method:'POST',cache:'no-store',signal,headers:{'Content-Type':'application/json','X-Craft-Session':token!},body:JSON.stringify({...request})})
   ready()
   if(response.status===409)throw new NativeSaveError('conflict')
   if(response.status===401||response.status===403)throw new NativeSaveError('write_revoked')
   if(!response.ok)throw new Error('native_restore_unconfirmed')
   const receipt=parseNativeRestoreReceipt(await response.json());ready()
   if(!receipt||BigInt(receipt.version)!==BigInt(request.baseVersion)+1n)throw new Error('native_restore_unconfirmed')
   return receipt
  },
  save:(request,signal)=>mutate("save",request,signal),
  reconcile:(request,signal)=>mutate("reconcile",request,signal),
  close(){active=false;const reason=new Error('native_client_unavailable');eventAbort?.abort(reason);reauthorizeAbort?.abort(reason);token=undefined;sessionId=undefined;applicationId=undefined},
 }
 return client
}
