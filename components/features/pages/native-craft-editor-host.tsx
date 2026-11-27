"use client"

import {agentVersionHref} from '@/lib/mooring/version-href'
import * as React from "react"
import {createPortal,flushSync} from "react-dom"
import {registerDocumentDraftWriter} from "@/lib/document-queue"
import {Bot,MoreHorizontal,RotateCcw} from "lucide-react"
import {Button} from "@/components/ui/button"
import {DropdownMenu,DropdownMenuContent,DropdownMenuItem,DropdownMenuSeparator,DropdownMenuTrigger} from "@/components/ui/dropdown-menu"
import {TooltipProvider} from "@/components/ui/tooltip"
import {HintTooltip} from "@/components/ui/hint-tooltip"
import {apiFetch} from "@/lib/api-fetch"
import {NativeCraftAgentStart} from "./native-craft-agent-start"
import {NativeCraftAgentTaskStatus} from "./native-craft-agent-task-status"
import {useLeaveGuard} from "@/lib/editor-lifecycle/use-leave-guard"
import {createNativeDocumentClient,LegacyWorkbookError,type NativeDocumentSnapshot,type NativeProposal} from "@/lib/mooring/native-document-client"
import {createNativeEditorPort,type NativeEditorPort} from "@/lib/mooring/native-editor-port"
import {createGridcraftNativePort} from "@/lib/mooring/gridcraft-native-port"
import type {GridcraftOpened} from "@/lib/mooring/gridcraft-port"
import {openJournalWithRetry} from "@/lib/mooring/native-journal-open"
import {openNativeDraftJournal,validNativeVersion,type NativeDraft,type NativeDraftJournal,type NativeRecovery} from "@/lib/mooring/native-draft"
import {createSingleEditorSave,type NativeSaveState} from "@/lib/mooring/single-editor-save"
import {confirmNativeRead,verifyNativeReadLoss} from "@/lib/mooring/native-read-loss"
import {NATIVE_ADMISSION_DEADLINE_MS,NATIVE_REAUTHORIZE_INTERVAL_MS} from "@/lib/mooring/native-revocation-bounds"
import {createEditorFrame,editorFrameSupported} from "./workbook-editor-host"
import {downloadNativeExport} from "@/lib/mooring/native-export-download"
import {createAgentVersionQueue,subscribeAgentVersions,type AgentVersionHost} from "@/lib/mooring/native-agent-version"
import {keepLocalOver,rebaseOverLocal,sameEngineBuild,type RebaseOutcome} from "@/lib/mooring/native-rebase"
import {GRIDCRAFT_REPLAY} from "@/lib/mooring/gridcraft-replay"
import {registerSheetUndo} from "@/lib/mooring/native-undo"
import {takeUnreleasedCreation} from "@/lib/mooring/creation-custody"
import {NativeCraftLeaseBar,TAKE_OVER_HINT,type NativeCraftLeaseAction,type NativeCraftLeaseView} from "./native-craft-lease-bar"
import {NativeCraftVersionHistory} from "./native-craft-version-history"
import {NativeAppOffError,appDrainHolderText,appOffText,nativeAppOff} from "@/lib/mooring/native-app-off"
import type {NativeVersionDocument,NativeVersionList} from "@/lib/mooring/native-versions"

interface Lease extends NativeCraftLeaseView {expires_at: string|null}
const vacant:Lease={can_edit:false,holder:null,mine:false,generation:null,can_write:false,acquired_at:null,expires_at:null,handoff_requested_by:[]}
const object=(v:unknown):v is Record<string,unknown>=>Boolean(v)&&typeof v==='object'&&!Array.isArray(v)
const text=(v:unknown):v is string=>typeof v==='string'&&v.length>0&&v.length<=512&&!v.includes('\0')
/** A take-over that failed: the stage it reached and, when it never became
 * writable, whether its fresh lease was given back (#5104). */
class AcquireFailure extends Error {
 constructor(readonly stage:string,readonly released:boolean|undefined,readonly inner:unknown){super(inner instanceof Error?inner.message:'acquire_failed')}
}
/** What a person reads after a take-over that never became writable. */
function acquireFailure(stage:string,released:boolean):string{
 const what=stage==='journal'||stage==='recovery'?'The draft storage in this browser could not be opened, so editing did not start.':'Taking over editing did not finish.'
 return released?`${what} The document is free again; try Take over editing again.`:`${what} Editing could not be released with confirmation; use Hand back, or reload the page.`
}
function leaseView(raw:unknown):Lease {
 if(!object(raw)||typeof raw.mine!=='boolean'||typeof raw.can_write!=='boolean'||!(raw.generation===null||validNativeVersion(raw.generation)&&raw.generation!=='0')||!(raw.acquired_at===null||text(raw.acquired_at))||!(raw.expires_at===null||text(raw.expires_at))||!Array.isArray(raw.handoff_requested_by)||raw.handoff_requested_by.length>128)throw Error('invalid_lease')
 const actor=(v:unknown)=>object(v)&&text(v.actor_id)&&text(v.display_name)&&(v.kind==='human'||v.kind==='agent')
 if(raw.holder!==null&&!actor(raw.holder)||raw.handoff_requested_by.some(v=>!actor(v)||!object(v)||!text(v.request_id)))throw Error('invalid_lease')
 if(raw.can_edit!==undefined&&typeof raw.can_edit!=='boolean')throw Error('invalid_lease')
 if(raw.can_share!==undefined&&typeof raw.can_share!=='boolean')throw Error('invalid_lease')
 if(raw.can_stop_agent!==undefined&&typeof raw.can_stop_agent!=='boolean')throw Error('invalid_lease')
 if(raw.mine&&(!raw.holder||!raw.generation))throw Error('invalid_lease')
 return raw as unknown as Lease
}
/** How long a lapsed cached lease may wait for the server's confirmation before write is retired. */
const RECHECK_MS=5_000
// A return read that never settles is given up, so the editor is not left blank for good.
const RETURN_READ_DEADLINE_MS=10_000
/** A return's check is usually over before anyone could read this. */
const ACCESS_NOTICE_MS=1_000
// Each of the three import steps (snapshot, open, snapshot) has 10s, plus 2s per
// whole MiB of the document, at most 120s. The frame cap is 20s for ready plus all
// three steps, never under the 50s it has always been.
const NATIVE_IMPORT_STEP_MS=10_000
function editorSentence(message:string):string{
 const plain=message.replace(/'[^']*'|"[^"]*"|`[^`]*`|\u2018[^\u2019]*\u2019|\u201c[^\u201d]*\u201d/g,'…').replace(/[^A-Za-z0-9 .,:;()/…-]/g,'').replace(/\s+/g,' ').trim()
 return plain.length>80?`${plain.slice(0,80)}…`:plain
}
export function nativeOpenBudget(byteLength:number,carrier:'native'|'file'='native'):{stepMs:number,totalMs:number}{
 const mib=Math.floor(Math.max(0,byteLength)/(1024*1024))
 // A workbook file is compressed: one MiB of .xlsx is several of cells to read. It opens in
 // one step (no snapshot before or after), 20s plus 8s per whole MiB, at most 180s.
 if(carrier==='file'){const stepMs=Math.min(180_000,20_000+8_000*mib);return {stepMs,totalMs:20_000+stepMs+10_000}}
 const stepMs=Math.min(120_000,NATIVE_IMPORT_STEP_MS+2_000*mib)
 return {stepMs,totalMs:Math.max(50_000,20_000+3*stepMs)}
}
/** What the opened workbook holds that the editor keeps as it is, or cannot keep. Codes carry no content. */
export function sheetWarningText(code:string,count:number):string{
 const many=count!==1
 switch(code){
  case 'vba_preserved':return 'Macros (VBA) are kept exactly as they are. They never run here.'
  case 'unsupported_function_held':return `${count} ${many?'cells use':'cell uses'} Excel functions this editor does not calculate. ${many?'They keep':'It keeps'} the value Excel calculated.`
  case 'external_links':return 'Links to other workbooks are kept, but their values are not updated here.'
  case 'encrypted_part_dropped':return `${count} encrypted ${many?'parts':'part'} cannot be kept. Saving here leaves ${many?'them':'it'} out.`
  case 'pivot_readonly':return 'Pivot tables are shown as they are and are not refreshed here.'
  default:return `Some parts of this workbook may not be kept exactly when you save (${code}).`
 }
}
const FIDELITY_PARTS:Record<string,[string,string]>={pivot_fields:['pivot table field','pivot table fields'],pivot_tables:['pivot table','pivot tables'],pivot_formats:['pivot table format','pivot table formats'],charts:['chart','charts'],conditional_formats:['conditional format','conditional formats'],data_validations:['data validation rule','data validation rules'],defined_names:['named range','named ranges'],comments:['comment','comments'],tables:['table','tables'],sheets:['sheet','sheets']}
/** What a save here would drop (fidelity_loss), in words: parts and counts only, never content. */
export function fidelityPartsText(parts:Array<{code:string;count:number}>):string{
 return parts.map(({code,count})=>code==='vba_project'?'the macros (VBA)':code==='vba_signature'?'the digital signature of the macros':FIDELITY_PARTS[code]?`${count} ${FIDELITY_PARTS[code][count===1?0:1]}`:`${count} × ${code}`).join(', ')
}
/** The stored Sheet is the workbook file itself (mooring protocol 2), not native JSON. */
/** A workbook file is saved whole; autosave waits this long after the last change. */
export const SAVED_VALUES='Values saved in the file (not recalculated)'
const SAVED_VALUES_NOTE='The values are the ones saved in the file. They update once the Sheet is recalculated.'
/** What the saved values mean, in one sentence. */
const SAVED_VALUES_HINT='Values come from the saved file and are recalculated after your first change.'
export const WORKBOOK_IDLE_MS=10_000
export function workbookFile(snapshot:NativeDocumentSnapshot):{format:'xlsx'|'xlsm',fileName:string}|null{
 const named=snapshot as NativeDocumentSnapshot&{format?:unknown;fileName?:unknown}
 if(snapshot.applicationId!=='gridcraft'||(named.format!=='xlsx'&&named.format!=='xlsm'))return null
 const format=named.format as 'xlsx'|'xlsm'
 const name=typeof named.fileName==='string'&&named.fileName.length>0&&named.fileName.length<=255&&!/[\\/\0]/.test(named.fileName)?named.fileName:'Workbook'
 return {format,fileName:name.toLowerCase().endsWith(`.${format}`)?name:`${name.replace(/\.(xlsx|xlsm)$/i,'')}.${format}`}
}
function holds(view:Lease,generation?:string):boolean{return view.can_edit===true&&view.mine&&view.can_write&&view.generation!==null&&(!generation||view.generation===generation)}
function owns(view:Lease,generation?:string):boolean{return holds(view,generation)&&typeof view.expires_at==='string'&&Date.parse(view.expires_at)>Date.now()}
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
interface Comparison {version:string;server:string;draft:string;serverHash:string;draftHash:string;equal:boolean;draftLabel?:string;files?:boolean}
/** What the host knows about its save and lock, for a frame's status chip (Artifacts, #5132). */
export interface NativeCraftHostStatus {ready:boolean;phase?:string;savedVersion?:string;dirty:boolean;canEdit:boolean;editing:boolean;holder:{kind:'human'|'agent';name:string}|null;holderIsMe:boolean;openProposals:number;recoveredDrafts:number
 /** A save was refused because the Sheet changed on the server; the person's changes are kept. */
 conflict?:boolean
 /** The version the editor shows: the last acknowledged save, else the version it opened. */
 version?:string}
/** What became of the person's edit access when a message asked to pass it on. */
export type NativeCraftHandBack={state:'returned'}|{state:'not_held'}|{state:'kept';reason:'unsaved'|'busy'|'unconfirmed'}
/** How long a save already on its way may still finish before the access is kept instead. */
const HAND_BACK_SAVE_MS=3000
interface Props {workspaceId:string;pageId:string;humanId:string;enabled?:boolean
 /** Where the version history renders; inline by default. The Artifacts frame moves it into its History drawer. */
 placeHistory?:(history:React.ReactNode)=>React.ReactNode
 /** Where the agent task form renders, once the server admits agents; inline by default. */
 placeAgentStart?:(form:React.ReactNode)=>React.ReactNode
 onStatus?:(status:NativeCraftHostStatus)=>void
 /** Filled with the way to give the edit access back; it answers once the server confirmed it, or says why it was kept. */
 handBackRef?:React.MutableRefObject<(()=>Promise<NativeCraftHandBack>)|null>
 /** Where the lock controls render (who edits since when, take over, request, hand back); inline by default. The Artifacts frame puts them in its status chip. */
 placeLease?:(controls:React.ReactNode)=>React.ReactNode
 /** Hides only the editor surface, never its notices, while the body shows a reflowed read-only view instead (Word on a phone). */
 editorHidden?:boolean
 /** The bar above the grid in the panel beside the chat: who edits, the saved-values badge and a menu (downloads, history, publish) render there, compact, instead of rows above the editor. Notices stay above the editor. */
 toolbar?:HTMLElement|null
 /** The version history link in that bar's menu. */
 historyHref?:string}
type Coordinator=ReturnType<typeof createSingleEditorSave>
// A recovered draft whose bytes the server already holds is a save whose reply
// was lost; nothing in it can be lost, so retire it instead of offering it.
async function retireCommitted(journal:NativeDraftJournal,rows:NativeRecovery[],server:Uint8Array):Promise<NativeRecovery[]>{
 const digest=async(bytes:Uint8Array)=>Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',new Uint8Array(bytes))),v=>v.toString(16).padStart(2,'0')).join('')
 const serverHash=await digest(server),kept:NativeRecovery[]=[]
 for(const row of rows){
  if(await digest(row.draft.bytes)===serverHash){try{await journal.discardRecovered(row);continue}catch{/* Kept for comparison. */}}
  kept.push(row)
 }
 return kept
}
interface Runtime {/** The engine build the editor named in ready. */editorBuild?:string;/** The engine build the server named for this Sheet. */engineBuild?:string;/** The editor revision of the version last saved (or opened): the base a replay keeping the person's cells names. */savedRevision?:string;held?:boolean;/** Rebases after a save refused as moved, in a row. */movedTries?:number;mountedVersion:string;bundle:string;revision?:string;transferSaved?:boolean;enginePin:string;sessionId:string;port:NativeEditorPort;frame:HTMLIFrameElement;abort:AbortController;version:string;generation:string;ready:boolean;writable:boolean;nativeReadOnly:boolean;coordinator?:Coordinator;journal?:NativeDraftJournal;phase?:NativeSaveState;ackedVersion?:string;receiptAck?:Promise<boolean>;receiptVersion?:string;lastAck?:Promise<boolean>;lastAckVersion?:string;edit:number;blocked:boolean;capture?:NativeDraft;retiring?:Promise<boolean>;retirementFinished?:boolean;mountedState?:string}

