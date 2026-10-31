"use client"

import * as React from "react"
import {registerDocumentDraftWriter} from "@/lib/document-queue"
import {Button} from "@/components/ui/button"
import {apiFetch} from "@/lib/api-fetch"
import {NativeCraftAgentTaskStatus} from "./native-craft-agent-task-status"
import {useLeaveGuard} from "@/lib/editor-lifecycle/use-leave-guard"
import {createNativeDocumentClient,type NativeDocumentSnapshot,type NativeProposal} from "@/lib/mooring/native-document-client"
import {createNativeEditorPort,type NativeEditorPort} from "@/lib/mooring/native-editor-port"
import {openNativeDraftJournal,validNativeVersion,type NativeDraft,type NativeDraftJournal,type NativeRecovery} from "@/lib/mooring/native-draft"
import {createSingleEditorSave,type NativeSaveState} from "@/lib/mooring/single-editor-save"
import {createEditorFrame,editorFrameSupported} from "./workbook-editor-host"
import {downloadNativeExport} from "@/lib/mooring/native-export-download"
import {NativeCraftLeaseBar,type NativeCraftLeaseAction,type NativeCraftLeaseView} from "./native-craft-lease-bar"

interface Lease extends NativeCraftLeaseView {expires_at: string|null}
const vacant:Lease={can_edit:false,holder:null,mine:false,generation:null,can_write:false,acquired_at:null,expires_at:null,handoff_requested_by:[]}
const object=(v:unknown):v is Record<string,unknown>=>Boolean(v)&&typeof v==='object'&&!Array.isArray(v)
const text=(v:unknown):v is string=>typeof v==='string'&&v.length>0&&v.length<=512&&!v.includes('\0')
function leaseView(raw:unknown):Lease {
 if(!object(raw)||typeof raw.mine!=='boolean'||typeof raw.can_write!=='boolean'||!(raw.generation===null||validNativeVersion(raw.generation)&&raw.generation!=='0')||!(raw.acquired_at===null||text(raw.acquired_at))||!(raw.expires_at===null||text(raw.expires_at))||!Array.isArray(raw.handoff_requested_by)||raw.handoff_requested_by.length>128)throw Error('invalid_lease')
 const actor=(v:unknown)=>object(v)&&text(v.actor_id)&&text(v.display_name)&&(v.kind==='human'||v.kind==='agent')
 if(raw.holder!==null&&!actor(raw.holder)||raw.handoff_requested_by.some(v=>!actor(v)||!object(v)||!text(v.request_id)))throw Error('invalid_lease')
 if(raw.can_edit!==undefined&&typeof raw.can_edit!=='boolean')throw Error('invalid_lease')
 if(raw.can_stop_agent!==undefined&&typeof raw.can_stop_agent!=='boolean')throw Error('invalid_lease')
 if(raw.mine&&(!raw.holder||!raw.generation))throw Error('invalid_lease')
 return raw as unknown as Lease
}
function owns(view:Lease,generation?:string):boolean{return view.can_edit===true&&view.mine&&view.can_write&&view.generation!==null&&(!generation||view.generation===generation)&&typeof view.expires_at==='string'&&Date.parse(view.expires_at)>Date.now()}
/** The server selects the approved bundle. No query, credentials or alternative executable path is accepted. */
export function nativeCraftFrameURL(snapshot:NativeDocumentSnapshot,hostOrigin:string):string|null {
 try{
  if(snapshot.applicationId!=='wordcraft'&&snapshot.applicationId!=='gridcraft'||!/^([a-f0-9]{64})$/.test(snapshot.editorBundle))return null
  if(snapshot.editorURL.startsWith('//')||/[?#]/.test(snapshot.editorURL)||Array.from(snapshot.editorURL).some(c=>c.charCodeAt(0)<=32||c.charCodeAt(0)===127))return null
  const url=new URL(snapshot.editorURL,hostOrigin),host=new URL(hostOrigin)
  const loopback=new Set(['localhost','127.0.0.1','[::1]'])
  if(url.username||url.password||url.search||url.hash||url.protocol!=='https:'&&!((process.env.NODE_ENV!=='production'||url.origin===host.origin)&&url.protocol==='http:'&&host.protocol==='http:'&&loopback.has(url.hostname)&&loopback.has(host.hostname)))return null
  if(url.pathname!==(url.origin===host.origin?`/_editor/b/${snapshot.editorBundle}/index.html`:`/b/${snapshot.editorBundle}/index.html`))return null
  for(const [key,value]of Object.entries({host:'port',parent_origin:host.origin,save:'managed',readonly:'1'}))url.searchParams.set(key,value)
  return url.toString()
 }catch{return null}
}
/** The one origin the page's CSP lets frame; null when the policy names none we can safely show. */
export function allowedFrameOrigin(policy:string):string|null {
 for(const directive of policy.split(';')){
  const [name,...sources]=directive.trim().split(/\s+/)
  if(name!=='frame-src'&&name!=='child-src')continue
  for(const source of sources){
   if(!/^https?:\/\/[^*\s/]+$/i.test(source))continue
   try{const u=new URL(source);if(u.origin===source.toLowerCase().replace(/\/$/,'')||u.origin===source)return u.origin}catch{}
  }
 }
 return null
}
/** True when a CSP violation is the browser refusing exactly this editor frame. */
function blocksEditorFrame(e:SecurityPolicyViolationEvent,src:string):boolean {
 if(e.effectiveDirective!=='frame-src'&&e.effectiveDirective!=='child-src'&&e.violatedDirective!=='frame-src')return false
 try{const want=new URL(src),got=new URL(e.blockedURI);return got.origin===want.origin&&(got.pathname==='/'||got.pathname===want.pathname)}catch{return false}
}
interface Comparison {version:string;server:string;draft:string;serverHash:string;draftHash:string;equal:boolean;draftLabel?:string}
interface Props {workspaceId:string;pageId:string;humanId:string;enabled?:boolean}
type Coordinator=ReturnType<typeof createSingleEditorSave>
// A recovered draft whose bytes the server already holds is a save whose reply
// was lost; nothing in it can be lost, so retire it instead of offering it.
async function retireCommitted(journal:NativeDraftJournal,rows:NativeRecovery[],server:Uint8Array):Promise<NativeRecovery[]>{
 const digest=async(bytes:Uint8Array)=>Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',new Uint8Array(bytes))),v=>v.toString(16).padStart(2,'0')).join('')
 const serverHash=await digest(server),kept:NativeRecovery[]=[]
 for(const row of rows){
  if(await digest(row.draft.bytes)===serverHash){try{await journal.confirm(row.draft);continue}catch{/* Kept for comparison. */}}
  kept.push(row)
 }
 return kept
}
interface Runtime {enginePin:string;port:NativeEditorPort;frame:HTMLIFrameElement;abort:AbortController;version:string;generation:string;ready:boolean;writable:boolean;nativeReadOnly:boolean;coordinator?:Coordinator;journal?:NativeDraftJournal;phase?:NativeSaveState;ackedVersion?:string;receiptAck?:Promise<boolean>;receiptVersion?:string;lastAck?:Promise<boolean>;lastAckVersion?:string;edit:number;blocked:boolean;capture?:NativeDraft;retiring?:Promise<boolean>;retirementFinished?:boolean;mountedState?:string}

/** One authenticated client/session owns both lease controls and the isolated full Craft editor. */
export function NativeCraftEditorHost({workspaceId,pageId,humanId,enabled=false}:Props){
 const publishPreview=React.useRef<()=>void>(()=>{}),[publishedVersion,setPublishedVersion]=React.useState<string>()
 const stopAgent=React.useRef<()=>void>(()=>{})
 const [proposals,setProposals]=React.useState<NativeProposal[]>([]),decide=React.useRef<(id:string,decision:'accept'|'reject')=>void>(()=>{}),compareProposal=React.useRef<(proposal:NativeProposal)=>void>(()=>{})
 const [quotaFull,setQuotaFull]=React.useState(false),downloadDraft=React.useRef<()=>void>(()=>{})
 const compare=React.useRef<(draft:NativeDraft)=>void>(()=>{}),reconcile=React.useRef<()=>void>(()=>{}),[uncertain,setUncertain]=React.useState(false)
 const [applicationId,setApplicationId]=React.useState<NativeDocumentSnapshot['applicationId']>()
 const mount=React.useRef<HTMLDivElement>(null),control=React.useRef<(action:NativeCraftLeaseAction,body:{generation?:string;request_id?:string})=>void>(()=>{})
 const [view,setView]=React.useState<Lease>(vacant),[busy,setBusy]=React.useState(false),[label,setLabel]=React.useState('Připojuji…'),[savedVersion,setSavedVersion]=React.useState<string>(),[error,setError]=React.useState<string>(),[mismatch,setMismatch]=React.useState<{message:string;address:string}>(),[dirty,setDirty]=React.useState(false),[recoveries,setRecoveries]=React.useState<NativeRecovery[]>([]),[comparison,setComparison]=React.useState<Comparison>(),[ownsEditor,setOwnsEditor]=React.useState(false),[connectionRetired,setConnectionRetired]=React.useState(false),[quota,setQuota]=React.useState<NativeDocumentSnapshot['quota']>()
 useLeaveGuard(dirty)
 React.useEffect(()=>{
  if(!enabled||!humanId||!workspaceId||!pageId)return
  setBusy(true);setConnectionRetired(false)
  const lifetime=new AbortController(),client=createNativeDocumentClient({workspaceId,pageId})
  let sessionApplication:NativeDocumentSnapshot['applicationId']|undefined
  let dead=false,networkRetired=false,nativeFailed=false,sessionId='',current:Runtime|undefined,transition=false,viewNow=vacant,readerRefresh:Promise<void>|undefined
  let privacyJournal:NativeDraftJournal|undefined,privacyRetirement:Promise<void>|undefined
  let reauthorize:ReturnType<typeof setInterval>|undefined,leaseWatch:ReturnType<typeof setInterval>|undefined,proposalWatch:ReturnType<typeof setInterval>|undefined
  const live=(r:Runtime)=>!dead&&!lifetime.signal.aborted&&current===r&&!r.abort.signal.aborted
  const display=(r:Runtime)=>{
   if(!live(r))return
   const phase=r.phase?.phase
   setUncertain(phase==='uncertain')
   setDirty(r.phase?.dirty??false)
   const saved=phase==='saved'&&r.ackedVersion!==undefined&&r.writable&&!r.blocked
   setSavedVersion(saved?r.ackedVersion:undefined)
   setQuotaFull(phase==='draft'&&r.phase?.reason==='quota')
   setLabel(saved?'Uloženo':phase==='uncertain'?'Výsledek nepotvrzen':phase==='saving'?'Ukládám…':r.blocked?'Koncept zůstává v tomto editoru':r.phase?.dirty?'Neuložený koncept':r.writable?'Připraveno k úpravám':'Pouze čtení')
  }
  stopAgent.current=()=>{void(async()=>{
   if(dead||networkRetired||transition||viewNow.can_edit!==true||viewNow.holder?.kind!=='agent'||viewNow.can_stop_agent!==true)return
   transition=true;setBusy(true);setError(undefined)
   try{
    const query=new URLSearchParams({workspace_id:workspaceId})
    const response=await apiFetch(`/api/v1/pages/${encodeURIComponent('@'+pageId)}/craft/tasks/cancel?${query}`,{method:'POST',cache:'no-store',redirect:'error',skipRefresh:true,signal:lifetime.signal,headers:{'Content-Type':'application/json'},body:'{}'})
    const result:unknown=await response.json()
    if(!response.ok||!object(result)||typeof result.stop_pending!=='boolean')throw Error('native_agent_stop_unconfirmed')
    if(dead||lifetime.signal.aborted)return
    setLabel(result.stop_pending?'Zápis agenta zastaven · ukončení běhu se ověřuje':'Agent zastaven')
    transition=false
    await observeLease(await client.lease(lifetime.signal));await refreshReader()
   }catch{if(!dead&&!lifetime.signal.aborted)setError('Zastavení agenta se nepotvrdilo. Stav úprav se ověřuje na serveru.')}
   finally{transition=false;if(!dead)setBusy(false)}
  })()}
  function freezeInput(r:Runtime){r.frame.setAttribute('inert','');r.frame.tabIndex=-1;r.frame.style.pointerEvents='none';r.frame.blur();mount.current?.focus();setBusy(true)}
  function resumeViewing(r:Runtime){if(live(r)&&(r.nativeReadOnly||!r.blocked)){r.frame.removeAttribute('inert');r.frame.removeAttribute('tabindex');r.frame.style.pointerEvents=''}setBusy(false)}
  function detach(r:Runtime){r.abort.abort();r.port.close();r.frame.remove()}
  function stop(message:string,explicitClose=false){
   if(dead||nativeFailed&&!explicitClose)return
   dead=true;setConnectionRetired(true);lifetime.abort();clearInterval(reauthorize);clearInterval(leaseWatch);clearInterval(proposalWatch);setError(message);setSavedVersion(undefined);setLabel('Editor odpojen · koncept zachován');setOwnsEditor(false);setBusy(false)
   if(current){detach(current);void current.coordinator?.close();if(!current.coordinator)void current.journal?.close()}
   client.close()
  }
  function failNative(r:Runtime,message:string){
   if(!live(r)||nativeFailed)return
   // Bootstrap has no writable buffer or custody, and its read monitor is not ready yet.
   if(!r.coordinator){stop('Editor se bezpečně nepřipojil.');return}
   // A dead port cannot capture the pending native buffer. Keep that frame
   // frozen and keep the exact journal custody lock for a later privacy purge.
   nativeFailed=true;networkRetired=true;r.writable=false;r.blocked=true
   freezeInput(r);r.abort.abort();r.port.close()
   // The engine wrapper below rejects locally BEFORE any dead-port RPC.
   // This stops HTTP writes without closing/releasing the journal or claiming
   // that a fresh snapshot was captured. Read retirement remains available.
   void r.coordinator?.revokeWrite().catch(()=>{})
   if(r.capture)setRecoveries(rows=>rows.some(row=>row.draft.sessionId===r.capture!.sessionId&&row.draft.editorRevision===r.capture!.editorRevision)?rows:[...rows,{draft:r.capture!,actions:['compare','copy']}])
   setConnectionRetired(true);setOwnsEditor(false);setUncertain(false);setDirty(true);setSavedVersion(undefined);setBusy(false)
   setLabel('Zachycení posledních změn nebylo potvrzeno');setError(message)
  }
  function retirePrivacy():Promise<void>{
   if(privacyRetirement)return privacyRetirement
   if(dead)return Promise.resolve()
   // Publish the shared barrier before detach/state callbacks can reenter.
   let resolve!:()=>void,reject!:(cause:unknown)=>void
   privacyRetirement=new Promise<void>((done,fail)=>{resolve=done;reject=fail})
   const r=current;dead=true;lifetime.abort();clearInterval(reauthorize);clearInterval(leaseWatch)
   if(r){detach(r);r.capture=undefined;current=undefined}
   // Keep the precise custody handle even after write retirement closed it.
   // Its closed-journal fence must reject rather than erase a fresh mount.
   // A pure viewer never acquired a private journal to retire here.
   const purged=r?.coordinator?r.coordinator.revokeRead():privacyJournal?privacyJournal.revokeRead():Promise.resolve()
   client.close();setView(vacant);setQuota(undefined);setConnectionRetired(true);setUncertain(false);setRecoveries([]);setComparison(undefined);setDirty(false);setSavedVersion(undefined);setOwnsEditor(false);setLabel('Přístup ke čtení byl odebrán')
   void purged.then(resolve,reject)
   return privacyRetirement
  }
  async function mountEditor(snapshot:NativeDocumentSnapshot,generation:string):Promise<Runtime>{
   if(dead)throw Error('retired')
   if(snapshot.applicationId!==sessionApplication)throw Error('native_application_changed')
   const src=nativeCraftFrameURL(snapshot,window.location.origin)
   if(!src||!editorFrameSupported())throw Error('unsafe_native_editor')
   const previous=current
   if(previous){if(previous.writable||previous.blocked||!previous.nativeReadOnly)throw Error('unsaved_editor_replacement');await previous.coordinator?.close();if(!previous.coordinator)await previous.journal?.close();detach(previous)}
   if(dead||lifetime.signal.aborted)throw Error('retired')
   const abort=new AbortController();let r:Runtime|undefined
   const frame=createEditorFrame(document,src,()=>r?.port.loaded())
   if(!frame)throw Error('isolated_editor_unavailable')
   // The sandbox gives the editor an opaque origin; delegate writes explicitly while
   // editororigin's response policy continues to deny clipboard reads.
   frame.setAttribute('allow','clipboard-write *')
   mount.current?.append(frame)
   if(!frame.contentWindow){frame.remove();throw Error('isolated_editor_unavailable')}
   frame.title=snapshot.applicationId==='gridcraft'?'GridCraft editor':'WordCraft editor';setApplicationId(snapshot.applicationId);setQuota(snapshot.quota)
   let resolve!:(r:Runtime)=>void,reject!:(e:Error)=>void
   const ready=new Promise<Runtime>((done,fail)=>{resolve=done;reject=fail})
   const timer=setTimeout(()=>{reject(Error('native_bootstrap_timeout'));if(r&&live(r))stop('Editor se bezpečně nepřipojil. Koncept nebyl označen jako uložený.')},20000)
   // A page opened on another origin than the configured editor origin has the frame
   // refused by CSP at once; say so instead of waiting for the bootstrap timeout.
   // Reporting only: the policy and the draft are untouched.
   const onViolation=(e:Event)=>{
    if(!blocksEditorFrame(e as SecurityPolicyViolationEvent,src)||dead)return
    const allowed=allowedFrameOrigin((e as SecurityPolicyViolationEvent).originalPolicy||'')
    clearTimeout(timer);reject(Error('native_editor_origin_mismatch'))
    const message=allowed?`Editor je dostupný jen na adrese ${allowed}. Otevřete stránku tam. Koncept nebyl označen jako uložený.`:'Editor je dostupný jen na nastavené adrese editoru. Otevřete stránku tam. Koncept nebyl označen jako uložený.'
    if(allowed)setMismatch({message,address:allowed})
    stop(message)
   }
   document.addEventListener('securitypolicyviolation',onViolation)
   ready.then(()=>document.removeEventListener('securitypolicyviolation',onViolation),()=>document.removeEventListener('securitypolicyviolation',onViolation))
   let port:NativeEditorPort
   try{port=createNativeEditorPort({applicationId:snapshot.applicationId,enginePin:snapshot.enginePin,bundle:snapshot.editorBundle,frame:frame.contentWindow,scope:{workspaceId,pageId,sessionId,baseVersion:snapshot.version,generation},requireAccessProtocol:true,
    onExport(file){
     if(!r||!live(r)||!r.ready||r.blocked)return
     try{downloadNativeExport(file)}catch{setError('Export se nepodařilo stáhnout. Dokument zůstává otevřený.')}
    },
    onChanged(){if(r&&live(r)&&r.ready&&r.writable&&!r.blocked){r.edit++;r.ackedVersion=undefined;r.coordinator?.edited();display(r)}},
    onSaveRequest(request){if(r&&live(r)&&r.writable&&!r.blocked&&r.coordinator){r.ackedVersion=undefined;void r.coordinator.save(request.requestId).catch(()=>{if(r&&live(r)){if(request.requestId)r.port.saveFailed(request.requestId,'Uložení nebylo potvrzeno.');display(r)}})}},
    onError(){clearTimeout(timer);reject(Error('native_lifecycle_failure'));if(r&&live(r))failNative(r,'Spojení s editorem skončilo. Poslední změny nejsou potvrzené; původní editor zůstává uzamčený.')},
    onReady(){if(!r||!live(r))return;void port.bootstrap(snapshot.bytes,abort.signal).then(mounted=>{if(r&&live(r)){r.mountedState=mounted?.nativeState;r.ready=true;clearTimeout(timer);display(r);resolve(r)}}).catch(()=>{clearTimeout(timer);reject(Error('native_bootstrap_failed'));if(r&&live(r))stop('Dokument nelze bezpečně otevřít.')})},
   })}catch(cause){clearTimeout(timer);frame.remove();throw cause}
   r={enginePin:snapshot.enginePin,port,frame,abort,version:snapshot.version,generation,ready:false,writable:false,nativeReadOnly:true,edit:0,blocked:false};current=r
   return ready
  }
  compare.current=draft=>{void(async()=>{
   const snapshot=await client.read(lifetime.signal)
   const digest=async(bytes:Uint8Array)=>Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',new Uint8Array(bytes))),v=>v.toString(16).padStart(2,'0')).join('')
   const [serverHash,draftHash]=await Promise.all([digest(snapshot.bytes),digest(draft.bytes)])
   const decoder=new TextDecoder('utf-8',{fatal:true})
   if(!dead&&!lifetime.signal.aborted)setComparison({version:snapshot.version,server:decoder.decode(snapshot.bytes),draft:decoder.decode(draft.bytes),serverHash,draftHash,equal:serverHash===draftHash})
  })().catch(()=>{if(!dead)setError('Porovnání se nepodařilo načíst. Koncept se nepřepsal.')})}
  // An agent proposal is a separate candidate; the open document is never
  // replaced until this holder accepts it as its own fenced save (#4480).
  const loadProposals=()=>client.proposals(lifetime.signal).then(list=>{if(!dead)setProposals(list.filter(p=>p.state==='open'||p.state==='stale'))}).catch(()=>{})
  compareProposal.current=proposal=>{void(async()=>{
   const [snapshot,candidate]=await Promise.all([client.read(lifetime.signal),client.proposalBytes(proposal.id,lifetime.signal)])
   const digest=async(bytes:Uint8Array)=>Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',new Uint8Array(bytes))),v=>v.toString(16).padStart(2,'0')).join('')
   const [serverHash,draftHash]=await Promise.all([digest(snapshot.bytes),digest(candidate)])
   const decoder=new TextDecoder('utf-8',{fatal:true})
   if(!dead&&!lifetime.signal.aborted)setComparison({version:snapshot.version,server:decoder.decode(snapshot.bytes),draft:decoder.decode(candidate),serverHash,draftHash,equal:serverHash===draftHash,draftLabel:`Návrh ${proposal.authorName} nad verzí ${proposal.baseVersion}`})
  })().catch(()=>{if(!dead)setError('Návrh se nepodařilo načíst k porovnání.')})}
  decide.current=(id,decision)=>{
   if(dead||networkRetired||transition||viewNow.can_edit!==true)return
   transition=true;setBusy(true);setError(undefined)
   const r=current
   void(async()=>{
    if(decision==='reject'){await client.decideProposal(id,'reject',undefined,lifetime.signal);return}
    if(!r||!r.writable||!owns(viewNow,r.generation))throw Error('lease_not_current')
    // Save this holder's own pending edits first; a proposal over an older
    // version then goes stale instead of overwriting them.
    if(!await flushBeforeTransfer(r))throw Error('final_save_unconfirmed')
    const result=await client.decideProposal(id,'accept',r.generation,lifetime.signal)
    if(result.state==='stale'){setError('Dokument se po přípravě návrhu změnil. Návrh nebyl použit, dokument zůstává beze změny.');if(live(r)&&r.writable)resumeViewing(r);return}
    if(!await retireWrite(r))return
    transition=false;await acquire()
   })().catch(()=>{
    if(dead)return
    if(r&&r.writable&&owns(viewNow,r.generation)&&r.phase?.phase!=='read-only')resumeViewing(r)
    setError('Rozhodnutí o návrhu nebylo potvrzeno. Dokument i místní koncept zůstávají zachovány.')
   }).finally(()=>{transition=false;if(!dead){setBusy(false);void loadProposals()}})
  }
  // With full history the unsaved draft stays in this browser; the person can
  // still take the current document out as a file. Nothing is deleted.
  downloadDraft.current=()=>{void(async()=>{
   const r=current;if(!r||!live(r)||!r.ready)return
   const snapshot=await r.port.snapshot(lifetime.signal)
   copy({bytes:snapshot.bytes,applicationId:sessionApplication!} as NativeDraft)
  })().catch(()=>{if(!dead)setError('Koncept se nepodařilo stáhnout. Zůstává v tomto prohlížeči.')})}
  reconcile.current=()=>{const r=current;if(r&&live(r)&&r.writable&&r.phase?.phase==='uncertain')void r.coordinator?.reconcile()}
  const windowMessage=(event:MessageEvent)=>{current?.port.handleWindowMessage(event)}
  window.addEventListener('message',windowMessage)
  const unregisterPrivacy=registerDocumentDraftWriter(humanId,retirePrivacy,{suspend:suspendForAuth,hasDraft:()=>Boolean(current?.phase?.dirty||current?.capture)})
  function wrapJournal(journal:NativeDraftJournal,r:Runtime):NativeDraftJournal{return {...journal,
   async persist(draft){await journal.persist(draft);if(live(r))r.capture={...draft,bytes:draft.bytes.slice()}},
   confirm:async(draft)=>{await journal.confirm(draft);if(r.capture?.editorRevision===draft.editorRevision)r.capture=undefined},
  }}
  function retireWrite(r:Runtime,closeJournal=true):Promise<boolean>{
   if(r.retiring)return r.retiring
   const retiring=(async()=>{
   if(!live(r))return false
   freezeInput(r);r.writable=false;setOwnsEditor(false);r.ackedVersion=undefined
   const captured=r.coordinator?await r.coordinator.revokeWrite():true
   if(!live(r))return false
   // A failed capture keeps the live iframe and local data. It cannot authorize custody transfer.
   if(!captured){
    r.blocked=true
    if(await r.port.setReadOnly(true,r.abort.signal)!==true)throw Error('native_read_only_unconfirmed')
    if(!live(r))return false
    r.nativeReadOnly=true;resumeViewing(r)
    r.retirementFinished=true;setError('Koncept nelze bezpečně zachytit. Předání neproběhlo.');display(r);setBusy(false);return false
   }
   if(await r.port.setReadOnly(true,r.abort.signal)!==true)throw Error('native_read_only_unconfirmed')
   r.nativeReadOnly=true
   if(!live(r))return false
   if(r.capture)setRecoveries(rows=>[...rows,{draft:r.capture!,actions:['compare','copy']}])
   if(closeJournal){await r.coordinator?.close();if(!r.coordinator)await r.journal?.close();r.coordinator=undefined;r.journal=undefined}
   r.retirementFinished=true;resumeViewing(r);display(r);return true
   })()
   r.retiring=retiring;return retiring
  }
  async function suspendForAuth(){
   const r=current
   if(r?.blocked&&r.journal&&r.ready&&!dead){
    // Retry only a local recovery capture. The failed retirement never
    // released its frame/journal, and no save or custody action is replayed.
    freezeInput(r)
    const snapshot=await r.port.snapshot(r.abort.signal)
    const draft:NativeDraft={applicationId:sessionApplication!,enginePin:r.enginePin,sessionId,generation:r.generation,baseVersion:r.receiptVersion??r.version,editorRevision:snapshot.editorRevision,bytes:snapshot.bytes.slice()}
    await r.port.validate(draft,r.abort.signal)
    await r.journal.persist(draft)
    r.capture=draft;r.blocked=false
    if(!r.nativeReadOnly&&await r.port.setReadOnly(true,r.abort.signal)!==true)throw Error('native_read_only_unconfirmed')
    r.nativeReadOnly=true
    await r.coordinator?.close();if(!r.coordinator)await r.journal.close()
    stop('Přihlášení skončilo · koncept zachován');return
   }
   await retireConnection('Přihlášení skončilo · koncept zachován')
   if(!dead&&current?.blocked)throw Error('native_auth_draft_capture_unconfirmed')
  }
  async function retireConnection(message:string){
   if(dead||networkRetired)return
   networkRetired=true;setConnectionRetired(true);setError(message);clearInterval(reauthorize);clearInterval(leaseWatch)
   const r=current
   // Retire writing before closing the client so a save whose reply was lost
   // can still be reconciled while the network allows it.
   if(r&&(r.writable||r.retiring&&!r.retirementFinished)){
    if(!await retireWrite(r)){client.close();return}
   }
   client.close()
   if(!dead)stop(message)
  }
  async function refreshReader(){
   if(dead||networkRetired||transition||current?.writable||current?.blocked||current&&(!current.nativeReadOnly||current.retiring&&!current.retirementFinished))return
   if(readerRefresh)return readerRefresh
   readerRefresh=(async()=>{
    const snapshot=await client.read(lifetime.signal)
    if(dead||networkRetired||transition||current?.writable||current?.blocked||current&&(!current.nativeReadOnly||current.retiring&&!current.retirementFinished))return
    if(snapshot.version!==current?.version){
     setBusy(true)
     try{await mountEditor(snapshot,viewNow.generation??'1')}
     finally{if(!dead&&!networkRetired&&!transition)setBusy(false)}
    }
   })().finally(()=>{readerRefresh=undefined})
   return readerRefresh
  }
  async function observeLease(raw:unknown){
   if(nativeFailed)return
   const next=leaseView(raw);viewNow=next;setView(next)
   const r=current
   if(r?.writable&&!owns(next,r.generation)){if(!await retireWrite(r))return}
   if(!transition)await refreshReader()
  }
  async function acquire(){
   const response=leaseView(await client.leaseAction('acquire',{},lifetime.signal))
   const proof=leaseView(await client.lease(lifetime.signal))
   if(!owns(response)||!owns(proof,response.generation!))throw Error('lease_not_current')
   viewNow=proof;setView(proof)
   // Never import an old local draft. A new generation begins from a fresh server whole read.
   const snapshot=await client.read(lifetime.signal),r=await mountEditor(snapshot,proof.generation!)
   const journal=await openNativeDraftJournal({human:humanId,workspace:workspaceId,page:pageId,applicationId:snapshot.applicationId})
   if(!live(r)){await journal.close();return}
   r.journal=journal;privacyJournal=journal;const recovery=await retireCommitted(journal,await journal.recovery(),snapshot.bytes);if(!live(r)){await journal.close();return}setRecoveries(recovery)
   const finalProof=leaseView(await client.lease(lifetime.signal))
   if(!live(r)){await journal.close();return}
   if(!owns(finalProof,r.generation)){await journal.close();r.journal=undefined;throw Error('lease_not_current')}
   viewNow=finalProof;setView(finalProof)
   const coordinator=createSingleEditorSave({workspaceId,pageId,applicationId:snapshot.applicationId,enginePin:snapshot.enginePin,baseVersion:snapshot.version,loaded:{bytes:snapshot.bytes,...(r.mountedState?{mountedState:r.mountedState}:{})},generation:r.generation,sessionId,journal:wrapJournal(journal,r),engine:{snapshot:(signal,id)=>nativeFailed?Promise.reject(Error('native_engine_retired')):r.port.snapshot(signal,id),validate:(draft,signal)=>nativeFailed?Promise.reject(Error('native_engine_retired')):r.port.validate(draft,signal)},transport:client,
    onState(state){r.phase=state;display(r);if(state.phase==='read-only'&&r.writable&&!transition){r.writable=false;freezeInput(r);queueMicrotask(()=>{if(live(r))void retireWrite(r).catch(()=>stop('Zápis byl zastaven; přístup editoru se nepotvrdil.'))})}},
    onReceipt(receipt){
     const edit=r.edit
     const acknowledgement=(async()=>{
      if(!live(r)||!r.writable)return false
      try{
       const acknowledged=await r.port.ack(receipt)
       if(!live(r)||!r.writable)return false
       if(!acknowledged){failNative(r,'Potvrzení editoru selhalo. Poslední změny nejsou potvrzené; původní editor zůstává uzamčený.');return false}
       if(r.edit===edit)r.ackedVersion=receipt.version
       display(r);return r.edit===edit
      }catch{if(live(r))failNative(r,'Potvrzení editoru selhalo. Poslední změny nejsou potvrzené; původní editor zůstává uzamčený.');return false}
     })()
     r.receiptAck=acknowledgement;r.receiptVersion=receipt.version;r.lastAck=acknowledgement;r.lastAckVersion=receipt.version
     return acknowledgement
    },
   });r.coordinator=coordinator
   freezeInput(r)
   if(await r.port.setReadOnly(false,r.abort.signal)!==false)throw Error('native_write_unconfirmed')
   if(!live(r))return
   r.nativeReadOnly=false
   if(!owns(viewNow,r.generation)){
    const captured=await coordinator.revokeWrite()
    if(await r.port.setReadOnly(true,r.abort.signal)!==true)throw Error('native_read_only_unconfirmed')
    r.nativeReadOnly=true;r.blocked=!captured;resumeViewing(r);throw Error('lease_not_current')
   }
   r.writable=true;setOwnsEditor(true);resumeViewing(r);display(r)
  }
  async function flushBeforeTransfer(r:Runtime):Promise<boolean>{
   if(!live(r))return false
   // An editor that was never unlocked has no native draft to publish.
   if(!r.coordinator)return r.nativeReadOnly&&!r.writable&&!r.capture
   freezeInput(r);r.ackedVersion=undefined;r.receiptAck=undefined;r.receiptVersion=undefined;setSavedVersion(undefined)
   await r.coordinator.flush()
   const acknowledgement=r.receiptAck,version=r.receiptVersion
   // Nothing new to send: the final capture equals the confirmed server version.
   if(!acknowledgement&&!version){
    const confirmed=r.coordinator.confirmedVersion()
    if(!confirmed||!live(r)||!r.writable||r.phase?.phase!=='saved'||r.phase.dirty)return false
    // A version confirmed by an earlier receipt moves custody only once the editor acknowledged it.
    if(r.lastAckVersion===confirmed&&!(r.lastAck&&await r.lastAck))return false
    if(!live(r)||!r.writable||r.coordinator.confirmedVersion()!==confirmed)return false
    r.ackedVersion=confirmed;return true
   }
   if(!acknowledgement||!version||!await acknowledgement)return false
   return live(r)&&r.writable&&r.phase?.phase==='saved'&&!r.phase.dirty&&r.ackedVersion===version&&r.receiptAck===acknowledgement
  }
  publishPreview.current=()=>{void(async()=>{
   const r=current
   if(dead||networkRetired||transition||!r?.ready||viewNow.can_edit!==true||!owns(viewNow,r.generation)||!r.writable)return
   transition=true;setBusy(true);setError(undefined)
   try{
    if(!await flushBeforeTransfer(r)||!r.ackedVersion||!owns(viewNow,r.generation))throw Error('publish_save_unconfirmed')
    const version=r.ackedVersion
    const receipt=await client.share({workspaceId,pageId,applicationId:sessionApplication!,enginePin:r.enginePin,sessionId,generation:r.generation,baseVersion:version,targetVersion:version},lifetime.signal)
    if(live(r))setPublishedVersion(receipt.version)
   }catch{if(!dead)setError('Publikování náhledu nebylo potvrzeno. Dokument i místní koncept zůstávají zachovány.')}
   finally{transition=false;if(live(r)){if(r.writable&&owns(viewNow,r.generation))resumeViewing(r);setBusy(false)}}
  })()}
  control.current=(action,body)=>{
   if(dead||networkRetired||transition||viewNow.can_edit!==true||!current?.ready)return
   transition=true;setBusy(true);setError(undefined)
   void(async()=>{
    if(action==='acquire'){await acquire();return}
    if(action==='handoff'||action==='release'){
     const r=current;if(!r||body.generation!==r.generation||!owns(viewNow,r.generation))throw Error('lease_not_current')
     if(!await flushBeforeTransfer(r))throw Error('final_save_unconfirmed')
     if(!await retireWrite(r,false))return
     await client.leaseAction(action,body,lifetime.signal)
     const proof=leaseView(await client.lease(lifetime.signal));viewNow=proof;setView(proof)
     if(proof.mine&&proof.generation===r.generation)throw Error('custody_transfer_unconfirmed')
     await mountEditor(await client.read(lifetime.signal),proof.generation??'1');return
    }
    await client.leaseAction(action,body,lifetime.signal);await observeLease(await client.lease(lifetime.signal))
   })().catch(async()=>{
    if(dead||nativeFailed)return
    const r=current
    if(r&&(action==='handoff'||action==='release')){
     if(r.writable&&owns(viewNow,r.generation)&&r.phase?.phase!=='read-only')resumeViewing(r)
     else if(!r.nativeReadOnly)await retireWrite(r).catch(()=>stop('Přístup editoru se nepotvrdil. Koncept zachován.'))
    }
    if(!dead){setError('Výsledek akce nebyl potvrzen. Koncept zůstává zachován.');setSavedVersion(undefined)}
   }).finally(()=>{transition=false;if(!dead)setBusy(false)})
  }
  void(async()=>{
   const opened=await client.open(lifetime.signal);if(opened.applicationId!=='wordcraft'&&opened.applicationId!=='gridcraft')throw Error('unsupported_native_application');sessionApplication=opened.applicationId;sessionId=opened.sessionId
   const snapshot=await client.read(lifetime.signal);viewNow=leaseView(await client.lease(lifetime.signal));setView(viewNow)
   await mountEditor(snapshot,viewNow.generation??'1');if(dead)return;setBusy(false)
   leaseWatch=setInterval(()=>{const r=current;if(r?.writable&&!owns(viewNow,r.generation))void retireWrite(r).catch(()=>stop('Platnost úprav skončila. Koncept zůstává zachován.'))},1000)
   void loadProposals();proposalWatch=setInterval(()=>{if(viewNow.can_edit===true)void loadProposals()},5000)
   reauthorize=setInterval(()=>{void client.reauthorize(lifetime.signal).catch(()=>{if(!dead)void retireConnection('Spojení bylo ukončeno. Koncept zůstává zachován.').catch(()=>stop('Přístup editoru se nepotvrdil. Koncept zachován.'))})},30000)
   void client.events(lifetime.signal,(event,value)=>{
    if(dead)return
    if(event==='session_retired'&&value.draft==='purge'){
     void retirePrivacy().catch(()=>setError('Vymazání místního konceptu nebylo potvrzeno.'));return
    }
    if(event==='session_retired'){void retireConnection('Spojení bylo ukončeno. Koncept zachován.').catch(()=>stop('Přístup editoru se nepotvrdil. Koncept zachován.'));return}
    void observeLease(value).catch(()=>stop('Aktuální přístup nelze potvrdit. Koncept zachován.'))
   }).catch(()=>{if(!dead)void retireConnection('Spojení přerušeno. Zápis není potvrzen.').catch(()=>stop('Přístup editoru se nepotvrdil. Koncept zachován.'))})
  })().catch(()=>{if(!dead)stop('Dokument nelze bezpečně otevřít.')})
  return()=>{unregisterPrivacy();window.removeEventListener('message',windowMessage);control.current=()=>{};publishPreview.current=()=>{};stopAgent.current=()=>{};reconcile.current=()=>{};compare.current=()=>{};decide.current=()=>{};compareProposal.current=()=>{};downloadDraft.current=()=>{};stop('Editor uzavřen · koncept zachován',true)}
 },[enabled,humanId,workspaceId,pageId])
 function copy(draft:NativeDraft){const blob=new Blob([new Uint8Array(draft.bytes)],{type:'application/json'}),url=URL.createObjectURL(blob),link=document.createElement('a');link.href=url;link.download=`${draft.applicationId}-recovery.json`;link.click();setTimeout(()=>URL.revokeObjectURL(url),0)}
 if(!enabled)return <p className="p-6">Nativní editor není zapnutý.</p>
 return <section className="flex h-full min-h-[600px] flex-col" aria-label={applicationId==='gridcraft'?'GridCraft sešit':'WordCraft dokument'}>
  <NativeCraftLeaseBar view={view} mayEdit={view.can_edit===true&&!connectionRetired} pending={busy} onAction={(action,body)=>control.current(action,body)} onStopAgent={()=>stopAgent.current()}/>
  <NativeCraftAgentTaskStatus workspaceId={workspaceId} pageId={pageId} humanId={humanId} enabled={view.can_edit===true&&!connectionRetired}/>
  {!connectionRetired&&view.can_edit===true&&view.mine&&view.can_write&&!ownsEditor&&<Button disabled={busy} onClick={()=>control.current('acquire',{})}>Převzít úpravy</Button>}
  {!connectionRetired&&view.can_edit===true&&ownsEditor&&<Button variant="outline" disabled={busy} onClick={()=>publishPreview.current()}>Publikovat náhled</Button>}
  {publishedVersion&&<p className="px-3 py-2">Sdílený náhled · verze {publishedVersion}</p>}
  <div role="status" className="px-3 py-2"><span>{label}</span>{savedVersion&&<span> · verze {savedVersion}</span>}{quota&&<span className="ml-3 text-muted-foreground">Historie {quota.usedBytes} / {quota.limitBytes} B{quota.warning?' · blíží se limit':''}</span>}</div>
  {quotaFull&&<div role="alert" className="space-y-2 border-y px-3 py-2"><p><b>Kapacita úložiště neumožňuje další uložení.</b> Další verze se neuloží. Všechny dosavadní verze zůstávají a vaše neuložené změny jsou v tomto prohlížeči.</p><p className="text-sm text-muted-foreground">Navýšení ani mazání historie aplikace zatím nenabízí. Práci si stáhněte jako soubor koncept nebo exportem z editoru; koncept se nabídne i po znovuotevření.</p><Button variant="outline" onClick={()=>downloadDraft.current()}>Stáhnout koncept</Button></div>}
  {uncertain&&<Button variant="outline" onClick={()=>reconcile.current()}>Ověřit výsledek</Button>}
  {error&&<p role="alert" className="px-3 py-2">{error}{mismatch&&error===mismatch.message&&<> <a className="underline" href={mismatch.address+window.location.pathname+window.location.search+window.location.hash} rel="noopener noreferrer">Otevřít na {mismatch.address}</a></>}</p>}
  {!connectionRetired&&proposals.map(proposal=><div key={proposal.id} role="group" aria-label={`Návrh ${proposal.authorName}`} className="flex flex-wrap items-center gap-2 border-y px-3 py-2">
   <span className="min-w-0 flex-1"><b>{proposal.authorName} připravil návrh nad verzí {proposal.baseVersion}</b>{proposal.summary&&<span> · {proposal.summary}</span>}<small className="block text-muted-foreground">{proposal.state==='stale'?'Zastaralé: dokument se mezitím změnil, návrh nelze použít.':'Dokument zůstává beze změny, dokud návrh nepřijmete.'}</small></span>
   <Button variant="outline" onClick={()=>compareProposal.current(proposal)}>Porovnat</Button>
   {proposal.state==='open'&&view.can_edit===true&&<Button variant="outline" disabled={busy} onClick={()=>decide.current(proposal.id,'reject')}>Odmítnout</Button>}
   {proposal.state==='open'&&ownsEditor&&<Button disabled={busy} onClick={()=>decide.current(proposal.id,'accept')}>Přijmout</Button>}
  </div>)}
  {recoveries.map((recovery,index)=><div key={index} className="flex gap-2 px-3 py-2"><span>Místní koncept · verze {recovery.draft.baseVersion}</span><Button variant="outline" onClick={()=>compare.current(recovery.draft)}>Porovnat</Button><Button variant="outline" onClick={()=>copy(recovery.draft)}>Kopie konceptu</Button></div>)}
  {comparison&&<div className="grid gap-2 p-3 md:grid-cols-2"><p className="md:col-span-2">Serverová verze {comparison.version} a {comparison.draftLabel?.toLowerCase()??'místní koncept'}: {comparison.equal?'shodná data':'odlišná data'}. Nativní data jsou pouze pro čtení.</p><label>Serverová verze<textarea aria-label="Serverová verze" readOnly value={comparison.server} className="h-48 w-full font-mono text-xs"/><span className="break-all text-xs">SHA256 {comparison.serverHash}</span></label><label>{comparison.draftLabel??'Místní koncept'}<textarea aria-label={comparison.draftLabel??'Místní koncept'} readOnly value={comparison.draft} className="h-48 w-full font-mono text-xs"/><span className="break-all text-xs">SHA256 {comparison.draftHash}</span></label><Button variant="outline" onClick={()=>setComparison(undefined)}>Zavřít porovnání</Button></div>}
  <div ref={mount} tabIndex={-1} className="relative min-h-0 flex-1" aria-busy={busy}/>
 </section>
}
