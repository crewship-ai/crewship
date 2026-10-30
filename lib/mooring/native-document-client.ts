import { apiFetch,tryRefresh,type RefreshResult } from "../api-fetch"
import {getAuthMode} from "../server-base"
import {validNativeDraft,validNativeVersion} from "./native-draft"
import {NativeSaveError,type NativeSaveRequest} from "./single-editor-save"

export interface NativeDocumentClientOptions {
 workspaceId:string;pageId:string;fetch?:typeof fetch
 /** Transport fixtures supply their own credential rotation; production uses tryRefresh. */
 refresh?:()=>Promise<RefreshResult>;bearer?:()=>boolean
}
export interface NativeDocumentSnapshot {
 version:string;bytes:Uint8Array;applicationId:"wordcraft"|"gridcraft";enginePin:string;editorBundle:string;editorURL:string
 quota:{usedBytes:string;limitBytes:string;warning:boolean;retention:string}
}
export interface NativeShareRequest {
 workspaceId:string;pageId:string;applicationId:string;enginePin:string;sessionId:string;generation:string;baseVersion:string;targetVersion:string
}
export interface NativeShareReceipt {version:string;durability:'node';issuedBy:'crewship';phase:'shared'}
export interface NativeDocumentClient {
 open(signal: AbortSignal): Promise<{sessionId: string; applicationId: string}>
 read(signal:AbortSignal):Promise<NativeDocumentSnapshot>
 events(signal:AbortSignal,onEvent:(event:"lease"|"session_retired",value:Record<string,unknown>)=>void):Promise<void>
 reauthorize(signal:AbortSignal):Promise<boolean>
 lease(signal:AbortSignal):Promise<unknown>
 leaseAction(action:"acquire"|"release"|"request-handoff"|"handoff",body:{generation?:string;request_id?:string},signal:AbortSignal):Promise<unknown>
 share(request:NativeShareRequest,signal:AbortSignal):Promise<NativeShareReceipt>
 save(request: NativeSaveRequest,signal: AbortSignal): Promise<unknown>
 reconcile(request: NativeSaveRequest,signal: AbortSignal): Promise<unknown>
 close(): void
}
// Only network I/O and explicitly temporary HTTP failures can reconnect.
// Invalid metadata, changed authority and callback failures remain terminal.
class NativeMetadataTransportError extends Error {}
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
  const response=await send(`${base}document/${operation}?${query}`,{method:"POST",cache:"no-store",signal,headers:{"Content-Type":"application/json","X-Craft-Session":token!},body:JSON.stringify({...request,bytes:nativeBase64(request.bytes)})})
  ready()
  if(response.status===409)throw new NativeSaveError("conflict")
  if(response.status===413)throw new NativeSaveError("quota")
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
    originalSignal.removeEventListener('abort',abort)
    if(reauthorizing===pending)reauthorizing=undefined
    if(reauthorizeAbort===controller)reauthorizeAbort=undefined
   }
  },
  async events(signal,onEvent){
   ready()
   if(eventRunning)throw new Error('native_stream_already_active')
   eventRunning=true
   let reconnecting=false,retries=0,networkRetries=0
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
      // Closing the old fetch can precede the server's live-stream cleanup.
      // Only a planned credential rotation permits bounded duplicate-GET retries.
      if(response.status===409&&reconnecting&&retries<8){
       retries++;await response.body?.cancel().catch(()=>{});await reconnectPause(controller.signal);continue
      }
      if([408,429,500,502,503,504].includes(response.status)){await response.body?.cancel().catch(()=>{});throw new NativeMetadataTransportError('native_stream_unavailable')}
      if(!response.ok||!response.headers.get('Content-Type')?.startsWith('text/event-stream')||!response.body)throw new Error('native_stream_unavailable')
      retries=0;reconnecting=false;streamStarted=Date.now()
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
      reconnecting=true;retries=0
     }finally{eventRecovering=false;signal.removeEventListener('abort',abort);if(eventAbort===controller)eventAbort=undefined}
    }
    ready();throw new Error('native_stream_aborted')
   }finally{eventRunning=false}
  },
  async read(signal){
   ready()
   const response=await send(`${base}document?${query}`,{method:"GET",cache:"no-store",signal,headers:{"X-Craft-Session":token!}})
   ready()
   if(!response.ok)throw new Error("native_document_unavailable")
   const value:unknown=await response.json();ready()
   if(!value||typeof value!=="object")throw new Error("invalid_native_document")
   const v=value as Record<string,unknown>,q=v.quota as Record<string,unknown>|undefined
   if(!validNativeVersion(v.version)||v.version==="0"||v.applicationId!==applicationId||typeof v.enginePin!=="string"||!/^[a-f0-9]{64}$/.test(v.enginePin)||typeof v.editorBundle!=="string"||!/^[a-f0-9]{64}$/.test(v.editorBundle)||typeof v.editorURL!=="string"||!v.editorURL.endsWith(`/b/${v.editorBundle}/index.html`)||typeof v.bytes!=="string"||!v.bytes.length||v.bytes.length>44739244||!q||!validNativeVersion(q.usedBytes)||!validNativeVersion(q.limitBytes)||q.limitBytes==="0"||typeof q.warning!=="boolean"||typeof q.retention!=="string")throw new Error("invalid_native_document")
   let bytes:Uint8Array
   try {const binary=atob(v.bytes);if(!binary.length||binary.length>32*1024*1024)throw new Error();bytes=Uint8Array.from(binary,c=>c.charCodeAt(0))}catch{throw new Error("invalid_native_document")}
   return {version:v.version,bytes,applicationId:applicationId as "wordcraft"|"gridcraft",enginePin:v.enginePin,editorBundle:v.editorBundle,editorURL:v.editorURL,quota:{usedBytes:q.usedBytes,limitBytes:q.limitBytes,warning:q.warning,retention:q.retention}}
  },
  save:(request,signal)=>mutate("save",request,signal),
  reconcile:(request,signal)=>mutate("reconcile",request,signal),
  close(){active=false;const reason=new Error('native_client_unavailable');eventAbort?.abort(reason);reauthorizeAbort?.abort(reason);token=undefined;sessionId=undefined;applicationId=undefined},
 }
 return client
}