/** One authenticated client/session owns both lease controls and the isolated full Craft editor. */
const READ_ONLY_REFUSED='Edit access was removed. Unsaved changes stay as a local draft. Reload the page to see the shared version.'
const METADATA_RETRY_MS=5000,METADATA_RETRY_MAX_MS=60000
export function NativeCraftEditorHost({workspaceId,pageId,humanId,enabled=false,placeHistory,placeAgentStart,onStatus,placeLease,editorHidden=false,handBackRef,toolbar,historyHref}:Props){
 const compact=Boolean(toolbar)
 // Opening the latest version again (after a conflict) is a fresh open of the same page: the unsaved draft stays in this browser and is offered beside it.
 const [reopened,setReopened]=React.useState(0)
 const [attemptState,setAttemptState]=React.useState('')
 const scopeKey=JSON.stringify([humanId,workspaceId,pageId,reopened])
 // "Connecting…" covers another person or page only. A reopen of the same page keeps the editor's
 // place in the page, or the new editor would have nowhere to mount.
 const identityKey=JSON.stringify([humanId,workspaceId,pageId])
 const [stateScope,setStateScope]=React.useState(identityKey)
 const [readVerified,setReadVerified]=React.useState(false),[offerWithdrawn,setOfferWithdrawn]=React.useState(false)
 const [retainedBuffer,setRetainedBuffer]=React.useState(false),[authEnded,setAuthEnded]=React.useState(false),[accessCheck,setAccessCheck]=React.useState(false)
 const retryRead=React.useRef<()=>void>(()=>{})
 const copyRecovery=React.useRef<(scope:string,draft:NativeDraft)=>void>(()=>{})
 const publishPreview=React.useRef<()=>void>(()=>{}),[publishedVersion,setPublishedVersion]=React.useState<string>()
 const stopAgent=React.useRef<()=>void>(()=>{})
 const history=React.useRef<{load:(signal:AbortSignal)=>Promise<NativeVersionList|null>;loadVersion:(version:string,signal:AbortSignal)=>Promise<NativeVersionDocument>;restore:(version:string)=>void}>({load:async()=>null,loadVersion:async()=>{throw Error('native_editor_closed')},restore:()=>{}}),[historyKey,setHistoryKey]=React.useState(0),[openedVersion,setOpenedVersion]=React.useState<string>()
 const [proposals,setProposals]=React.useState<NativeProposal[]>([]),decide=React.useRef<(id:string,decision:'accept'|'reject')=>void>(()=>{}),compareProposal=React.useRef<(proposal:NativeProposal)=>void>(()=>{})
 const [quotaFull,setQuotaFull]=React.useState(false),downloadDraft=React.useRef<()=>void>(()=>{}),retrySave=React.useRef<()=>void>(()=>{})
 const compare=React.useRef<(draft:NativeDraft,scope?:string)=>void>(()=>{}),reconcile=React.useRef<(refocus?:boolean)=>void>(()=>{}),[uncertain,setUncertain]=React.useState(false)
 const [applicationId,setApplicationId]=React.useState<NativeDocumentSnapshot['applicationId']>()
 // Download .xlsx: the server converts the last saved version under this admission's read; one at a time.
 const downloadXlsx=React.useRef<(version?:string)=>void>(()=>{})
 const [exporting,setExporting]=React.useState(false),[exportNote,setExportNote]=React.useState<{alert:boolean;text:string}>(),[exportRefused,setExportRefused]=React.useState<{reason:'forbidden'}|{reason:'too_large';version:string}>()
 const [largeOpen,setLargeOpen]=React.useState<'workbook'|'document'>()
 // The opened workbook file: its format, whether its values are Excel's, and what it holds that
 // the editor keeps as it is or cannot keep. Asked once, before the first edit.
 const [openedFile,setOpenedFile]=React.useState<GridcraftOpened>(),[fidelityAsk,setFidelityAsk]=React.useState(false),[fidelityLoss,setFidelityLoss]=React.useState<Array<{code:string;count:number}>|null>(null),[fidelityKept,setFidelityKept]=React.useState(false)
 const fidelity=React.useRef({warnings:0,accepted:false})
 const mount=React.useRef<HTMLDivElement>(null),control=React.useRef<(action:NativeCraftLeaseAction,body:{generation?:string;request_id?:string},settled?:(released:boolean)=>void)=>void>(()=>{}),handBack=React.useRef<()=>Promise<NativeCraftHandBack>>(async()=>({state:'not_held'}))
 const [view,setView]=React.useState<Lease>(vacant),[busy,setBusy]=React.useState(false),[label,setLabel]=React.useState('Connecting…'),[savedVersion,setSavedVersion]=React.useState<string>(),[error,setError]=React.useState<string>(),[mismatch,setMismatch]=React.useState<{message:string;address:string}>(),[dirty,setDirty]=React.useState(false),[recoveries,setRecoveries]=React.useState<NativeRecovery[]>([]),[comparison,setComparison]=React.useState<Comparison>(),[ownsEditor,setOwnsEditor]=React.useState(false),[connectionRetired,setConnectionRetired]=React.useState(false),[quota,setQuota]=React.useState<NativeDocumentSnapshot['quota']>()
 // An administrator turned this application off or it is draining (#5381): the label, once the server said so.
 const [appOff,setAppOff]=React.useState<string>()
 const [takeNotice,setTakeNotice]=React.useState<'taking'|'taken'|'held'|'viewer'|'dropped'>()
 const [saveTrace,setSaveTrace]=React.useState<{phase:string;reason?:string}>()
 const [handoffRequest,setHandoffRequest]=React.useState<{generation:string;holder:string}>()
 const [legacySource,setLegacySource]=React.useState(false)
 const downloadOriginalWorkbook=React.useRef<()=>void>(()=>{})
 const keepMine=React.useRef<((version:string,hash:string)=>boolean)>(undefined)
 // A save refused as moved is answered by a replay; only after 3 in a row does the conflict show.
 const [movedGaveUp,setMovedGaveUp]=React.useState(false)
 const [rebaseNote,setRebaseNote]=React.useState<RebaseOutcome>(),[agentNotice,setAgentNotice]=React.useState<string>(),[agentWorking,setAgentWorking]=React.useState(false),[agentWork,setAgentWork]=React.useState<string>(),[hostBlocked,setHostBlocked]=React.useState(false),[hostRetiring,setHostRetiring]=React.useState(false)
 const agentStarted=React.useCallback((workId:string)=>{
  setAgentWork(workId)
  const url=new URL(window.location.href);url.searchParams.set('craft_task_id',workId);window.history.replaceState(window.history.state,'',url)
 },[])
 // A save on its way is not yet the server's: leaving warns until it lands.
 // A fidelity refusal keeps the change only in the open editor (no file reached the draft journal).
 useLeaveGuard(dirty||saveTrace?.phase==='saving'||fidelityLoss!==null)
 const shownVersion=React.useRef<string|undefined>(undefined)
 shownVersion.current=savedVersion??openedVersion
 React.useEffect(()=>{if(!handBackRef)return;const ask=()=>handBack.current();handBackRef.current=ask;return()=>{if(handBackRef.current===ask)handBackRef.current=null}},[handBackRef])
 React.useEffect(()=>{if(takeNotice!=='taken'&&takeNotice!=='viewer'&&takeNotice!=='dropped')return;const timer=setTimeout(()=>setTakeNotice(undefined),takeNotice==='taken'?4000:8000);return()=>clearTimeout(timer)},[takeNotice])
 // Who holds it is said from the current lock; once it is free there is nothing to say.
 React.useEffect(()=>{if(!view.holder||view.mine)setTakeNotice(notice=>notice==='held'?undefined:notice)},[view.holder,view.mine])
 const statusSink=React.useRef(onStatus);statusSink.current=onStatus
 const openProposals=proposals.filter(proposal=>proposal.state==='open').length
 const conflictShown=saveTrace?.phase==='draft'&&(saveTrace.reason==='conflict'||saveTrace.reason==='moved'&&movedGaveUp&&rebaseNote?.kind!=='layout')&&!connectionRetired
 React.useEffect(()=>{statusSink.current?.({ready:readVerified&&!connectionRetired,phase:saveTrace?.phase,savedVersion,dirty,canEdit:view.can_edit===true,editing:ownsEditor,holder:view.holder?{kind:view.holder.kind,name:view.holder.display_name}:null,holderIsMe:view.mine,openProposals,recoveredDrafts:recoveries.length,version:savedVersion??openedVersion,...(conflictShown?{conflict:true}:{})})},[readVerified,connectionRetired,saveTrace?.phase,savedVersion,openedVersion,dirty,view.can_edit,ownsEditor,view.holder,view.mine,openProposals,recoveries.length,conflictShown])
 React.useEffect(()=>{
  if(!enabled||!humanId||!workspaceId||!pageId)return
  setOpenedFile(undefined);setFidelityAsk(false);setFidelityLoss(null);setFidelityKept(false);fidelity.current={warnings:0,accepted:false}
  setOfferWithdrawn(false);setAccessCheck(false);setStateScope(identityKey);setReadVerified(false);setRetainedBuffer(false);setAuthEnded(false);setView(vacant);setRecoveries([]);setComparison(undefined);setProposals([]);setQuota(undefined);setApplicationId(undefined);setExporting(false);setExportNote(undefined);setExportRefused(undefined);setPublishedVersion(undefined);setHistoryKey(0);setOpenedVersion(undefined);setSavedVersion(undefined);setDirty(false);setOwnsEditor(false);setError(undefined);setAppOff(undefined);setMismatch(undefined);setSaveTrace(undefined);setLabel('Connecting…')
  setBusy(true);setConnectionRetired(false);setHandoffRequest(undefined);setAgentNotice(undefined);setAgentWorking(false);setTakeNotice(undefined);setLegacySource(false)
  const lifetime=new AbortController()
  // Replaced only by a fresh read admission after the server retires write (#4348).
  let client=createNativeDocumentClient({workspaceId,pageId})
  let sessionApplication:NativeDocumentSnapshot['applicationId']|undefined
  // metadataOnly: the frame is gone after a refused private read, and the lease
  // admission stays open only so an explicit read loss can still purge the draft.
  let closed=false,metadataOnly=false,connectionLost=false,draftOffered=false,keptRecoveries=false,offerStale=false,frameWithdrawn=false,checkGeneration=0,metadataDelay=METADATA_RETRY_MS,metadataRetry:ReturnType<typeof setTimeout>|undefined
  // Bumped whenever private access stops being confirmed: a server comparison or
  // proposal list started before that may no longer reach the page.
  let privateEpoch=0
  const dropPrivateViews=()=>{privateEpoch++;setComparison(undefined);setProposals([])}
  // Authority invariants: readable gates every content action; authSuspended is
  // terminal for this scope. networkRetired blocks old admission work, while
  // readOpening admits only the monitored successor under its bounded epoch.
  // Runtime.blocked owns an uncaptured, inert buffer: never mount over it.
  let readable=false,authSuspended=false,readOpening=false,readMismatch=false,readerRefreshNeeded=false,readOpeningAbort:AbortController|undefined
  let leaseObservation=0
  let admissionEpoch=0,initializing=true,admissionDeadline:ReturnType<typeof setTimeout>|undefined,admissionEnds=0
  let dead=false,networkRetired=false,nativeFailed=false,sessionId='',current:Runtime|undefined,transition=false,viewNow=vacant,readerRefresh:{client:typeof client,done:Promise<void>}|undefined
  let privacyJournal:NativeDraftJournal|undefined,privacyRetirement:Promise<void>|undefined,readLossCheck:Promise<boolean>|undefined
  let offerCheck:AbortController|undefined,renewNow:()=>void=()=>{}
  // The frame taken off screen by a return, until a read asked after that return.
  let concealed:Runtime|undefined,accessNotice:ReturnType<typeof setTimeout>|undefined
  let reauthorize:ReturnType<typeof setInterval>|undefined,leaseWatch:ReturnType<typeof setInterval>|undefined,proposalWatch:ReturnType<typeof setInterval>|undefined,keptDraftWatch:ReturnType<typeof setInterval>|undefined
  // Every server confirmation of read, however it arrives, supersedes earlier proofs.
  const readLossAbort=new AbortController()
  const draftScope={human:humanId,workspace:workspaceId,page:pageId},readConfirmed=()=>confirmNativeRead(draftScope)
  // While a return waits for its read, an older answer grants nothing and cancels nothing.
  function readAccess(value:boolean){if(value){if(!offerStale){frameWithdrawn=false;readConfirmed();reveal()}}else{clearTimeout(accessNotice);setAccessCheck(false)}readable=value;setReadVerified(value)}
  function withdrawOffer(value:boolean){offerStale=value;setOfferWithdrawn(value)}
  // Opacity, not visibility: a hidden frame stops rendering (#5060). Neither inert nor
  // blur: both drop the engine's focus inside its frame, no call brings that back, and
  // inert does not even stop the keys. Nor read-only: that ends a cell being typed, and
  // the keyboard with it until a click. So the engine keeps taking input, and what it
  // takes stays out of the journal until the read answers: a loss of Edit keeps a capture
  // from before the return, a confirmed read lets the typing carry on.
  function conceal(r:Runtime){
   concealed=r;r.frame.style.opacity='0';r.frame.style.pointerEvents='none';r.frame.tabIndex=-1;r.frame.setAttribute('aria-hidden','true')
   r.coordinator?.hold()
   clearTimeout(accessNotice);accessNotice=setTimeout(()=>{if(concealed===r)setAccessCheck(true)},ACCESS_NOTICE_MS)
  }
  // The same for an editor about to be captured: it still has to answer.
  function unseen(r:Runtime){r.frame.style.visibility='';r.frame.style.opacity='0'}
  function reveal(){const r=concealed;concealed=undefined;clearTimeout(accessNotice);setAccessCheck(false);if(!r?.ready)return;r.frame.style.opacity='';r.frame.removeAttribute('aria-hidden');if(!r.frame.hasAttribute('inert')){r.frame.style.pointerEvents='';r.frame.removeAttribute('tabindex')}r.coordinator?.release()}
  function offerRecoveries(rows:NativeRecovery[]){keptRecoveries=rows.length>0;setRecoveries(rows)}
  const live=(r:Runtime)=>!dead&&!lifetime.signal.aborted&&current===r&&!r.abort.signal.aborted
  const display=(r:Runtime)=>{
   if(!live(r))return
   const phase=r.phase?.phase
   setUncertain(phase==='uncertain')
   setHostBlocked(r.blocked)
   setDirty(r.phase?.dirty??false)
   const saved=phase==='saved'&&r.ackedVersion!==undefined&&r.writable&&!r.blocked
   setSavedVersion(saved?r.ackedVersion:undefined)
   setQuotaFull(phase==='draft'&&r.phase?.reason==='quota')
   // Phase and reason only (never content): a stalled save is diagnosable from the page (#4852).
   // "unretired": the server committed exactly the editor's content, only the
   // local recovery copy could not be removed yet. It stays dirty (leave guard,
   // no native ACK) but is not called unsaved (#4870).
   setSaveTrace(phase?{phase,reason:r.phase?.reason}:undefined)
   setLabel(saved?'Saved':phase==='uncertain'?'Result not confirmed':phase==='saving'?'Saving…':r.blocked?'The draft stays in this editor':r.phase?.dirty?(phase==='draft'&&r.phase.reason==='unretired'?'Saved on the server · the local copy stays in this browser for now':phase==='draft'&&r.phase.reason==='storage'?'Unsaved draft · browser storage did not respond':'Unsaved draft'):r.writable?'Ready to edit':'Read-only')
   // Anything that waited for the person's work to be saved is handled now.
   agentQueue.settled()
  }
  // An agent's saved version of this Sheet reaches the open editor (live): replayed when the editor is clean and the version
  // follows its own, opened as before when it only reads, and never over anything the person has not saved.
  const agentHost:AgentVersionHost={
   state:()=>{
    const r=current
    if(dead||!r||!r.ready||r.retiring&&!r.retirementFinished)return{mode:'none',clean:false}
    if(!r.writable&&!r.blocked)return{mode:'reader',clean:false,confirmedVersion:r.version}
    const confirmed=r.coordinator?.confirmedVersion()
    return{mode:'writable',clean:Boolean(confirmed)&&!r.blocked&&r.phase?.phase==='saved'&&!r.phase.dirty,confirmedVersion:confirmed}
   },
   targets:()=>{
    const r=current
    if(!r||!r.writable||r.blocked||!r.coordinator)return undefined
    return {port:r.port,save:r.coordinator,beginReplay:r.coordinator.beginReplay,replayed(result,version,adopted){
     if(!live(r))return
     r.savedRevision=result.savedRevision??result.revision
     r.revision=result.revision
     if(adopted){r.ackedVersion=version;display(r)}
    }}
   },
   refreshReader:()=>refreshReader(),
   replayReader:async(commands,version,signal)=>{
    // A refresh that is already running decides what the editor shows; replay only onto what it ends with.
    if(readerRefresh)await readerRefresh.done.catch(()=>{})
    const r=current
    if(dead||signal.aborted||!r||!r.ready||r.writable||r.blocked||transition||r.retiring&&!r.retirementFinished||BigInt(version)!==BigInt(r.version)+1n)return false
    try{
     const result=await r.port.execute(commands,r.revision===undefined?{}:{baseRevision:r.revision},signal)
     // Replaced meanwhile (a take-over, a refresh): that editor was read fresh, so there is nothing left to do here.
     if(!live(r))return true
     // The editor must still only read after the replay.
     if(await r.port.setReadOnly(true,r.abort.signal)!==true)return false
     r.revision=result.revision;r.version=version;r.nativeReadOnly=true
     return true
    }catch{return false}
   },
   notice:text=>{if(!dead)setAgentNotice(text)},
   // Concurrent editing: the versions saved since this editor's base are replayed over its state, the person's cells kept.
   rebase:async(_event,signal)=>{
    const r=current
    // Not a writable Sheet here: today's path (unsaved work waits, a reader opens the version).
    if(dead||!r||!r.writable||r.blocked||!r.coordinator||sessionApplication!=='gridcraft')return{kind:'reload',reason:'operations_404'}
    const coordinator=r.coordinator
    const release=coordinator.beginReplay()
    if(!release)return{kind:'reload',reason:'save_unsettled'}
    try{return await rebaseOverLocal({base:coordinator.currentBase(),enginePin:r.enginePin,replay:GRIDCRAFT_REPLAY,savedRevision:r.savedRevision,sameBuild:sameEngineBuild(r.editorBuild,r.engineBuild),label:'Agent: change',signal,
     operations:async(from,s)=>{let res:Response;try{res=await apiFetch(`/api/v1/pages/${encodeURIComponent('@'+pageId)}/craft/document/operations?${new URLSearchParams({workspace_id:workspaceId,from})}`,{signal:s,cache:'no-store',redirect:'error'})}catch(error){if(s.aborted)throw error;return{status:0,body:null}}return{status:res.status,body:await res.json().catch(()=>null)}},
     // The saved version a replay over unsaved work reports is the base of the next one.
     execute:(commands,options,s)=>r.port.execute(commands,options,s).then(result=>{if(result.savedRevision&&live(r))r.savedRevision=result.savedRevision;return result}),
     rebaseBase:(version,hash)=>live(r)&&coordinator.rebaseBase(version,hash)})}
    finally{release()}
   },
   retryMoved:()=>{const r=current;if(r&&live(r))r.coordinator?.retryMoved()},
   rebased:(_event,outcome)=>{
    // Keep mine moves this editor's save base to the head, as it is, while the same editor is open.
    const r=current;keepMine.current=r&&r.coordinator?(version,hash)=>live(r)&&r.coordinator!.rebaseBase(version,hash,{keepLocal:true}):undefined
    if(!dead)setRebaseNote(outcome.kind==='current'||outcome.kind==='reload'||outcome.kind==='rebased'&&outcome.conflicts.length===0?undefined:outcome)},
   working:on=>{if(!dead)setAgentWorking(on)},
   baseRevision:()=>current?.savedRevision??current?.revision,
  }
  const agentQueue=createAgentVersionQueue(agentHost)
  const unsubscribeAgent=subscribeAgentVersions(pageId,event=>agentQueue.push(event))
  // Undo of an agent's change from the chat card runs here: the server needs this editor's session and lease.
  // A creation custody whose release did not go: released here, exactly that session and generation.
  const leftCustody=takeUnreleasedCreation(pageId)
  if(leftCustody)void apiFetch(`/api/v1/pages/${encodeURIComponent('@'+pageId)}/craft/lease/release?${new URLSearchParams({workspace_id:workspaceId})}`,{method:'POST',redirect:'error',skipRefresh:true,headers:{'Content-Type':'application/json','X-Craft-Session':leftCustody.token},body:JSON.stringify({generation:leftCustody.generation})}).catch(()=>{})
  const unregisterUndo=registerSheetUndo(pageId,async operationId=>{
   const r=current,view=viewNow
   // Only under the lease this editor holds and writes with: the server checks it again.
   if(dead||!r||!live(r)||!r.writable||r.blocked||!holds(view,r.generation)||!view.generation)return 'not_editing'
   return client.undo(operationId,view.generation,lifetime.signal)
  })
  stopAgent.current=()=>{void(async()=>{
   if(dead||networkRetired||transition||viewNow.can_edit!==true||viewNow.holder?.kind!=='agent'||viewNow.can_stop_agent!==true)return
   transition=true;setBusy(true);setError(undefined)
   try{
    const query=new URLSearchParams({workspace_id:workspaceId})
    const response=await apiFetch(`/api/v1/pages/${encodeURIComponent('@'+pageId)}/craft/tasks/cancel?${query}`,{method:'POST',cache:'no-store',redirect:'error',skipRefresh:true,signal:lifetime.signal,headers:{'Content-Type':'application/json'},body:'{}'})
    const result:unknown=await response.json()
    if(!response.ok||!object(result)||typeof result.stop_pending!=='boolean')throw Error('native_agent_stop_unconfirmed')
    if(dead||lifetime.signal.aborted)return
    setLabel(result.stop_pending?'Agent writing stopped · checking that the run ended':'Agent stopped')
    transition=false
    await observeLease(await currentLease(client,lifetime.signal));await refreshReader()
   }catch{if(!dead&&!lifetime.signal.aborted)setError('Stopping the agent was not confirmed. The server is checking the editing state.')}
   finally{transition=false;if(!dead)setBusy(false)}
  })()}
  function freezeInput(r:Runtime){r.frame.setAttribute('inert','');r.frame.tabIndex=-1;r.frame.style.pointerEvents='none';r.frame.blur();mount.current?.focus();setBusy(true)}
  function resumeViewing(r:Runtime){if(live(r)&&(r.nativeReadOnly||!r.blocked)){r.frame.removeAttribute('inert');if(concealed!==r){r.frame.removeAttribute('tabindex');r.frame.style.pointerEvents=''}}setBusy(false)}
  function detach(r:Runtime){r.abort.abort();r.port.close();r.frame.remove()}
  function admissionExpired(epoch:number){if(epoch!==admissionEpoch)return;if(metadataOnly)retryMetadata();else stop('The document cannot be opened safely. The connection timed out; reload the page to try again.')}
  function armAdmissionDeadline(){
   const epoch=++admissionEpoch;initializing=true;readerRefreshNeeded=false;clearTimeout(admissionDeadline)
   admissionEnds=Date.now()+NATIVE_ADMISSION_DEADLINE_MS;admissionDeadline=setTimeout(()=>admissionExpired(epoch),NATIVE_ADMISSION_DEADLINE_MS)
   return epoch
  }
  // A large document's import may outlast the admission deadline; only ever later, never sooner.
  function stretchAdmissionDeadline(ms:number){
   if(!initializing||Date.now()+ms<=admissionEnds)return
   const epoch=admissionEpoch;clearTimeout(admissionDeadline);admissionEnds=Date.now()+ms;admissionDeadline=setTimeout(()=>admissionExpired(epoch),ms)
  }
  function finishAdmission(epoch:number){if(epoch===admissionEpoch){initializing=false;clearTimeout(admissionDeadline);queueMicrotask(catchUpReader)}}
  function endDraftHold(){metadataOnly=false;clearTimeout(metadataRetry)}
  function stop(message:string,explicitClose=false){
   if(dead)return
   if(current?.blocked&&!explicitClose){
    // No unconfirmed native buffer is discarded to recover connectivity.
    // Hide it on uncertain authority; one explicit read recheck may reveal
    // this same frozen frame, never import over it or resurrect its writer.
    admissionEpoch++;initializing=false;readOpening=false;readOpeningAbort?.abort();clearTimeout(admissionDeadline);endDraftHold()
    networkRetired=true;clearInterval(reauthorize);clearInterval(leaseWatch);clearInterval(proposalWatch)
    freezeInput(current);current.frame.style.visibility='hidden';readAccess(false);setRetainedBuffer(true)
    setConnectionRetired(true);setOwnsEditor(false);setUncertain(false);setDirty(true);setSavedVersion(undefined);setBusy(false)
    setLabel('Capturing the last changes was not confirmed')
    setError(readMismatch?'The editor does not match the document type. The original local copy stays locked.':'Access to the local copy was not confirmed. The original editor stays locked; the last changes are not confirmed.')
    client.close();return
   }
   if(nativeFailed&&!explicitClose)return
   readAccess(false)
   dead=true;endDraftHold();readOpening=false;readOpeningAbort?.abort();admissionEpoch++;clearTimeout(admissionDeadline);setConnectionRetired(true);setLargeOpen(undefined);lifetime.abort();clearInterval(reauthorize);clearInterval(leaseWatch);clearInterval(proposalWatch);setError(message);setSavedVersion(undefined);setLabel(current?.capture?'Editor disconnected · the captured local copy is kept':'Editor disconnected');setOwnsEditor(false);setBusy(false)
   if(current){detach(current);void current.coordinator?.close();if(!current.coordinator)void current.journal?.close()}
   client.close()
  }
  function failNative(r:Runtime,message:string){
   if(!live(r)||nativeFailed)return
   // Bootstrap has no writable buffer or custody, and its read monitor is not ready yet.
   if(!r.coordinator){stop('The editor did not connect safely.');return}
   // A dead port cannot capture the pending native buffer. Keep that frame
   // frozen and keep the exact journal custody lock for a later privacy purge.
   nativeFailed=true;networkRetired=true;r.writable=false;r.blocked=true
   freezeInput(r);r.abort.abort();r.port.close()
   // The engine wrapper below rejects locally BEFORE any dead-port RPC.
   // This stops HTTP writes without closing/releasing the journal or claiming
   // that a fresh snapshot was captured. Read retirement remains available.
   void r.coordinator?.revokeWrite().catch(()=>{})
   if(r.capture){keptRecoveries=true;setRecoveries(rows=>rows.some(row=>row.draft.sessionId===r.capture!.sessionId&&row.draft.editorRevision===r.capture!.editorRevision)?rows:[...rows,{draft:r.capture!,actions:['compare','copy']}])}
   setConnectionRetired(true);setOwnsEditor(false);setUncertain(false);setDirty(true);setSavedVersion(undefined);setBusy(false)
   setLabel('Capturing the last changes was not confirmed');setError(message)
  }
  function retirePrivacy():Promise<void>{
   clearInterval(keptDraftWatch)
   if(privacyRetirement)return privacyRetirement
   // Only unmounting ends this duty: drafts outlive a stopped connection.
   if(closed)return Promise.resolve()
   const stopped=dead
   // Publish the shared barrier before detach/state callbacks can reenter.
   let resolve!:()=>void,reject!:(cause:unknown)=>void
   privacyRetirement=new Promise<void>((done,fail)=>{resolve=done;reject=fail})
   const r=current;readAccess(false);dead=true;clearTimeout(metadataRetry);readOpening=false;readOpeningAbort?.abort();admissionEpoch++;clearTimeout(admissionDeadline);lifetime.abort();clearInterval(reauthorize);clearInterval(leaseWatch);clearInterval(proposalWatch)
   if(r){detach(r);r.capture=undefined;current=undefined}
   // Keep the precise custody handle even after write retirement closed it.
   // Its closed-journal fence rejects a scope purge (an old handle never
   // erases a fresh mount), but the row it persisted is still in storage, so
   // it erases exactly that own row (#4348). A pure viewer never acquired a
   // private journal to retire here.
   const journalPurge=(j:NativeDraftJournal)=>j.revokeRead().catch((cause:unknown)=>j.eraseOwnRow?j.eraseOwnRow():Promise.reject(cause))
   const purged=r?.coordinator&&!stopped?r.coordinator.revokeRead():privacyJournal?journalPurge(privacyJournal):Promise.resolve()
   if(stopped||metadataOnly)setError(undefined)
   dropPrivateViews()
   metadataOnly=false
   client.close();readAccess(false);setRetainedBuffer(false);setView(vacant);setQuota(undefined);setConnectionRetired(true);setUncertain(false);offerRecoveries([]);setDirty(false);setSavedVersion(undefined);setOwnsEditor(false);setLabel('Read access was removed')
   void purged.then(resolve,reject)
   return privacyRetirement
  }
  // A monitor failure or refused admission says nothing about the draft by itself.
  // This asks for the one proof that does; anything short of it keeps the draft.
  // Native admission refuses Comment, so nothing else speaks once it has: no
  // stream, no reauthorization. A kept draft still follows read of the Page.
  const keepsDraft=()=>Boolean(metadataOnly||draftOffered||keptRecoveries||current?.capture||current?.blocked||current?.phase?.dirty)
  function watchKeptDraft(){
   if(keptDraftWatch||!keepsDraft())return
   keptDraftWatch=setInterval(proveReadLoss,NATIVE_REAUTHORIZE_INTERVAL_MS)
  }
  function proveReadLoss(){
   if(readLossCheck||privacyRetirement||authSuspended)return
   // One question at a time, however long it takes: a late answer is still proof.
   const check=verifyNativeReadLoss(draftScope,retirePrivacy,readLossAbort.signal).catch(()=>false)
   readLossCheck=check;void check.finally(()=>{if(readLossCheck===check)readLossCheck=undefined})
  }
  // Keeping a draft and offering it are separate decisions. An offer stands only on
  // an answer this mount got for this content, so a return withdraws it, then asks.
  function askContent(){
   const own=client,check=offerCheck=new AbortController(),generation=checkGeneration
   const stale=()=>generation!==checkGeneration||authSuspended
   // An unclear answer leaves it sealed and asks again, without waiting for a renewal.
   const again=()=>{if(offerStale)setTimeout(()=>{if(offerStale&&!offerCheck&&!dead&&!networkRetired&&!authSuspended)askContent()},RECHECK_MS)}
   let deadline:ReturnType<typeof setTimeout>|undefined
   const late=new Promise<never>((_,fail)=>{deadline=setTimeout(()=>{check.abort();fail(Error('native_read_deadline'))},RETURN_READ_DEADLINE_MS)})
   void Promise.race([own.read(AbortSignal.any([check.signal,lifetime.signal])),late]).then(()=>{
    if(stale()||offerCheck!==check)return
    offerCheck=undefined
    if(dead)return
    if(client!==own){again();return}
    const frozen=frameWithdrawn?current:undefined
    if(!frozen){withdrawOffer(false);if(readable)reveal();return}
    if(!live(frozen)||metadataOnly||privacyRetirement)return
    withdrawOffer(false);frozen.frame.style.visibility='';frozen.frame.style.opacity='';readAccess(true)
   }).catch((cause:unknown)=>{
    if(stale()||offerCheck!==check)return
    offerCheck=undefined
    // A refusal during an admission is answered once that admission has finished.
    if(!dead&&client===own&&cause instanceof Error&&cause.message==='native_private_read_refused'&&(readable||!initializing)){frameWithdrawn=false;void reopenReadOnly().catch(()=>stop('Editor access was not confirmed.'));return}
    again()
   }).finally(()=>clearTimeout(deadline))
  }
  // Only a read started after the latest return, with no suspension since, counts.
  function invalidateCheck(){checkGeneration++;offerCheck?.abort();offerCheck=undefined}
  function checkOnReturn(){
   if(closed||authSuspended)return
   if(keepsDraft())proveReadLoss()
   if(dead)return
   const r=current
   invalidateCheck()
   // An admission in flight answers a question asked before the return.
   const opening=initializing||readOpening||Boolean(readerRefresh)||Boolean(r&&!r.ready)
   if(opening||(readable||frameWithdrawn)&&(r||keptRecoveries)){
    // A frozen frame holds what was never saved: it leaves now, and only the read
    // restores it. The buffer itself stays until read loss is proven.
    if(r?.blocked&&readable){frameWithdrawn=true;r.frame.style.visibility='hidden';readAccess(false)}
    // A live editor holds the same unsaved work, and the same read has to answer for it.
    else if(r?.ready&&readable)conceal(r)
    // The offer goes before the next paint too; the frame above already has.
    flushSync(()=>withdrawOffer(true))
    askContent()
   }
   if(!networkRetired)renewNow()
  }
  async function currentLease(admission:typeof client,signal:AbortSignal){
   const observed=leaseObservation
   const proof=leaseView(await admission.lease(signal))
   if(dead||authSuspended||signal.aborted||client!==admission)throw Error('native_admission_retired')
   let confirmed=proof
   if(observed!==leaseObservation){
    // Arrival order is not server order: neither a delayed HTTP grant nor a
    // queued stream grant may override the other's denial. Keep the latest
    // advisory view, but require both to confirm the same writable custody.
    const latest=viewNow,same=latest.generation===proof.generation&&latest.holder?.actor_id===proof.holder?.actor_id&&latest.holder?.kind===proof.holder?.kind
    confirmed={...latest,can_edit:latest.can_edit===true&&proof.can_edit===true,mine:same&&latest.mine&&proof.mine,can_write:same&&latest.mine&&proof.mine&&latest.can_write&&proof.can_write,expires_at:same?proof.expires_at:latest.expires_at}
   }
   viewNow=confirmed;setView(confirmed);return confirmed
  }
  async function mountEditor(snapshot:NativeDocumentSnapshot,generation:string):Promise<Runtime>{
   setLegacySource(snapshot.legacySource===true)
   if(dead)throw Error('retired')
   if(snapshot.applicationId!==sessionApplication)throw Error('native_application_changed')
   const src=nativeCraftFrameURL(snapshot,window.location.origin)
   if(!src)throw Error('unsafe_native_editor url')
   if(!editorFrameSupported())throw Error('unsafe_native_editor frame')
   const previous=current
   if(previous){if(previous.writable||previous.blocked||!previous.nativeReadOnly)throw Error('unsaved_editor_replacement');await previous.coordinator?.close();if(!previous.coordinator)await previous.journal?.close();detach(previous)}
   if(dead||lifetime.signal.aborted)throw Error('retired')
   const admission=client,admittedSession=sessionId
   const abort=new AbortController();let r:Runtime|undefined
   const liveMount=()=>Boolean(r&&live(r)&&!authSuspended&&client===admission&&sessionId===admittedSession)
   const frame=createEditorFrame(document,src,()=>r?.port.loaded())
   if(!frame)throw Error('isolated_editor_unavailable')
   // The sandbox gives the editor an opaque origin; delegate writes explicitly while
   // editororigin's response policy continues to deny clipboard reads.
   frame.setAttribute('allow','clipboard-write *')
   // Imported content stays unseen and noninteractive until live authority is checked.
   // Not visibility:hidden: full Chromium stops animation frames there and the engine
   // never answers bootstrap (#5060).
   frame.style.opacity='0';frame.setAttribute('inert','')
   mount.current?.append(frame)
   if(!frame.contentWindow){frame.remove();throw Error('isolated_editor_unavailable')}
   frame.title=snapshot.applicationId==='gridcraft'?'GridCraft editor':'WordCraft editor';setApplicationId(snapshot.applicationId);setQuota(snapshot.quota)
   let resolve!:(r:Runtime)=>void,reject!:(e:Error)=>void
   const ready=new Promise<Runtime>((done,fail)=>{resolve=done;reject=fail})
   // This deadline covers frame loading and the verified read-only ready reply.
   // Import keeps its own RPC deadlines. A separate 50s wall cap also bounds
   // final authority checking and any future/inoperative adapter stage.
   const file=workbookFile(snapshot)
   const budget=nativeOpenBudget(snapshot.bytes.byteLength,file?'file':'native')
   if(file||budget.stepMs>NATIVE_IMPORT_STEP_MS)stretchAdmissionDeadline(budget.totalMs+10000)
   if(file?snapshot.bytes.byteLength>=2*1024*1024:budget.stepMs>NATIVE_IMPORT_STEP_MS)setLargeOpen(snapshot.applicationId==='gridcraft'?'workbook':'document')
   const timer=setTimeout(()=>{reject(Error('native_bootstrap_timeout'));if(liveMount())stop('The editor did not connect safely. The draft was not marked as saved. Reload the page to try again.')},20000)
   const totalTimer=setTimeout(()=>{reject(Error('native_startup_deadline'));if(liveMount())stop('The document cannot be opened safely. Opening timed out; reload the page to try again.')},budget.totalMs)
   const canceled=()=>reject(Error('native_startup_canceled'))
   abort.signal.addEventListener('abort',canceled,{once:true})
   // A page opened on another origin than the configured editor origin has the frame
   // refused by CSP at once; say so instead of waiting for the bootstrap timeout.
   // Reporting only: the policy and the draft are untouched.
   const onViolation=(e:Event)=>{
    if(!blocksEditorFrame(e as SecurityPolicyViolationEvent,src)||dead)return
    const allowed=allowedFrameOrigin((e as SecurityPolicyViolationEvent).originalPolicy||'')
    clearTimeout(timer);reject(Error('native_editor_origin_mismatch'))
    const message=allowed?`The editor is only available at ${allowed}. Open the page there. The draft was not marked as saved.`:'The editor is only available at the configured editor address. Open the page there. The draft was not marked as saved.'
    if(allowed)setMismatch({message,address:allowed})
    stop(message)
   }
   document.addEventListener('securitypolicyviolation',onViolation)
   const finish=()=>{setLargeOpen(undefined);clearTimeout(timer);clearTimeout(totalTimer);abort.signal.removeEventListener('abort',canceled);document.removeEventListener('securitypolicyviolation',onViolation)}
   ready.then(finish,finish)
   let port:NativeEditorPort
   try{port=(file?(o:Parameters<typeof createNativeEditorPort>[0])=>createGridcraftNativePort({...o,fileName:file.fileName,saveTimeoutMs:budget.stepMs,onCalculated(){if(liveMount())setOpenedFile(f=>f?{...f,calculated:true}:f)},onOpened(opened){if(!liveMount())return;fidelity.current={warnings:opened.warnings.length,accepted:false};setOpenedFile(opened)},onSaveRefused(refusal){if(!liveMount())return;setFidelityLoss(refusal?refusal.parts:null);setFidelityKept(false)
     // The file as it would be written is kept in this browser as an incomplete copy, only to download.
     const at=r,journal=at?.journal
     if(!refusal?.bytes||!refusal.format||!refusal.editorRevision||!at||!journal||!live(at))return
     const draft:NativeDraft={applicationId:'gridcraft',enginePin:at.enginePin,sessionId:at.sessionId,generation:at.generation,baseVersion:at.ackedVersion??at.version,editorRevision:refusal.editorRevision,format:refusal.format,bytes:refusal.bytes,lossy:refusal.parts}
     void Promise.resolve().then(()=>journal.persist(draft)).then(()=>{if(live(at)&&liveMount())setFidelityKept(true)},()=>{/* not kept: the bar keeps saying so */})}}):createNativeEditorPort)({applicationId:snapshot.applicationId,workbookFormat:snapshot.format,enginePin:snapshot.enginePin,bundle:snapshot.editorBundle,frame:frame.contentWindow,scope:{workspaceId,pageId,sessionId,baseVersion:snapshot.version,generation},requireAccessProtocol:true,
    ...({onEditAttempt(kind:'pointer'|'key'){if(r&&liveMount())editAttempt(r,kind)},onHeldInputDropped(reason:string){if(r&&liveMount()){r.held=false;if(reason!=='refused')setTakeNotice('dropped')}}}),
    onExport(file){
     if(!r||!liveMount()||!r.ready||r.blocked||!readable||offerStale||networkRetired)return
     try{downloadNativeExport(file)}catch{setError('The export could not be downloaded. The document stays open.')}
    },
    onChanged(revision){if(!r||!liveMount()||sessionApplication==='gridcraft'&&r.revision===revision)return;r.revision=revision;if(r.ready&&r.writable&&!r.blocked){r.edit++;r.ackedVersion=undefined;r.coordinator?.edited(revision);display(r)}},
    onSaveRequest(request){if(r&&liveMount()&&r.writable&&!r.blocked&&r.coordinator){r.ackedVersion=undefined;void r.coordinator.save(request.requestId).catch(()=>{if(r&&live(r)){if(request.requestId)r.port.saveFailed(request.requestId,'Saving was not confirmed.');display(r)}})}},
    onError(){clearTimeout(timer);reject(Error('native_lifecycle_failure'));if(r&&liveMount())failNative(r,'The connection to the editor ended. The last changes are not confirmed; the original editor stays locked.')},
    onReady(_version?:string,build?:string){if(!r||!liveMount())return;r.editorBuild=build;clearTimeout(timer);void(async()=>{
     const mounted=await port.bootstrap(snapshot.bytes,abort.signal,budget.stepMs)
     if(!r||!liveMount())throw Error('native_startup_canceled')
     r.savedRevision=mounted.editorRevision
     // The original admission checks the current credential, Page authority and
     // session epoch again. Neither ready nor an imported snapshot grants access.
     await currentLease(admission,abort.signal)
     if(!r||!liveMount())throw Error('native_startup_canceled')
     r.mountedState=mounted?.nativeState;r.ready=true;setOpenedVersion(r.version);setHistoryKey(n=>n+1)
     frame.style.visibility='';frame.removeAttribute('inert')
     if(offerStale)conceal(r);else frame.style.opacity=''
     readAccess(true);setRetainedBuffer(false);display(r);resolve(r)
     if(offerStale&&!offerCheck)askContent()
    })().catch((cause:unknown)=>{
     // A code as it is; an editor's own sentence shortened and without what it quotes,
     // since a quote may be document content.
     const message=cause instanceof Error?cause.message:'',code=/^[a-z][a-z0-9_ ]{0,79}$/.test(message)?message:'editor_error'
     console.warn(code==='editor_error'&&message?`native_bootstrap_failed cause=editor_error text="${editorSentence(message)}"`:`native_bootstrap_failed cause=${code}`)
     reject(Error('native_bootstrap_failed'))
     const kind=snapshot.applicationId==='gridcraft'?'workbook':'document'
     if(liveMount())stop(code==='native_request_timeout'?`This ${kind} took too long to open in the editor. Reload the page to try again; a very large one may not open in this browser.`:code==='native_document_too_large'?`This ${kind} is larger than the 32 MiB ${kind==='workbook'?'a Sheet':'a document'} can hold once opened, so it cannot be opened in the editor.`:'The document cannot be opened safely.')
    })},
   })}catch(cause){finish();frame.remove();throw cause}
   r={engineBuild:snapshot.engineBuild,enginePin:snapshot.enginePin,sessionId:admittedSession,port,frame,abort,version:snapshot.version,mountedVersion:snapshot.version,bundle:JSON.stringify(snapshot.editorBundle??null),generation,ready:false,writable:false,nativeReadOnly:true,edit:0,blocked:false};current=r
   return ready
  }
  copyRecovery.current=(scope,draft)=>{if(scope===scopeKey&&readable&&!offerStale&&!privacyRetirement)copy(draft)}
  compare.current=(draft,scope=scopeKey)=>{
   if(scope!==scopeKey)return
   if(!readable||offerStale)return
   const own=client,epoch=privateEpoch
   void(async()=>{
   const snapshot=await own.read(lifetime.signal)
   const digest=async(bytes:Uint8Array)=>Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',new Uint8Array(bytes))),v=>v.toString(16).padStart(2,'0')).join('')
   const [serverHash,draftHash]=await Promise.all([digest(snapshot.bytes),digest(draft.bytes)])
   const decoder=new TextDecoder('utf-8',{fatal:true}),zip=(b:Uint8Array)=>b[0]===0x50&&b[1]===0x4b,files=zip(snapshot.bytes)
   // Workbook files are compared by content, never shown as text.
   const shown=(b:Uint8Array)=>files?'':decoder.decode(b)
   if(readable&&!dead&&client===own&&!lifetime.signal.aborted&&epoch===privateEpoch)setComparison({version:snapshot.version,server:shown(snapshot.bytes),draft:shown(draft.bytes),serverHash,draftHash,equal:serverHash===draftHash,...(files?{files:true}:{})})
  })().catch(()=>{if(!dead&&epoch===privateEpoch)setError('The comparison could not be loaded. The draft was not overwritten.')})}
  // An agent proposal is a separate candidate; the open document is never
  // replaced until this holder accepts it as its own fenced save (#4480).
  const loadProposals=()=>{const epoch=privateEpoch;return client.proposals(lifetime.signal).then(list=>{if(!dead&&epoch===privateEpoch)setProposals(list.filter(p=>p.state==='open'||p.state==='stale'))}).catch(()=>{})}
  compareProposal.current=proposal=>{
   const epoch=privateEpoch
   void(async()=>{
   const [snapshot,candidate]=await Promise.all([client.read(lifetime.signal),client.proposalBytes(proposal.id,lifetime.signal)])
   const digest=async(bytes:Uint8Array)=>Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',new Uint8Array(bytes))),v=>v.toString(16).padStart(2,'0')).join('')
   const [serverHash,draftHash]=await Promise.all([digest(snapshot.bytes),digest(candidate)])
   const decoder=new TextDecoder('utf-8',{fatal:true}),zip=(b:Uint8Array)=>b[0]===0x50&&b[1]===0x4b,files=zip(snapshot.bytes)
   // Workbook files are compared by content, never shown as text.
   const shown=(b:Uint8Array)=>files?'':decoder.decode(b)
   if(!dead&&!lifetime.signal.aborted&&epoch===privateEpoch)setComparison({version:snapshot.version,server:shown(snapshot.bytes),draft:shown(candidate),serverHash,draftHash,equal:serverHash===draftHash,...(files?{files:true}:{}),draftLabel:`Proposal by ${proposal.authorName} on version ${proposal.baseVersion}`})
  })().catch(()=>{if(!dead&&epoch===privateEpoch)setError('The proposal could not be loaded for comparison.')})}
  decide.current=(id,decision)=>{
   if(dead||networkRetired||transition||acquiring||viewNow.can_edit!==true)return
   transition=true;setBusy(true);setError(undefined)
   const r=current
   void(async()=>{
    if(decision==='reject'){await client.decideProposal(id,'reject',undefined,lifetime.signal);return}
    if(!r||!r.writable||!owns(viewNow,r.generation))throw Error('lease_not_current')
    // Save this holder's own pending edits first; a proposal over an older
    // version then goes stale instead of overwriting them.
    if(!await flushBeforeTransfer(r))throw Error('final_save_unconfirmed')
    const result=await client.decideProposal(id,'accept',r.generation,lifetime.signal)
    if(result.state==='stale'){setError('The document changed after the proposal was prepared. The proposal was not applied; the document is unchanged.');if(live(r)&&r.writable)resumeViewing(r);return}
    if(!await retireWrite(r))return
    transition=false;await acquire()
   })().catch(()=>{
    if(dead)return
    if(r&&r.writable&&owns(viewNow,r.generation)&&r.phase?.phase!=='read-only')resumeViewing(r)
    setError('The decision on the proposal was not confirmed. The document and the local draft are kept.')
   }).finally(()=>{transition=false;if(!dead){setBusy(false);void loadProposals()}})
  }
  // With full history the unsaved draft stays in this browser; the person can
  // still take the current document out as a file. Nothing is deleted.
  downloadDraft.current=()=>{void(async()=>{
   const r=current,own=client,started=checkGeneration;if(!readable||offerStale)return;if(!r||!live(r)||!r.ready)throw Error('native_copy_unavailable')
   const snapshot=await r.port.snapshot(lifetime.signal)
   if(!readable||offerStale||checkGeneration!==started||authSuspended||!live(r)||client!==own)return
   copy({bytes:snapshot.bytes,applicationId:sessionApplication!} as NativeDraft)
  })().catch(()=>{if(!dead)setError('The local copy could not be downloaded.')})}
  // A press, like Ctrl+S: asks the server again at the same base. Still full,
  // the alert stays and so does the draft; with space freed it saves (#5124).
  // The export is the server's: it converts the last saved version, never what the editor holds unsaved.
  let exportBusy=false
  downloadXlsx.current=(version?:string)=>{
   if(exportBusy||!readable)return
   exportBusy=true;setExporting(true);setExportNote({alert:false,text:'Preparing the .xlsx… a large workbook can take a minute.'})
   const own=client
   void own.exportXlsx(lifetime.signal,version).then(({blob,filename,version:got})=>{
    if(dead||client!==own)return
    const url=URL.createObjectURL(blob),link=document.createElement('a');link.href=url;link.download=filename;link.click();setTimeout(()=>URL.revokeObjectURL(url),0)
    const ext=/\.xlsm$/i.test(filename)?'xlsm':'xlsx'
    setExportNote({alert:false,text:version==='1'?`Downloaded the original file (version 1) as .${ext}.`:`Downloaded version ${got} as .${ext}.`})
   },(cause:unknown)=>{
    if(dead||client!==own)return
    const reason=(cause as {reason?:unknown}|undefined)?.reason
    if(reason==='forbidden'){setExportRefused({reason:'forbidden'});setExportNote({alert:true,text:'You can no longer read this Sheet, so it cannot be downloaded.'});return}
    if(reason==='too_large'){setExportRefused({reason:'too_large',version:shownVersion.current??''});setExportNote({alert:true,text:'This workbook is too large to download as .xlsx.'});return}
    setExportNote({alert:true,text:reason==='busy'?'The server is preparing other downloads. Try again in a moment.':reason==='not_found'||reason==='bad_version'?'This version is no longer available to download.':'The workbook could not be turned into an .xlsx file. The Sheet itself did not change.'})
   }).finally(()=>{exportBusy=false;if(!dead)setExporting(false)})
  }
  retrySave.current=()=>{const r=current;if(r&&live(r)&&r.ready&&r.writable&&!r.blocked&&r.coordinator){r.ackedVersion=undefined;void r.coordinator.save().catch(()=>{}).finally(()=>{if(r&&live(r))display(r)})}}
  reconcile.current=(refocus)=>{const r=current;if(r&&live(r)&&r.writable&&r.phase?.phase==='uncertain'){void r.coordinator?.reconcile();if(refocus)r.frame.focus()}}
  // Back online, ask the server whether an uncertain save landed, as "Ověřit
  // výsledek" does. Only the reconcile query runs; nothing is resent unasked.
  const backOnline=()=>{reconcile.current();checkOnReturn()}
  window.addEventListener('online',backOnline)
  // A frozen tab's timers stopped with it, so its return cannot wait for one.
  const backVisible=()=>{if(document.visibilityState==='visible')checkOnReturn();else void current?.coordinator?.captureLocal()}
  document.addEventListener('visibilitychange',backVisible)
  window.addEventListener('pageshow',checkOnReturn)
  // Only the editor frame's own window may speak. Its sandbox gives it the opaque
  // origin "null", never its URL's origin; the port requires exactly that and the hello.
  const windowMessage=(event:MessageEvent)=>{const r=current,w=r?.frame.contentWindow;if(!r||!w||event.source!==w)return;r.port.handleWindowMessage(event)}
  window.addEventListener('message',windowMessage)
  const unregisterPrivacy=registerDocumentDraftWriter(humanId,retirePrivacy,{suspend:suspendForAuth,hasDraft:()=>Boolean(metadataOnly||current?.phase?.dirty||current?.capture)})
  function wrapJournal(journal:NativeDraftJournal,r:Runtime):NativeDraftJournal{return {...journal,
   async persist(draft,final=Boolean(r.retiring)){await journal.persist(draft,final);if(live(r))r.capture={...draft,bytes:draft.bytes.slice()}},
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
    r.nativeReadOnly=true;freezeInput(r);setRetainedBuffer(true);setBusy(false)
    r.retirementFinished=true;setError('The draft cannot be captured safely. The handover did not happen.');display(r);setBusy(false);return false
   }
   // The capture is confirmed. A teardown of this editor (its own abort, the page leaving) ends a pending access change
   // and closes the port, so the editor can write nothing more: that is as safe as read-only, and the retirement ends
   // here instead of failing every later suspension.
   let readOnly:boolean
   try{readOnly=await r.port.setReadOnly(true,r.abort.signal)===true}catch(cause){if(live(r))throw cause;return false}
   if(!readOnly)throw Error('native_read_only_unconfirmed')
   r.nativeReadOnly=true
   if(!live(r))return false
   if(r.capture){keptRecoveries=true;setRecoveries(rows=>[...rows,{draft:r.capture!,actions:['compare','copy']}])}
   if(closeJournal){await r.coordinator?.close();if(!r.coordinator)await r.journal?.close();r.coordinator=undefined;r.journal=undefined}
   r.retirementFinished=true;resumeViewing(r);display(r);return true
   })()
   r.retiring=retiring;setHostRetiring(true)
   // A failed retirement stays closed; its callers end in a disconnected editor.
   void retiring.then(()=>{if(r.retirementFinished)setHostRetiring(false)},()=>{})
   return retiring
  }
  async function suspendForAuth(){
   authSuspended=true;invalidateCheck();clearInterval(keptDraftWatch);readLossAbort.abort();setAuthEnded(true);readAccess(false);endDraftHold()
   const r=current
   if(r){freezeInput(r);unseen(r)}
   // Another retirement may already be capturing the only native buffer.
   // Wait for its result: suspension cannot silently succeed ahead of it.
   if(r?.retiring&&!r.retirementFinished&&await r.retiring){if(!dead)stop('Your session ended');return}
   // Writing is retired here, before the connection, so that its capture's result is known. A capture that failed (now
   // or in an earlier retirement) leaves the buffer blocked: the end of a session never depends on the server, so a
   // confirmed local capture is enough; only when even that cannot be confirmed does it refuse.
   if(r&&live(r)&&!r.blocked&&r.writable&&!r.retiring)await retireWrite(r)
   if(r?.blocked&&r.journal&&r.ready&&!dead){
    if(!await keepLocally(r))throw Error('native_auth_draft_capture_unconfirmed')
    stop('Your session ended');return
   }
   await retireConnection('Your session ended')
   if(!dead&&current?.blocked)throw Error('native_auth_draft_capture_unconfirmed')
  }
  /** The frozen buffer, kept in the local journal as the final draft: no save or custody action is replayed. */
  async function keepLocally(r:Runtime):Promise<boolean>{
   // The frame renders while it is captured (#5060) and goes back to how it was if the capture fails: never revealed.
   const visibility=r.frame.style.visibility
   freezeInput(r);unseen(r)
   let draft:NativeDraft
   try{
    const snapshot=await r.port.snapshot(r.abort.signal)
    draft={applicationId:sessionApplication!,enginePin:r.enginePin,sessionId:r.sessionId,generation:r.generation,baseVersion:r.receiptVersion??r.version,editorRevision:snapshot.editorRevision,bytes:snapshot.bytes.slice()}
    await r.port.validate(draft,r.abort.signal)
    await r.journal!.persist(draft,true)
   }catch{
    // Not even the local copy is confirmed: the suspension refuses with its own code, and the buffer stays in the tab.
    if(live(r))r.frame.style.visibility=visibility
    throw Error('native_auth_draft_capture_unconfirmed')
   }
   if(!live(r))return false
   r.capture=draft;r.blocked=false
   if(!r.nativeReadOnly&&await r.port.setReadOnly(true,r.abort.signal)!==true)throw Error('native_read_only_unconfirmed')
   r.nativeReadOnly=true
   await r.coordinator?.close();if(!r.coordinator)await r.journal!.close()
   return true
  }
  async function retireConnection(message:string){
   const hadDraft=()=>Boolean((current?.capture||draftOffered)&&!current?.blocked)
   // A failed native port retired writing, not the independent read monitor.
   // Its terminal failure must still hide the retained buffer and copy UI.
   if(dead||networkRetired&&!readOpening&&!nativeFailed)return
   readAccess(false)
   if(current){freezeInput(current);unseen(current)}
   proveReadLoss()
   readOpening=false;readOpeningAbort?.abort()
   networkRetired=true;setConnectionRetired(true);setError(message);clearInterval(reauthorize);clearInterval(leaseWatch)
   const r=current
   // Retire writing before closing the client so a save whose reply was lost
   // can still be reconciled while the network allows it.
   if(r&&(r.writable||r.retiring&&!r.retirementFinished)){
    if(!await retireWrite(r)){stop(message);return}
   }
   if(current)current.frame.style.visibility='hidden'
   client.close()
   if(dead)return
   // A confirmed draft waits for read to be confirmed again, not for a reload.
   if(hadDraft()&&!authSuspended){
    const r=current;if(r){detach(r);void r.coordinator?.close();if(!r.coordinator)void r.journal?.close();current=undefined}
    metadataOnly=true;connectionLost=true;setOwnsEditor(false);setUncertain(false);setSavedVersion(undefined);setBusy(false);setLabel('Editor disconnected · the captured local copy is kept')
    retryMetadata();return
   }
   stop(message)
  }
  // #4348: the server retired this admission because Page write was removed
  // while read remains. Retire local writing exactly as on lease loss (capture
  // the draft, never send it), then ask the server for a FRESH read admission.
  // Nothing of the old token or lease generation is reused. A refused private
  // read keeps the admission for lease status; any other refusal ends in the
  // ordinary disconnected state with the draft kept.
  async function reopenReadOnly(retry=false){
   if(dead||authSuspended||readOpening||networkRetired&&!(retry&&current?.blocked))return
   const message='Edit access was removed. The document is read-only.'
   networkRetired=true;clearInterval(reauthorize);clearInterval(leaseWatch);viewNow=vacant;setView(vacant);setHandoffRequest(undefined);setOwnsEditor(false);dropPrivateViews()
   if(!metadataOnly)setError(message)
   const r=current
   if(r){freezeInput(r);unseen(r)}
   readAccess(false)
   if(r&&(r.writable||r.retiring&&!r.retirementFinished))await retireWrite(r)
   if(dead)return
   if(authSuspended){stop('Your session ended');return}
   // A failed capture keeps this exact frozen runtime/journal. Re-admit only
   // reading: no new native document, lease, coordinator or writer is created.
   const retained=Boolean(r?.blocked)
   if(r){freezeInput(r);r.frame.style.visibility='hidden'}
   // Before admission: its deadline ends this editor, not the duty to the draft.
   watchKeptDraft()
   client.close();client=createNativeDocumentClient({workspaceId,pageId})
   const startup=armAdmissionDeadline(),admission=client,operation=new AbortController()
   readOpening=true;readMismatch=false;readOpeningAbort=operation
   const abort=()=>operation.abort(lifetime.signal.reason)
   lifetime.signal.addEventListener('abort',abort,{once:true})
   const valid=()=>!dead&&!authSuspended&&client===admission&&admissionEpoch===startup&&!operation.signal.aborted
   try{
    const opened=await admission.open(operation.signal);if(!valid())return
    readConfirmed()
    if(opened.applicationId!==sessionApplication){readMismatch=true;throw Error('native_application_changed')}
    sessionId=opened.sessionId;watch();if(!valid())return
    if(retained){
     // The frozen frame is private content too: only a confirmed read reveals it.
     // Only an explicit recheck reads; a reconnecting draft hold stays hidden.
     if(metadataOnly&&!retry){
      await currentLease(admission,operation.signal);if(!valid()||current!==r)return
      networkRetired=false;readOpening=false;finishAdmission(startup);setBusy(false);return
     }
     try{await admission.read(operation.signal)}
     catch(cause){
      if(!(cause instanceof Error)||cause.message!=='native_private_read_refused')throw cause
      if(!valid()||current!==r)return
      // The refused frame stays hidden and intact; lease events still reach it.
      metadataOnly=true;dropPrivateViews();setConnectionRetired(true);setRetainedBuffer(true);setError(READ_ONLY_REFUSED);setBusy(false)
      await currentLease(admission,operation.signal);if(!valid()||current!==r)return
      networkRetired=false;readOpening=false;finishAdmission(startup);return
     }
     if(!valid()||current!==r)return
     await currentLease(admission,operation.signal);if(!valid()||current!==r)return
     // Keep input inert even if native read-only could not be confirmed.
     if(offerStale){frameWithdrawn=true;if(!offerCheck)askContent()}
     else{r!.frame.style.opacity='';r!.frame.style.visibility='';readAccess(true)}
     setRetainedBuffer(true)
    }else{
     let snapshot:NativeDocumentSnapshot
     try{snapshot=await admission.read(operation.signal)}
     catch(cause){
      if(!(cause instanceof Error)||cause.message!=='native_private_read_refused')throw cause
      if(!valid())return
      // After a lost stream this refusal may be read loss whose purge never
      // arrives; only a write downgrade holds the draft, and holding offers nothing.
      if(connectionLost){endDraftHold();connectionLost=false;stop(READ_ONLY_REFUSED);proveReadLoss();return}
      await holdMetadata(admission,operation.signal);if(!valid())return
      metadataDelay=METADATA_RETRY_MS;networkRetired=false;readOpening=false;finishAdmission(startup);return
     }
     if(!valid())return
     await currentLease(admission,operation.signal);if(!valid())return
     await mountEditor(snapshot,viewNow.generation??'1');if(!valid())return
    }
    metadataOnly=false;networkRetired=false;readOpening=false;setConnectionRetired(false);finishAdmission(startup);setBusy(false)
    setError(retained?'Capturing the last changes was not confirmed. The original editor is locked; once read access is confirmed you can download the local copy.':connectionLost?'The connection is back. The document is read-only; your unsaved changes are in the draft.':message)
    if(connectionLost)draftOffered=true
    connectionLost=false;metadataDelay=METADATA_RETRY_MS
   }catch{if(client===admission&&admissionEpoch===startup&&!dead){proveReadLoss();if(metadataOnly)retryMetadata();else stop(READ_ONLY_REFUSED)}}
   finally{
    lifetime.signal.removeEventListener('abort',abort)
    if(readOpeningAbort===operation){readOpeningAbort=undefined;readOpening=false}
   }
  }
  // No frame or private request survives this; only lease status and the
  // explicit read-loss event still reach this person, and the draft stays theirs.
  async function holdMetadata(admission:typeof client,signal:AbortSignal){
   const r=current,captured=Boolean(r?.capture)
   metadataOnly=true;dropPrivateViews()
   if(r){await r.coordinator?.close();if(!r.coordinator)await r.journal?.close();detach(r);current=undefined}
   if(dead)return
   setConnectionRetired(true);setOwnsEditor(false);setSavedVersion(undefined);setQuota(undefined);setQuotaFull(false);setSaveTrace(undefined);setUncertain(false);setLabel(captured?'Editor disconnected · draft kept':'Editor disconnected');setError(READ_ONLY_REFUSED);setBusy(false)
   // The monitor stays, and the draft stays sealed: seeing a shared version gives
   // no right to unpublished content. Only a confirmed read offers it.
   await currentLease(admission,signal)
  }
  // A lost metadata admission must not end the watch while a draft is held.
  function retryMetadata(){
   if(dead||authSuspended)return
   // Retire the failed admission first, or its stalled read would hold the slot.
   admissionEpoch++;initializing=false;readOpening=false;readOpeningAbort?.abort();readOpeningAbort=undefined;clearTimeout(admissionDeadline)
   networkRetired=true;clearInterval(reauthorize);clearInterval(leaseWatch);client.close();clearTimeout(metadataRetry)
   metadataRetry=setTimeout(()=>{networkRetired=false;void reopenReadOnly().catch(retryMetadata)},metadataDelay)
   metadataDelay=Math.min(metadataDelay*2,METADATA_RETRY_MAX_MS)
  }
  retryRead.current=()=>{void reopenReadOnly(true).catch(()=>stop('Access to the local copy was not confirmed.'))}
  function catchUpReader(){
   if(!readerRefreshNeeded||dead||metadataOnly||networkRetired||initializing||transition||current?.writable||current?.blocked||current&&!current.ready)return
   readerRefreshNeeded=false
   void refreshReader().catch(()=>{if(!dead)stop('The current document cannot be confirmed.')})
  }
  async function refreshReader(){
   if(dead||metadataOnly||networkRetired||transition||current?.writable||current?.blocked||current&&(!current.nativeReadOnly||current.retiring&&!current.retirementFinished))return
   // Both success and failure belong to the reader runtime that started the
   // request. An acquire can replace it without replacing the client. Its late
   // failure must not detach that new writer or interrupt a pending capture.
   const own=client,reader=current
   const obsolete=()=>client!==own||current!==reader||transition||Boolean(current&&(current.writable||current.retiring&&!current.retirementFinished))
   if(readerRefresh?.client===own)return readerRefresh.done
   const refresh={client:own,done:(async()=>{
    let snapshot:NativeDocumentSnapshot
    try{snapshot=await own.read(lifetime.signal)}catch(error){if(obsolete())return;throw error}
    if(obsolete()||dead||networkRetired||current?.blocked||current&&!current.nativeReadOnly)return
    if(snapshot.version!==current?.version){
     setBusy(true)
     try{await mountEditor(snapshot,viewNow.generation??'1')}
     catch(error){if(client!==own)return;throw error}
     finally{if(!dead&&!networkRetired&&!transition)setBusy(false)}
    }
   })().finally(()=>{if(readerRefresh===refresh)readerRefresh=undefined;queueMicrotask(catchUpReader)})}
   readerRefresh=refresh
   return refresh.done
  }
  async function observeLease(raw:unknown,streamed=false){
   // A failed engine still relies on this monitor for current read authority.
   const next=leaseView(raw)
   if(nativeFailed)return
   if(streamed)leaseObservation++
   viewNow=next;setView(next)
   const r=current
   // A streamed renewal read late after a main-thread stall can carry a past
   // expiry while custody holds (#4863). Only a custody change retires at once;
   // an expiry-only lapse goes through leaseWatch's bounded server recheck.
   if(r?.writable&&!(streamed?holds:owns)(next,r.generation)){if(!await retireWrite(r))return}
   if(initializing||r&&!r.ready)readerRefreshNeeded=true
   else if(!transition)await refreshReader()
  }
  // One take-over at a time (#5104): a later one must never meet the cleanup of
  // an earlier one, which could otherwise release the same-session generation.
  let acquiring=false
  // The reader that is already open becomes the editor only when it shows exactly
  // the bytes just read: the same version it was opened with, the same engine and
  // session. Anything else is read and opened fresh. The port stays locked.
  function keepMounted(snapshot:NativeDocumentSnapshot,generation:string):Runtime|undefined{
   const r=current
   if(sessionApplication!=='gridcraft'||snapshot.applicationId!=='gridcraft'||!r||!live(r)||!r.ready||r.writable||r.blocked||!r.nativeReadOnly||r.retiring||r.coordinator||r.journal||r.capture||r.sessionId!==sessionId||r.version!==snapshot.version||r.mountedVersion!==snapshot.version||r.enginePin!==snapshot.enginePin||r.bundle!==JSON.stringify(snapshot.editorBundle??null))return undefined
   const port=r.port as NativeEditorPort&{rebind?:(scope:{sessionId:string;generation:string;baseVersion:string})=>boolean}
   if(port.rebind?.({sessionId,generation,baseVersion:snapshot.version})!==true)return undefined
   r.generation=generation
   return r
  }
  let attempted=false
  const dropHeld=(r:Runtime)=>{r.held=false;if(live(r)&&!r.writable)void r.port.setReadOnly(true,r.abort.signal).catch(()=>{})}
  // The person clicked or typed in the read-only Sheet. Taking over is the same
  // action as the button; the server decides, and nothing unlocks before it did.
  function editAttempt(r:Runtime,kind:'pointer'|'key'){
   // Which condition held is kept on the mount: a reason only, never content.
   const why=sessionApplication!=='gridcraft'?'application':!live(r)?'not_live':!r.ready?'not_ready':r.writable?'writing':r.blocked?'blocked':undefined
   if(why){setAttemptState(`ignored:${why}`);return}
   if(kind==='key')r.held=true
   if(acquiring){setAttemptState('ignored:acquiring');return}
   const closed=!readable?'not_readable':offerStale?'offer_stale':networkRetired?'retired':transition?'transition':initializing?'initializing':undefined
   if(closed){setAttemptState(`ignored:${closed}`);dropHeld(r);return}
   if(viewNow.can_edit!==true){setAttemptState('ignored:viewer');setTakeNotice('viewer');dropHeld(r);return}
   // The person's own lease without this editor writing: the click takes over, as Take over editing does.
   if(viewNow.holder&&!viewNow.mine){setAttemptState('ignored:held');setTakeNotice('held');dropHeld(r);return}
   setAttemptState('taking');setTakeNotice('taking');attempted=true
   control.current('acquire',{},taken=>{attempted=false;if(dead||taken)return;setTakeNotice(undefined);if(current)dropHeld(current)})
  }
  async function acquire(){
   if(acquiring)throw new AcquireFailure('busy',undefined,Error('acquire_in_progress'))
   // Startup limits must never detach a writer's uncaptured buffer.
   if(current&&(current.writable||current.blocked||!current.nativeReadOnly))throw Error('unsaved_editor_replacement')
   acquiring=true
   // What this take-over reached; the one message on screen covers them all.
   let acquireStage='lease'
   const startup=armAdmissionDeadline()
   let taken:string|undefined,mounted:Runtime|undefined,opened:NativeDraftJournal|undefined
   try{
   const response=leaseView(await client.leaseAction('acquire',{},lifetime.signal))
   const proof=await currentLease(client,lifetime.signal)
   if(!owns(response)||!owns(proof,response.generation!))throw Error('lease_not_current')
   taken=proof.generation!
   // Never import an old local draft. A new generation begins from a fresh server whole read.
   acquireStage='read'
   const snapshot=await client.read(lifetime.signal)
   acquireStage='mount'
   const kept=keepMounted(snapshot,proof.generation!),heldInput=current?.held===true
   const r=kept??await mountEditor(snapshot,proof.generation!);mounted=r
   acquireStage='journal'
   // A slow browser store gets a bounded number of attempts; a late answer
   // of an abandoned attempt is closed and never unlocks this editor (#5104).
   const journal=await openJournalWithRetry(signal=>openNativeDraftJournal({human:humanId,workspace:workspaceId,page:pageId,applicationId:snapshot.applicationId},{signal}),{alive:()=>live(r),signal:lifetime.signal});opened=journal
   if(!live(r)){await journal.close();return}
   acquireStage='recovery'
   r.journal=journal;privacyJournal=journal;const recovery=await retireCommitted(journal,await journal.recovery(),snapshot.bytes);if(!live(r)){await journal.close();return}offerRecoveries(recovery)
   acquireStage='final-proof'
   const finalProof=await currentLease(client,lifetime.signal)
   if(!live(r)){await journal.close();return}
   if(!owns(finalProof,r.generation)){await journal.close();r.journal=undefined;throw Error('lease_not_current')}
   acquireStage='writable'
   // A workbook file saves as its format, 10 s after the last change: each save is the whole file.
   const workbook=workbookFile(snapshot)
   const coordinator=createSingleEditorSave({workspaceId,pageId,applicationId:snapshot.applicationId,...(workbook?{format:workbook.format,idleMs:WORKBOOK_IDLE_MS}:{}),enginePin:snapshot.enginePin,baseVersion:snapshot.version,loaded:{bytes:snapshot.bytes,...(r.mountedState?{mountedState:r.mountedState}:{})},generation:r.generation,sessionId,journal:wrapJournal(journal,r),engine:{snapshot:(signal,id)=>nativeFailed?Promise.reject(Error('native_engine_retired')):r.port.snapshot(signal,id),validate:(draft,signal)=>nativeFailed?Promise.reject(Error('native_engine_retired')):r.port.validate(draft,signal)},transport:client,
    onTransportSettled(){agentQueue.settled()},
    onState(state){r.phase=state;display(r);
     // Refused because the Sheet moved on: replay what was saved meanwhile and save on top, at most 3 times in a row (then the conflict shows).
     if(state.phase==='draft'&&state.reason==='moved'&&r.writable&&(r.movedTries??0)<3){r.movedTries=(r.movedTries??0)+1;agentQueue.moved()}
     else if(state.phase==='draft'&&state.reason==='moved'){if(!dead)setMovedGaveUp(true)}
     else if(state.phase==='saved'){r.movedTries=0;if(!dead)setMovedGaveUp(false)}
     if(state.phase==='read-only'&&r.writable&&!transition){r.writable=false;freezeInput(r);queueMicrotask(()=>{if(live(r))void retireWrite(r).catch(()=>stop('Writing was stopped; editor access was not confirmed.'))})}},
    onReceipt(receipt,captureId){
     const edit=r.edit
     const acknowledgement=(async()=>{
      if(!live(r)||!r.writable)return false
      try{
       const acknowledged=await (captureId===undefined?r.port.ack(receipt):r.port.ack(receipt,captureId))
       if(!live(r)||!r.writable)return false
       if(!acknowledged){failNative(r,'The editor confirmation failed. The last changes are not confirmed; the original editor stays locked.');return false}
       r.savedRevision=receipt.editorRevision
       if(r.edit===edit)r.ackedVersion=receipt.version
       display(r);return r.edit===edit
      }catch{if(live(r))failNative(r,'The editor confirmation failed. The last changes are not confirmed; the original editor stays locked.');return false}
     })()
     r.receiptAck=acknowledgement;r.receiptVersion=receipt.version;r.lastAck=acknowledgement;r.lastAckVersion=receipt.version
     return acknowledgement
    },
   });r.coordinator=coordinator
   if(concealed===r)coordinator.hold()
   // A kept frame stays focused and holds the keys itself until it is unlocked.
   if(!kept)freezeInput(r)
   // A runtime already retiring (a custody transfer began) never becomes writable.
   if(r.retiring)throw Error('runtime_retiring')
   if(await r.port.setReadOnly(false,r.abort.signal)!==false)throw Error('native_write_unconfirmed')
   if(!live(r))return
   r.nativeReadOnly=false
   if(!owns(viewNow,r.generation)){
    const captured=await coordinator.revokeWrite()
    if(await r.port.setReadOnly(true,r.abort.signal)!==true)throw Error('native_read_only_unconfirmed')
    r.nativeReadOnly=true;r.blocked=!captured;setHostBlocked(r.blocked);resumeViewing(r);throw Error('lease_not_current')
   }
   if(r.retiring||!live(r)){const captured=await coordinator.revokeWrite();if(await r.port.setReadOnly(true,r.abort.signal)!==true)throw Error('native_read_only_unconfirmed');r.nativeReadOnly=true;r.blocked=!captured;setHostBlocked(r.blocked);throw Error('runtime_retiring')}
   r.transferSaved=false;r.writable=true;r.held=false;setOwnsEditor(true);resumeViewing(r);display(r)
   if(attempted)setTakeNotice(!kept&&heldInput?'dropped':'taken')
   }catch(cause){
    let released:boolean|undefined
    if(acquireStage==='read'||acquireStage==='mount'||acquireStage==='journal'||acquireStage==='recovery'){
     // A journal this take-over opened but never wrote through gives up its
     // exclusive custody now. privacyJournal keeps the closed handle, whose
     // retirement erases only its own row (#4348).
     if(opened){if(mounted?.journal===opened)mounted.journal=undefined;await opened.close().catch(()=>{})}
     // The editor never became writable, so nothing uncaptured is at stake:
     // give the fresh lease back instead of holding a document nobody can
     // edit. The release is fenced by holder and this generation; take-overs
     // are serialized, and no writable editor of this tab may exist.
     if(taken&&!dead&&!current?.writable)released=await releaseUnused(taken)
    }
    throw new AcquireFailure(acquireStage,released,cause)
   }finally{acquiring=false;finishAdmission(startup)}
  }
  async function releaseUnused(generation:string):Promise<boolean>{
   try{
    await client.leaseAction('release',{generation},lifetime.signal)
    const proof=await currentLease(client,lifetime.signal)
    return !(proof.mine&&proof.generation===generation)
   }catch{return false}
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
   if(dead||networkRetired||transition||acquiring||!r?.ready||viewNow.can_edit!==true||viewNow.can_share!==true||!owns(viewNow,r.generation)||!r.writable)return
   transition=true;setBusy(true);setError(undefined)
   try{
    if(!await flushBeforeTransfer(r)||!r.ackedVersion||!owns(viewNow,r.generation))throw Error('publish_save_unconfirmed')
    const version=r.ackedVersion
    const receipt=await client.share({workspaceId,pageId,applicationId:sessionApplication!,enginePin:r.enginePin,sessionId,generation:r.generation,baseVersion:version,targetVersion:version},lifetime.signal)
    if(live(r))setPublishedVersion(receipt.version)
   }catch{if(!dead)setError('Publishing the preview was not confirmed. The document and the local draft are kept.')}
   finally{transition=false;if(live(r)){if(r.writable&&owns(viewNow,r.generation))resumeViewing(r);setBusy(false)}}
  })()}
  // Restore is the existing fenced write under this holder's generation and
  // confirmed base version; the reopened editor then starts from the new head.
  history.current={load:signal=>client.versions(signal),loadVersion:(version,signal)=>client.version(version,signal),restore:target=>{void(async()=>{
   const r=current
   if(dead||networkRetired||transition||acquiring||!r?.ready||viewNow.can_edit!==true||!owns(viewNow,r.generation)||!r.writable)return
   transition=true;setBusy(true);setError(undefined)
   try{
    if(!await flushBeforeTransfer(r)||!r.ackedVersion||!owns(viewNow,r.generation))throw Error('restore_save_unconfirmed')
    await client.restore({workspaceId,pageId,applicationId:sessionApplication!,enginePin:r.enginePin,sessionId,generation:r.generation,baseVersion:r.ackedVersion,targetVersion:target},lifetime.signal)
    if(!await retireWrite(r))return
    transition=false;await acquire()
   }catch{
    if(dead)return
    if(r.writable&&owns(viewNow,r.generation)&&r.phase?.phase!=='read-only')resumeViewing(r)
    setError('Restoring the version was not confirmed. The document and the local draft are kept.')
   }finally{transition=false;if(!dead){setBusy(false);setHistoryKey(n=>n+1)}}
  })()}}
  // Whether the administrator has this application off right now: a hand-over
  // would be refused, and the writer must not retire for it (#5381). Unknown is no.
  const appOffNow=async():Promise<string|null>=>{
   try{
    const response=await apiFetch(`/api/v1/pages/${encodeURIComponent('@'+pageId)}/craft/availability?${new URLSearchParams({workspace_id:workspaceId})}`,{signal:lifetime.signal,cache:'no-store',redirect:'error'})
    if(!response?.ok)return null
    const value:unknown=await response.json()
    return object(value)?nativeAppOff(value):null
   }catch{return null}
  }
  // Passes the access on only from a clean editor: it waits briefly for a save
  // already on its way, never forces one, and answers from the server's proof.
  handBack.current=async()=>{
   if(dead||!viewNow.mine)return {state:'not_held'}
   const deadline=Date.now()+HAND_BACK_SAVE_MS
   const unsettled=()=>transition||acquiring||!current?.ready||current.phase?.dirty===true
   while(!dead&&viewNow.mine&&unsettled()&&Date.now()<deadline)await new Promise(resolve=>setTimeout(resolve,100))
   if(!dead&&!viewNow.mine)return {state:'not_held'}
   const r=current
   if(dead||networkRetired||!r||!owns(viewNow,r.generation))return {state:'kept',reason:'unconfirmed'}
   if(r.phase?.dirty)return {state:'kept',reason:'unsaved'}
   if(unsettled())return {state:'kept',reason:'busy'}
   return new Promise(resolve=>control.current('release',{generation:r.generation},released=>resolve(released?{state:'returned'}:{state:'kept',reason:'unconfirmed'})))
  }
  control.current=(action,body,report)=>{
   // Answered once: a take-over that succeeded must never be reported as refused when it finishes.
   let answered=false
   const settled=report&&((released:boolean)=>{if(answered)return;answered=true;report(released)})
   if(dead||networkRetired||transition||viewNow.can_edit!==true||!current?.ready||acquiring){settled?.(false);return}
   // What saving here would change is said before the first edit, not after.
   if(action==='acquire'&&fidelity.current.warnings>0&&!fidelity.current.accepted){settled?.(false);setFidelityAsk(true);return}
   transition=true;setBusy(true);setError(undefined)
   let handoffConfirmed=false
   void(async()=>{
    if(action==='acquire'){await acquire();if(current?.writable)settled?.(true);return}
    if(action==='handoff'||action==='release'){
     const r=current;if(!r||body.generation!==r.generation||!owns(viewNow,r.generation))throw Error('lease_not_current')
     // During a drain the holder keeps editing and saving; only the hand-over is refused.
     if(action==='handoff'&&!r.transferSaved){const off=await appOffNow();if(dead)return;if(off){setAppOff(off);setError(appDrainHolderText(off));return}}
     // After a failed POST the final save is still proven and write stays retired;
     // the coordinator refuses a second flush, so a retry only sends the POST again.
     if(!r.transferSaved){
      if(!await flushBeforeTransfer(r))throw Error('final_save_unconfirmed')
      if(!await retireWrite(r,false))return
      r.transferSaved=true
     }
     // The writer has retired, so nothing uncaptured is at stake: the transfer,
     // its proof and the reader mount all run under one admission deadline.
     const startup=armAdmissionDeadline()
     try{
      await client.leaseAction(action,body,lifetime.signal)
      const proof=await currentLease(client,lifetime.signal)
      if(proof.mine&&proof.generation===r.generation)throw Error('custody_transfer_unconfirmed')
      // Confirmed free: what follows only reopens the reader.
      settled?.(true)
      await mountEditor(await client.read(lifetime.signal),proof.generation??'1')
     }finally{finishAdmission(startup)}
     return
    }
    const holder=viewNow.holder?.actor_id
    const result=await client.leaseAction(action,body,lifetime.signal)
    // A successful POST confirms only a request, never custody. Tie its
    // indication to the server's generation and the holder it was sent to.
    if(action==='request-handoff'){
     if(!object(result)||!text(result.request_id)||!validNativeVersion(result.generation)||result.generation==='0'||!holder)throw Error('handoff_request_unconfirmed')
     if(dead||networkRetired||lifetime.signal.aborted)return
     setHandoffRequest({generation:result.generation,holder});handoffConfirmed=true
    }
    await observeLease(await currentLease(client,lifetime.signal))
   })().catch(async(cause:unknown)=>{
    // Codes only: no document content reaches these messages.
    if(action==='acquire'){const reached=cause instanceof AcquireFailure?cause.stage:'lease',inner=cause instanceof AcquireFailure?cause.inner:cause;console.warn(`native_acquire_failed stage=${reached} error=${(inner instanceof Error?inner.message:'unknown').slice(0,80)}`)}
    if(dead||nativeFailed)return
    const r=current
    if(r&&(action==='handoff'||action==='release')){
     if(r.writable&&owns(viewNow,r.generation)&&r.phase?.phase!=='read-only')resumeViewing(r)
     else if(!r.nativeReadOnly)await retireWrite(r).catch(()=>stop('Editor access was not confirmed.'))
    }
    const off=cause instanceof AcquireFailure?cause.inner:cause
    if(!dead&&off instanceof NativeAppOffError){setAppOff(off.label);setError(viewNow.mine&&current?.writable?appDrainHolderText(off.label):appOffText(off.label));setSavedVersion(undefined);return}
    if(!dead){setError(cause instanceof AcquireFailure&&cause.released!==undefined?acquireFailure(cause.stage,cause.released):handoffConfirmed?'The request was sent, but the current editing state cannot be loaded. The draft is kept.':'The result of the action was not confirmed. The draft is kept.');setSavedVersion(undefined)}
   }).finally(()=>{settled?.(false);transition=false;if(!dead){setBusy(false);catchUpReader()}})
  }
  downloadOriginalWorkbook.current=()=>{
   const recovery=createNativeDocumentClient({workspaceId,pageId})
   void(async()=>{
    await recovery.open(readLossAbort.signal)
    if(!recovery.originalWorkbook)throw Error('original_workbook_unavailable')
    const bytes=await recovery.originalWorkbook(readLossAbort.signal)
    if(!closed&&!readLossAbort.signal.aborted)downloadNativeExport({name:'sheet-original.json',bytes})
   })().catch(()=>{if(!closed)setError('The original Sheet could not be downloaded. Check your access and try again.')}).finally(()=>recovery.close())
  }
  const initialStartup=armAdmissionDeadline()
  void(async()=>{
   const admission=client
   const opened=await admission.open(lifetime.signal);if(dead||client!==admission)return
   if(opened.applicationId!=='wordcraft'&&opened.applicationId!=='gridcraft')throw Error('unsupported_native_application');sessionApplication=opened.applicationId;sessionId=opened.sessionId
   // Start authority monitoring before document delivery and the potentially
   // slow import. Lease events cannot start reader refreshes during bootstrap.
   watch();if(dead||client!==admission)return
   const snapshot=await admission.read(lifetime.signal);if(dead||client!==admission)return
   await currentLease(admission,lifetime.signal);if(dead||client!==admission)return
   await mountEditor(snapshot,viewNow.generation??'1');if(dead||client!==admission)return;finishAdmission(initialStartup);setBusy(false)
   void loadProposals();proposalWatch=setInterval(()=>{if(viewNow.can_edit===true)void loadProposals()},5000)
  })().catch(cause=>{
   if(dead)return
   if(cause instanceof LegacyWorkbookError){
    setLegacySource(true)
    stop(cause.code==='legacy_workbook_fidelity'
     ? 'The Sheet could not be converted to Excel with its contents intact.'
     : cause.code==='legacy_workbook_needs_editor'
       ? 'Someone with edit access needs to open this Sheet to convert it to Excel.'
       : 'The Sheet could not be converted now. Reopen it to try again.')
    return
   }
   stop('The document cannot be opened safely.');proveReadLoss()
  })
  function watch(){
   // Callbacks of a superseded admission never act on its fresh successor.
   const own=client
   // The server renews a human holder while this stream is open, but a stalled
   // renderer can read those renewals late. A lapsed cached expiry alone asks the
   // server once; loss, an invalid reply, a failed check or no answer within
   // RECHECK_MS still retires write.
   let recheckSince=0
   leaseWatch=setInterval(()=>{
    const r=current;if(!r?.writable||owns(viewNow,r.generation))return
    const retire=()=>void retireWrite(r).catch(()=>stop('Editing expired.'))
    if(recheckSince){if(Date.now()-recheckSince>=RECHECK_MS)retire();return}
    if(!holds(viewNow,r.generation)){retire();return}
    const since=Date.now();recheckSince=since
    void currentLease(own,lifetime.signal).then(raw=>{if(dead||client!==own||!r.writable)return;const next=leaseView(raw);if(!owns(next,r.generation)){retire();return}}).catch(()=>{if(!dead&&client===own&&r.writable)retire()}).finally(()=>{if(recheckSince===since)recheckSince=0})
   },1000)
   renewNow=()=>{void own.reauthorize(lifetime.signal).then(()=>{readConfirmed();if((offerStale&&readable||frameWithdrawn)&&!offerCheck&&client===own)askContent()}).catch(()=>{if(!dead&&client===own&&metadataOnly){retryMetadata();return}if(!dead&&client===own)void retireConnection('The connection was closed.').catch(()=>stop('Editor access was not confirmed.'))})}
   reauthorize=setInterval(renewNow,NATIVE_REAUTHORIZE_INTERVAL_MS)
   void own.events(lifetime.signal,(event,value)=>{
    if(dead||client!==own)return
    if(event==='session_retired'&&value.draft==='purge'){
     void retirePrivacy().catch(()=>setError('Deleting the local draft was not confirmed.'));return
    }
    if(event==='session_retired'&&metadataOnly&&value.reason!=='write_revoked'){retryMetadata();return}
    if(event==='session_retired'&&nativeFailed&&!metadataOnly){void retireConnection('The connection was closed.').catch(()=>stop('Editor access was not confirmed.'));return}
    if(event==='session_retired'&&value.reason==='write_revoked'&&(initializing||current&&!current.ready)){stop('Edit access was removed while opening. The document did not open.');return}
    if(event==='session_retired'&&value.reason==='write_revoked'){void reopenReadOnly().catch(()=>stop('Editor access was not confirmed.'));return}
    if(event==='session_retired'){void retireConnection('The connection was closed.').catch(()=>stop('Editor access was not confirmed.'));return}
    readConfirmed();void observeLease(value,true).catch(()=>{if(!dead&&client===own&&metadataOnly){retryMetadata();return}if(!dead&&client===own)void retireConnection('Current access cannot be confirmed.').catch(()=>stop('Editor access was not confirmed.'))})
   }).catch(()=>{if(!dead&&client===own&&metadataOnly){retryMetadata();return}if(!dead&&client===own)void retireConnection('Connection lost. The write is not confirmed.').catch(()=>stop('Editor access was not confirmed.'))})
  }
  return()=>{unsubscribeAgent();unregisterUndo();agentQueue.dispose();closed=true;clearTimeout(metadataRetry);clearTimeout(accessNotice);clearInterval(keptDraftWatch);readLossAbort.abort();unregisterPrivacy();window.removeEventListener('message',windowMessage);window.removeEventListener('online',backOnline);document.removeEventListener('visibilitychange',backVisible);window.removeEventListener('pageshow',checkOnReturn);control.current=()=>{};retryRead.current=()=>{};copyRecovery.current=()=>{};publishPreview.current=()=>{};history.current={load:async()=>null,loadVersion:async()=>{throw Error('native_editor_closed')},restore:()=>{}};stopAgent.current=()=>{};reconcile.current=()=>{};compare.current=()=>{};decide.current=()=>{};compareProposal.current=()=>{};downloadDraft.current=()=>{};retrySave.current=()=>{};downloadXlsx.current=()=>{};downloadOriginalWorkbook.current=()=>{};stop('Editor closed',true)}
 },[enabled,humanId,workspaceId,pageId,scopeKey,identityKey])
 function copy(draft:NativeDraft){
  // A workbook file draft is downloaded as the workbook it is (it starts with the zip signature).
  const named=draft as NativeDraft&{format?:unknown;name?:unknown},zip=draft.bytes[0]===0x50&&draft.bytes[1]===0x4b
  const ext=zip?(named.format==='xlsm'?'xlsm':'xlsx'):'json'
  const type=ext==='xlsm'?'application/vnd.ms-excel.sheet.macroEnabled.12':ext==='xlsx'?'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet':'application/json'
  const base=zip&&typeof named.name==='string'&&named.name.length>0&&named.name.length<=255?named.name.replace(/\.(xlsx|xlsm)$/i,''):draft.applicationId
  const blob=new Blob([new Uint8Array(draft.bytes)],{type}),url=URL.createObjectURL(blob),link=document.createElement('a');link.href=url;link.download=`${base}-${draft.lossy?'incomplete':'recovery'}.${ext}`;link.click();setTimeout(()=>URL.revokeObjectURL(url),0)}
 const shownProposals=readVerified&&!retainedBuffer&&!connectionRetired?proposals:[],shownRecoveries=readVerified&&!offerWithdrawn?recoveries:[]
 const mayPublish=!connectionRetired&&view.can_edit===true&&view.can_share===true&&ownsEditor
 const mayDownload=readVerified&&!retainedBuffer&&!connectionRetired&&applicationId==='gridcraft'&&exportRefused?.reason!=='forbidden'
 const downloadBlocked=exporting||(exportRefused?.reason==='too_large'&&exportRefused.version===(savedVersion??openedVersion??''))
 const savedValues=Boolean(openedFile&&readVerified&&!connectionRetired&&!openedFile.calculated)
 const bar=toolbar&&stateScope===identityKey&&enabled?createPortal(<TooltipProvider><div className="flex w-full min-w-0 flex-wrap items-center gap-1.5">
  <NativeCraftLeaseBar variant="inline" view={view} self={humanId} mayEdit={readVerified&&!retainedBuffer&&view.can_edit===true&&!connectionRetired&&!(appOff&&!view.mine)} pending={busy} requested={Boolean(handoffRequest&&!connectionRetired&&!view.mine&&view.generation===handoffRequest.generation&&view.holder?.actor_id===handoffRequest.holder)} onAction={(action,body)=>control.current(action,body)} onStopAgent={()=>stopAgent.current()}/>
  {readVerified&&!retainedBuffer&&!connectionRetired&&view.can_edit===true&&view.mine&&view.can_write&&!ownsEditor&&!appOff&&<HintTooltip hint={TAKE_OVER_HINT}><Button size="sm" disabled={busy} onClick={()=>control.current('acquire',{})}>Take over editing</Button></HintTooltip>}
  {/* No badges in the bar; the note about saved values is in the menu, and only until the Sheet is recalculated. */}
  <DropdownMenu><DropdownMenuTrigger asChild><Button variant="ghost" size="icon" className="ml-auto shrink-0" aria-label="More for this Sheet"><MoreHorizontal className="size-4" aria-hidden/></Button></DropdownMenuTrigger>
   <DropdownMenuContent align="end" className="max-w-xs">
    {savedValues&&<><HintTooltip hint={SAVED_VALUES_HINT} side="left"><p className="px-2 py-1.5 text-xs text-muted-foreground">{SAVED_VALUES_NOTE}</p></HintTooltip><DropdownMenuSeparator/></>}
    {mayDownload&&<DropdownMenuItem disabled={downloadBlocked} onSelect={()=>downloadXlsx.current()}><span>Download .{openedFile?.format??'xlsx'}{dirty&&<small className="block text-muted-foreground">Last saved version, without unsaved changes</small>}</span></DropdownMenuItem>}
    {mayDownload&&openedFile&&<DropdownMenuItem disabled={exporting} onSelect={()=>downloadXlsx.current('1')}>Download original (version 1)</DropdownMenuItem>}
    {historyHref&&<DropdownMenuItem asChild><a href={historyHref} target="_blank" rel="noopener noreferrer">Version history</a></DropdownMenuItem>}
    {mayPublish&&<DropdownMenuItem disabled={busy} onSelect={()=>publishPreview.current()}>Publish preview</DropdownMenuItem>}
   </DropdownMenuContent></DropdownMenu>
 </div></TooltipProvider>,toolbar):null
 if(stateScope!==identityKey)return <p role="status" className="p-6">Connecting…</p>
 if(!enabled)return <p className="p-6">The native editor is not turned on.</p>
 return <section className={editorHidden?'flex flex-col':'flex h-full min-h-[600px] flex-col'} aria-label={applicationId==='gridcraft'?'GridCraft workbook':'WordCraft document'}>
  {bar}
  {(()=>{const controls=<><NativeCraftLeaseBar variant={placeLease?'popover':'bar'} view={view} self={humanId} mayEdit={readVerified&&!retainedBuffer&&view.can_edit===true&&!connectionRetired&&!(appOff&&!view.mine)} pending={busy} requested={Boolean(handoffRequest&&!connectionRetired&&!view.mine&&view.generation===handoffRequest.generation&&view.holder?.actor_id===handoffRequest.holder)} onAction={(action,body)=>control.current(action,body)} onStopAgent={()=>stopAgent.current()}/>{readVerified&&!retainedBuffer&&!connectionRetired&&view.can_edit===true&&view.mine&&view.can_write&&!ownsEditor&&!appOff&&<HintTooltip hint={TAKE_OVER_HINT}><Button disabled={busy} onClick={()=>control.current('acquire',{})}>Take over editing</Button></HintTooltip>}</>;return compact?null:placeLease?placeLease(controls):controls})()}
  <NativeCraftAgentTaskStatus workspaceId={workspaceId} pageId={pageId} humanId={humanId} enabled={readVerified&&!retainedBuffer&&view.can_edit===true&&!connectionRetired} workId={agentWork}/>
  <NativeCraftAgentStart workspaceId={workspaceId} pageId={pageId} humanId={humanId} enabled={readVerified&&!retainedBuffer&&view.can_edit===true&&!connectionRetired&&applicationId!==undefined&&!hostBlocked&&!hostRetiring} holder={view.holder?.kind??null} onStarted={agentStarted} place={placeAgentStart}/>
  {!compact&&mayPublish&&<Button variant="outline" disabled={busy} onClick={()=>publishPreview.current()}>Publish preview</Button>}
  {publishedVersion&&<p className="px-3 py-2">Shared preview · version {publishedVersion}</p>}
  {!compact&&mayDownload&&<div className="flex flex-wrap items-center gap-2 px-3 py-2">
   <Button variant="outline" disabled={downloadBlocked} onClick={()=>downloadXlsx.current()}>Download .{openedFile?.format??'xlsx'}</Button>
   {openedFile&&<Button variant="ghost" disabled={exporting} onClick={()=>downloadXlsx.current('1')}>Download original</Button>}
   {dirty&&<span className="text-sm text-muted-foreground">The download is the last saved version; your unsaved changes are not in it.</span>}
  </div>}
  {largeOpen&&!connectionRetired&&<p aria-live="polite" className="px-3 py-2 text-sm text-muted-foreground">Opening a large {largeOpen}… this can take a few minutes.</p>}
  {exportNote&&(exportNote.alert?<p role="alert" className="px-3 py-2">{exportNote.text}</p>:<p aria-live="polite" className="px-3 py-2 text-sm text-muted-foreground">{exportNote.text}</p>)}
  <div role="status" className={placeLease||compact?'sr-only':'px-3 py-2'} data-save-phase={saveTrace?.phase} data-save-reason={saveTrace?.reason} data-quota-used={quota?.usedBytes} data-quota-limit={quota?.limitBytes} data-quota-level={quota?.level}><span>{label}</span>{savedVersion&&<span> · version {savedVersion}</span>}{quota&&(quota.level==='full'||quota.level==='critical'||quota.warning)&&<span className="ml-3 text-muted-foreground">{quota.level==='full'?'Version history is full':quota.level==='critical'?'Version history is almost full':'Version history is nearing its limit'}</span>}</div>
  {!compact&&savedValues&&<HintTooltip hint={SAVED_VALUES_HINT}><p tabIndex={0} className="w-fit px-3 pb-1 text-xs text-muted-foreground">{SAVED_VALUES}</p></HintTooltip>}
  {openedFile&&readVerified&&!connectionRetired&&fidelityAsk&&<div role="alertdialog" aria-labelledby="sheet-fidelity-title" aria-describedby="sheet-fidelity-body" className="space-y-2 border-y px-3 py-3">
   <p id="sheet-fidelity-title" className="font-medium">Before you edit this workbook</p>
   <div id="sheet-fidelity-body" className="space-y-2 text-sm">
    <ul className="list-disc space-y-1 pl-5">{openedFile.warnings.map(w=><li key={w.code}>{sheetWarningText(w.code,w.count)}</li>)}</ul>
    <p className="text-muted-foreground">Saving stores a new version. The file as it was uploaded stays as version 1, and you can download it at any time.</p>
   </div>
   <div className="flex flex-wrap gap-2"><Button onClick={()=>{fidelity.current.accepted=true;setFidelityAsk(false);control.current('acquire',{})}}>Edit anyway</Button><Button variant="outline" onClick={()=>setFidelityAsk(false)}>Keep reading</Button></div>
  </div>}
  {openedFile&&readVerified&&!connectionRetired&&!fidelityAsk&&openedFile.warnings.length>0&&<details className="px-3 pb-2 text-sm">
   <summary className="cursor-pointer text-muted-foreground">What this editor keeps as it is in this workbook</summary>
   <ul className="mt-1 list-disc space-y-1 pl-5">{openedFile.warnings.map(w=><li key={w.code}>{sheetWarningText(w.code,w.count)}</li>)}</ul>
  </details>}
  {readVerified&&historyKey>0&&!connectionRetired&&(placeHistory?placeHistory(<NativeCraftVersionHistory embedded load={signal=>history.current.load(signal)} loadVersion={(version,signal)=>history.current.loadVersion(version,signal)} scope={{humanId,workspaceId,pageId}} refreshKey={`${historyKey}:${savedVersion??''}`} canRestore={view.can_edit===true&&ownsEditor} restoreHint={view.can_edit===true?'Take over editing to restore a version.':'Only the person editing the document now can restore a version.'} pending={busy} onRestore={version=>history.current.restore(version)}/>):<NativeCraftVersionHistory load={signal=>history.current.load(signal)} loadVersion={(version,signal)=>history.current.loadVersion(version,signal)} scope={{humanId,workspaceId,pageId}} refreshKey={`${historyKey}:${savedVersion??''}`} canRestore={view.can_edit===true&&ownsEditor} restoreHint={view.can_edit===true?'Take over editing to restore a version.':'Only the person editing the document now can restore a version.'} pending={busy} onRestore={version=>history.current.restore(version)}/>)}
  {saveTrace?.reason==='document_too_large'&&<div role="alert" className="space-y-2 border-y px-3 py-2"><p><b>This workbook is larger than the 32 MiB a Sheet can save, so it is NOT saved on the server.</b> Your changes stay in this editor. Do not close the tab; remove data or split the workbook, or download it as a file.</p></div>}
  {/* One sentence and two actions; the detail folded, so the grid keeps its room. */}
  {conflictShown&&<div role="alert" className="flex flex-wrap items-center gap-x-3 gap-y-1 border-y px-3 py-1.5 text-sm">
   <p className="min-w-0 flex-1 basis-56">Not saved: this Sheet changed on the server meanwhile. Your changes are kept here.</p>
   <div className="flex shrink-0 flex-wrap gap-2"><Button size="sm" onClick={()=>setReopened(n=>n+1)}>Load the latest version</Button><Button size="sm" variant="outline" onClick={()=>downloadDraft.current()}>Download my changes</Button></div>
   <details className="basis-full text-xs text-muted-foreground"><summary className="cursor-pointer">What happens</summary>Your changes stay in this editor and in this browser. Load the latest version to see what changed; your changes are offered beside it to compare or keep as a copy. Saving again here would be refused, because the server already holds a newer version.</details>
  </div>}
  {fidelityLoss&&!connectionRetired&&<div role="alert" data-craft-banner="fidelity-loss" className="space-y-1 border-y px-3 py-2"><p><b>Saving here would drop parts of this workbook ({fidelityPartsText(fidelityLoss)}). Your changes stay in the editor.</b> {fidelityKept?'Nothing was saved on the server. This browser keeps a copy of the file without those parts, to download only.':'Nothing was saved, on the server or in this browser: leaving or reloading this page loses them.'}</p><p className="text-sm text-muted-foreground">The stored version and the original file (version 1) keep every part. Saving is tried again after your next change.</p></div>}
  {saveTrace?.phase==='draft'&&saveTrace.reason==='fidelity_loss'&&!connectionRetired&&<p role="alert" className="border-y px-3 py-2 text-sm">This save would drop macros or signatures, so it was not saved. Your work is kept.</p>}
  {quotaFull&&<div role="alert" className="space-y-2 border-y px-3 py-2"><p><b>Storage is full, so nothing more can be saved. Your changes are not saved on the server yet.</b> The editor keeps them and the draft stays in this browser. All earlier versions stay.</p><p className="text-sm text-muted-foreground">Do not close the editor. As soon as space is free, choose Save again (or Ctrl+S). To be safe, download your work as a file.</p><div className="flex flex-wrap gap-2"><Button onClick={()=>retrySave.current()}>Save again</Button><Button variant="outline" onClick={()=>downloadDraft.current()}>Download draft</Button></div></div>}
  {retainedBuffer&&<div role="group" aria-label="Local copy recovery" className="space-y-2 border-y px-3 py-2"><p>The last changes were not captured with confirmation. The original editor stays locked; its content is not overwritten by a new document.</p>{readVerified&&!offerWithdrawn?<Button disabled={busy} variant="outline" onClick={()=>downloadDraft.current()}>Download the local copy</Button>:<Button disabled={busy||authEnded} variant="outline" onClick={()=>retryRead.current()}>Check access again</Button>}</div>}
  {uncertain&&<Button variant="outline" onClick={()=>reconcile.current(true)}>Check the result</Button>}
  {legacySource&&<Button type="button" variant="outline" className="mx-3 self-start" onClick={()=>downloadOriginalWorkbook.current()}>Download original Sheet</Button>}
  {agentNotice&&<p role="status" data-testid="agent-changed-notice" className="px-3 py-2">{agentNotice}</p>}
  {rebaseNote&&<RebaseNote key={`${rebaseNote.kind}:${rebaseNote.kind==='layout'?rebaseNote.head:rebaseNote.kind==='rebased'?rebaseNote.version:''}`} note={rebaseNote} historyHref={historyHref} onDismiss={()=>setRebaseNote(undefined)} onKeepMine={layout=>{const c=keepMine.current;return Boolean(c&&keepLocalOver(layout,c))}}/>}
  {error&&<p role="alert" className="px-3 py-2">{error}{mismatch&&error===mismatch.message&&<> <a className="underline" href={mismatch.address+window.location.pathname+window.location.search+window.location.hash} rel="noopener noreferrer">Open at {mismatch.address}</a></>}</p>}
  {(shownProposals.length>0||shownRecoveries.length>0)&&<div data-craft-banner={shownProposals.length>0?'proposal':'draft'} className={'mx-2 my-1.5 grid gap-0.5 rounded-lg border p-1 text-sm '+(shownProposals.length>0?'border-chip-violet-fg/30 bg-chip-violet-bg':'border-warn/40 bg-warn/10')}>
   {shownProposals.map(proposal=><div key={proposal.id} role="group" aria-label={`Proposal by ${proposal.authorName}`} className="flex flex-wrap items-center gap-2 px-2 py-1.5">
   <Bot aria-hidden className="size-4 shrink-0 text-chip-violet-fg"/><span className="min-w-0 flex-1"><b>{proposal.authorName} prepared a proposal on version {proposal.baseVersion}</b>{proposal.summary&&<span> · {proposal.summary}</span>}<small className="block text-muted-foreground">{proposal.state==='stale'?'Out of date: the document changed in the meantime, so the proposal cannot be applied.':'The document stays unchanged until you accept the proposal.'}</small></span>
   <Button variant="outline" onClick={()=>compareProposal.current(proposal)}>Compare</Button>
   {proposal.state==='open'&&view.can_edit===true&&<Button variant="outline" disabled={busy} onClick={()=>decide.current(proposal.id,'reject')}>Reject</Button>}
   {proposal.state==='open'&&ownsEditor&&<Button disabled={busy} onClick={()=>decide.current(proposal.id,'accept')}>Accept</Button>}
  </div>)}
   {shownRecoveries.map((recovery,index)=>recovery.draft.lossy?<div key={index} role="group" aria-label="Unsaved changes, incomplete file" className="flex flex-wrap items-center gap-2 px-2 py-1.5"><RotateCcw aria-hidden className="size-4 shrink-0 text-warn"/><span className="min-w-0 flex-1">Unsaved changes on version {recovery.draft.baseVersion}, not saved: this copy lacks {fidelityPartsText(recovery.draft.lossy)}.<small className="block text-muted-foreground">The stored version keeps every part.</small></span><Button variant="outline" onClick={()=>copyRecovery.current(scopeKey,recovery.draft)}>Download incomplete copy</Button></div>:<div key={index} className="flex flex-wrap items-center gap-2 px-2 py-1.5"><RotateCcw aria-hidden className="size-4 shrink-0 text-warn"/><span className="min-w-0 flex-1">Local draft · version {recovery.draft.baseVersion}</span><Button variant="outline" onClick={()=>compare.current(recovery.draft,scopeKey)}>Compare</Button><Button variant="outline" onClick={()=>copyRecovery.current(scopeKey,recovery.draft)}>Keep a copy</Button></div>)}
  </div>}
  {readVerified&&!offerWithdrawn&&comparison&&<div className="grid gap-2 p-3 md:grid-cols-2"><p className="md:col-span-2">Server version {comparison.version} and {comparison.draftLabel?.toLowerCase()??'local draft'}: {comparison.equal?(comparison.files?'identical files':'identical data'):(comparison.files?'different files':'different data')}.{comparison.files?' Download the draft to open it next to the server version.':' The native data is read-only.'}</p>{comparison.files?<><span className="break-all text-xs">Server SHA256 {comparison.serverHash}</span><span className="break-all text-xs">{comparison.draftLabel??'Local draft'} SHA256 {comparison.draftHash}</span></>:<><label>Server version<textarea aria-label="Server version" readOnly value={comparison.server} className="h-48 w-full font-mono text-xs"/><span className="break-all text-xs">SHA256 {comparison.serverHash}</span></label><label>{comparison.draftLabel??'Local draft'}<textarea aria-label={comparison.draftLabel??'Local draft'} readOnly value={comparison.draft} className="h-48 w-full font-mono text-xs"/><span className="break-all text-xs">SHA256 {comparison.draftHash}</span></label></>}<Button variant="outline" onClick={()=>setComparison(undefined)}>Close comparison</Button></div>}
  {accessCheck&&<p className="px-3 py-2 text-sm text-muted-foreground">Checking access…</p>}
  {/* Over the top edge of the editor, so nothing below moves when a line comes or goes. */}
  <div className="pointer-events-none relative z-10 h-0"><div className="absolute inset-x-2 top-2 flex flex-col items-center gap-1 text-sm">
   {agentWorking&&<p role="status" data-testid="agent-working" className="max-w-full rounded-md border bg-popover px-3 py-1.5 text-popover-foreground shadow-sm">The agent is changing the Sheet…</p>}
   <div aria-live="polite" data-take-over-notice={takeNotice??''} data-edit-attempt={attemptState} className="max-w-full">{takeNotice&&!connectionRetired&&<div className="pointer-events-auto flex max-w-full flex-wrap items-center gap-2 rounded-md border bg-popover px-3 py-1.5 text-popover-foreground shadow-sm">
    <span>{takeNotice==='taking'?'Taking over editing…':takeNotice==='taken'?'You are editing now.':takeNotice==='viewer'?'You can view this Sheet but not edit it.':takeNotice==='dropped'?'What you typed was not entered. Click the cell and type it again.':view.holder?.kind==='agent'?`${view.holder.display_name.trim()||'The agent'} is changing this Sheet. You can edit when it is done.`:view.holder&&(view.holder.actor_id===`user:${humanId}`||view.holder.actor_id===humanId)?'You are editing this Sheet in another tab or window.':`${view.holder?.display_name.trim()||'Someone'} is editing this Sheet. Use Request edit to ask for it.`}</span>
    <Button size="xs" variant="ghost" className="coarse:min-h-[44px]" onClick={()=>setTakeNotice(undefined)}>Dismiss</Button>
   </div>}</div>
  </div></div>
  <div ref={mount} tabIndex={-1} className={editorHidden?'hidden':'relative min-h-0 flex-1'} aria-busy={busy||accessCheck}/>
 </section>
}

/** After the agent's version was added to the person's unsaved work: the cells where the person's value was kept, or why it could not be added. Cell texts stay in this page. */
function RebaseNote({note,historyHref,onDismiss,onKeepMine}:{note:RebaseOutcome;historyHref?:string;onDismiss():void;onKeepMine(layout:{head:string;headHash?:string}):boolean}){
 const [kept,setKept]=React.useState<'yes'|'no'>()
 // The sentence has its own row: beside the buttons in a narrow panel it got no width and wrapped word by word.
 if(note.kind==='layout')return <div role="status" data-testid="agent-rebase-note" className="space-y-2 px-3 py-2 text-sm">
  <p>{kept==='yes'?`Your changes are saved on top. ${note.actorName??'The agent'}'s change from version ${note.head} is not in the Sheet; it stays in version ${note.head} in the history.`
   :kept==='no'?'Your changes could not be put on top right now. They are kept here; try again in a moment.'
   :`${note.actorName??'The agent'} saved version ${note.head}, which changes the sheet's layout, so it was not added to your unsaved changes. Your changes are kept. If you keep yours, ${note.actorName??'the agent'}'s change in version ${note.head} will not be in the Sheet; it stays in the history.`}</p>
  <div className="flex flex-wrap items-center gap-2">
  {kept!=='yes'&&note.headHash&&<Button type="button" size="xs" variant="secondary" onClick={()=>setKept(onKeepMine(note)?'yes':'no')}>Keep mine</Button>}
  {historyHref&&<a className="underline underline-offset-2" href={agentVersionHref(historyHref,note.head)} target="_blank" rel="noopener noreferrer">Open the agent&apos;s version</a>}
  <Button type="button" size="xs" variant="ghost" onClick={onDismiss}>Dismiss</Button>
  </div>
 </div>
 if(note.kind!=='rebased')return null
 const shown=note.conflicts.slice(0,20),more=note.conflicts.length-shown.length
 return <div role="status" data-testid="agent-rebase-note" className="px-3 py-2 text-sm">
  <p>{note.actorName??'The agent'}&apos;s change (version {note.version}) is in your Sheet. In {note.conflicts.length}{note.truncated?'+':''} {note.conflicts.length===1?'cell':'cells'} you had changed too, your value was kept:</p>
  <ul className="mt-1 max-h-32 overflow-auto text-xs">{shown.map(c=><li key={`${c.sheet}!${c.cell}`}>{`'${c.sheet}'!${c.cell}`}: kept “{c.kept}”, {note.actorName??'the agent'} wanted “{c.agent}”</li>)}</ul>
  {(more>0||note.truncated)&&<p className="text-xs text-muted-foreground">and more</p>}
  <Button type="button" size="xs" variant="ghost" onClick={onDismiss}>Dismiss</Button>
 </div>
}

/** The agent's version itself in Artifacts: what Keep mine leaves out of the Sheet. */
export {agentVersionHref}
