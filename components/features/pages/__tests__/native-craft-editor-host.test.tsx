import {webcrypto} from "node:crypto"
import {render,screen,fireEvent,waitFor,act,within,cleanup} from "@testing-library/react"
import {beforeEach,afterEach,describe,it,expect,vi} from "vitest"
import type {NativeEditorPortOptions} from "@/lib/mooring/native-editor-port"
import {NativeSaveError,type NativeSaveRequest} from "@/lib/mooring/single-editor-save"
import {NativeCraftEditorHost,nativeCraftFrameURL,nativeOpenBudget,type NativeCraftHandBack} from "../native-craft-editor-host"
import {publishAgentVersion} from "@/lib/mooring/native-agent-version"

const m=vi.hoisted(()=>({apiFetch:vi.fn(),createClient:vi.fn(),openJournal:vi.fn(),createPort:vi.fn(),registerPrivacy:vi.fn(),purge:vi.fn()}))
import { requestSheetUndo } from "@/lib/mooring/native-undo"
vi.mock("@/lib/api-fetch",()=>({apiFetch:m.apiFetch,AUTH_EVENT:"unit-native-auth-retired"}))
vi.mock("../native-craft-agent-start",()=>({NativeCraftAgentStart:(p:{enabled:boolean;holder:string|null;onStarted(id:string):void})=><button data-testid="agent-start" data-enabled={String(p.enabled)} data-holder={String(p.holder)} onClick={()=>p.onStarted("work-7")}/>}))
vi.mock("@/lib/mooring/native-document-client",()=>({createNativeDocumentClient:m.createClient}))
vi.mock("@/lib/mooring/native-editor-port",()=>({createNativeEditorPort:m.createPort}))
vi.mock("@/lib/mooring/native-draft",async()=>({...await vi.importActual("@/lib/mooring/native-draft"),openNativeDraftJournal:m.openJournal}))
// Journal retries without real pauses; deadlines and late-answer handling stay real.
vi.mock("@/lib/mooring/native-journal-open",async()=>{const actual=await vi.importActual<typeof import("@/lib/mooring/native-journal-open")>("@/lib/mooring/native-journal-open");return {...actual,openJournalWithRetry:(open:Parameters<typeof actual.openJournalWithRetry>[0],o:Parameters<typeof actual.openJournalWithRetry>[1])=>actual.openJournalWithRetry(open,{...o,sleep:()=>Promise.resolve()})}})
vi.mock("@/lib/document-queue",async()=>({...await vi.importActual("@/lib/document-queue"),registerDocumentDraftWriter:m.registerPrivacy,purgeDocumentDrafts:m.purge}))
vi.mock("../workbook-editor-host",()=>({editorFrameSupported:()=>true,createEditorFrame:(doc:Document,src:string,onLoad:()=>void)=>{const f=doc.createElement('iframe');f.setAttribute('sandbox','allow-scripts');f.setAttribute('credentialless','');f.setAttribute('referrerpolicy','no-referrer');f.srcdoc='';f.src=src;queueMicrotask(onLoad);return f}}))
const deferred=<T,>()=>{let resolve!:(v:T)=>void,reject!:(cause:unknown)=>void;const promise=new Promise<T>((r,f)=>{resolve=r;reject=f});return {promise,resolve,reject}}
const emptyLease={can_edit:true,can_share:true,holder:null,mine:false,generation:null,can_write:false,acquired_at:null,expires_at:null,handoff_requested_by:[]}
const ownedLease={...emptyLease,holder:{actor_id:"user:human",display_name:"Pavel",kind:"human"},mine:true,generation:"9007199254740995",can_write:true,expires_at:'2099-01-01T00:00:00Z'}
// Another person (a holder with the viewer's own actor id is the viewer's other tab).
const otherPerson={actor_id:"user:pavel",display_name:"Pavel",kind:"human"}
const receipt=(r:NativeSaveRequest)=>({...r,bytes:undefined,version:(BigInt(r.baseVersion)+1n).toString(),candidateHash:r.contentHash,durability:"node",issuedBy:"crewship",phase:"committed"})
let authSuspend:(()=>Promise<void>)|undefined
let privacyRetire:(()=>Promise<void>)|undefined
let client:ReturnType<typeof clientRig>,journal:ReturnType<typeof journalRig>,ports:ReturnType<typeof portRig>[],portOptions:NativeEditorPortOptions[],stream:((event:"lease"|"session_retired",value:Record<string,unknown>)=>void)|undefined
function clientRig(){return {share:vi.fn(async(r:{targetVersion:string})=>({version:r.targetVersion,durability:'node',issuedBy:'crewship',phase:'shared'})),open:vi.fn().mockResolvedValue({sessionId:"private-session",applicationId:"wordcraft"}),read:vi.fn().mockResolvedValue({version:"1",bytes:new TextEncoder().encode('{"body":[]}'),applicationId:"wordcraft",enginePin:"a".repeat(64),editorBundle:"b".repeat(64),editorURL:`https://native.example/b/${"b".repeat(64)}/index.html`,quota:{usedBytes:"12",limitBytes:"99999",warning:false,retention:"keep-all"}}),lease:vi.fn().mockResolvedValue(emptyLease),leaseAction:vi.fn(async(action:string):Promise<unknown>=>{if(action==='acquire')client.lease.mockResolvedValue(ownedLease);return ownedLease}),events:vi.fn((_signal:AbortSignal,callback:typeof stream)=>{stream=callback;return new Promise<void>(()=>{})}),reauthorize:vi.fn().mockResolvedValue(true),save:vi.fn(async(r:NativeSaveRequest)=>receipt(r)),reconcile:vi.fn(),close:vi.fn(),exportXlsx:vi.fn(async()=>({blob:new Blob(['PK']),filename:'Quarterly plan.xlsx',version:'1'})),proposals:vi.fn().mockResolvedValue([]),proposalBytes:vi.fn(),decideProposal:vi.fn()}}
function journalRig(){return {persist:vi.fn().mockResolvedValue(undefined),confirm:vi.fn().mockResolvedValue(undefined),discardRecovered:vi.fn().mockResolvedValue(undefined),recovery:vi.fn().mockResolvedValue([]),onRetire:vi.fn(()=>()=>{}),close:vi.fn().mockResolvedValue(undefined),revokeRead:vi.fn().mockResolvedValue(undefined),eraseOwnRow:vi.fn().mockResolvedValue(undefined)}}
// Word's port names the engine's own revision as nativeState; most tests leave it unset.
// Bootstrap and an untouched snapshot export the same normalized bytes, as the real port requires.
let rigNativeState:string|undefined
function portRig(){const state=rigNativeState?{nativeState:rigNativeState}:{};return {loaded:vi.fn(),handleWindowMessage:vi.fn(),bootstrap:vi.fn().mockResolvedValue({bytes:new TextEncoder().encode('{"body":[1]}'),editorRevision:"initial",documentId:"doc",...state}),snapshot:vi.fn().mockResolvedValue({bytes:new TextEncoder().encode('{"body":[1]}'),editorRevision:"snapshot-1",documentId:"doc",...state}),validate:vi.fn().mockResolvedValue(undefined),ack:vi.fn().mockResolvedValue(true),saveFailed:vi.fn(),setReadOnly:vi.fn(async(v:boolean)=>v),execute:vi.fn(async()=>({documentId:'doc',revision:'doc:9',applied:1})),close:vi.fn()}}
beforeEach(()=>{vi.stubGlobal('crypto',webcrypto);rigNativeState=undefined;client=clientRig();journal=journalRig();ports=[];portOptions=[];stream=undefined;privacyRetire=undefined;authSuspend=undefined;m.purge.mockResolvedValue(undefined);m.registerPrivacy.mockImplementation((_human:string,retire:()=>Promise<void>,options?:{suspend?:()=>Promise<void>})=>{privacyRetire=retire;authSuspend=options?.suspend;return vi.fn()});m.createClient.mockReturnValue(client);m.openJournal.mockResolvedValue(journal);m.createPort.mockImplementation((o:NativeEditorPortOptions)=>{const p=portRig();ports.push(p);portOptions.push(o);queueMicrotask(()=>o.onReady?.('qualified'));return p})})
afterEach(()=>{vi.unstubAllGlobals();vi.clearAllMocks()})
const mount=()=>render(<NativeCraftEditorHost workspaceId="w" pageId="p" humanId="human" enabled />)
async function ready(){await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalledOnce());await waitFor(()=>expect(screen.getByRole('button',{name:'Take over editing'})).toBeEnabled())}
async function acquire(){fireEvent.click(screen.getByRole('button',{name:'Take over editing'}));await waitFor(()=>expect(ports.at(-1)?.setReadOnly).toHaveBeenCalledWith(false,expect.any(AbortSignal)))}

describe('retired draft rendered state',()=>{
 it.each(['untouched','edited','unretired'] as const)('write retirement keeps leave guard only for recoverable work (%s)',async(kind)=>{
  rigNativeState='revision:opened'
  const hashes:Promise<ArrayBuffer>[]=[]
  vi.stubGlobal('crypto',{randomUUID:webcrypto.randomUUID.bind(webcrypto),getRandomValues:webcrypto.getRandomValues.bind(webcrypto),subtle:{digest(algorithm:AlgorithmIdentifier,data:Parameters<typeof webcrypto.subtle.digest>[1]){const result=webcrypto.subtle.digest(algorithm,data);hashes.push(result);return result}}})
  const rendered=mount();await ready();await acquire()
  // Complete the baseline hash before retiring so this test isolates dirty
  // state from the independently reproduced pending-seed issue.
  await act(async()=>{await Promise.all(hashes)})
  vi.stubGlobal('crypto',webcrypto)
  if(kind==='edited'){
   ports.at(-1)!.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"body":[2]}'),editorRevision:'typed',documentId:'doc',nativeState:'revision:typed'})
   // Snapshot identity proves the edit even if the change event arrives late.
  }
  if(kind==='unretired')journal.confirm.mockRejectedValue(Error('journal unavailable'))
  await act(async()=>{
   stream?.('lease',{...ownedLease,mine:false,can_write:false})
   await waitFor(()=>expect(journal.close).toHaveBeenCalledOnce())
  })
  const status=rendered.container.querySelector('[data-save-phase]')!
  expect(status).not.toBeNull()
  const leaves=new Event('beforeunload',{cancelable:true})
  window.dispatchEvent(leaves)
  expect(client.save).not.toHaveBeenCalled()
  if(kind==='untouched'){
   expect(journal.confirm).toHaveBeenCalledOnce()
   expect.soft(leaves.defaultPrevented).toBe(false)
   expect(status).toHaveTextContent('Read-only')
   expect(status).not.toHaveTextContent('Unsaved draft')
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
  }else{
   expect(status).toHaveTextContent('Unsaved draft')
   expect(leaves.defaultPrevented).toBe(true)
   expect(screen.getByRole('button',{name:'Keep a copy'})).toBeInTheDocument()
  }
 })
})

// Off screen but still rendering while a capture runs; hidden after it (#5060).
const unseen=(frame:HTMLElement|null)=>frame?.style.visibility==='hidden'||frame?.style.opacity==='0'
describe('native Word host authority and persistence',()=>{
 it('confirms a handoff request only after the server reply and clears it for a new lease generation',async()=>{
  const held={...ownedLease,mine:false,can_write:false,holder:otherPerson}
  client.lease.mockResolvedValue(held)
  const confirmation=deferred<unknown>();client.leaseAction.mockReturnValueOnce(confirmation.promise)
  mount();const ask=await screen.findByRole('button',{name:'Request edit'})
  await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled())
  fireEvent.click(ask)
  expect(screen.queryByText('Request sent')).not.toBeInTheDocument()
  await act(async()=>confirmation.resolve({request_id:'confirmed-request',generation:held.generation}))
  await waitFor(()=>expect(screen.getByRole('button',{name:'Request sent'})).toBeDisabled())
  expect(screen.getByText('Waiting for the handover.')).toHaveAttribute('role','status')
  expect(ports[0].setReadOnly).not.toHaveBeenCalledWith(false,expect.anything())
  const next={...held,generation:'9007199254740996'};client.lease.mockResolvedValue(next)
  await act(async()=>stream?.('lease',next))
  expect(screen.getByRole('button',{name:'Request edit'})).toBeEnabled()
  expect(screen.queryByText('Request sent')).not.toBeInTheDocument()
 })
 it('keeps a confirmed handoff request truthful when the following lease read fails',async()=>{
  const held={...ownedLease,mine:false,can_write:false,holder:otherPerson}
  client.lease.mockResolvedValue(held)
  mount();const ask=await screen.findByRole('button',{name:'Request edit'})
  await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled())
  client.leaseAction.mockResolvedValueOnce({request_id:'confirmed-request',generation:held.generation})
  client.lease.mockRejectedValueOnce(Error('lease temporarily unavailable'))
  fireEvent.click(ask)
  await screen.findByText('The request was sent, but the current editing state cannot be loaded. The draft is kept.')
  expect(screen.getByRole('button',{name:'Request sent'})).toBeDisabled()
  expect(screen.queryByText('The result of the action was not confirmed. The draft is kept.')).not.toBeInTheDocument()
  expect(client.leaseAction).toHaveBeenCalledOnce()
 })
 it('ignores a late handoff reply while the old admission retires',async()=>{
  const held={...ownedLease,mine:false,can_write:false,holder:otherPerson}
  client.lease.mockResolvedValue(held)
  const confirmation=deferred<unknown>();client.leaseAction.mockReturnValueOnce(confirmation.promise)
  mount();const ask=await screen.findByRole('button',{name:'Request edit'})
  await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled())
  fireEvent.click(ask)
  const successor=clientRig(),opening=deferred<{sessionId:string;applicationId:string}>()
  successor.open.mockReturnValueOnce(opening.promise);successor.lease.mockResolvedValue(held)
  m.createClient.mockReturnValueOnce(successor)
  act(()=>stream?.('session_retired',{reason:'write_revoked'}))
  await waitFor(()=>expect(successor.open).toHaveBeenCalledOnce())
  await act(async()=>confirmation.resolve({request_id:'retired-request',generation:held.generation}))
  await act(async()=>opening.resolve({sessionId:'fresh-session',applicationId:'wordcraft'}))
  await waitFor(()=>expect(ports.at(-1)?.bootstrap).toHaveBeenCalled())
  await waitFor(()=>expect(screen.getByRole('button',{name:'Request edit'})).toBeEnabled())
  expect(screen.queryByText('Request sent')).not.toBeInTheDocument()
 })
 it.each(['offline','malformed'])('does not claim handoff was requested when the server response is %s',async(failure)=>{
  client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:otherPerson})
  if(failure==='offline')client.leaseAction.mockRejectedValueOnce(Error('offline'))
  else client.leaseAction.mockResolvedValueOnce({request_id:'missing-generation'})
  mount();const ask=await screen.findByRole('button',{name:'Request edit'})
  await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled())
  fireEvent.click(ask)
  await screen.findByText('The result of the action was not confirmed. The draft is kept.')
  expect(screen.getByRole('button',{name:'Request edit'})).toBeEnabled()
  expect(screen.queryByText('Request sent')).not.toBeInTheDocument()
 })
 it('keeps a freshly confirmed writer when an expired same-custody stream event overtakes its recheck',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   mount();await ready();await acquire();await screen.findByText('Ready to edit')
   const port=ports.at(-1)!,checks=client.lease.mock.calls.length,proof=deferred<typeof ownedLease>(),stale={...ownedLease,expires_at:new Date(Date.now()-1000).toISOString()}
   client.lease.mockReturnValueOnce(proof.promise)
   act(()=>stream?.('lease',stale));await act(async()=>vi.advanceTimersByTimeAsync(1001));await waitFor(()=>expect(client.lease.mock.calls.length).toBeGreaterThan(checks))
   act(()=>stream?.('lease',stale));await act(async()=>proof.resolve(ownedLease))
   expect(port.setReadOnly).not.toHaveBeenCalledWith(true,expect.anything());expect(screen.getByText('Ready to edit')).toBeInTheDocument();expect(client.save).not.toHaveBeenCalled()
  }finally{vi.useRealTimers()}
 })

 it('never unlocks on an older streamed grant when the final fresh HTTP lease proof denies custody',async()=>{
  mount();await ready();const checks=client.lease.mock.calls.length,proof=deferred<unknown>()
  client.leaseAction.mockImplementation(async()=>{client.lease.mockResolvedValueOnce(ownedLease).mockResolvedValueOnce(ownedLease).mockReturnValueOnce(proof.promise);return ownedLease})
  fireEvent.click(screen.getByRole('button',{name:'Take over editing'}));await waitFor(()=>expect(client.lease).toHaveBeenCalledTimes(checks+3))
  act(()=>stream?.('lease',ownedLease))
  await act(async()=>proof.resolve({...emptyLease,holder:otherPerson,generation:'9007199254740996'}))
  await waitFor(()=>expect(screen.getByRole('alert')).toHaveTextContent('The result of the action was not confirmed.'))
  expect(ports.at(-1)!.setReadOnly).not.toHaveBeenCalledWith(false,expect.anything());expect(client.save).not.toHaveBeenCalled()
 })

 it.each(['stream','retirement','write-revoked','reauthorization','invalid-lease'] as const)('M1: hides a failed engine on terminal %s while retaining custody for explicit purge',async failure=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   const monitor=deferred<void>();client.events.mockImplementation((_signal,callback)=>{stream=callback;return monitor.promise})
   mount();await ready();await acquire();const frame=document.querySelector('iframe')
   act(()=>portOptions.at(-1)!.onError?.('native_protocol'));await waitFor(()=>expect(frame).toHaveAttribute('inert'))
   if(failure==='stream')await act(async()=>monitor.reject(Error('terminal stream failure')))
   if(failure==='invalid-lease')await act(async()=>stream?.('lease',{mine:'invalid'}))
   if(failure==='retirement')await act(async()=>stream?.('session_retired',{delivery:'stopped',draft:'retain',reason:'authority_changed'}))
   if(failure==='write-revoked')await act(async()=>stream?.('session_retired',{delivery:'stopped',draft:'retain',reason:'write_revoked'}))
   if(failure==='reauthorization'){client.reauthorize.mockRejectedValue(Error('authority unconfirmed'));await act(async()=>vi.advanceTimersByTimeAsync(30001))}
   expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveStyle({visibility:'hidden'});expect(client.close).toHaveBeenCalled()
   expect(journal.revokeRead).not.toHaveBeenCalled();expect(journal.close).not.toHaveBeenCalled();expect(m.createClient).toHaveBeenCalledOnce();expect(screen.queryByText('Saved')).not.toBeInTheDocument()
   await act(async()=>privacyRetire!());expect(document.querySelector('iframe')).toBeNull();expect(journal.revokeRead).toHaveBeenCalled()
  }finally{vi.useRealTimers()}
 })
 it.each(['active','lease-retirement','auth-retirement'] as const)('M2: old read failure preserves new writer during %s and its pending capture',async phase=>{
  mount();await ready();const pending=deferred<unknown>(),before=client.read.mock.calls.length
  client.read.mockReturnValueOnce(pending.promise);act(()=>stream?.('lease',emptyLease));await waitFor(()=>expect(client.read).toHaveBeenCalledTimes(before+1))
  await acquire();await screen.findByText('Ready to edit')
  const frame=document.querySelector('iframe'),port=ports.at(-1)!,capture=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>()
  port.snapshot.mockReturnValue(capture.promise)
  let suspension:Promise<boolean>|undefined
  try{
   act(()=>portOptions.at(-1)!.onChanged('new-writer-buffer','doc'));await waitFor(()=>expect(port.snapshot).toHaveBeenCalled());expect(journal.persist).not.toHaveBeenCalled()
   if(phase==='lease-retirement')act(()=>stream?.('lease',{...emptyLease,holder:otherPerson,generation:'9007199254740996'}))
   if(phase==='auth-retirement')act(()=>{suspension=authSuspend!().then(()=>true,()=>false)})
   if(phase!=='active')await waitFor(()=>expect(frame).toHaveAttribute('inert'))
   await act(async()=>pending.reject(Error('old read failed')))
   expect(document.querySelector('iframe')).toBe(frame);expect(journal.revokeRead).not.toHaveBeenCalled();expect(journal.persist).not.toHaveBeenCalled();expect(screen.queryByText('Saved')).not.toBeInTheDocument()
  }finally{await act(async()=>capture.resolve({bytes:new TextEncoder().encode('{"body":["QA pending writer"]}'),editorRevision:'captured',documentId:'doc'}))}
  await waitFor(()=>expect(journal.persist).toHaveBeenCalled())
  if(suspension){await expect(suspension).resolves.toBe(true);expect(document.querySelector('iframe')).toBeNull()}
  else expect(document.querySelector('iframe')).toBe(frame)
 })
 it('M2: old read failure does not cancel an acquisition still awaiting its lease action',async()=>{
  mount();await ready();const frame=document.querySelector('iframe'),pending=deferred<unknown>(),action=deferred<typeof ownedLease>(),reads=client.read.mock.calls.length
  client.read.mockReturnValueOnce(pending.promise);act(()=>stream?.('lease',emptyLease));await waitFor(()=>expect(client.read).toHaveBeenCalledTimes(reads+1))
  client.leaseAction.mockReturnValueOnce(action.promise);fireEvent.click(screen.getByRole('button',{name:'Take over editing'}));await waitFor(()=>expect(client.leaseAction).toHaveBeenCalled())
  try{await act(async()=>pending.reject(Error('old read failed')));expect(document.querySelector('iframe')).toBe(frame)}
  finally{client.lease.mockResolvedValue(ownedLease);await act(async()=>action.resolve(ownedLease))}
  await screen.findByText('Ready to edit');expect(journal.revokeRead).not.toHaveBeenCalled()
 })
 it('keeps a starting editor rendering but unseen, and shows it once admitted',async()=>{
  const inserted:{visibility:string,opacity:string,inert:boolean}[]=[]
  const watch=new MutationObserver(()=>{const f=document.querySelector('iframe');if(f&&!inserted.length)inserted.push({visibility:f.style.visibility,opacity:f.style.opacity,inert:f.hasAttribute('inert')})})
  watch.observe(document.body,{childList:true,subtree:true})
  mount();await ready();await acquire();await screen.findByText('Ready to edit');watch.disconnect()
  expect(inserted).toEqual([{visibility:'',opacity:'0',inert:true}])
  const frame=document.querySelector('iframe')!
  expect(frame).not.toHaveAttribute('inert');expect(frame.style.opacity).toBe('');expect(frame.style.visibility).toBe('')
 })
 it.each([['captured','stream'],['failed-capture','stream'],['captured','invalid-lease'],['failed-capture','invalid-lease']] as const)('monitor terminal failure hides immediately and preserves %s custody while capture settles (%s)',async(outcome,failure)=>{
  const monitor=deferred<void>();client.events.mockImplementation((_signal,callback)=>{stream=callback;return monitor.promise})
  mount();await ready();await acquire();await screen.findByText('Ready to edit')
  const frame=document.querySelector('iframe'),capture=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>(),port=ports.at(-1)!
  port.snapshot.mockReturnValue(capture.promise)
  if(outcome==='failed-capture')journal.persist.mockRejectedValue(Error('storage unavailable'))
  act(()=>portOptions.at(-1)!.onChanged('buffer-before-terminal-fault','doc'));await waitFor(()=>expect(port.snapshot).toHaveBeenCalled())
  try{
   if(failure==='invalid-lease')await act(async()=>stream?.('lease',{mine:'invalid'}))
   else await act(async()=>monitor.reject(Error('terminal monitor fault')))
   expect(document.querySelector('iframe')).toBe(frame);expect(unseen(frame)).toBe(true);expect(frame).toHaveAttribute('inert')
   expect(journal.persist).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled();expect(screen.queryByText('Saved')).not.toBeInTheDocument()
  }finally{await act(async()=>capture.resolve({bytes:new TextEncoder().encode('{"body":["retained QA buffer"]}'),editorRevision:'terminal-capture',documentId:'doc'}))}
  await waitFor(()=>expect(journal.persist).toHaveBeenCalled())
  await waitFor(()=>expect(client.close).toHaveBeenCalled());expect(journal.revokeRead).not.toHaveBeenCalled();expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
  if(outcome==='captured')await waitFor(()=>expect(document.querySelector('iframe')).toBeNull())
  else {expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveStyle({visibility:'hidden'});expect(journal.close).not.toHaveBeenCalled()}
 })
 it.each(['stream','reauthorization'] as const)('delivers no engine export while terminal %s retirement awaits capture',async failure=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  const monitor=deferred<void>();client.events.mockImplementation((_signal,callback)=>{stream=callback;return monitor.promise})
  const capture=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>()
  const download=vi.spyOn(URL,'createObjectURL')
  try{
   mount();await ready();await acquire();await screen.findByText('Ready to edit')
   const frame=document.querySelector('iframe'),port=ports.at(-1)!,options=portOptions.at(-1)!
   port.snapshot.mockReturnValue(capture.promise)
   act(()=>options.onChanged('buffer-before-export','doc'));await waitFor(()=>expect(port.snapshot).toHaveBeenCalled())
   if(failure==='stream')await act(async()=>monitor.reject(Error('terminal monitor fault')))
   else{client.reauthorize.mockRejectedValue(Error('authority unconfirmed'));await act(async()=>vi.advanceTimersByTimeAsync(30001))}
   expect(unseen(frame)).toBe(true)
   act(()=>options.onExport?.({name:'late.docx',bytes:new Uint8Array([80,75,3,4])}))
   expect(download).not.toHaveBeenCalled()
  }finally{download.mockRestore();await act(async()=>capture.resolve({bytes:new TextEncoder().encode('{"body":["retained"]}'),editorRevision:'terminal-capture',documentId:'doc'}));vi.useRealTimers()}
 })
 it('monitor retirement hides confirmed recovery copies without purging them',async()=>{
  mount();await ready();await acquire();const second=clientRig();m.createClient.mockReturnValueOnce(second)
  act(()=>{portOptions.at(-1)!.onChanged('before-read-reopen','doc');stream?.('session_retired',{delivery:'stopped',draft:'retain',reason:'write_revoked'})})
  await screen.findByRole('button',{name:'Keep a copy'})
  await act(async()=>stream?.('session_retired',{delivery:'stopped',draft:'retain',reason:'authority_changed'}))
  expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument();expect(journal.revokeRead).not.toHaveBeenCalled();expect(second.close).toHaveBeenCalled()
 })
 it('authority control confirmed read purge removes even a failed engine and revokes its journal',async()=>{
  mount();await ready();await acquire();const frame=document.querySelector('iframe')
  act(()=>portOptions.at(-1)!.onError?.('native_protocol'))
  await waitFor(()=>expect(frame).toHaveAttribute('inert'))
  await act(async()=>stream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'authority_changed'}))
  expect(document.querySelector('iframe')).toBeNull();expect(journal.revokeRead).toHaveBeenCalled();expect(client.close).toHaveBeenCalled()
 })
 it('authority control ignores an old failed read after a fresh admission replaces its client',async()=>{
  mount();await ready();const oldFrame=document.querySelector('iframe'),pending=deferred<unknown>(),reads=client.read.mock.calls.length
  client.read.mockReturnValueOnce(pending.promise)
  act(()=>stream?.('lease',emptyLease));await waitFor(()=>expect(client.read).toHaveBeenCalledTimes(reads+1))
  const second=clientRig();second.open.mockResolvedValue({sessionId:'successor-read',applicationId:'wordcraft'});m.createClient.mockReturnValueOnce(second)
  act(()=>stream?.('session_retired',{delivery:'stopped',draft:'retain',reason:'write_revoked'}))
  await waitFor(()=>expect(second.events).toHaveBeenCalledOnce());await waitFor(()=>expect(ports).toHaveLength(2))
  const next=document.querySelector('iframe');expect(next).not.toBe(oldFrame);await waitFor(()=>expect(next).not.toHaveStyle({visibility:'hidden'}))
  await act(async()=>pending.reject(Error('old admission read failure')))
  expect(document.querySelector('iframe')).toBe(next);expect(second.close).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
 })


 it('does not overwrite a newer streamed lease with a delayed bootstrap proof',async()=>{
  const proof=deferred<typeof ownedLease>()
  client.lease.mockResolvedValueOnce(emptyLease).mockReturnValueOnce(proof.promise)
  mount();await waitFor(()=>expect(client.lease).toHaveBeenCalledTimes(2))
  const revoked={...emptyLease,can_edit:false}
  act(()=>stream?.('lease',revoked))
  await act(async()=>proof.resolve(ownedLease))
  await waitFor(()=>expect(document.querySelector('iframe')).not.toHaveStyle({visibility:'hidden'}))
  expect(screen.queryByRole('button',{name:'Take over editing'})).not.toBeInTheDocument()
  expect(ports[0].setReadOnly).not.toHaveBeenCalledWith(false,expect.anything())
 })
 it('never unlocks an acquire after a newer streamed custody loss overtakes final proof',async()=>{
  mount();await ready()
  const proof=deferred<typeof ownedLease>(),checks=client.lease.mock.calls.length
  client.leaseAction.mockImplementation(async()=>{client.lease.mockResolvedValueOnce(ownedLease).mockResolvedValueOnce(ownedLease).mockReturnValueOnce(proof.promise);return ownedLease})
  fireEvent.click(screen.getByRole('button',{name:'Take over editing'}))
  await waitFor(()=>expect(client.lease).toHaveBeenCalledTimes(checks+3))
  act(()=>stream?.('lease',{...emptyLease,can_edit:false}))
  await act(async()=>proof.resolve(ownedLease))
  await screen.findByText('The result of the action was not confirmed. The draft is kept.')
  expect(ports.at(-1)!.setReadOnly).not.toHaveBeenCalledWith(false,expect.anything());expect(client.save).not.toHaveBeenCalled()
 })
 it('bounds a retained-buffer read admission and ignores its late proof without dropping the buffer',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   mount();await ready();await acquire();const frame=document.querySelector('iframe'),port=ports.at(-1)!
   journal.persist.mockRejectedValue(Error('storage unavailable'))
   const second=clientRig(),proof=deferred<typeof emptyLease>();second.lease.mockReturnValue(proof.promise);m.createClient.mockReturnValueOnce(second)
   act(()=>{portOptions.at(-1)!.onChanged('uncaptured','doc');stream?.('session_retired',{draft:'retain',reason:'write_revoked'})})
   await waitFor(()=>expect(second.lease).toHaveBeenCalled())
   await act(async()=>vi.advanceTimersByTimeAsync(60001))
   expect(frame).toHaveStyle({visibility:'hidden'});expect(frame).toHaveAttribute('inert');expect(second.close).toHaveBeenCalled()
   expect(screen.getByRole('button',{name:'Check access again'})).toBeEnabled()
   await act(async()=>proof.resolve(emptyLease))
   expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveStyle({visibility:'hidden'});expect(port.close).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
  }finally{vi.useRealTimers()}
 })
 it('hides the retained buffer on monitor failure and drops a pending copy after read revocation',async()=>{
  mount();await ready();await acquire();const frame=document.querySelector('iframe'),port=ports.at(-1)!
  journal.persist.mockRejectedValue(Error('storage unavailable'))
  const second=clientRig(),monitor=deferred<void>();second.events.mockImplementation((_signal,callback)=>{stream=callback;return monitor.promise});m.createClient.mockReturnValueOnce(second)
  act(()=>{portOptions.at(-1)!.onChanged('uncaptured','doc');stream?.('session_retired',{draft:'retain',reason:'write_revoked'})})
  await screen.findByRole('button',{name:'Download the local copy'})
  await act(async()=>monitor.reject(Error('stream ended')))
  expect(frame).toHaveStyle({visibility:'hidden'});expect(screen.queryByRole('button',{name:'Download the local copy'})).not.toBeInTheDocument()
  const third=clientRig();m.createClient.mockReturnValueOnce(third);fireEvent.click(screen.getByRole('button',{name:'Check access again'}))
  await screen.findByRole('button',{name:'Download the local copy'})
  const snapshot=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>();port.snapshot.mockReturnValueOnce(snapshot.promise)
  const copy=vi.fn();vi.stubGlobal('URL',Object.assign(class extends URL {},{createObjectURL:copy,revokeObjectURL:vi.fn()}))
  fireEvent.click(screen.getByRole('button',{name:'Download the local copy'}))
  act(()=>stream?.('session_retired',{draft:'purge',reason:'authority_changed'}))
  await act(async()=>snapshot.resolve({bytes:new Uint8Array([1]),editorRevision:'late',documentId:'doc'}))
  expect(copy).not.toHaveBeenCalled();expect(document.querySelector('iframe')).toBeNull();expect(journal.revokeRead).toHaveBeenCalled()
 })

 describe('a return with a frozen frame and no recovery row',()=>{
  const refused=Error('native_private_read_refused'),download=()=>screen.queryByRole('button',{name:'Download the local copy'})
  const back=()=>window.dispatchEvent(new Event('pageshow'))
  async function frozen(){
   mount();await ready();await acquire();const frame=document.querySelector('iframe')!,port=ports.at(-1)!
   journal.persist.mockRejectedValue(Error('storage unavailable'))
   const second=clientRig();m.createClient.mockReturnValueOnce(second)
   act(()=>{portOptions.at(-1)!.onChanged('uncaptured','doc');stream?.('session_retired',{draft:'retain',reason:'write_revoked'})})
   await screen.findByRole('button',{name:'Download the local copy'})
   expect(frame).not.toHaveStyle({visibility:'hidden'});expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
   second.read.mockClear()
   return {frame,port,second}
  }
  it('hides the frame and removes the download in the same turn, and only the read brings them back',async()=>{
   const {frame,second}=await frozen(),answer=deferred<{version:string;bytes:Uint8Array}>();second.read.mockReturnValueOnce(answer.promise)
   act(back)
   expect(frame).toHaveStyle({visibility:'hidden'});expect(download()).not.toBeInTheDocument();expect(second.read).toHaveBeenCalledOnce()
   await act(async()=>{second.reauthorize.mockResolvedValue(true);await new Promise(resolve=>setTimeout(resolve,20))})
   expect(frame).toHaveStyle({visibility:'hidden'});expect(download()).not.toBeInTheDocument()
   await act(async()=>answer.resolve({version:'1',bytes:new Uint8Array()}))
   expect(frame).not.toHaveStyle({visibility:'hidden'});expect(download()).toBeInTheDocument()
  })
  it('a refused read keeps it hidden and the buffer intact',async()=>{
   const {frame,port,second}=await frozen();second.read.mockRejectedValue(refused)
   // The refusal re-admits; that admission is refused too, as the server would.
   const third=clientRig();third.lease.mockResolvedValue({...emptyLease,can_edit:false});third.read.mockRejectedValue(refused);m.createClient.mockReturnValueOnce(third)
   await act(async()=>{back();await new Promise(resolve=>setTimeout(resolve,20))})
   expect(frame).toHaveStyle({visibility:'hidden'});expect(download()).not.toBeInTheDocument()
   expect(document.querySelector('iframe')).toBe(frame);expect(port.close).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
  })
  it('an unclear answer keeps it hidden and the buffer intact',async()=>{
   const {frame,port,second}=await frozen();second.read.mockRejectedValue(Error('native_read_unavailable'))
   await act(async()=>{back();await new Promise(resolve=>setTimeout(resolve,20))})
   expect(frame).toHaveStyle({visibility:'hidden'});expect(download()).not.toBeInTheDocument();expect(port.close).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
  })
  it('a second return leaves the first read unable to restore anything',async()=>{
   const {frame,second}=await frozen(),first=deferred<{version:string;bytes:Uint8Array}>(),latest=deferred<{version:string;bytes:Uint8Array}>()
   second.read.mockReturnValueOnce(first.promise).mockReturnValueOnce(latest.promise)
   act(back);act(back)
   await act(async()=>first.resolve({version:'1',bytes:new Uint8Array()}))
   expect(frame).toHaveStyle({visibility:'hidden'});expect(download()).not.toBeInTheDocument()
   expect(second.read).toHaveBeenCalledTimes(2);expect(second.read.mock.calls[0][0].aborted).toBe(true)
   await act(async()=>latest.resolve({version:'1',bytes:new Uint8Array()}))
   expect(frame).not.toHaveStyle({visibility:'hidden'});expect(download()).toBeInTheDocument()
  })
  it('auth suspension leaves a pending read unable to restore anything',async()=>{
   const {frame,port,second}=await frozen(),answer=deferred<{version:string;bytes:Uint8Array}>();second.read.mockReturnValueOnce(answer.promise)
   port.snapshot.mockReturnValue(new Promise(()=>{}))
   act(back)
   act(()=>{void authSuspend!().catch(()=>{})})
   await act(async()=>answer.resolve({version:'1',bytes:new Uint8Array()}))
   expect(unseen(frame)).toBe(true);expect(download()).not.toBeInTheDocument()
   expect(second.read.mock.calls[0][0].aborted).toBe(true)
  })
  it('a download started before the return is dropped even after read comes back',async()=>{
   const {port,second}=await frozen(),snapshot=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>();port.snapshot.mockReturnValueOnce(snapshot.promise)
   const copy=vi.fn();vi.stubGlobal('URL',Object.assign(class extends URL {},{createObjectURL:copy,revokeObjectURL:vi.fn()}))
   fireEvent.click(download()!)
   second.read.mockResolvedValue({version:'1',bytes:new Uint8Array()})
   await act(async()=>{back();await new Promise(resolve=>setTimeout(resolve,20))})
   expect(download()).toBeInTheDocument()
   await act(async()=>snapshot.resolve({bytes:new Uint8Array([1]),editorRevision:'late',documentId:'doc'}))
   expect(copy).not.toHaveBeenCalled()
  })
 })

 it.each(['stream','retirement','reauthorization','auth-suspend'] as const)('retires a fresh read bootstrap on %s failure and ignores its late completion',async failure=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   mount();await ready();await acquire()
   const second=clientRig(),opening=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>(),transport=deferred<void>()
   second.events.mockImplementation((_signal,callback)=>{stream=callback;return transport.promise})
   m.createClient.mockReturnValueOnce(second)
   m.createPort.mockImplementationOnce((o:NativeEditorPortOptions)=>{const p=portRig();p.bootstrap.mockReturnValue(opening.promise);ports.push(p);portOptions.push(o);queueMicrotask(()=>o.onReady?.('qualified'));return p})
   act(()=>{portOptions.at(-1)!.onChanged('before-retirement','doc');stream?.('session_retired',{draft:'retain',reason:'write_revoked'})})
   await waitFor(()=>expect(second.events).toHaveBeenCalledOnce());await waitFor(()=>expect(ports.at(-1)!.bootstrap).toHaveBeenCalled())
   if(failure==='stream')await act(async()=>transport.reject(Error('fresh stream refused')))
   if(failure==='retirement')act(()=>stream?.('session_retired',{draft:'retain',reason:'authority_changed'}))
   if(failure==='reauthorization'){second.reauthorize.mockRejectedValue(Error('fresh reauthorization refused'));await act(async()=>vi.advanceTimersByTimeAsync(30001))}
   if(failure==='auth-suspend')await act(async()=>authSuspend!())
   await waitFor(()=>expect(document.querySelector('iframe')).toBeNull())
   expect(second.close).toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled();expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
   await act(async()=>opening.resolve({bytes:new Uint8Array([1]),editorRevision:'late',documentId:'doc'}))
   expect(document.querySelector('iframe')).toBeNull();expect(client.save).not.toHaveBeenCalled();expect(second.leaseAction).not.toHaveBeenCalled()
  }finally{vi.useRealTimers()}
 })
 it('does not continue a pending fresh admission after auth suspension',async()=>{
  mount();await ready();await acquire()
  const second=clientRig(),admission=deferred<{sessionId:string;applicationId:string}>()
  second.open.mockReturnValue(admission.promise);m.createClient.mockReturnValueOnce(second)
  act(()=>stream?.('session_retired',{draft:'retain',reason:'write_revoked'}))
  await waitFor(()=>expect(second.open).toHaveBeenCalledOnce())
  await act(async()=>authSuspend!());expect(document.querySelector('iframe')).toBeNull()
  await act(async()=>admission.resolve({sessionId:'fresh',applicationId:'wordcraft'}))
  expect(second.read).not.toHaveBeenCalled();expect(second.events).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
 })
 it('waits for in-flight retirement on auth suspension and never starts its fresh admission',async()=>{
  mount();await ready();await acquire()
  const snapshot=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>()
  ports.at(-1)!.snapshot.mockReturnValue(snapshot.promise)
  act(()=>{portOptions.at(-1)!.onChanged('capture-pending','doc');stream?.('session_retired',{draft:'retain',reason:'write_revoked'})})
  let suspended!:Promise<void>;act(()=>{suspended=authSuspend!()})
  await act(async()=>{snapshot.resolve({bytes:new TextEncoder().encode('{"body":["captured-QA"]}'),editorRevision:'pending-capture',documentId:'doc'});await suspended})
  expect(m.createClient).toHaveBeenCalledOnce();expect(document.querySelector('iframe')).toBeNull();expect(journal.persist).toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
 })
 it('rejects auth suspension when an in-flight retirement cannot capture and never reveals or reopens',async()=>{
  mount();await ready();await acquire();const frame=document.querySelector('iframe'),port=ports.at(-1)!,snapshot=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>()
  port.snapshot.mockReturnValue(snapshot.promise);journal.persist.mockRejectedValue(Error('storage unavailable'))
  act(()=>{portOptions.at(-1)!.onChanged('pending','doc');stream?.('session_retired',{draft:'retain',reason:'write_revoked'})})
  let suspended!:Promise<unknown>;act(()=>{suspended=authSuspend!().catch(cause=>cause)})
  expect(unseen(frame)).toBe(true)
  await act(async()=>{snapshot.resolve({bytes:new TextEncoder().encode('{"body":["pending"]}'),editorRevision:'pending',documentId:'doc'});expect(await suspended).toEqual(Error('native_auth_draft_capture_unconfirmed'))})
  expect(m.createClient).toHaveBeenCalledOnce();expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveAttribute('inert');expect(frame).toHaveStyle({visibility:'hidden'});expect(journal.revokeRead).not.toHaveBeenCalled();expect(port.close).not.toHaveBeenCalled();expect(screen.queryByText('Saved')).not.toBeInTheDocument()
 })
 it('catches up one metadata change observed during initial bootstrap',async()=>{
  const opening=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>()
  m.createPort.mockImplementationOnce((o:NativeEditorPortOptions)=>{const p=portRig();p.bootstrap.mockReturnValue(opening.promise);ports.push(p);portOptions.push(o);queueMicrotask(()=>o.onReady?.('qualified'));return p})
  mount();await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled())
  const newer={...await client.read(),version:'2'};client.read.mockResolvedValue(newer);client.read.mockClear()
  act(()=>{stream?.('lease',emptyLease);stream?.('lease',emptyLease)})
  expect(client.read).not.toHaveBeenCalled()
  await act(async()=>opening.resolve({bytes:new Uint8Array([1]),editorRevision:'opened',documentId:'doc'}))
  await waitFor(()=>expect(ports).toHaveLength(2));expect(client.read).toHaveBeenCalledOnce()
  expect(ports[1].bootstrap).toHaveBeenCalledWith(newer.bytes,expect.any(AbortSignal),nativeOpenBudget(newer.bytes.byteLength).stepMs)
 })
 it('retains an uncaptured buffer without importing over it and monitors a fresh read admission',async()=>{
  mount();await ready();await acquire();const frame=document.querySelector('iframe'),port=ports.at(-1)!,count=ports.length
  journal.persist.mockRejectedValue(Error('local storage unavailable'))
  const second=clientRig();second.lease.mockResolvedValue({...emptyLease,can_edit:false});m.createClient.mockReturnValueOnce(second)
  act(()=>{portOptions.at(-1)!.onChanged('uncaptured','doc');stream?.('session_retired',{draft:'retain',reason:'write_revoked'})})
  await waitFor(()=>expect(second.events).toHaveBeenCalledOnce());await waitFor(()=>expect(screen.getByRole('button',{name:'Download the local copy'})).toBeEnabled())
  expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveAttribute('inert');expect(frame).not.toHaveStyle({visibility:'hidden'});expect(ports).toHaveLength(count)
  expect(second.read).toHaveBeenCalledOnce();expect(second.leaseAction).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled();expect(port.close).not.toHaveBeenCalled()
  act(()=>stream?.('session_retired',{draft:'purge',reason:'authority_changed'}))
  await waitFor(()=>expect(document.querySelector('iframe')).toBeNull());expect(journal.revokeRead).toHaveBeenCalled()
 })
 it('keeps an uncaptured buffer hidden on refused read and permits one bounded explicit recheck',async()=>{
  mount();await ready();await acquire();const frame=document.querySelector('iframe'),count=ports.length
  journal.persist.mockRejectedValue(Error('storage unavailable'))
  const second=clientRig();second.open.mockRejectedValue(Error('read unconfirmed'));m.createClient.mockReturnValueOnce(second)
  act(()=>{portOptions.at(-1)!.onChanged('uncaptured','doc');stream?.('session_retired',{draft:'retain',reason:'write_revoked'})})
  const retry=await screen.findByRole('button',{name:'Check access again'})
  expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveStyle({visibility:'hidden'});expect(journal.revokeRead).not.toHaveBeenCalled()
  const third=clientRig();third.lease.mockResolvedValue({...emptyLease,can_edit:false});m.createClient.mockReturnValueOnce(third)
  fireEvent.click(retry);await screen.findByRole('button',{name:'Download the local copy'})
  expect(document.querySelector('iframe')).toBe(frame);expect(ports).toHaveLength(count);expect(third.read).toHaveBeenCalledOnce();expect(third.leaseAction).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled()
 })

 it('hides a retired writer and its captured copy while the fresh read admission is pending or refused',async()=>{
  mount();await ready();await acquire()
  const frame=document.querySelector('iframe'),oldOptions=portOptions.at(-1)!,second=clientRig(),admission=deferred<{sessionId:string;applicationId:string}>()
  second.open.mockReturnValue(admission.promise);m.createClient.mockReturnValueOnce(second)
  act(()=>{portOptions.at(-1)!.onChanged('pending-before-retirement','doc');stream?.('session_retired',{draft:'retain',reason:'write_revoked'})})
  await waitFor(()=>expect(second.open).toHaveBeenCalledOnce())
  expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveStyle({visibility:'hidden'});expect(frame).toHaveAttribute('inert')
  expect(journal.persist).toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled();expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
  const download=vi.spyOn(URL,'createObjectURL')
  act(()=>oldOptions.onExport?.({name:'old.json',bytes:new Uint8Array([1])}))
  expect(download).not.toHaveBeenCalled();download.mockRestore()
  await act(async()=>{admission.resolve({sessionId:'fresh-session',applicationId:'unsupported'})})
  await waitFor(()=>expect(document.querySelector('iframe')).toBeNull())
  expect(journal.revokeRead).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled()
  expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
 })

 it('purges the captured writer row when read is revoked during the fresh read-only bootstrap',async()=>{
  mount();await ready();await acquire()
  const second=clientRig(),opening=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>()
  m.createClient.mockReturnValueOnce(second)
  m.createPort.mockImplementationOnce((o:NativeEditorPortOptions)=>{const p=portRig();p.bootstrap.mockReturnValue(opening.promise);ports.push(p);portOptions.push(o);queueMicrotask(()=>o.onReady?.('qualified'));return p})
  act(()=>{portOptions.at(-1)!.onChanged('before-write-revoke','doc');stream?.('session_retired',{draft:'retain',reason:'write_revoked'})})
  await waitFor(()=>expect(ports.at(-1)!.bootstrap).toHaveBeenCalled())
  await waitFor(()=>expect(second.events).toHaveBeenCalledOnce())
  const fresh=ports.at(-1)!
  act(()=>stream?.('session_retired',{draft:'purge',reason:'authority_changed'}))
  await waitFor(()=>expect(document.querySelector('iframe')).toBeNull())
  expect(journal.revokeRead).toHaveBeenCalled();expect(second.close).toHaveBeenCalled()
  await act(async()=>opening.resolve({bytes:new TextEncoder().encode('{"body":[1]}'),editorRevision:'late',documentId:'doc'}))
  expect(document.querySelector('iframe')).toBeNull();expect(fresh.setReadOnly).not.toHaveBeenCalledWith(false,expect.anything());expect(client.save).not.toHaveBeenCalled()
 })
 it('makes no captured-draft claim when authentication ends during the initial import',async()=>{
  const opening=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>()
  m.createPort.mockImplementationOnce((o:NativeEditorPortOptions)=>{const p=portRig();p.bootstrap.mockReturnValue(opening.promise);ports.push(p);portOptions.push(o);queueMicrotask(()=>o.onReady?.('qualified'));return p})
  mount();await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled())
  await act(async()=>authSuspend!())
  expect(document.querySelector('iframe')).toBeNull();expect(screen.queryByText(/draft kept/i)).not.toBeInTheDocument();expect(journal.persist).not.toHaveBeenCalled()
  await act(async()=>opening.resolve({bytes:new Uint8Array([1]),editorRevision:'late',documentId:'doc'}))
  expect(document.querySelector('iframe')).toBeNull();expect(client.save).not.toHaveBeenCalled()
 })
 it.each(['human','page','workspace'] as const)('never carries recovered draft UI across a changed %s scope',async changed=>{
  journal.recovery.mockResolvedValueOnce([{draft:{applicationId:'wordcraft',enginePin:'a'.repeat(64),sessionId:'previous',generation:'1',baseVersion:'1',editorRevision:'previous',bytes:new TextEncoder().encode('{"private":"old-person-fixture"}')},actions:['compare','copy']}])
  const ui=mount();await ready();await acquire();await screen.findByRole('button',{name:'Keep a copy'})
  ui.rerender(<NativeCraftEditorHost workspaceId={changed==='workspace'?'w2':'w'} pageId={changed==='page'?'p2':'p'} humanId={changed==='human'?'human2':'human'} enabled />)
  expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument();expect(screen.queryByDisplayValue(/old-person-fixture/)).not.toBeInTheDocument()
  await waitFor(()=>expect(ports.at(-1)!.bootstrap).toHaveBeenCalled())
  expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
 })

 // A failed capture for retirement is answered at once by a local capture of the frozen frame; the
 // session ends only once that final local write is confirmed, and nothing is sent to the server.
 it('after a failed auth capture keeps the live frame locally without a save, and closes only after that write',async()=>{
  mount();await ready();await acquire();const frame=document.querySelector('iframe')
  journal.persist.mockRejectedValueOnce(Error('storage failed'))
  let pending!:Promise<void>
  act(()=>{portOptions.at(-1)!.onChanged('pending-at-expiry','doc');pending=authSuspend!()})
  await act(async()=>{await expect(pending).resolves.toBeUndefined()})
  expect(journal.persist).toHaveBeenCalledTimes(2)
  expect(journal.persist.mock.calls[1][1],'the second write is the final local draft').toBe(true)
  expect(journal.close).toHaveBeenCalled()
  expect(journal.close.mock.invocationCallOrder[0],'the journal closes only after the confirmed write').toBeGreaterThan(journal.persist.mock.invocationCallOrder[1])
  expect(client.save).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
  void frame
 })
 it('offers publishing to the page owner only, even to an editor holding custody',async()=>{
  client.lease.mockResolvedValue({...emptyLease,can_share:false})
  const editorLease={...ownedLease,can_share:false}
  client.leaseAction.mockImplementation(async()=>{client.lease.mockResolvedValue(editorLease);return editorLease})
  mount();await ready();await acquire()
  expect(screen.getByRole('button',{name:'Hand back'})).toBeInTheDocument()
  expect(screen.queryByRole('button',{name:'Publish preview'})).not.toBeInTheDocument()
  expect(client.share).not.toHaveBeenCalled()
 })
 it('publishes only the final saved and acknowledged version under current custody',async()=>{
  mount();await ready();expect(screen.queryByRole('button',{name:'Publish preview'})).not.toBeInTheDocument();await acquire()
  const ack=deferred<boolean>();ports.at(-1)!.ack.mockReturnValueOnce(ack.promise)
  act(()=>portOptions.at(-1)!.onChanged('last-before-preview','doc'))
  fireEvent.click(screen.getByRole('button',{name:'Publish preview'}))
  await waitFor(()=>expect(ports.at(-1)!.ack).toHaveBeenCalledOnce());expect(client.share).not.toHaveBeenCalled()
  await act(async()=>ack.resolve(true))
  await waitFor(()=>expect(client.share).toHaveBeenCalledOnce())
  expect(client.share).toHaveBeenCalledWith({workspaceId:'w',pageId:'p',applicationId:'wordcraft',enginePin:'a'.repeat(64),sessionId:'private-session',generation:'9007199254740995',baseVersion:'2',targetVersion:'2'},expect.any(AbortSignal))
  await screen.findByText('Shared preview · version 2')
  expect(client.save.mock.invocationCallOrder[0]).toBeLessThan(client.share.mock.invocationCallOrder[0])
 })
 it('does not publish an unconfirmed final save and keeps the native draft',async()=>{
  mount();await ready();await acquire();client.save.mockRejectedValueOnce(Error('lost ack'))
  act(()=>portOptions.at(-1)!.onChanged('final-edit','doc'));fireEvent.click(screen.getByRole('button',{name:'Publish preview'}))
  await screen.findByRole('alert');expect(client.share).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled();expect(document.querySelector('iframe')).not.toBeNull()
 })
 it('never retries an ambiguous preview publication or changes native data',async()=>{
  mount();await ready();await acquire();client.share.mockRejectedValueOnce(Error('lost publication reply'))
  act(()=>portOptions.at(-1)!.onChanged('final-edit','doc'));fireEvent.click(screen.getByRole('button',{name:'Publish preview'}));await screen.findByRole('alert')
  expect(client.share).toHaveBeenCalledOnce();expect(client.save).toHaveBeenCalledOnce();expect(journal.revokeRead).not.toHaveBeenCalled();expect(document.querySelector('iframe')).not.toBeNull()
 })

 it.each([false,undefined])('shows a read-only preview with no custody controls unless server confirms edit capability (%s)',async(canEdit)=>{
  client.lease.mockResolvedValue({...emptyLease,can_edit:canEdit})
  mount();await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalledOnce())
  expect(screen.queryByRole('button',{name:'Take over editing'})).not.toBeInTheDocument()
  expect(screen.queryByRole('button',{name:'Request edit'})).not.toBeInTheDocument()
  expect(client.leaseAction).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled();expect(client.share).not.toHaveBeenCalled();expect(screen.queryByRole('button',{name:'Publish preview'})).not.toBeInTheDocument();expect(m.openJournal).not.toHaveBeenCalled()
  act(()=>stream?.('lease',{...ownedLease,holder:otherPerson,mine:false,can_write:false,can_edit:canEdit}))
  await screen.findByText('Pavel is editing')
  expect(screen.queryByRole('button',{name:'Request edit'})).not.toBeInTheDocument()
  expect(ports.some(p=>p.setReadOnly.mock.calls.some(c=>c[0]===false))).toBe(false)
 })

 it('opens Grid with the same read-only lease gate, isolated port and matching draft namespace',async()=>{
  client.open.mockResolvedValueOnce({sessionId:'private-session',applicationId:'gridcraft'})
  const base=await client.read(new AbortController().signal)
  client.read.mockResolvedValue({...base,applicationId:'gridcraft',bytes:new TextEncoder().encode('{"sheets":[]}')})
  mount();await ready()
  expect(portOptions[0].applicationId).toBe('gridcraft')
  expect(portOptions[0].requireAccessProtocol).toBe(true)
  expect(document.querySelector('iframe')?.title).toBe('GridCraft editor')
  expect(client.leaseAction).not.toHaveBeenCalled()
  expect(m.openJournal).not.toHaveBeenCalled()
  await acquire()
  expect(m.openJournal).toHaveBeenCalledWith({human:'human',workspace:'w',page:'p',applicationId:'gridcraft'},{signal:expect.any(AbortSignal)})
  ports.at(-1)!.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"sheets":[1]}'),editorRevision:'grid:2',documentId:'doc'})
  act(()=>portOptions.at(-1)!.onSaveRequest({documentId:'doc',editorRevision:'grid:2'}))
  await waitFor(()=>expect(client.save).toHaveBeenCalledOnce())
  expect(client.save.mock.calls[0][0].applicationId).toBe('gridcraft')
  await screen.findByText('Saved')
 })

 it('#4852: a Sheet keeps saving autosave and Ctrl+S after one save could not retire its local draft row',async()=>{
  client.open.mockResolvedValueOnce({sessionId:'private-session',applicationId:'gridcraft'})
  const base=await client.read(new AbortController().signal)
  client.read.mockResolvedValue({...base,applicationId:'gridcraft',bytes:new TextEncoder().encode('{"sheets":[]}')})
  mount();await ready();await acquire()
  const p=ports.at(-1)!,o=portOptions.at(-1)!
  const sheet=(n:number)=>({bytes:new TextEncoder().encode(`{"sheets":[${n}]}`),editorRevision:`grid:${n}`,documentId:'doc'})
  // The server commits the save; only the IDB row retirement misses its deadline.
  journal.confirm.mockRejectedValueOnce(Error('storage deadline'))
  p.snapshot.mockResolvedValue(sheet(2));act(()=>o.onChanged('grid:2','doc'))
  await waitFor(()=>expect(client.save).toHaveBeenCalledTimes(1))
  await waitFor(()=>expect(journal.confirm).toHaveBeenCalledTimes(1))
  // Second sheet / next row: the autosave must reach the server again.
  p.snapshot.mockResolvedValue(sheet(3));act(()=>o.onChanged('grid:3','doc'))
  await waitFor(()=>expect(client.save).toHaveBeenCalledTimes(2))
  expect(client.save.mock.calls[1][0].baseVersion).toBe('2')
  await screen.findByText('Saved')
  // Sheet Ctrl+S, as the real GridCraft port reports it: no manual ticket id.
  p.snapshot.mockResolvedValue(sheet(4));act(()=>o.onSaveRequest({documentId:'doc',editorRevision:'doc:4'}))
  await waitFor(()=>expect(client.save).toHaveBeenCalledTimes(3))
  expect(p.saveFailed).not.toHaveBeenCalled()
  await waitFor(()=>expect(screen.getByRole('status')).toHaveAttribute('data-save-phase','saved'))
 })

 it('#4870: a committed save whose local copy could not be removed says it is on the server, not unsaved, and claims no version',async()=>{
  client.open.mockResolvedValueOnce({sessionId:'private-session',applicationId:'gridcraft'})
  const base=await client.read(new AbortController().signal)
  client.read.mockResolvedValue({...base,applicationId:'gridcraft',bytes:new TextEncoder().encode('{"sheets":[]}')})
  mount();await ready();await acquire()
  const p=ports.at(-1)!,o=portOptions.at(-1)!
  // The server commits; the row retirement and its one retry both fail.
  journal.confirm.mockRejectedValueOnce(Error('storage deadline')).mockRejectedValueOnce(Error('storage deadline'))
  p.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"sheets":[2]}'),editorRevision:'grid:2',documentId:'doc'});act(()=>o.onChanged('grid:2','doc'))
  await waitFor(()=>expect(journal.confirm).toHaveBeenCalledTimes(2))
  const status=screen.getByRole('status')
  await waitFor(()=>expect(status).toHaveAttribute('data-save-reason','unretired'))
  expect(status).toHaveTextContent('Saved on the server · the local copy stays in this browser for now')
  expect(status).not.toHaveTextContent('Unsaved draft')
  // No native ACK yet, so no "Saved · version" claim, and nothing sent twice.
  expect(status).not.toHaveTextContent('version')
  expect(p.ack).not.toHaveBeenCalled()
  expect(client.save).toHaveBeenCalledOnce()
  // A further edit is genuinely unsaved again.
  p.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"sheets":[3]}'),editorRevision:'grid:3',documentId:'doc'});act(()=>o.onChanged('grid:3','doc'))
  expect(status).toHaveTextContent('Unsaved draft')
 })

 it('#4852: after storage recovers with no further edit, the original receipt is acknowledged and publish waits for that ACK',async()=>{
  client.open.mockResolvedValueOnce({sessionId:'private-session',applicationId:'gridcraft'})
  const base=await client.read(new AbortController().signal)
  client.read.mockResolvedValue({...base,applicationId:'gridcraft',bytes:new TextEncoder().encode('{"sheets":[]}')})
  mount();await ready();await acquire()
  const p=ports.at(-1)!,o=portOptions.at(-1)!,ack=deferred<boolean>();p.ack.mockReturnValueOnce(ack.promise)
  journal.confirm.mockRejectedValueOnce(Error('storage deadline'))
  p.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"sheets":[2]}'),editorRevision:'grid:2',documentId:'doc'});act(()=>o.onChanged('grid:2','doc'))
  // The bounded retry retires the row; the original receipt goes to the editor.
  await waitFor(()=>expect(p.ack).toHaveBeenCalledOnce())
  expect(client.save).toHaveBeenCalledOnce()
  expect(p.ack).toHaveBeenCalledWith(expect.objectContaining({version:'2',contentHash:client.save.mock.calls[0][0].contentHash}))
  expect(screen.queryByText('Saved')).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button',{name:'Publish preview'}))
  await new Promise(resolve=>setTimeout(resolve,50))
  expect(client.share).not.toHaveBeenCalled()
  await act(async()=>ack.resolve(true))
  await waitFor(()=>expect(client.share).toHaveBeenCalledOnce())
  expect(client.share).toHaveBeenCalledWith(expect.objectContaining({baseVersion:'2',targetVersion:'2'}),expect.any(AbortSignal))
  expect(client.save).toHaveBeenCalledOnce()
  await waitFor(()=>expect(screen.getByRole('status')).toHaveTextContent('Saved · version 2'))
 })

 it('stops only the current agent mandate with one H1 request and reloads its fresh version without acquiring',async()=>{
  const agent={...ownedLease,mine:false,can_write:false,can_stop_agent:true,holder:{actor_id:'agent:actual',display_name:'Jamie',kind:'agent'}}
  client.lease.mockResolvedValue(agent)
  const stopped=deferred<Response>();m.apiFetch.mockReturnValueOnce(stopped.promise)
  mount();await screen.findByRole('button',{name:'Stop agent'});await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled())
  fireEvent.click(screen.getByRole('button',{name:'Stop agent'}))
  expect(screen.getByRole('button',{name:'Stop agent'})).toBeDisabled()
  expect(m.apiFetch).toHaveBeenCalledWith('/api/v1/pages/%40p/craft/tasks/cancel?workspace_id=w',expect.objectContaining({method:'POST',body:'{}',skipRefresh:true,redirect:'error',signal:expect.any(AbortSignal)}))
  const fresh={...await client.read(),version:'2',bytes:new TextEncoder().encode('{"body":["agent"]}')}
  client.read.mockResolvedValue(fresh);client.lease.mockResolvedValue(emptyLease)
  await act(async()=>stopped.resolve(new Response(JSON.stringify({stop_pending:true}),{status:202})))
  await waitFor(()=>expect(ports).toHaveLength(2))
  expect(ports[1].bootstrap).toHaveBeenCalledWith(fresh.bytes,expect.any(AbortSignal),nativeOpenBudget(fresh.bytes.byteLength).stepMs)
  expect(m.apiFetch).toHaveBeenCalledOnce();expect(client.leaseAction).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled();expect(m.openJournal).not.toHaveBeenCalled()
  expect(screen.queryByText('Agent stopped')).not.toBeInTheDocument();expect(screen.queryByText('Saved')).not.toBeInTheDocument()
 })

 it.each(['free','handed'])('disables takeover while a newer readonly snapshot bootstraps, then acquires on one click (%s)',async(leaseState)=>{
  mount();await ready()
  const newer=await client.read(new AbortController().signal)
  client.read.mockResolvedValue({...newer,version:'2'})
  const opening=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>()
  m.createPort.mockImplementationOnce((o:NativeEditorPortOptions)=>{
   const p=portRig();p.bootstrap.mockReturnValueOnce(opening.promise);ports.push(p);portOptions.push(o);queueMicrotask(()=>o.onReady?.('qualified'));return p
  })
  act(()=>stream?.('lease',leaseState==='handed'?ownedLease:emptyLease))
  await waitFor(()=>expect(ports[1]?.bootstrap).toHaveBeenCalledOnce())
  expect(screen.getByRole('button',{name:'Take over editing'})).toBeDisabled()
  fireEvent.click(screen.getByRole('button',{name:'Take over editing'}))
  expect(client.leaseAction).not.toHaveBeenCalled()
  await act(async()=>opening.resolve({bytes:newer.bytes,editorRevision:'reader:2',documentId:'doc'}))
  await waitFor(()=>expect(screen.getByRole('button',{name:'Take over editing'})).toBeEnabled())
  await acquire()
  expect(client.leaseAction).toHaveBeenCalledExactlyOnceWith('acquire',{},expect.any(AbortSignal))
 })

 it('opens Grid with the same read-only lease gate, isolated port and matching draft namespace',async()=>{
  client.open.mockResolvedValueOnce({sessionId:'private-session',applicationId:'gridcraft'})
  const base=await client.read(new AbortController().signal)
  client.read.mockResolvedValue({...base,applicationId:'gridcraft',bytes:new TextEncoder().encode('{"sheets":[]}')})
  mount();await ready()
  expect(portOptions[0].applicationId).toBe('gridcraft')
  expect(portOptions[0].requireAccessProtocol).toBe(true)
  expect(document.querySelector('iframe')?.title).toBe('GridCraft editor')
  expect(client.leaseAction).not.toHaveBeenCalled()
  expect(m.openJournal).not.toHaveBeenCalled()
  await acquire()
  expect(m.openJournal).toHaveBeenCalledWith({human:'human',workspace:'w',page:'p',applicationId:'gridcraft'},{signal:expect.any(AbortSignal)})
  ports.at(-1)!.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"sheets":[1]}'),editorRevision:'grid:2',documentId:'doc'})
  act(()=>portOptions.at(-1)!.onSaveRequest({documentId:'doc',editorRevision:'grid:2'}))
  await waitFor(()=>expect(client.save).toHaveBeenCalledOnce())
  expect(client.save.mock.calls[0][0].applicationId).toBe('gridcraft')
  await screen.findByText('Saved')
 })

 it('is default-off and never admits or mounts an editor',()=>{render(<NativeCraftEditorHost workspaceId="w" pageId="p" humanId="human"/>);expect(m.createClient).not.toHaveBeenCalled();expect(document.querySelector('iframe')).toBeNull()})
 it('opens one shared human session, boots an isolated read-only Word frame and never auto-acquires',async()=>{mount();await ready();expect(client.open).toHaveBeenCalledOnce();expect(m.createClient).toHaveBeenCalledOnce();expect(client.leaseAction).not.toHaveBeenCalled();expect(portOptions[0].requireAccessProtocol).toBe(true);expect(portOptions[0].applicationId).toBe('wordcraft');expect(document.querySelector('iframe')).toHaveAttribute('sandbox','allow-scripts');expect(document.querySelector('iframe')).toHaveAttribute('credentialless');expect(document.querySelector('iframe')?.src).toContain('readonly=1');expect(journal.persist).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled()})
 it('delegates only clipboard writes to the opaque native frame',async()=>{mount();await ready();const frame=document.querySelector('iframe');expect(frame).toHaveAttribute('allow','clipboard-write *');expect(frame).toHaveAttribute('sandbox','allow-scripts');expect(frame).toHaveAttribute('credentialless');expect(frame).toHaveAttribute('referrerpolicy','no-referrer')})
 it('downloads an editor export as a copy without confirming a server save',async()=>{
  const originalCreate=Object.getOwnPropertyDescriptor(URL,'createObjectURL'),originalRevoke=Object.getOwnPropertyDescriptor(URL,'revokeObjectURL')
  const blob=vi.fn(()=> 'blob:export-copy'),revoke=vi.fn(),click=vi.spyOn(HTMLAnchorElement.prototype,'click').mockImplementation(()=>{})
  Object.defineProperty(URL,'createObjectURL',{configurable:true,value:blob});Object.defineProperty(URL,'revokeObjectURL',{configurable:true,value:revoke})
  try{
   mount();await ready();await acquire();act(()=>portOptions.at(-1)!.onChanged('dirty','doc'))
   expect(portOptions.at(-1)!.onExport).toBeTypeOf('function')
   act(()=>portOptions.at(-1)!.onExport?.({name:'copy.docx',bytes:new Uint8Array([80,75,3,4]),saveAs:true}))
   expect(blob).toHaveBeenCalledOnce();expect(click).toHaveBeenCalledOnce()
   const anchor=click.mock.instances[0] as HTMLAnchorElement
   expect(anchor.download).toBe('copy.docx');expect(anchor.href).toBe('blob:export-copy')
   expect(client.save).not.toHaveBeenCalled();expect(ports.at(-1)!.ack).not.toHaveBeenCalled();expect(screen.queryByText('Saved')).not.toBeInTheDocument()
  }finally{click.mockRestore();if(originalCreate)Object.defineProperty(URL,'createObjectURL',originalCreate);else Reflect.deleteProperty(URL,'createObjectURL');if(originalRevoke)Object.defineProperty(URL,'revokeObjectURL',originalRevoke);else Reflect.deleteProperty(URL,'revokeObjectURL')}
 })
 it('does not deliver an export callback after read access has been retired',async()=>{
  const originalCreate=Object.getOwnPropertyDescriptor(URL,'createObjectURL')
  const blob=vi.fn();Object.defineProperty(URL,'createObjectURL',{configurable:true,value:blob})
  try{
  mount();await ready();const old=portOptions[0]
  act(()=>stream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'read_revoked'}))
  await waitFor(()=>expect(document.querySelector('iframe')).toBeNull())
  act(()=>old.onExport?.({name:'copy.docx',bytes:new Uint8Array([80,75,3,4])}))
  expect(blob).not.toHaveBeenCalled()
  }finally{if(originalCreate)Object.defineProperty(URL,'createObjectURL',originalCreate);else Reflect.deleteProperty(URL,'createObjectURL')}
 })
 it('puts its lock controls where the frame asks and keeps the save status in the page for assistive tech (#5132)',async()=>{client.lease.mockResolvedValue({...ownedLease,holder:otherPerson,mine:false,can_write:false});render(<NativeCraftEditorHost workspaceId="w" pageId="p" humanId="human" enabled placeLease={controls=><div data-testid="popover">{controls}</div>}/>);await screen.findByText('Pavel is editing');const popover=screen.getByTestId('popover');expect(popover).toContainElement(screen.getByRole('toolbar',{name:'Document editing'}));expect(popover).toContainElement(screen.getByRole('button',{name:'Request edit'}));expect(document.querySelector('div.sr-only[role="status"]')).not.toBeNull();expect(client.leaseAction).not.toHaveBeenCalled()})
 it('hides only the editor surface when the body reads the document instead (#5132)',async()=>{client.lease.mockResolvedValue({...ownedLease,holder:otherPerson,mine:false,can_write:false});const {container}=render(<NativeCraftEditorHost workspaceId="w" pageId="p" humanId="human" enabled editorHidden/>);await screen.findByText('Pavel is editing');expect(container.querySelector('[aria-busy][tabindex="-1"]')).toHaveClass('hidden');expect(screen.getByRole('toolbar',{name:'Document editing'})).toBeInTheDocument()})
 it('shows the exact named editor to another human without acquiring a write',async()=>{client.lease.mockResolvedValue({...ownedLease,holder:otherPerson,mine:false,can_write:false});mount();await screen.findByText('Pavel is editing');expect(screen.getByRole('button',{name:'Request edit'})).toBeEnabled();expect(client.leaseAction).not.toHaveBeenCalled();expect(m.openJournal).not.toHaveBeenCalled();expect(ports.some(p=>p.setReadOnly.mock.calls.some(c=>c[0]===false))).toBe(false)})
 it('cannot unlock from an acquire response without a fresh matching current lease proof',async()=>{mount();await ready();client.leaseAction.mockImplementationOnce(async()=>ownedLease);fireEvent.click(screen.getByRole('button',{name:'Take over editing'}));await waitFor(()=>expect(client.leaseAction).toHaveBeenCalled());await waitFor(()=>expect(screen.getByRole('alert')).toBeInTheDocument());expect(ports.some(p=>p.setReadOnly.mock.calls.some(c=>c[0]===false))).toBe(false);expect(client.save).not.toHaveBeenCalled()})
 it('reports Saved only after durable local persist, engine validation, server receipt and editor ACK',async()=>{mount();await ready();await acquire();const p=ports.at(-1)!,ack=deferred<boolean>();p.ack.mockReturnValueOnce(ack.promise);act(()=>portOptions.at(-1)!.onChanged('r2','doc'));await waitFor(()=>expect(p.ack).toHaveBeenCalledOnce());expect(journal.persist.mock.invocationCallOrder[0]).toBeLessThan(p.validate.mock.invocationCallOrder[0]);expect(p.validate.mock.invocationCallOrder[0]).toBeLessThan(client.save.mock.invocationCallOrder[0]);expect(screen.queryByText('Saved')).not.toBeInTheDocument();await act(async()=>ack.resolve(true));await screen.findByText('Saved')})
 it('retains the frozen engine and tells the truth when native failure precedes idle capture',async()=>{journal.recovery.mockResolvedValueOnce([{draft:{applicationId:'wordcraft',enginePin:'a'.repeat(64),sessionId:'old',generation:'1',baseVersion:'1',editorRevision:'old',bytes:new Uint8Array([99])},actions:['compare','copy']}]);mount();await ready();await acquire();const p=ports.at(-1)!,frame=document.querySelector('iframe');act(()=>{portOptions.at(-1)!.onChanged('not-yet-durable','doc');portOptions.at(-1)!.onError('native_port_closed')});await screen.findByText('Capturing the last changes was not confirmed');expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveAttribute('inert');expect(p.close).toHaveBeenCalled();expect(p.snapshot).not.toHaveBeenCalled();expect(journal.persist).not.toHaveBeenCalled();expect(journal.close).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled();expect(client.close).not.toHaveBeenCalled();expect(screen.getByRole('button',{name:'Keep a copy'})).toBeInTheDocument();expect(screen.queryByText('Saved')).not.toBeInTheDocument();expect(screen.queryByText(/draft kept/i)).not.toBeInTheDocument()})
 it('retains concurrent pending text after editor receipt acknowledgement fails',async()=>{mount();await ready();await acquire();const p=ports.at(-1)!,ack=deferred<boolean>(),frame=document.querySelector('iframe');p.ack.mockReturnValueOnce(ack.promise);act(()=>portOptions.at(-1)!.onChanged('r1','doc'));await waitFor(()=>expect(p.ack).toHaveBeenCalledOnce());act(()=>portOptions.at(-1)!.onChanged('r2-before-idle','doc'));await act(async()=>ack.resolve(false));await screen.findByText('Capturing the last changes was not confirmed');expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveAttribute('inert');expect(journal.close).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled();expect(client.close).not.toHaveBeenCalled();expect(screen.queryByText('Saved')).not.toBeInTheDocument()})
 it('explicit read revocation after fatal native failure detaches immediately and purges while journal custody is held',async()=>{mount();await ready();await acquire();const purge=deferred<void>(),p=ports.at(-1)!;journal.revokeRead.mockReturnValueOnce(purge.promise);act(()=>{portOptions.at(-1)!.onChanged('unconfirmed','doc');portOptions.at(-1)!.onError('native_port_closed')});await screen.findByText('Capturing the last changes was not confirmed');expect(journal.close).not.toHaveBeenCalled();act(()=>stream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'read_revoked'}));expect(document.querySelector('iframe')).toBeNull();await waitFor(()=>expect(journal.revokeRead).toHaveBeenCalledOnce());expect(journal.close).not.toHaveBeenCalled();expect(m.purge).not.toHaveBeenCalled();expect(p.snapshot).not.toHaveBeenCalled();expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument();await act(async()=>purge.resolve())})
 it('the registered privacy retirement removes a fatal frame before awaiting private journal purge',async()=>{mount();await ready();await acquire();const purge=deferred<void>();journal.revokeRead.mockReturnValueOnce(purge.promise);act(()=>portOptions.at(-1)!.onError('native_port_closed'));await screen.findByText('Capturing the last changes was not confirmed');expect(m.registerPrivacy).toHaveBeenCalledWith('human',expect.any(Function),expect.objectContaining({suspend:expect.any(Function)}));let retired!:Promise<void>;act(()=>{retired=privacyRetire!()});expect(document.querySelector('iframe')).toBeNull();expect(journal.revokeRead).toHaveBeenCalledOnce();expect(m.purge).not.toHaveBeenCalled();await act(async()=>{purge.resolve();await retired})})
 it('late lease or ACK callbacks and an unregistered old privacy callback cannot revive or purge a fresh mount',async()=>{const first=mount();await ready();await acquire();const p=ports.at(-1)!,oldPrivacy=privacyRetire!,oldOptions=portOptions.at(-1)!,frame=document.querySelector('iframe');act(()=>oldOptions.onError('native_port_closed'));await screen.findByText('Capturing the last changes was not confirmed');act(()=>{stream?.('lease',ownedLease);oldOptions.onReady?.('late');oldOptions.onChanged('late-revision','doc')});expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveAttribute('inert');expect(p.setReadOnly.mock.calls.filter(c=>c[0]===false)).toHaveLength(1);first.unmount();expect(m.registerPrivacy.mock.results[0].value).toHaveBeenCalledOnce();mount();await waitFor(()=>expect(ports.at(-1)!.bootstrap).toHaveBeenCalled());const fresh=document.querySelector('iframe');await act(async()=>oldPrivacy());expect(document.querySelector('iframe')).toBe(fresh);expect(journal.revokeRead).not.toHaveBeenCalled();expect(m.purge).not.toHaveBeenCalled()})
 it('detaches bootstrap failures that never acquired a writable journal',async()=>{const boot=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>();m.createPort.mockImplementationOnce((o:NativeEditorPortOptions)=>{const p=portRig();p.bootstrap.mockReturnValueOnce(boot.promise);ports.push(p);portOptions.push(o);queueMicrotask(()=>o.onReady?.('qualified'));return p});mount();await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled());act(()=>portOptions[0].onError('bootstrap_failure'));expect(document.querySelector('iframe')).toBeNull();expect(m.openJournal).not.toHaveBeenCalled();expect(client.close).toHaveBeenCalled();expect(screen.queryByText('Capturing the last changes was not confirmed')).not.toBeInTheDocument()})
 it('reuses the same pending privacy barrier until exact journal purge finishes',async()=>{mount();await ready();await acquire();const purge=deferred<void>();journal.revokeRead.mockReturnValueOnce(purge.promise);let first!:Promise<void>,second!:Promise<void>;act(()=>{first=privacyRetire!();second=privacyRetire!()});expect(first).toBe(second);let finished=false;void second.then(()=>{finished=true});await act(async()=>Promise.resolve());expect(finished).toBe(false);expect(journal.revokeRead).toHaveBeenCalledOnce();await act(async()=>{purge.resolve();await second});expect(finished).toBe(true)})
 it('a late old observer privacy event uses the closed custody fence and cannot purge a fresh writer',async()=>{mount();await ready();await acquire();const oldJournal=journal,oldStream=stream,oldPrivacy=privacyRetire!;oldJournal.close.mockImplementation(async()=>{oldJournal.revokeRead.mockRejectedValue(Error('closed_journal_scope_purge_forbidden'))});act(()=>stream?.('lease',{...ownedLease,mine:false,can_write:false}));await waitFor(()=>expect(oldJournal.close).toHaveBeenCalled());const freshJournal=journalRig();journal=freshJournal;client=clientRig();m.createClient.mockReturnValue(client);m.openJournal.mockResolvedValue(freshJournal);const fresh=mount();await waitFor(()=>expect(within(fresh.container).getByRole('button',{name:'Take over editing'})).toBeEnabled());fireEvent.click(within(fresh.container).getByRole('button',{name:'Take over editing'}));await waitFor(()=>expect(freshJournal.recovery).toHaveBeenCalled());await waitFor(()=>expect(ports.at(-1)!.setReadOnly).toHaveBeenCalledWith(false,expect.any(AbortSignal)));const freshFrame=fresh.container.querySelector('iframe');act(()=>oldStream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'read_revoked'}));await waitFor(()=>expect(oldJournal.revokeRead).toHaveBeenCalledOnce());await expect(oldPrivacy()).resolves.toBeUndefined();expect(oldJournal.eraseOwnRow).toHaveBeenCalledOnce();expect(m.purge).not.toHaveBeenCalled();expect(freshJournal.revokeRead).not.toHaveBeenCalled();expect(freshJournal.eraseOwnRow).not.toHaveBeenCalled();expect(freshJournal.close).not.toHaveBeenCalled();expect(fresh.container.querySelector('iframe')).toBe(freshFrame);expect(freshFrame).not.toHaveAttribute('inert')})
 it('refuses a mismatched application snapshot before mounting executable content',async()=>{client.open.mockResolvedValueOnce({sessionId:'private-session',applicationId:'gridcraft'});mount();await screen.findByRole('alert');expect(document.querySelector('iframe')).toBeNull();expect(m.openJournal).not.toHaveBeenCalled()})
 it('never unlocks or saves without exclusive durable draft storage',async()=>{m.openJournal.mockRejectedValue(Error('storage unavailable'));mount();await ready();fireEvent.click(screen.getByRole('button',{name:'Take over editing'}));await screen.findByRole('alert');expect(ports.some(p=>p.setReadOnly.mock.calls.some(c=>c[0]===false))).toBe(false);expect(client.save).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()})
 // #5104: a slow browser store once left the person holding a lease in a tab that could not edit.
 const leaseFollowsActions=()=>client.leaseAction.mockImplementation(async(action:string)=>{const next=action==='acquire'?ownedLease:emptyLease;client.lease.mockResolvedValue(next);return next})
 it('retries a journal that fails to open once and then takes over normally (#5104)',async()=>{m.openJournal.mockRejectedValueOnce(Error('storage slow'));mount();await ready();await acquire();expect(m.openJournal).toHaveBeenCalledTimes(2);expect(client.leaseAction.mock.calls.some(c=>c[0]==='release')).toBe(false);expect(screen.queryByRole('alert')).not.toBeInTheDocument()})
 it('gives a fresh lease back when the journal never opens, and offers the take-over again (#5104)',async()=>{m.openJournal.mockRejectedValue(Error('storage unavailable'));leaseFollowsActions();mount();await ready();fireEvent.click(screen.getByRole('button',{name:'Take over editing'}));await waitFor(()=>expect(screen.getByRole('alert')).toHaveTextContent('The document is free again'));expect(m.openJournal).toHaveBeenCalledTimes(3);expect(client.leaseAction).toHaveBeenCalledWith('release',{generation:ownedLease.generation},expect.any(AbortSignal));expect(ports.some(p=>p.setReadOnly.mock.calls.some(c=>c[0]===false))).toBe(false);expect(client.save).not.toHaveBeenCalled();expect(screen.getByRole('alert')).toHaveTextContent('The draft storage in this browser could not be opened');await waitFor(()=>expect(screen.getByRole('button',{name:'Take over editing'})).toBeEnabled());expect(screen.queryByRole('button',{name:'Hand back'})).not.toBeInTheDocument()})
 it('says so when the unused lease cannot be confirmed released (#5104)',async()=>{m.openJournal.mockRejectedValue(Error('storage unavailable'));client.leaseAction.mockImplementation(async(action:string)=>{if(action==='release')throw Error('offline');client.lease.mockResolvedValue(ownedLease);return ownedLease});mount();await ready();fireEvent.click(screen.getByRole('button',{name:'Take over editing'}));await waitFor(()=>expect(screen.getByRole('alert')).toHaveTextContent('Editing could not be released with confirmation'));expect(ports.some(p=>p.setReadOnly.mock.calls.some(c=>c[0]===false))).toBe(false);expect(client.save).not.toHaveBeenCalled()})
 it('a journal that opens only after the take-over gave up never unlocks the editor (#5104)',async()=>{const late=deferred<ReturnType<typeof journalRig>>(),lateJournal=journalRig();m.openJournal.mockReturnValueOnce(late.promise).mockRejectedValue(Error('storage unavailable'));leaseFollowsActions();vi.useFakeTimers({shouldAdvanceTime:true});try{mount();await ready();fireEvent.click(screen.getByRole('button',{name:'Take over editing'}));await waitFor(()=>expect(m.openJournal).toHaveBeenCalledOnce());await vi.advanceTimersByTimeAsync(5000);await waitFor(()=>expect(screen.getByRole('alert')).toHaveTextContent('The document is free again'));await act(async()=>{late.resolve(lateJournal)});await waitFor(()=>expect(lateJournal.close).toHaveBeenCalledOnce());expect(lateJournal.recovery).not.toHaveBeenCalled();expect(ports.some(p=>p.setReadOnly.mock.calls.some(c=>c[0]===false))).toBe(false);expect(client.save).not.toHaveBeenCalled()}finally{vi.useRealTimers()}})
 it('a recovery read that fails closes the unused journal before giving the lease back, and a later take-over works (#5104)',async()=>{const failing=journalRig();failing.recovery.mockRejectedValue(Error('recovery transaction timed out'));m.openJournal.mockResolvedValueOnce(failing);leaseFollowsActions();mount();await ready();fireEvent.click(screen.getByRole('button',{name:'Take over editing'}));await waitFor(()=>expect(screen.getByRole('alert')).toHaveTextContent('The document is free again'));expect(failing.close).toHaveBeenCalledOnce();const closedAt=failing.close.mock.invocationCallOrder[0],releaseAt=client.leaseAction.mock.invocationCallOrder[client.leaseAction.mock.calls.findIndex(c=>c[0]==='release')];expect(closedAt).toBeLessThan(releaseAt);expect(ports.some(p=>p.setReadOnly.mock.calls.some(c=>c[0]===false))).toBe(false);await waitFor(()=>expect(screen.getByRole('button',{name:'Take over editing'})).toBeEnabled());await acquire();expect(journal.recovery).toHaveBeenCalled();expect(journal.close).not.toHaveBeenCalled()})
 it('a take-over click while a re-acquire after accepting a proposal is still opening its journal starts no second take-over (#5104)',async()=>{const proposal={id:'ncp_0123456789abcdef01234567',authorName:'Jamie',baseVersion:'1',summary:'Nadpis',createdAt:'2026-10-29T17:00:00Z',state:'open' as const};client.proposals.mockResolvedValue([proposal]);rigNativeState='revision:opened';mount();await ready();await acquire();const pending=deferred<ReturnType<typeof journalRig>>();m.openJournal.mockReturnValueOnce(pending.promise);client.decideProposal.mockResolvedValue({...proposal,state:'accepted',resultVersion:'2'});fireEvent.click(await screen.findByRole('button',{name:'Accept'}));await waitFor(()=>expect(m.openJournal).toHaveBeenCalledTimes(2));const acquires=()=>client.leaseAction.mock.calls.filter(c=>c[0]==='acquire').length;expect(acquires()).toBe(2);const takeOver=screen.queryAllByRole('button',{name:'Take over editing'});for(const button of takeOver)fireEvent.click(button);await act(async()=>{await Promise.resolve()});expect(acquires()).toBe(2);await act(async()=>pending.resolve(journalRig()));await waitFor(()=>expect(ports.at(-1)!.setReadOnly).toHaveBeenCalledWith(false,expect.any(AbortSignal)));expect(acquires()).toBe(2);expect(client.leaseAction.mock.calls.some(c=>c[0]==='release')).toBe(false)})
 it('no release or hand-off runs while a re-acquire after accepting a proposal is still opening its journal (#5104)',async()=>{const proposal={id:'ncp_0123456789abcdef01234567',authorName:'Jamie',baseVersion:'1',summary:'Nadpis',createdAt:'2026-10-29T17:00:00Z',state:'open' as const};client.proposals.mockResolvedValue([proposal]);rigNativeState='revision:opened';mount();await ready();await acquire();const pending=deferred<ReturnType<typeof journalRig>>();m.openJournal.mockReturnValueOnce(pending.promise);client.decideProposal.mockResolvedValue({...proposal,state:'accepted',resultVersion:'2'});fireEvent.click(await screen.findByRole('button',{name:'Accept'}));await waitFor(()=>expect(m.openJournal).toHaveBeenCalledTimes(2));for(const button of screen.queryAllByRole('button',{name:'Hand back'}))fireEvent.click(button);act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}));for(const button of screen.queryAllByRole('button',{name:'Hand over to Jana'}))fireEvent.click(button);await act(async()=>{await Promise.resolve()});expect(client.leaseAction.mock.calls.some(c=>c[0]==='release'||c[0]==='handoff')).toBe(false);await act(async()=>pending.resolve(journalRig()));await waitFor(()=>expect(ports.at(-1)!.setReadOnly).toHaveBeenCalledWith(false,expect.any(AbortSignal)));expect(client.leaseAction.mock.calls.some(c=>c[0]==='release'||c[0]==='handoff')).toBe(false)})
 it('retires a recovered draft whose bytes the server already holds instead of offering it',async()=>{const same={applicationId:'wordcraft',enginePin:'a'.repeat(64),sessionId:'old',generation:'1',baseVersion:'0',editorRevision:'lost-ack',bytes:new TextEncoder().encode('{"body":[]}')},other={...same,editorRevision:'later',bytes:new Uint8Array([99])};journal.recovery.mockResolvedValueOnce([{draft:same,actions:['compare','copy']},{draft:other,actions:['compare','copy']}]);mount();await ready();await acquire();await waitFor(()=>expect(journal.discardRecovered).toHaveBeenCalledWith(expect.objectContaining({draft:same})));expect(journal.discardRecovered).not.toHaveBeenCalledWith(expect.objectContaining({draft:other}));expect(screen.getAllByRole('button',{name:'Compare'})).toHaveLength(1);expect(client.save).not.toHaveBeenCalled()})
 it('reconciles the lost save before closing the client when the connection retires',async()=>{mount();await ready();await acquire();client.save.mockImplementationOnce(()=>new Promise(()=>{}));client.reconcile.mockImplementation(async(r:NativeSaveRequest)=>receipt(r));act(()=>portOptions.at(-1)!.onChanged('pending','doc'));await waitFor(()=>expect(client.save).toHaveBeenCalledOnce());act(()=>stream?.('session_retired',{delivery:'stopped',reason:'session_closed'}));await waitFor(()=>expect(client.close).toHaveBeenCalled());expect(client.reconcile).toHaveBeenCalled();expect(client.reconcile.mock.invocationCallOrder[0]).toBeLessThan(client.close.mock.invocationCallOrder[0])})
 // The button sits outside the frame; a person who pressed it keeps typing into the document.
 it('returns the keyboard to the document after the check button',async()=>{
  mount();await ready();await acquire()
  client.save.mockRejectedValueOnce(new TypeError('Failed to fetch'))
  client.reconcile.mockImplementation(async(r:NativeSaveRequest)=>({...r,bytes:undefined,version:'2',durability:'node',issuedBy:'crewship',phase:'committed',candidateHash:r.contentHash}))
  act(()=>portOptions.at(-1)!.onChanged('during-outage','doc'))
  const check=await screen.findByRole('button',{name:'Check the result'})
  const frame=document.querySelector('iframe') as HTMLIFrameElement,focus=vi.spyOn(frame,'focus')
  fireEvent.click(check)
  await waitFor(()=>expect(client.reconcile).toHaveBeenCalled())
  expect(focus).toHaveBeenCalled()
 })
 // UX walkthrough RC-4 R05: offline before the save left the browser. The
 // server retires the attempt that never landed (reconcile 410); the editor must resume
 // saving instead of staying unsaved forever, and coming back online asks
 // the server by itself.
 it('resumes saving after an outage whose save never reached the server',async()=>{
  mount();await ready();await acquire()
  client.save.mockRejectedValueOnce(new TypeError('Failed to fetch'))
  client.reconcile.mockRejectedValueOnce(new NativeSaveError('absent'))
  act(()=>portOptions.at(-1)!.onChanged('during-outage','doc'))
  await screen.findByText('Result not confirmed');expect(screen.getByRole('button',{name:'Check the result'})).toBeVisible()
  act(()=>{window.dispatchEvent(new Event('online'))})
  await waitFor(()=>expect(client.reconcile).toHaveBeenCalledOnce())
  await waitFor(()=>expect(client.save).toHaveBeenCalledTimes(2))
  await screen.findByText('Saved');expect(screen.queryByRole('button',{name:'Check the result'})).not.toBeInTheDocument()
  // The new save is a fresh capture at the unchanged base, not the lost ticket replayed.
  expect(client.save.mock.calls[1][0]).toMatchObject({baseVersion:'1'})
  // Reconcile names exactly the lost attempt; the resumed save is a new attempt.
  const lost=client.save.mock.calls[0][0].attemptId
  expect(lost).toMatch(/^[A-Za-z0-9_-]{8,64}$/);expect(client.reconcile.mock.calls[0][0].attemptId).toBe(lost)
  expect(client.save.mock.calls[1][0].attemptId).not.toBe(lost)
  ports.at(-1)!.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"body":[2]}'),editorRevision:'snapshot-2',documentId:'doc'})
  act(()=>portOptions.at(-1)!.onChanged('after-outage','doc'))
  await waitFor(()=>expect(client.save).toHaveBeenCalledTimes(3))
  expect(client.save.mock.calls[2][0]).toMatchObject({baseVersion:'2'})
  expect(client.reconcile).toHaveBeenCalledOnce()
 })
 it('offers old local recovery only for compare or copy and never publishes it on mount or acquire',async()=>{journal.recovery.mockResolvedValueOnce([{draft:{applicationId:'wordcraft',enginePin:'a'.repeat(64),sessionId:'old',generation:'1',baseVersion:'1',editorRevision:'old',bytes:new Uint8Array([99])},actions:['compare','copy']}]);mount();await ready();expect(m.openJournal).not.toHaveBeenCalled();await acquire();await screen.findByRole('button',{name:'Compare'});expect(screen.getByRole('button',{name:'Keep a copy'})).toBeInTheDocument();expect(client.save).not.toHaveBeenCalled();expect(journal.persist).not.toHaveBeenCalled();fireEvent.click(screen.getByRole('button',{name:'Compare'}));const server=await screen.findByRole('textbox',{name:'Server version'});expect(server).toHaveAttribute('readonly');expect(screen.getByRole('textbox',{name:'Local draft'})).toHaveAttribute('readonly');expect(server).toHaveValue('{\"body\":[]}');expect(client.save).not.toHaveBeenCalled()})
 it('does not send a whole version when native engine validation fails',async()=>{mount();await ready();await acquire();ports.at(-1)!.validate.mockRejectedValueOnce(Error('invalid engine state'));act(()=>portOptions.at(-1)!.onChanged('r2','doc'));await waitFor(()=>expect(ports.at(-1)!.validate).toHaveBeenCalledOnce());await screen.findByText('Unsaved draft');expect(client.save).not.toHaveBeenCalled();expect(screen.queryByText('Saved')).not.toBeInTheDocument()})
 it('awaits pending-draft IDB and the applied native read-only ACK before server handoff and journal release',async()=>{mount();await ready();await acquire();const p=ports.at(-1)!,disk=deferred<void>(),guard=deferred<boolean>();journal.persist.mockReturnValueOnce(disk.promise);p.setReadOnly.mockImplementationOnce(()=>guard.promise);client.leaseAction.mockImplementationOnce(async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,generation:'9007199254740996',holder:{actor_id:'user:other',display_name:'Jana',kind:'human'}});return emptyLease});act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}));act(()=>portOptions.at(-1)!.onChanged('final-edit','doc'));fireEvent.click(await screen.findByRole('button',{name:'Hand over to Jana'}));await waitFor(()=>expect(journal.persist).toHaveBeenCalledOnce());expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false);expect(document.querySelector('iframe')?.style.pointerEvents).toBe('none');await act(async()=>disk.resolve());await waitFor(()=>expect(p.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal)));expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false);await act(async()=>guard.resolve(true));await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(true));expect(journal.revokeRead).not.toHaveBeenCalled();await waitFor(()=>expect(journal.close).toHaveBeenCalled());const handoffIndex=client.leaseAction.mock.calls.findIndex(c=>c[0]==='handoff');expect(client.leaseAction.mock.invocationCallOrder[handoffIndex]).toBeLessThan(journal.close.mock.invocationCallOrder[0]);expect(client.save).toHaveBeenCalledOnce()})
 it('commits and acknowledges the last edit before handing custody to another human before idle autosave',async()=>{mount();await ready();await acquire();const p=ports.at(-1)!,saved=new TextEncoder().encode('{"body":[1]}'),initial=await client.read();client.save.mockImplementationOnce(async(r:NativeSaveRequest)=>{client.read.mockResolvedValue({...initial,version:'2',bytes:r.bytes});return receipt(r)});client.leaseAction.mockImplementationOnce(async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:{actor_id:'user:other',display_name:'Jana',kind:'human'},generation:'9007199254740996'});return emptyLease});act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}));await screen.findByRole('button',{name:'Hand over to Jana'});act(()=>portOptions.at(-1)!.onChanged('last-unflushed-edit','doc'));fireEvent.click(screen.getByRole('button',{name:'Hand over to Jana'}));await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(true));expect(client.save).toHaveBeenCalled();expect(p.ack).toHaveBeenCalled();const handoff=client.leaseAction.mock.calls.findIndex(c=>c[0]==='handoff');expect(client.save.mock.invocationCallOrder[0]).toBeLessThan(client.leaseAction.mock.invocationCallOrder[handoff]);expect(p.ack.mock.invocationCallOrder[0]).toBeLessThan(client.leaseAction.mock.invocationCallOrder[handoff]);await waitFor(()=>expect(ports.at(-1)).not.toBe(p));await waitFor(()=>expect(ports.at(-1)!.bootstrap).toHaveBeenCalled());expect(ports.at(-1)!.bootstrap.mock.calls[0][0]).toEqual(saved);expect(ports.at(-1)!.setReadOnly).not.toHaveBeenCalledWith(false,expect.anything());expect(await client.read()).toMatchObject({version:'2',bytes:saved})})
 it('awaits successful editor receipt acknowledgement before transferring custody',async()=>{mount();await ready();await acquire();const acknowledgement=deferred<boolean>(),p=ports.at(-1)!;p.ack.mockReturnValueOnce(acknowledgement.promise);act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}));act(()=>portOptions.at(-1)!.onChanged('final-edit','doc'));fireEvent.click(await screen.findByRole('button',{name:'Hand over to Jana'}));await waitFor(()=>expect(p.ack).toHaveBeenCalledOnce());expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false);await act(async()=>acknowledgement.resolve(true));await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(true))})
 describe('the first click or keystroke in a Sheet takes over editing',()=>{
  type Attempts={onEditAttempt?:(kind:'pointer'|'key')=>void;onHeldInputDropped?:(reason:string)=>void}
  const attempt=(kind:'pointer'|'key'='key')=>act(()=>(portOptions.at(-1) as unknown as Attempts).onEditAttempt!(kind))
  const rebindable=(answer=true)=>{const rebind=vi.fn(()=>answer);Object.assign(ports[0],{rebind});return rebind}
  const acquires=()=>client.leaseAction.mock.calls.filter(c=>c[0]==='acquire').length
  const readyReader=()=>waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalledOnce())
  beforeEach(async()=>{
   client.open.mockResolvedValue({sessionId:'private-session',applicationId:'gridcraft'})
   const snapshot=await client.read();client.read.mockResolvedValue({...snapshot,applicationId:'gridcraft'})
  })
  it('takes over in the same frame, so the clicked cell and the keys stay, and says so',async()=>{
   mount();await ready();const frame=document.querySelector('iframe'),rebind=rebindable()
   attempt()
   await waitFor(()=>expect(ports[0].setReadOnly).toHaveBeenCalledWith(false,expect.any(AbortSignal)))
   expect(rebind).toHaveBeenCalledWith({sessionId:'private-session',generation:ownedLease.generation,baseVersion:'1'})
   expect(document.querySelector('iframe')).toBe(frame);expect(ports).toHaveLength(1);expect(frame).not.toHaveAttribute('inert')
   expect(await screen.findByText('You are editing now.')).toBeInTheDocument()
   // The take-over that succeeded is not undone when it finishes: the editor stays writable.
   expect(ports[0].setReadOnly.mock.calls.at(-1)?.[0]).toBe(false)
  })
  it('never unlocks before the server gave the lock',async()=>{
   mount();await ready();rebindable();const reply=deferred<unknown>();client.leaseAction.mockImplementationOnce(()=>reply.promise)
   attempt()
   await waitFor(()=>expect(acquires()).toBe(1))
   expect(ports[0].setReadOnly).not.toHaveBeenCalledWith(false,expect.anything())
   await act(async()=>reply.resolve(emptyLease))
   expect(ports[0].setReadOnly).not.toHaveBeenCalledWith(false,expect.anything())
  })
  // A click beside the chat did not take over, silently. What the host did with the last attempt is on the
  // mount (a reason, never content), so a run can say which condition held.
  it('says on the mount what it did with the last attempt',async()=>{
   mount();await ready();rebindable()
   const state=()=>document.querySelector('[data-edit-attempt]')?.getAttribute('data-edit-attempt')
   expect(state()).toBe('')
   attempt('pointer')
   await waitFor(()=>expect(state()).toBe('taking'))
   await waitFor(()=>expect(ports[0].setReadOnly).toHaveBeenCalledWith(false,expect.any(AbortSignal)))
   attempt('pointer')
   await waitFor(()=>expect(state()).toBe('ignored:writing'))
  })
  // The person's own lease without this editor (a creation custody not yet released) is
  // not a reason to ignore the click: it takes over, as Take over editing does.
  it('takes over on a click while the lease is the person\'s own but no editor writes',async()=>{
   mount();await ready();rebindable()
   act(()=>{stream?.('lease',ownedLease)})
   attempt('pointer')
   await waitFor(()=>expect(acquires()).toBe(1))
   expect(document.querySelector('[data-edit-attempt]')?.getAttribute('data-edit-attempt')).toBe('taking')
  })
  it('asks once, however many keys are pressed',async()=>{
   mount();await ready();rebindable();attempt('pointer');attempt();attempt()
   await waitFor(()=>expect(ports[0].setReadOnly).toHaveBeenCalledWith(false,expect.any(AbortSignal)))
   expect(acquires()).toBe(1)
  })
  it('reopens as before when the frame cannot be kept, and says the typing was not entered',async()=>{
   mount();await ready();rebindable(false);attempt()
   await waitFor(()=>expect(ports.at(-1)!.setReadOnly).toHaveBeenCalledWith(false,expect.any(AbortSignal)))
   expect(ports).toHaveLength(2)
   expect(await screen.findByText(/What you typed was not entered/)).toBeInTheDocument()
  })
  it.each([['another person',otherPerson,/Pavel is editing this Sheet/],['the agent',{actor_id:'agent:a',display_name:'Riley',kind:'agent'},/Riley is changing this Sheet/],['another tab of the same person',ownedLease.holder,/another tab/]] as const)('takes nothing while held by %s, says who, and drops the keys at once',async(_,holder,text)=>{
   client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder})
   mount();await readyReader();await waitFor(()=>expect(screen.getByRole('button',{name:/Request edit|Ask the other tab/})).toBeEnabled())
   attempt()
   expect(await screen.findByText(text,{selector:'[data-take-over-notice] *'})).toBeInTheDocument()
   expect(acquires()).toBe(0)
   expect(ports[0].setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal))
  })
  it('a reader is told they can only view, and nothing is asked of the server',async()=>{
   client.lease.mockResolvedValue({...emptyLease,can_edit:false,can_share:false})
   mount();await readyReader();await waitFor(()=>expect(screen.getAllByText('Read-only',{selector:'span'}).length).toBeGreaterThan(0))
   attempt()
   expect(await screen.findByText('You can view this Sheet but not edit it.')).toBeInTheDocument()
   expect(acquires()).toBe(0)
  })
  it('a refused take-over drops the held keys and stays read-only',async()=>{
   mount();await ready();rebindable();client.leaseAction.mockRejectedValueOnce(Error('holder changed'));attempt()
   await screen.findByRole('alert')
   expect(ports[0].setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal))
   expect(ports[0].setReadOnly).not.toHaveBeenCalledWith(false,expect.anything())
   expect(screen.queryByText('You are editing now.')).not.toBeInTheDocument()
  })
  it('says when the editor had to drop what was typed',async()=>{
   mount();await ready();act(()=>(portOptions[0] as unknown as Attempts).onHeldInputDropped!('timeout'))
   expect(await screen.findByText(/What you typed was not entered/)).toBeInTheDocument()
  })
  // The view matrix: what each role sees comes only from the server's lease answer, never from the role itself.
  // MANAGER and ADMIN on a Sheet they do not own get whichever answer the server gives; both are covered.
  const reads={...emptyLease,can_edit:false,can_share:false}
  it.each([
   ['VIEWER',reads,false],
   ['MEMBER who only reads',reads,false],
   ['MEMBER who owns the Sheet',emptyLease,true],
   ['MANAGER the server lets edit',{...emptyLease,can_share:false},true],
   ['MANAGER the server does not let edit',reads,false],
   ['ADMIN the server lets edit',{...emptyLease,can_share:false},true],
   ['ADMIN the server does not let edit',reads,false],
  ] as const)('%s: the bar and a click offer only what the server allows',async(_,lease,mayEdit)=>{
   client.lease.mockResolvedValue(lease)
   mount();await readyReader()
   const takeOver=()=>screen.queryByRole('button',{name:'Take over editing'})
   if(mayEdit){
    await waitFor(()=>expect(takeOver()).toBeEnabled())
    rebindable();attempt()
    await waitFor(()=>expect(acquires()).toBe(1))
   }else{
    await waitFor(()=>expect(screen.getAllByText('Read-only',{selector:'span'}).length).toBeGreaterThan(0))
    expect(takeOver()===null||takeOver()!.hasAttribute('disabled')).toBe(true)
    attempt()
    expect(await screen.findByText('You can view this Sheet but not edit it.')).toBeInTheDocument()
    expect(acquires()).toBe(0)
    // Nothing the server would refuse is offered: no preview to publish without edit access.
    expect(screen.queryByRole('button',{name:'Publish preview'})).toBeNull()
   }
  })
  it('Word keeps the explicit take-over',async()=>{
   client.open.mockResolvedValue({sessionId:'private-session',applicationId:'wordcraft'});const s2=await client.read();client.read.mockResolvedValue({...s2,applicationId:'wordcraft'})
   mount();await ready();attempt()
   expect(acquires()).toBe(0)
  })
 })
 describe('handing the edit access back for a message',()=>{
  const mountWithHandBack=()=>{const ref:{current:null|(()=>Promise<NativeCraftHandBack>)}={current:null};render(<NativeCraftEditorHost workspaceId="w" pageId="p" humanId="human" enabled handBackRef={ref} />);return ref}
  const released=()=>client.leaseAction.mock.calls.some(c=>c[0]==='release')
  it('reports returned only after the release and a lease read that confirms it',async()=>{
   const ref=mountWithHandBack();await ready();await acquire()
   const reply=deferred<unknown>();let settled=false
   client.leaseAction.mockImplementation(async(action:string)=>{if(action==='release'){await reply.promise;client.lease.mockResolvedValue(emptyLease)}return emptyLease})
   let result!:Promise<NativeCraftHandBack>
   act(()=>{result=ref.current!();void result.then(()=>{settled=true})})
   await waitFor(()=>expect(released()).toBe(true))
   expect(settled).toBe(false)
   await act(async()=>reply.resolve(undefined))
   await expect(result).resolves.toEqual({state:'returned'})
  })
  it('reports kept when the release is not confirmed, and never takes the document as free',async()=>{
   const ref=mountWithHandBack();await ready();await acquire()
   client.leaseAction.mockImplementation(async(action:string)=>{if(action==='release')throw Error('offline');return ownedLease})
   let result!:NativeCraftHandBack
   await act(async()=>{result=await ref.current!()})
   expect(result).toEqual({state:'kept',reason:'unconfirmed'})
  })
  it('keeps an editor whose last change stays unsaved, without a release',async()=>{
   const ref=mountWithHandBack();await ready();await acquire()
   client.save.mockImplementation(()=>new Promise(()=>{}))
   act(()=>portOptions.at(-1)!.onChanged('typing','doc'))
   let result!:NativeCraftHandBack
   await act(async()=>{result=await ref.current!()})
   expect(result).toEqual({state:'kept',reason:'unsaved'})
   expect(released()).toBe(false)
  },10000)
  it('has nothing to return for a reader',async()=>{
   const ref=mountWithHandBack();await ready()
   await expect(ref.current!()).resolves.toEqual({state:'not_held'})
   expect(released()).toBe(false)
  })
  it('never takes the access back by itself afterwards',async()=>{
   const ref=mountWithHandBack();await ready();await acquire()
   client.leaseAction.mockImplementation(async(action:string)=>{if(action==='release')client.lease.mockResolvedValue(emptyLease);return emptyLease})
   await act(async()=>{await ref.current!()})
   await waitFor(()=>expect(screen.getByRole('button',{name:'Take over editing'})).toBeEnabled())
   expect(client.leaseAction.mock.calls.filter(c=>c[0]==='acquire')).toHaveLength(1)
  })
 })
 it('flushes and acknowledges the final whole version before explicit release too',async()=>{mount();await ready();await acquire();const p=ports.at(-1)!;act(()=>portOptions.at(-1)!.onChanged('last-before-release','doc'));fireEvent.click(screen.getByRole('button',{name:'Hand back'}));await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='release')).toBe(true));expect(client.save).toHaveBeenCalled();expect(p.ack).toHaveBeenCalled()})
 it('never hands off an uncertain final save and retains the original private draft',async()=>{mount();await ready();await acquire();client.save.mockRejectedValueOnce(Error('lost ACK'));act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}));act(()=>portOptions.at(-1)!.onChanged('final-edit','doc'));fireEvent.click(await screen.findByRole('button',{name:'Hand over to Jana'}));await screen.findByRole('alert');expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false);expect(journal.revokeRead).not.toHaveBeenCalled();expect(document.querySelector('iframe')).not.toBeNull();expect(screen.queryByText('Saved')).not.toBeInTheDocument()})
 it('a failed durable capture keeps the guarded native frame and never transfers custody',async()=>{mount();await ready();await acquire();journal.persist.mockResolvedValueOnce(undefined).mockRejectedValueOnce(Error('disk full'));act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}));fireEvent.click(await screen.findByRole('button',{name:'Hand over to Jana'}));await screen.findByText('The draft cannot be captured safely. The handover did not happen.');expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false);expect(document.querySelector('iframe')).not.toBeNull();expect(ports.at(-1)!.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal));expect(journal.close).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()})
 it('explicit read retirement immediately removes content and purges without awaiting a hung native capture',async()=>{mount();await ready();await acquire();ports.at(-1)!.snapshot.mockReturnValueOnce(new Promise(()=>{}));act(()=>stream?.('lease',{...ownedLease,can_write:false}));await waitFor(()=>expect(ports.at(-1)!.snapshot).toHaveBeenCalledOnce());act(()=>stream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'read_revoked'}));await waitFor(()=>expect(document.querySelector('iframe')).toBeNull());expect(journal.revokeRead).toHaveBeenCalledOnce();expect(client.save).not.toHaveBeenCalled()})
 it('hands off after a changed pulse during an identical final snapshot settles locally without a duplicate save',async()=>{
  mount();await ready();await acquire()
  const p=ports.at(-1)!
  let captures=0
  p.snapshot.mockImplementation(async()=>{
   captures++
   if(captures===1)portOptions.at(-1)!.onChanged('same-native-state','doc')
   return {bytes:new TextEncoder().encode('{"body":[1]}'),editorRevision:`capture-${captures}`,documentId:'doc',nativeState:'same-native-state'}
  })
  client.leaseAction.mockImplementationOnce(async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:{actor_id:'user:other',display_name:'Jana',kind:'human'},generation:'9007199254740996'});return emptyLease})
  act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}))
  fireEvent.click(await screen.findByRole('button',{name:'Hand over to Jana'}))
  await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(true))
  expect(captures).toBeGreaterThanOrEqual(2)
  expect(client.save).toHaveBeenCalledOnce()
  expect(p.ack).toHaveBeenCalledOnce()
  const handoff=client.leaseAction.mock.calls.findIndex(c=>c[0]==='handoff')
  expect(p.ack.mock.invocationCallOrder[0]).toBeLessThan(client.leaseAction.mock.invocationCallOrder[handoff])
 })
 // Taking the editor and handing it on without typing: the final capture is the
 // confirmed server version, so nothing is sent and custody still moves.
 for(const action of ['handoff','release'] as const)it(`${action === 'handoff' ? 'hands off' : 'releases'} an unchanged editor without a save`,async()=>{
  mount();await ready();await acquire()
  const p=ports.at(-1)!
  p.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"body":[]}'),editorRevision:'initial',documentId:'doc'})
  client.leaseAction.mockImplementationOnce(async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:action==='handoff'?{actor_id:'user:other',display_name:'Jana',kind:'human'}:null,generation:'9007199254740996'});return emptyLease})
  if(action==='handoff'){
   act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}))
   fireEvent.click(await screen.findByRole('button',{name:'Hand over to Jana'}))
  }else fireEvent.click(screen.getByRole('button',{name:'Hand back'}))
  await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]===action)).toBe(true))
  expect(p.snapshot).toHaveBeenCalled()
  expect(client.save).not.toHaveBeenCalled()
  expect(screen.queryByText('The result of the action was not confirmed. The draft is kept.')).not.toBeInTheDocument()
 })
 // The final save is already proven and write is retired, so a failed POST can
 // only be sent again. The other person must not wait for this tab to close.
 for(const action of ['handoff','release'] as const)it(`retries a failed ${action} without saving again`,async()=>{
  mount();await ready();await acquire()
  const p=ports.at(-1)!
  p.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"body":[]}'),editorRevision:'initial',documentId:'doc'})
  client.leaseAction.mockRejectedValueOnce(new Error('network'))
  const press=async()=>{
   if(action==='handoff'){
    act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}))
    fireEvent.click(await screen.findByRole('button',{name:'Hand over to Jana'}))
   }else fireEvent.click(await screen.findByRole('button',{name:'Hand back'}))
  }
  await press()
  await waitFor(()=>expect(client.leaseAction.mock.calls.filter(c=>c[0]===action)).toHaveLength(1))
  await screen.findByText('The result of the action was not confirmed. The draft is kept.')
  client.leaseAction.mockImplementationOnce(async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:action==='handoff'?{actor_id:'user:other',display_name:'Jana',kind:'human'}:null,generation:'9007199254740996'});return {}})
  await press()
  await waitFor(()=>expect(client.leaseAction.mock.calls.filter(c=>c[0]===action)).toHaveLength(2))
  expect(client.save).not.toHaveBeenCalled()
 })
 // The final capture equals a version an idle save just committed: custody waits
 // for the editor's ACK of that receipt and never moves when the ACK fails.
 for(const acknowledged of [true,false])it(`${acknowledged?'releases':'keeps'} an unchanged editor ${acknowledged?'after':'when'} the last save's editor ACK ${acknowledged?'arrives':'fails'}`,async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   mount();await ready();await acquire()
   const p=ports.at(-1)!,ack=deferred<boolean>()
   p.ack.mockReturnValueOnce(ack.promise)
   act(()=>portOptions.at(-1)!.onChanged('idle-edit','doc'))
   await act(async()=>{await vi.advanceTimersByTimeAsync(300)})
   await waitFor(()=>expect(p.ack).toHaveBeenCalledOnce())
   client.leaseAction.mockImplementationOnce(async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:null,generation:'9007199254740996'});return emptyLease})
   const captures=p.snapshot.mock.calls.length
   fireEvent.click(screen.getByRole('button',{name:'Hand back'}))
   await waitFor(()=>expect(p.snapshot.mock.calls.length).toBeGreaterThan(captures))
   await act(async()=>{await vi.advanceTimersByTimeAsync(50)})
   expect(client.leaseAction.mock.calls.some(c=>c[0]==='release')).toBe(false)
   await act(async()=>ack.resolve(acknowledged))
   if(acknowledged)await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='release')).toBe(true))
   else{await screen.findByRole('alert');expect(client.leaseAction.mock.calls.some(c=>c[0]==='release')).toBe(false)}
   expect(client.save).toHaveBeenCalledOnce()
  }finally{vi.useRealTimers()}
 })
 it('waits for the running idle save and commits its own final snapshot before handoff',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   vi.stubGlobal('crypto',{randomUUID:webcrypto.randomUUID.bind(webcrypto),getRandomValues:webcrypto.getRandomValues.bind(webcrypto),subtle:{digest:vi.fn(async(_a:string,data:Uint8Array)=>{const out=new Uint8Array(32);data.forEach((b,i)=>{out[i%32]=(out[i%32]*31+b)&255});return out.buffer})}})
   const idle=deferred<ReturnType<typeof receipt>>()
   client.save.mockReturnValueOnce(idle.promise)
   mount();await ready();await acquire()
   const p=ports.at(-1)!
   p.snapshot.mockResolvedValueOnce({bytes:new TextEncoder().encode('{"body":[1]}'),editorRevision:'idle-snapshot',documentId:'doc'}).mockResolvedValue({bytes:new TextEncoder().encode('{"body":[2]}'),editorRevision:'final-snapshot',documentId:'doc'})
   act(()=>portOptions.at(-1)!.onChanged('pending-idle','doc'))
   await act(async()=>{await vi.advanceTimersByTimeAsync(300)})
   await waitFor(()=>expect(client.save).toHaveBeenCalledOnce())
   client.leaseAction.mockImplementationOnce(async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:{actor_id:'user:other',display_name:'Jana',kind:'human'},generation:'9007199254740996'});return emptyLease})
   act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}))
   fireEvent.click(await screen.findByRole('button',{name:'Hand over to Jana'}))
   await waitFor(()=>expect(p.validate).toHaveBeenCalledTimes(2))
   await act(async()=>{})
   expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false)
   expect(client.save).toHaveBeenCalledOnce()
   await act(async()=>idle.resolve(receipt(client.save.mock.calls[0][0])))
   await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(true))
   expect(client.save).toHaveBeenCalledTimes(2)
   expect(client.save.mock.calls[1][0]).toMatchObject({baseVersion:'2',editorRevision:'final-snapshot'})
   const handoff=client.leaseAction.mock.calls.findIndex(c=>c[0]==='handoff')
   expect(client.save.mock.invocationCallOrder[1]).toBeLessThan(client.leaseAction.mock.invocationCallOrder[handoff])
   expect(p.ack).toHaveBeenCalledTimes(2)
  }finally{vi.useRealTimers()}
 })
 it('keeps the current editor and completes handoff when the 30-second reauthorization is a valid no-op',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   client.reauthorize.mockResolvedValue(false)
   mount();await ready();await acquire()
   const p=ports.at(-1)!,frame=document.querySelector('iframe'),acknowledgement=deferred<boolean>()
   p.ack.mockReturnValueOnce(acknowledgement.promise)
   client.leaseAction.mockImplementationOnce(async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:{actor_id:'user:other',display_name:'Jana',kind:'human'},generation:'9007199254740996'});return emptyLease})
   act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}))
   act(()=>portOptions.at(-1)!.onChanged('final-edit','doc'));fireEvent.click(await screen.findByRole('button',{name:'Hand over to Jana'}))
   await waitFor(()=>expect(p.ack).toHaveBeenCalledOnce())
   await act(async()=>{await vi.advanceTimersByTimeAsync(30000)})
   expect(client.reauthorize).toHaveBeenCalledOnce()
   expect(client.close).not.toHaveBeenCalled()
   expect(document.querySelector('iframe')).toBe(frame)
   expect(p.close).not.toHaveBeenCalled()
   expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false)
   await act(async()=>acknowledgement.resolve(true))
   await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(true))
  }finally{vi.useRealTimers()}
 })
 it('still retires the editor when the 30-second reauthorization rejects',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   client.reauthorize.mockRejectedValue(Error('native_session_unavailable'))
   mount();await ready()
   await act(async()=>{await vi.advanceTimersByTimeAsync(30000)})
   expect(client.reauthorize).toHaveBeenCalledOnce()
   expect(client.close).toHaveBeenCalled()
   expect(document.querySelector('iframe')).toBeNull()
   expect(screen.getByRole('alert')).toHaveTextContent('The connection was closed')
  }finally{vi.useRealTimers()}
 })
 it('a generic write refusal retains private journal rows and cannot claim Saved',async()=>{mount();await ready();await acquire();client.save.mockRejectedValueOnce(new NativeSaveError('write_revoked'));act(()=>portOptions.at(-1)!.onChanged('r2','doc'));await waitFor(()=>expect(client.save).toHaveBeenCalledOnce());await waitFor(()=>expect(ports.at(-1)!.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal)));expect(journal.revokeRead).not.toHaveBeenCalled();expect(screen.queryByText('Saved')).not.toBeInTheDocument()})

})


describe('write access removed while read access remains (#4348)',()=>{
 const readerLease={...emptyLease,can_edit:false}
 const writeRevoked={delivery:'stopped',draft:'retain',reason:'write_revoked'}
 // A queued fresh client must not leak into the next test when the host never asked for it.
 afterEach(()=>{m.createClient.mockReset()})
 describe('a person who loses Edit and has no private read',()=>{
  const refused=Error('native_private_read_refused')
  async function demoted(){
   mount();await ready();await acquire()
   const second=clientRig();second.lease.mockResolvedValue(readerLease);second.read.mockRejectedValue(refused);m.createClient.mockReturnValueOnce(second)
   act(()=>portOptions.at(-1)!.onChanged('unsaved-before-revoke','doc'))
   act(()=>stream?.('session_retired',writeRevoked))
   await waitFor(()=>expect(second.read).toHaveBeenCalledOnce())
   await waitFor(()=>expect(document.querySelectorAll('iframe')).toHaveLength(0))
   return second
  }
  it('removes the frame, seals the draft unoffered and stays on the metadata admission',async()=>{
   const second=await demoted()
   await waitFor(()=>expect(second.events).toHaveBeenCalledOnce())
   expect(second.close).not.toHaveBeenCalled()
   expect(screen.getByRole('alert')).toHaveTextContent('Reload the page to see the shared version')
   expect(screen.getByRole('status')).toHaveTextContent('Editor disconnected')
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
   expect(journal.revokeRead).not.toHaveBeenCalled()
   expect(second.save).not.toHaveBeenCalled();expect(second.leaseAction).not.toHaveBeenCalled()
  })
  it('then losing Read purges the draft and clears recovery and comparison material',async()=>{
   await demoted()
   await waitFor(()=>expect(stream).toBeDefined())
   act(()=>stream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'authority_changed'}))
   await waitFor(()=>expect(journal.revokeRead).toHaveBeenCalled())
   await waitFor(()=>expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument())
   expect(screen.queryByRole('button',{name:'Compare'})).not.toBeInTheDocument()
   expect(screen.queryByLabelText('Local draft')).not.toBeInTheDocument()
   expect(screen.getByRole('status')).toHaveTextContent('Read access was removed')
   expect(screen.queryByText(/stay as a local draft/)).not.toBeInTheDocument()
  })
  it('a retired metadata admission is replaced, so a later read loss still purges',async()=>{
   const second=await demoted()
   await waitFor(()=>expect(second.events).toHaveBeenCalledOnce())
   const third=clientRig();third.lease.mockResolvedValue(readerLease);third.read.mockRejectedValue(refused);m.createClient.mockReturnValueOnce(third)
   act(()=>stream?.('session_retired',writeRevoked))
   await waitFor(()=>expect(third.events).toHaveBeenCalledOnce())
   expect(second.close).toHaveBeenCalled()
   act(()=>stream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'authority_changed'}))
   await waitFor(()=>expect(journal.revokeRead).toHaveBeenCalled())
   await waitFor(()=>expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument())
  })
  const recovered=()=>({draft:{applicationId:'wordcraft',enginePin:'a'.repeat(64),sessionId:'old',generation:'1',baseVersion:'1',editorRevision:'old',bytes:new TextEncoder().encode('{"body":["draft"]}')},actions:['compare','copy']})
  const proposal={id:'ncp_private',author:{actor_id:'agent:a',display_name:'Agent',kind:'agent'},authorName:'Agent',baseVersion:'1',summary:'private summary',state:'open',createdAt:'2026-11-01T00:00:00Z'}
  const privateBytes=new TextEncoder().encode('{"body":["private-latest"]}')
  function revoke(){
   const second=clientRig();second.lease.mockResolvedValue(readerLease);second.read.mockRejectedValue(refused);m.createClient.mockReturnValueOnce(second)
   act(()=>stream?.('session_retired',writeRevoked))
   return second
  }
  async function settled(second:ReturnType<typeof clientRig>){
   await waitFor(()=>expect(second.events).toHaveBeenCalledOnce())
   await waitFor(()=>expect(document.querySelectorAll('iframe')).toHaveLength(0))
  }
  it('an open comparison leaves the page and its state when private read is refused',async()=>{
   journal.recovery.mockResolvedValueOnce([recovered()])
   mount();await ready();await acquire()
   client.read.mockResolvedValueOnce({version:'1',bytes:privateBytes})
   fireEvent.click(await screen.findByRole('button',{name:'Compare'}))
   await screen.findByLabelText('Server version')
   await settled(revoke())
   expect(screen.queryByLabelText('Server version')).not.toBeInTheDocument()
   expect(screen.queryByLabelText('Local draft')).not.toBeInTheDocument()
   expect(document.body.textContent).not.toContain('private-latest')
  })
  it('a comparison still pending at the refusal is never shown, and reports nothing',async()=>{
   journal.recovery.mockResolvedValueOnce([recovered()])
   mount();await ready();await acquire()
   const pending=deferred<unknown>();client.read.mockReturnValueOnce(pending.promise)
   fireEvent.click(await screen.findByRole('button',{name:'Compare'}))
   await settled(revoke())
   await act(async()=>{pending.resolve({version:'1',bytes:privateBytes});await new Promise(r=>setTimeout(r,20))})
   expect(screen.queryByLabelText('Server version')).not.toBeInTheDocument()
   expect(document.body.textContent).not.toContain('private-latest')
   expect(screen.getByRole('alert')).toHaveTextContent('Reload the page to see the shared version')
  })
  it('a pending proposal comparison is never shown and proposals do not survive a regained editor',async()=>{
   client.proposals.mockResolvedValue([proposal])
   mount();await ready();await acquire()
   const pending=deferred<Uint8Array>();client.proposalBytes.mockReturnValueOnce(pending.promise)
   fireEvent.click(await screen.findByRole('button',{name:'Compare'}))
   const second=revoke();await settled(second)
   await act(async()=>{pending.resolve(privateBytes);await new Promise(r=>setTimeout(r,20))})
   expect(screen.queryByLabelText('Server version')).not.toBeInTheDocument()
   // Edit returns: only what the fresh admission loads may appear.
   const third=clientRig();third.lease.mockResolvedValue(emptyLease);m.createClient.mockReturnValueOnce(third)
   act(()=>stream?.('session_retired',{delivery:'stopped',draft:'retain',reason:'authority_changed'}))
   await waitFor(()=>expect(third.read).toHaveBeenCalledOnce(),{timeout:7000})
   await waitFor(()=>expect(document.querySelectorAll('iframe')).toHaveLength(1))
   expect(screen.queryByText(/private summary/)).not.toBeInTheDocument()
  },10000)
  it('a retained draft offers no comparison and asks the server for nothing without private read',async()=>{
   journal.recovery.mockResolvedValueOnce([recovered()])
   mount();await ready();await acquire()
   const second=revoke();await settled(second)
   const reads=second.read.mock.calls.length
   expect(screen.queryByRole('button',{name:'Compare'})).not.toBeInTheDocument()
   expect(second.read).toHaveBeenCalledTimes(reads)
  })
  it('a metadata stream ending without a read loss is reopened, so the watch continues',async()=>{
   const second=await demoted()
   await waitFor(()=>expect(second.events).toHaveBeenCalledOnce())
   const third=clientRig();third.lease.mockResolvedValue(readerLease);third.read.mockRejectedValue(refused);m.createClient.mockReturnValueOnce(third)
   act(()=>stream?.('session_retired',{delivery:'stopped',draft:'retain',reason:'authority_changed'}))
   await waitFor(()=>expect(third.events).toHaveBeenCalledOnce(),{timeout:7000})
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
   act(()=>stream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'authority_changed'}))
   await waitFor(()=>expect(journal.revokeRead).toHaveBeenCalled())
  },10000)
  it('withdraws the held draft copy synchronously when the sign-in ends',async()=>{
   const second=await demoted()
   await waitFor(()=>expect(second.events).toHaveBeenCalledOnce())
   await act(async()=>authSuspend!())
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
  })
  it('keeps a refused uncaptured buffer hidden and still receives the read-loss purge',async()=>{
   mount();await ready();await acquire();const frame=document.querySelector('iframe')
   journal.persist.mockRejectedValue(Error('storage unavailable'))
   const second=clientRig();second.lease.mockResolvedValue(readerLease);second.read.mockRejectedValue(refused);m.createClient.mockReturnValueOnce(second)
   act(()=>{portOptions.at(-1)!.onChanged('uncaptured','doc');stream?.('session_retired',writeRevoked)})
   await waitFor(()=>expect(second.read).toHaveBeenCalledOnce());await waitFor(()=>expect(second.lease).toHaveBeenCalled())
   expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveStyle({visibility:'hidden'});expect(second.close).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
   act(()=>stream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'authority_changed'}))
   await waitFor(()=>expect(document.querySelector('iframe')).toBeNull());expect(journal.revokeRead).toHaveBeenCalled()
  })
  it('opens a successor when the metadata stream fails during a stalled reconnect read',async()=>{
   const second=await demoted()
   await waitFor(()=>expect(second.events).toHaveBeenCalledOnce())
   const stalled=clientRig();stalled.lease.mockResolvedValue(readerLease);stalled.read.mockReturnValue(new Promise(()=>{}));m.createClient.mockReturnValueOnce(stalled)
   act(()=>stream?.('session_retired',{delivery:'stopped',draft:'retain',reason:'authority_changed'}))
   await waitFor(()=>expect(stalled.read).toHaveBeenCalledOnce(),{timeout:7000})
   const third=clientRig();third.lease.mockResolvedValue(readerLease);third.read.mockRejectedValue(refused);m.createClient.mockReturnValueOnce(third)
   act(()=>stream?.('session_retired',{delivery:'stopped',draft:'retain',reason:'authority_changed'}))
   await waitFor(()=>expect(third.read).toHaveBeenCalledOnce(),{timeout:12000})
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
  },22000)
  it('checks read again after a lost stream and offers the confirmed draft without a reload',async()=>{
   const monitor=deferred<void>();client.events.mockImplementation((_signal,callback)=>{stream=callback;return monitor.promise})
   mount();await ready();await acquire()
   act(()=>portOptions.at(-1)!.onChanged('unsaved-before-drop','doc'))
   const second=clientRig();second.lease.mockResolvedValue(readerLease);m.createClient.mockReturnValueOnce(second)
   await act(async()=>{monitor.reject(Error('stream lost'));await Promise.resolve()})
   await waitFor(()=>expect(document.querySelectorAll('iframe')).toHaveLength(0))
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
   await waitFor(()=>expect(second.read).toHaveBeenCalledOnce(),{timeout:7000})
   expect(await screen.findByRole('button',{name:'Keep a copy'})).toBeInTheDocument()
   expect(screen.getByRole('alert')).toHaveTextContent('The connection is back')
  },10000)
  it('offers nothing when the read is refused after a lost stream, because the purge may never arrive',async()=>{
   const monitor=deferred<void>();client.events.mockImplementation((_signal,callback)=>{stream=callback;return monitor.promise})
   mount();await ready();await acquire()
   act(()=>portOptions.at(-1)!.onChanged('unsaved-before-drop','doc'))
   const second=clientRig();second.lease.mockResolvedValue(readerLease);second.read.mockRejectedValue(refused);m.createClient.mockReturnValueOnce(second)
   await act(async()=>{monitor.reject(Error('stream lost'));await Promise.resolve()})
   await waitFor(()=>expect(second.read).toHaveBeenCalledOnce(),{timeout:7000})
   await waitFor(()=>expect(screen.getByRole('alert')).toHaveTextContent('Reload the page to see the shared version'))
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
   expect(journal.revokeRead).not.toHaveBeenCalled()
  },10000)
  it('a refused monitor offers nothing before or after',async()=>{
   const second=await demoted()
   await waitFor(()=>expect(second.events).toHaveBeenCalledOnce())
   const third=clientRig();third.open.mockRejectedValue(Error('native_session_forbidden'));m.createClient.mockReturnValueOnce(third)
   act(()=>stream?.('session_retired',{delivery:'stopped',draft:'retain',reason:'authority_changed'}))
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
   await waitFor(()=>expect(third.open).toHaveBeenCalledOnce(),{timeout:7000})
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
   expect(journal.revokeRead).not.toHaveBeenCalled()
  },10000)
  it('offers nothing while a repeated write revocation replaces the admission',async()=>{
   const second=await demoted()
   await waitFor(()=>expect(second.events).toHaveBeenCalledOnce())
   const third=clientRig(),opening=deferred<{sessionId:string;applicationId:string}>();third.open.mockReturnValue(opening.promise);m.createClient.mockReturnValueOnce(third)
   act(()=>stream?.('session_retired',writeRevoked))
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
   await waitFor(()=>expect(third.open).toHaveBeenCalledOnce())
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
  })
  it('sign-out still purges after a generic failure ended content delivery',async()=>{
   mount();await ready();await acquire()
   const second=clientRig();second.open.mockRejectedValue(Error('native_session_unavailable'));m.createClient.mockReturnValueOnce(second)
   act(()=>portOptions.at(-1)!.onChanged('unsaved-before-revoke','doc'))
   act(()=>stream?.('session_retired',writeRevoked))
   await screen.findByText(/Editor disconnected/)
   // Read is unconfirmed here, so the draft stays stored but is not offered.
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
   expect(journal.revokeRead).not.toHaveBeenCalled()
   await act(async()=>{await privacyRetire!()})
   expect(journal.revokeRead).toHaveBeenCalled()
   expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
  })
 })
 it('reopens read-only under a fresh read admission and keeps the unsaved draft for copy or compare',async()=>{
  mount();await ready();await acquire()
  const first=client,second=clientRig(),writer=ports.length;second.lease.mockResolvedValue(readerLease);m.createClient.mockReturnValueOnce(second)
  act(()=>portOptions.at(-1)!.onChanged('unsaved-before-revoke','doc'))
  act(()=>stream?.('session_retired',writeRevoked))
  await waitFor(()=>expect(second.open).toHaveBeenCalledOnce())
  await waitFor(()=>expect(ports.length).toBe(writer+1))
  const reader=ports.at(-1)!
  await waitFor(()=>expect(reader.bootstrap).toHaveBeenCalledOnce())
  await waitFor(()=>expect(second.events).toHaveBeenCalledOnce())
  // The old write admission is closed; it never saves again or reuses its generation.
  expect(first.close).toHaveBeenCalled();expect(second.save).not.toHaveBeenCalled();expect(second.leaseAction).not.toHaveBeenCalled()
  expect(reader.setReadOnly.mock.calls.some(c=>c[0]===false)).toBe(false)
  expect(ports[writer-1].close).toHaveBeenCalled()
  expect(document.querySelectorAll('iframe')).toHaveLength(1)
  expect(screen.getByRole('status')).toHaveTextContent('Read-only')
  expect(screen.getByRole('alert')).toHaveTextContent('Edit access was removed')
  expect(screen.queryByText(/Editor disconnected/)).not.toBeInTheDocument()
  expect(screen.queryByRole('button',{name:'Take over editing'})).not.toBeInTheDocument()
  expect(screen.getByRole('button',{name:'Keep a copy'})).toBeInTheDocument();expect(screen.getByRole('button',{name:'Compare'})).toBeInTheDocument()
  expect(journal.revokeRead).not.toHaveBeenCalled()
  // The fresh read view stays subject to read authority: losing read stops delivery and never reconnects.
  act(()=>stream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'authority_changed'}))
  await waitFor(()=>expect(document.querySelector('iframe')).toBeNull())
  expect(screen.getByRole('status')).toHaveTextContent('Read access was removed')
  expect(m.createClient).toHaveBeenCalledTimes(2);expect(second.open).toHaveBeenCalledOnce();expect(second.close).toHaveBeenCalled()
 })
 it('stays disconnected with the draft kept when the server refuses the fresh read admission',async()=>{
  mount();await ready();await acquire()
  const second=clientRig();second.open.mockRejectedValue(Error('native_session_unavailable'));m.createClient.mockReturnValueOnce(second)
  act(()=>stream?.('session_retired',writeRevoked))
  await screen.findByText('Editor disconnected · the captured local copy is kept')
  expect(document.querySelector('iframe')).toBeNull();expect(second.open).toHaveBeenCalledOnce();expect(second.read).not.toHaveBeenCalled();expect(m.createClient).toHaveBeenCalledTimes(2)
  expect(journal.revokeRead).not.toHaveBeenCalled()
 })
 it('erases the persisted draft row on read revoke after write revoke closed its journal',async()=>{
  // Storage-backed rig: a closed handle refuses everything, exactly like the
  // real journal's closed-mount fence, so only a real purge removes the row.
  const storage=new Map<string,unknown>();let closed=false
  journal.persist.mockImplementation(async(draft:unknown)=>{if(closed)throw Error('journal_closed');storage.set('native:mount-1',draft)})
  journal.confirm.mockImplementation(async()=>{if(closed)throw Error('journal_closed');storage.delete('native:mount-1')})
  journal.close.mockImplementation(async()=>{closed=true})
  journal.revokeRead.mockImplementation(async()=>{if(closed)throw Error('closed_journal_scope_purge_forbidden');storage.clear()})
  // Valid after close, and only for this mount's own row.
  journal.eraseOwnRow.mockImplementation(async()=>{storage.delete('native:mount-1')})
  storage.set('native:other-tab',{})
  mount();await ready();await acquire()
  const second=clientRig(),writer=ports.length;second.lease.mockResolvedValue(readerLease);m.createClient.mockReturnValueOnce(second)
  act(()=>portOptions.at(-1)!.onChanged('unsaved-before-revoke','doc'))
  act(()=>stream?.('session_retired',writeRevoked))
  await waitFor(()=>expect(ports.length).toBe(writer+1))
  await waitFor(()=>expect(second.events).toHaveBeenCalledOnce())
  expect(closed).toBe(true);expect(storage.has('native:mount-1')).toBe(true)
  act(()=>stream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'authority_changed'}))
  await waitFor(()=>expect(document.querySelector('iframe')).toBeNull())
  await waitFor(()=>expect(storage.has('native:mount-1')).toBe(false))
  expect(m.purge).not.toHaveBeenCalled();expect(storage.has('native:other-tab')).toBe(true)
  await waitFor(()=>expect(privacyRetire).toBeDefined());await expect(privacyRetire!()).resolves.toBeUndefined()
  expect(screen.queryByText('Deleting the local draft was not confirmed.')).not.toBeInTheDocument()
  expect(screen.getByRole('status')).toHaveTextContent('Read access was removed')
 })
 it('never lets a read from the replaced admission stop the fresh read-only view',async()=>{
  mount();await ready()
  let failOld!:(cause:unknown)=>void
  const first=client,oldRead=new Promise<never>((_,reject)=>{failOld=reject})
  first.read.mockReturnValueOnce(oldRead)
  act(()=>stream?.('lease',{...emptyLease}))
  await waitFor(()=>expect(first.read).toHaveBeenCalledTimes(2))
  const second=clientRig(),before=ports.length;second.lease.mockResolvedValue(readerLease);m.createClient.mockReturnValueOnce(second)
  act(()=>stream?.('session_retired',writeRevoked))
  await waitFor(()=>expect(ports.length).toBe(before+1))
  await waitFor(()=>expect(second.events).toHaveBeenCalledOnce())
  // The closed old client now fails its pending GET (ready() throws).
  await act(async()=>{failOld(Error('native_session_closed'));await Promise.resolve()})
  await act(async()=>{await new Promise(r=>setTimeout(r,0))})
  expect(screen.queryByText(/Editor disconnected/)).not.toBeInTheDocument()
  expect(second.close).not.toHaveBeenCalled()
  expect(document.querySelectorAll('iframe')).toHaveLength(1)
  // The fresh admission still refreshes on its own lease events.
  act(()=>stream?.('lease',{...readerLease}))
  await waitFor(()=>expect(second.read).toHaveBeenCalledTimes(2))
  expect(screen.queryByText(/Editor disconnected/)).not.toBeInTheDocument()
 })
 it('keeps disconnecting for any other retirement reason',async()=>{
  mount();await ready();await acquire()
  act(()=>stream?.('session_retired',{delivery:'stopped',draft:'retain',reason:'authority_changed'}))
  await screen.findByText('Editor disconnected · the captured local copy is kept')
  expect(m.createClient).toHaveBeenCalledOnce()
 })
})

describe('editor origin mismatch (#4649)',()=>{
 const violation=(over:Record<string,unknown>={})=>{const e=new Event('securitypolicyviolation') as Event&Record<string,unknown>;Object.assign(e,{effectiveDirective:'frame-src',violatedDirective:'frame-src',blockedURI:`https://native.example/b/${'b'.repeat(64)}/index.html`,originalPolicy:"default-src 'self'; frame-src https://studio.example.test",...over});return e}
 it('names the configured editor address at once and keeps the draft',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   m.createPort.mockImplementationOnce((o:NativeEditorPortOptions)=>{portOptions.push(o);const p=portRig();p.bootstrap.mockReturnValue(new Promise(()=>{}));ports.push(p);return p})
   mount()
   await waitFor(()=>expect(document.querySelector('iframe')).not.toBeNull())
   act(()=>{document.dispatchEvent(violation())})
   const alert=await screen.findByRole('alert')
   expect(alert).toHaveTextContent('The editor is only available at https://studio.example.test')
   const here=window.location
   expect(within(alert).getByRole('link')).toHaveAttribute('href','https://studio.example.test'+here.pathname+here.search+here.hash)
   expect(screen.queryByText(/The editor did not connect safely/)).toBeNull()
   expect(journal.revokeRead).not.toHaveBeenCalled();expect(m.purge).not.toHaveBeenCalled()
   expect(screen.getByText('Editor disconnected')).toBeInTheDocument()
  }finally{vi.useRealTimers()}
 })
 it('still explains the mismatch without a link when the policy names no origin',async()=>{
  m.createPort.mockImplementationOnce((o:NativeEditorPortOptions)=>{portOptions.push(o);const p=portRig();p.bootstrap.mockReturnValue(new Promise(()=>{}));ports.push(p);return p})
  mount();await waitFor(()=>expect(document.querySelector('iframe')).not.toBeNull())
  act(()=>{document.dispatchEvent(violation({originalPolicy:"default-src 'self'"}))})
  const alert=await screen.findByRole('alert');expect(alert).toHaveTextContent('configured editor address')
  expect(within(alert).queryByRole('link')).toBeNull()
 })
 it('drops the link when a different error follows the mismatch on a reconnect',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   const hang=(o:NativeEditorPortOptions)=>{portOptions.push(o);const p=portRig();p.bootstrap.mockReturnValue(new Promise(()=>{}));ports.push(p);return p}
   m.createPort.mockImplementationOnce(hang).mockImplementationOnce(hang)
   const view=mount();await waitFor(()=>expect(document.querySelector('iframe')).not.toBeNull())
   act(()=>{document.dispatchEvent(violation())})
   expect(within(await screen.findByRole('alert')).getByRole('link')).toBeInTheDocument()
   view.rerender(<NativeCraftEditorHost workspaceId="w" pageId="p2" humanId="human" enabled />)
   await waitFor(()=>expect(ports.length).toBe(2))
   await act(async()=>{await vi.advanceTimersByTimeAsync(20001)})
   await waitFor(()=>expect(screen.getByRole('alert')).toHaveTextContent('The editor did not connect safely'))
   expect(screen.getByRole('alert')).not.toHaveTextContent('Open the page there')
   expect(within(screen.getByRole('alert')).queryByRole('link')).toBeNull()
  }finally{vi.useRealTimers()}
 })
 it('ignores violations for other resources',async()=>{
  m.createPort.mockImplementationOnce((o:NativeEditorPortOptions)=>{portOptions.push(o);const p=portRig();p.bootstrap.mockReturnValue(new Promise(()=>{}));ports.push(p);return p})
  mount();await waitFor(()=>expect(document.querySelector('iframe')).not.toBeNull())
  act(()=>{document.dispatchEvent(violation({blockedURI:'https://elsewhere.test/x'}));document.dispatchEvent(violation({effectiveDirective:'img-src',violatedDirective:'img-src'}))})
  expect(screen.queryByRole('alert')).toBeNull()
 })
})

describe('native Word executable URL binding',()=>{
 it.each(['http://127.0.0.1:8083','http://localhost:8083','http://[::1]:8083'])('accepts same-origin literal loopback in production %s',async(host)=>{vi.stubEnv('NODE_ENV','production');try{const snapshot=await client.read(new AbortController().signal),path=`/_editor/b/${'b'.repeat(64)}/index.html`;for(const editorURL of [path,host+path]){const value=nativeCraftFrameURL({...snapshot,editorURL},host);expect(value).not.toBeNull();const url=new URL(value!);expect(url.origin).toBe(host);expect(url.pathname).toBe(path);expect(url.searchParams.get('parent_origin')).toBe(host);expect([...url.searchParams.keys()].sort()).toEqual(['host','parent_origin','readonly','save'])}}finally{vi.unstubAllEnvs()}})
 it.each([['http://127.0.0.1:8083','http://127.0.0.1:8084'],['http://127.0.0.1:8083','http://localhost:8083'],['https://crew.example','http://127.0.0.1:8083'],['http://crew.example','http://crew.example']])('rejects cross-origin or nonliteral loopback HTTP in production %s to %s',async(host,editor)=>{vi.stubEnv('NODE_ENV','production');try{const snapshot=await client.read(new AbortController().signal),path=editor===host?'/_editor/b/':'/b/';expect(nativeCraftFrameURL({...snapshot,editorURL:`${editor}${path}${'b'.repeat(64)}/index.html`},host)).toBeNull()}finally{vi.unstubAllEnvs()}})
 it('accepts only the native same-host prefix or exact dedicated pinned path with four noncredential queries',async()=>{const snapshot=await client.read(new AbortController().signal);const host='https://crew.example';expect(new URL(nativeCraftFrameURL({...snapshot,editorURL:`${host}/_editor/b/${'b'.repeat(64)}/index.html`},host)!)).toMatchObject({pathname:`/_editor/b/${'b'.repeat(64)}/index.html`});const url=new URL(nativeCraftFrameURL(snapshot,host)!);expect([...url.searchParams.keys()].sort()).toEqual(['host','parent_origin','readonly','save']);expect(url.searchParams.get('host')).toBe('port');expect(url.searchParams.get('readonly')).toBe('1')})
 it.each(['https://user:secret@native.example/b/H/index.html','https://native.example/b/H/index.html?','https://native.example/b/H/index.html#','https://native.example/b/H/index.html?token=secret','https://crew.example/workbook-editor/b/H/index.html','https://native.example/b/OTHER/index.html','http://native.example/b/H/index.html','//native.example/b/H/index.html'])('rejects alternative paths, credentials or preexisting runtime query %s',async(raw)=>{const snapshot=await client.read(new AbortController().signal);expect(nativeCraftFrameURL({...snapshot,editorURL:raw.replace('H','b'.repeat(64))},'https://crew.example')).toBeNull()})
})

it('preserves the exact writer, private journal and pending save during successful credential rotation',async()=>{
 vi.useFakeTimers({shouldAdvanceTime:true})
 try{
  const rotation=deferred<boolean>(),pendingSave=deferred<ReturnType<typeof receipt>>()
  client.reauthorize.mockReturnValue(rotation.promise);client.save.mockReturnValueOnce(pendingSave.promise)
  mount();await ready();await acquire();const frame=document.querySelector('iframe'),port=ports.at(-1)!,ownedJournal=journal
  await act(async()=>{await vi.advanceTimersByTimeAsync(29500)})
  act(()=>portOptions.at(-1)!.onChanged('pending-before-cookie-rotation','doc'))
  await waitFor(()=>expect(client.save).toHaveBeenCalledOnce());expect(ownedJournal.persist).toHaveBeenCalledOnce()
  const captured=client.save.mock.calls[0][0]
  await act(async()=>{await vi.advanceTimersByTimeAsync(1000)})
  expect(client.reauthorize).toHaveBeenCalledOnce();expect(document.querySelector('iframe')).toBe(frame)
  expect(client.close).not.toHaveBeenCalled();expect(port.close).not.toHaveBeenCalled();expect(ownedJournal.close).not.toHaveBeenCalled();expect(ownedJournal.revokeRead).not.toHaveBeenCalled()
  expect(screen.queryByText('Saved')).toBeNull()
  await act(async()=>rotation.resolve(true))
  expect(document.querySelector('iframe')).toBe(frame);expect(client.save).toHaveBeenCalledOnce();expect(client.leaseAction.mock.calls.filter(call=>call[0]==='acquire')).toHaveLength(1)
  await act(async()=>pendingSave.resolve(receipt(captured)));await screen.findByText('Saved')
  expect(client.save).toHaveBeenCalledOnce();expect(ownedJournal.revokeRead).not.toHaveBeenCalled()
 }finally{vi.useRealTimers()}
})

it('preserves the exact writer, private journal and pending save during successful credential rotation',async()=>{
 vi.useFakeTimers({shouldAdvanceTime:true})
 try{
  const rotation=deferred<boolean>(),pendingSave=deferred<ReturnType<typeof receipt>>()
  client.reauthorize.mockReturnValue(rotation.promise);client.save.mockReturnValueOnce(pendingSave.promise)
  mount();await ready();await acquire();const frame=document.querySelector('iframe'),port=ports.at(-1)!,ownedJournal=journal
  await act(async()=>{await vi.advanceTimersByTimeAsync(29500)})
  act(()=>portOptions.at(-1)!.onChanged('pending-before-cookie-rotation','doc'))
  await waitFor(()=>expect(client.save).toHaveBeenCalledOnce());expect(ownedJournal.persist).toHaveBeenCalledOnce()
  const captured=client.save.mock.calls[0][0]
  await act(async()=>{await vi.advanceTimersByTimeAsync(1000)})
  expect(client.reauthorize).toHaveBeenCalledOnce();expect(document.querySelector('iframe')).toBe(frame)
  expect(client.close).not.toHaveBeenCalled();expect(port.close).not.toHaveBeenCalled();expect(ownedJournal.close).not.toHaveBeenCalled();expect(ownedJournal.revokeRead).not.toHaveBeenCalled()
  expect(screen.queryByText('Saved')).toBeNull()
  await act(async()=>rotation.resolve(true))
  expect(document.querySelector('iframe')).toBe(frame);expect(client.save).toHaveBeenCalledOnce();expect(client.leaseAction.mock.calls.filter(call=>call[0]==='acquire')).toHaveLength(1)
  await act(async()=>pendingSave.resolve(receipt(captured)));await screen.findByText('Saved')
  expect(client.save).toHaveBeenCalledOnce();expect(ownedJournal.revokeRead).not.toHaveBeenCalled()
 }finally{vi.useRealTimers()}
})

describe('agent proposals while a person holds the editor (#4480)',()=>{
 const proposal={id:'ncp_0123456789abcdef01234567',authorName:'Jamie',baseVersion:'1',summary:'Nadpis',createdAt:'2026-10-29T17:00:00Z',state:'open' as const}
 it('accepts only as the current holder after saving its own edits, then reopens from the server',async()=>{
  client.proposals.mockResolvedValue([proposal])
  mount();await ready()
  await screen.findByText('Jamie prepared a proposal on version 1')
  expect(screen.queryByRole('button',{name:'Accept'})).not.toBeInTheDocument()
  await acquire()
  client.decideProposal.mockResolvedValue({...proposal,state:'accepted',resultVersion:'2'})
  act(()=>portOptions.at(-1)!.onChanged('human-edit','doc'))
  fireEvent.click(await screen.findByRole('button',{name:'Accept'}))
  await waitFor(()=>expect(client.decideProposal).toHaveBeenCalledWith(proposal.id,'accept','9007199254740995',expect.any(AbortSignal)))
  expect(client.save.mock.invocationCallOrder[0]).toBeLessThan(client.decideProposal.mock.invocationCallOrder[0])
  await waitFor(()=>expect(client.read.mock.calls.length).toBeGreaterThanOrEqual(3))
 })
 // #4699: the rig's engine exports normalized bytes ({"body":[1]}) for the loaded
 // {"body":[]}. Untouched (same engine revision as at bootstrap), that export is
 // the loaded version, not a new one, so accepting decides the proposal over
 // version 1 instead of making it stale.
 it('accepts over the loaded version without saving an untouched normalized document first (#4699)',async()=>{
  rigNativeState='revision:opened'
  client.proposals.mockResolvedValue([proposal])
  mount();await ready();await acquire()
  client.decideProposal.mockResolvedValue({...proposal,state:'accepted',resultVersion:'2'})
  fireEvent.click(await screen.findByRole('button',{name:'Accept'}))
  await waitFor(()=>expect(client.decideProposal).toHaveBeenCalledWith(proposal.id,'accept','9007199254740995',expect.any(AbortSignal)))
  expect(client.save).not.toHaveBeenCalled()
 })
 it('releases an untouched normalized document without writing a version (#4699)',async()=>{
  rigNativeState='revision:opened'
  mount();await ready();await acquire()
  fireEvent.click(screen.getByRole('button',{name:'Hand back'}))
  await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='release')).toBe(true))
  expect(client.save).not.toHaveBeenCalled()
 })
 it('still saves a Word change whose changed event has not arrived when releasing (#4699)',async()=>{
  rigNativeState='revision:opened'
  mount();await ready();await acquire()
  ports.at(-1)!.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"body":[2]}'),editorRevision:'snapshot-2',documentId:'doc',nativeState:'revision:typed'})
  fireEvent.click(screen.getByRole('button',{name:'Hand back'}))
  await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='release')).toBe(true))
  expect(client.save).toHaveBeenCalledOnce()
 })
 it('reports a stale proposal without changing the document',async()=>{
  client.proposals.mockResolvedValue([proposal])
  mount();await ready();await acquire()
  client.decideProposal.mockResolvedValue({...proposal,state:'stale'})
  fireEvent.click(await screen.findByRole('button',{name:'Accept'}))
  await screen.findByText(/The proposal was not applied/)
  expect(document.querySelector('iframe')).not.toBeNull()
 })
 it('rejects without a lease and compares the candidate read-only',async()=>{
  client.proposals.mockResolvedValue([proposal])
  client.proposalBytes.mockResolvedValue(new TextEncoder().encode('{"body":["agent"]}'))
  client.decideProposal.mockResolvedValue({...proposal,state:'rejected'})
  mount();await ready()
  fireEvent.click(await screen.findByRole('button',{name:'Compare'}))
  expect(await screen.findByLabelText('Proposal by Jamie on version 1')).toHaveValue('{"body":["agent"]}')
  fireEvent.click(screen.getByRole('button',{name:'Reject'}))
  await waitFor(()=>expect(client.decideProposal).toHaveBeenCalledWith(proposal.id,'reject',undefined,expect.any(AbortSignal)))
  expect(client.leaseAction).not.toHaveBeenCalled()
 })
})

describe('full native history',()=>{
 it('stops saving, keeps every version and the draft, and offers the draft as a file',async()=>{
  mount();await ready();await acquire()
  client.save.mockRejectedValueOnce(new NativeSaveError('quota'))
  act(()=>portOptions.at(-1)!.onChanged('edit-at-full-quota','doc'))
  act(()=>portOptions.at(-1)!.onSaveRequest({documentId:'doc',editorRevision:'edit-at-full-quota'}))
  expect(await screen.findByText(/Storage is full, so nothing more can be saved/)).toBeInTheDocument()
  const click=vi.fn();const create=vi.spyOn(document,'createElement')
  vi.stubGlobal('URL',Object.assign(URL,{createObjectURL:vi.fn(()=>'blob:x'),revokeObjectURL:vi.fn()}))
  create.mockImplementationOnce(()=>Object.assign(document.createElementNS('http://www.w3.org/1999/xhtml','a') as HTMLAnchorElement,{click}))
  fireEvent.click(screen.getByRole('button',{name:'Download draft'}))
  await waitFor(()=>expect(click).toHaveBeenCalledOnce())
  expect(journal.revokeRead).not.toHaveBeenCalled();expect(document.querySelector('iframe')).not.toBeNull()
 })
 it.each([['near','Version history is nearing its limit'],['critical','Version history is almost full'],['full','Version history is full']] as const)('tells a %s history apart in the status line',async(level,text)=>{
  client.read.mockResolvedValue({version:"1",bytes:new TextEncoder().encode('{"body":[]}'),applicationId:"wordcraft",enginePin:"a".repeat(64),editorBundle:"b".repeat(64),editorURL:`https://native.example/b/${"b".repeat(64)}/index.html`,quota:{usedBytes:"96",limitBytes:"100",warning:true,level,retention:"keep-all"}})
  mount();await ready()
  expect(await screen.findByText(text)).toBeInTheDocument()
  // #5132: the raw byte budget is data for diagnostics, never printed.
  expect(screen.queryByText(/96 \/ 100 B/)).toBeNull()
  expect(screen.getAllByRole('status').find(node=>node.dataset.quotaUsed)?.dataset).toMatchObject({quotaUsed:'96',quotaLimit:'100',quotaLevel:level})
 })
 // #5124: the session used to stay blocked after one refusal, so the work
 // typed meanwhile could only be downloaded, never saved.
 it('keeps the changes and saves them with "Save again" once space is free',async()=>{
  mount();await ready();await acquire()
  client.save.mockRejectedValueOnce(new NativeSaveError('quota')).mockRejectedValueOnce(new NativeSaveError('quota'))
  act(()=>portOptions.at(-1)!.onChanged('edit-at-full-quota','doc'))
  expect(await screen.findByText(/Your changes are not saved on the server yet/)).toBeInTheDocument()
  expect(client.save).toHaveBeenCalledOnce()
  // Still full: refused again, nothing is lost and the alert stays.
  fireEvent.click(screen.getByRole('button',{name:'Save again'}))
  await waitFor(()=>expect(client.save).toHaveBeenCalledTimes(2))
  expect(await screen.findByRole('button',{name:'Save again'})).toBeInTheDocument()
  expect(journal.confirm).not.toHaveBeenCalled()
  // Space freed: the next press commits the draft as a new version.
  fireEvent.click(screen.getByRole('button',{name:'Save again'}))
  await waitFor(()=>expect(client.save).toHaveBeenCalledTimes(3))
  expect(client.save.mock.calls[2][0]).toMatchObject({baseVersion:'1'})
  expect(await screen.findByText('Saved')).toBeInTheDocument()
  expect(screen.queryByRole('button',{name:'Save again'})).toBeNull()
  expect(journal.revokeRead).not.toHaveBeenCalled()
 })
})

describe('unchanged-content transfer (G4 B1/B2)',()=>{
 const baseBytes=()=>new TextEncoder().encode('{"body":[]}')
 it('B1: releases custody when the editor content is identical to the loaded version',async()=>{
  mount();await ready();await acquire()
  ports.at(-1)!.snapshot.mockResolvedValue({bytes:baseBytes(),editorRevision:'same',documentId:'doc'})
  fireEvent.click(screen.getByRole('button',{name:'Hand back'}))
  await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='release')).toBe(true))
  expect(client.save).not.toHaveBeenCalled()
 })
 it('B1: publishes the loaded version when nothing changed',async()=>{
  mount();await ready();await acquire()
  ports.at(-1)!.snapshot.mockResolvedValue({bytes:baseBytes(),editorRevision:'same',documentId:'doc'})
  fireEvent.click(screen.getByRole('button',{name:'Publish preview'}))
  await waitFor(()=>expect(client.share).toHaveBeenCalledOnce())
  expect(client.share.mock.calls[0][0]).toMatchObject({baseVersion:'1',targetVersion:'1'})
  expect(client.save).not.toHaveBeenCalled()
 })
 it('B2: releases after an already confirmed autosave when the bytes are unchanged',async()=>{
  mount();await ready();await acquire();const p=ports.at(-1)!
  act(()=>portOptions.at(-1)!.onChanged('edit','doc'))
  await screen.findByText('Saved');expect(client.save).toHaveBeenCalledOnce()
  fireEvent.click(screen.getByRole('button',{name:'Hand back'}))
  await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='release')).toBe(true))
  expect(client.save).toHaveBeenCalledOnce();expect(p.ack).toHaveBeenCalledOnce()
 })
 it('B2: publishes the confirmed version after an autosave when the bytes are unchanged',async()=>{
  mount();await ready();await acquire()
  act(()=>portOptions.at(-1)!.onChanged('edit','doc'))
  await screen.findByText('Saved')
  fireEvent.click(screen.getByRole('button',{name:'Publish preview'}))
  await waitFor(()=>expect(client.share).toHaveBeenCalledOnce())
  expect(client.share.mock.calls[0][0]).toMatchObject({baseVersion:'2',targetVersion:'2'})
  expect(client.save).toHaveBeenCalledOnce()
 })
 it('B1: hands off custody on unchanged content without saving',async()=>{
  mount();await ready();await acquire()
  ports.at(-1)!.snapshot.mockResolvedValue({bytes:baseBytes(),editorRevision:'same',documentId:'doc'})
  client.leaseAction.mockImplementationOnce(async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:{actor_id:'user:other',display_name:'Jana',kind:'human'},generation:'9007199254740996'});return emptyLease})
  act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}))
  fireEvent.click(await screen.findByRole('button',{name:'Hand over to Jana'}))
  await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(true))
  expect(client.save).not.toHaveBeenCalled()
 })
 it('B1: accepts an agent proposal on unchanged content without saving first',async()=>{
  const proposal={id:'ncp_0123456789abcdef01234567',authorName:'Jamie',baseVersion:'1',summary:'Nadpis',createdAt:'2026-10-29T17:00:00Z',state:'open' as const}
  client.proposals.mockResolvedValue([proposal])
  mount();await ready();await screen.findByText('Jamie prepared a proposal on version 1');await acquire()
  ports.at(-1)!.snapshot.mockResolvedValue({bytes:baseBytes(),editorRevision:'same',documentId:'doc'})
  client.decideProposal.mockResolvedValue({...proposal,state:'accepted',resultVersion:'2'})
  fireEvent.click(await screen.findByRole('button',{name:'Accept'}))
  await waitFor(()=>expect(client.decideProposal).toHaveBeenCalledWith(proposal.id,'accept','9007199254740995',expect.any(AbortSignal)))
  expect(client.save).not.toHaveBeenCalled()
 })
 it('B20: a recovered draft with the server bytes is retired from storage, not just the current row',async()=>{
  const same={applicationId:'wordcraft',enginePin:'a'.repeat(64),sessionId:'old',generation:'1',baseVersion:'0',editorRevision:'lost-ack',bytes:baseBytes()}
  journal.recovery.mockResolvedValueOnce([{draft:same,actions:['compare','copy']}])
  mount();await ready();await acquire()
  await waitFor(()=>expect(journal.discardRecovered).toHaveBeenCalledWith(expect.objectContaining({draft:same})))
 })
})

describe('write and read revoked together while a lost save is reconciled',()=>{
 // The native client never reports read_revoked (a refused bearer maps to write_revoked);
 // read loss reaches the host as session_retired draft:purge. It purges the journal the
 // coordinator still holds while proveLost waits on reconcile.
 it('a read-loss event during write retirement purges the captured draft through the coordinator',async()=>{
  mount();await ready();await acquire()
  client.save.mockImplementationOnce(()=>new Promise(()=>{}))
  const reconciling=deferred<unknown>();client.reconcile.mockImplementationOnce(()=>reconciling.promise)
  act(()=>portOptions.at(-1)!.onChanged('pending','doc'))
  await waitFor(()=>expect(client.save).toHaveBeenCalledOnce())
  act(()=>stream?.('lease',{...ownedLease,mine:false,can_write:false}))
  await waitFor(()=>expect(client.reconcile).toHaveBeenCalledOnce())
  expect(journal.persist).toHaveBeenCalledTimes(2)
  act(()=>stream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'authority_changed'}))
  await waitFor(()=>expect(journal.revokeRead).toHaveBeenCalledOnce())
  expect(document.querySelector('iframe')).toBeNull()
  expect(journal.close.mock.invocationCallOrder.every(order=>order>journal.revokeRead.mock.invocationCallOrder[0])).toBe(true)
  await act(async()=>{reconciling.resolve(Promise.reject(new NativeSaveError('write_revoked')));await Promise.resolve()})
  expect(journal.revokeRead).toHaveBeenCalledOnce();expect(client.save).toHaveBeenCalledOnce()
  expect(screen.queryByRole('button',{name:'Keep a copy'})).not.toBeInTheDocument()
 })
})

// The server renews a human holder's lease while the event stream is open. A
// stalled renderer can read those renewals late, so a lapsed cached expiry asks
// the server once before write is retired (#4801).
describe('native host lease expiry re-verification',()=>{
 const soon=()=>({...ownedLease,expires_at:new Date(Date.now()+15_000).toISOString()})
 for(const server of ['renewed','lost'] as const)it(`${server==='renewed'?'keeps':'retires'} write when the cached expiry lapses and the server ${server==='renewed'?'still confirms':'no longer confirms'} custody`,async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   client.leaseAction.mockImplementation(async(action:string)=>{if(action==='acquire')client.lease.mockImplementation(async()=>soon());return soon()})
   mount();await ready();await acquire()
   const p=ports.at(-1)!,checks=client.lease.mock.calls.length
   if(server==='lost')client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:{actor_id:'user:other',display_name:'Jana',kind:'human'},generation:'9007199254740996'})
   await act(async()=>{await vi.advanceTimersByTimeAsync(16_000)})
   await waitFor(()=>expect(client.lease.mock.calls.length).toBeGreaterThan(checks))
   if(server==='renewed'){await act(async()=>{await vi.advanceTimersByTimeAsync(2_000)});expect(p.setReadOnly).not.toHaveBeenCalledWith(true,expect.anything());expect(screen.getByRole('button',{name:'Hand back'})).toBeInTheDocument()}
   else await waitFor(()=>expect(p.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal)))
  }finally{vi.useRealTimers()}
 })
 it('retires write when the re-verification itself fails',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   client.leaseAction.mockImplementation(async(action:string)=>{if(action==='acquire')client.lease.mockImplementation(async()=>soon());return soon()})
   mount();await ready();await acquire()
   const p=ports.at(-1)!;client.lease.mockRejectedValue(Error('offline'))
   await act(async()=>{await vi.advanceTimersByTimeAsync(16_000)})
   await waitFor(()=>expect(p.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal)))
  }finally{vi.useRealTimers()}
 })
 it('retires write when the re-verification never answers',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   client.leaseAction.mockImplementation(async(action:string)=>{if(action==='acquire')client.lease.mockImplementation(async()=>soon());return soon()})
   mount();await ready();await acquire()
   const p=ports.at(-1)!;client.lease.mockImplementation(()=>new Promise(()=>{}))
   await act(async()=>{await vi.advanceTimersByTimeAsync(16_000)})
   expect(p.setReadOnly).not.toHaveBeenCalledWith(true,expect.anything())
   await act(async()=>{await vi.advanceTimersByTimeAsync(6_000)})
   await waitFor(()=>expect(p.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal)))
  }finally{vi.useRealTimers()}
 })
 it('retires write when the re-verification reply is malformed',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   client.leaseAction.mockImplementation(async(action:string)=>{if(action==='acquire')client.lease.mockImplementation(async()=>soon());return soon()})
   mount();await ready();await acquire()
   const p=ports.at(-1)!;client.lease.mockResolvedValue({mine:'yes'})
   await act(async()=>{await vi.advanceTimersByTimeAsync(16_000)})
   await waitFor(()=>expect(p.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal)))
  }finally{vi.useRealTimers()}
 })
 // A renewal event queued during a main-thread stall arrives with an expiry that
 // is already past although the holder and generation are unchanged (#4863).
 // Only the server recheck may retire write for an expiry-only lapse.
 const stale=()=>({...ownedLease,expires_at:new Date(Date.now()-1_000).toISOString()})
 it('keeps write when a late lease event of the same holder arrives already expired and the server still confirms custody',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   client.leaseAction.mockImplementation(async(action:string)=>{if(action==='acquire')client.lease.mockImplementation(async()=>soon());return soon()})
   mount();await ready();await acquire()
   const p=ports.at(-1)!,reads=client.read.mock.calls.length,checks=client.lease.mock.calls.length
   await act(async()=>{stream?.('lease',stale())})
   await act(async()=>{await vi.advanceTimersByTimeAsync(2_000)})
   expect(p.setReadOnly).not.toHaveBeenCalledWith(true,expect.anything())
   expect(client.lease.mock.calls.length).toBeGreaterThan(checks)
   expect(client.read.mock.calls.length).toBe(reads)
   expect(ports.at(-1)).toBe(p)
   expect(screen.queryByText(/Read-only/)).not.toBeInTheDocument()
   expect(screen.queryByRole('button',{name:'Take over editing'})).not.toBeInTheDocument()
   expect(screen.getByRole('button',{name:'Hand back'})).toBeInTheDocument()
  }finally{vi.useRealTimers()}
 })
 // Retirement starts synchronously with freezeInput (the frame turns inert);
 // the draft capture that precedes setReadOnly(true) then runs on promises
 // whose length depends on the runner. Measure the start in fake time and
 // wait for the capture separately, so the bound is exact on a slow runner.
 function frozenClock(){
  const frame=document.querySelector('iframe')!,set=frame.setAttribute.bind(frame),clock:{at?:number}={}
  vi.spyOn(frame,'setAttribute').mockImplementation((name:string,value:string)=>{if(name==='inert'&&clock.at===undefined)clock.at=Date.now();set(name,value)})
  return clock
 }
 it('retires write within the recheck bound when a late expired lease event arrives and the server no longer confirms custody',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   client.leaseAction.mockImplementation(async(action:string)=>{if(action==='acquire')client.lease.mockImplementation(async()=>soon());return soon()})
   mount();await ready();await acquire()
   const p=ports.at(-1)!,frozen=frozenClock(),checks=client.lease.mock.calls.length
   client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:{actor_id:'user:other',display_name:'Jana',kind:'human'},generation:'9007199254740996'})
   const sent=Date.now()
   await act(async()=>{stream?.('lease',stale())})
   expect(frozen.at).toBeUndefined()
   await act(async()=>{await vi.advanceTimersByTimeAsync(6_000)})
   expect(client.lease.mock.calls.length).toBeGreaterThan(checks)
   expect(frozen.at).toBeDefined();expect(frozen.at!-sent).toBeLessThanOrEqual(6_000)
   await waitFor(()=>expect(p.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal)))
  }finally{vi.useRealTimers()}
 })
 it('retires write when a late expired lease event never gets a server answer within the recheck bound',async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  try{
   client.leaseAction.mockImplementation(async(action:string)=>{if(action==='acquire')client.lease.mockImplementation(async()=>soon());return soon()})
   mount();await ready();await acquire()
   const p=ports.at(-1)!,frozen=frozenClock(),checks=client.lease.mock.calls.length
   client.lease.mockImplementation(()=>new Promise(()=>{}))
   const sent=Date.now()
   await act(async()=>{stream?.('lease',stale())})
   await act(async()=>{await vi.advanceTimersByTimeAsync(4_000)})
   // The server was asked and has not answered: write still waits for it.
   expect(client.lease.mock.calls.length).toBe(checks+1)
   expect(frozen.at).toBeUndefined();expect(p.setReadOnly).not.toHaveBeenCalledWith(true,expect.anything())
   await act(async()=>{await vi.advanceTimersByTimeAsync(3_000)})
   // One lease-watch tick to start the recheck plus RECHECK_MS, never later.
   expect(frozen.at).toBeDefined();expect(frozen.at!-sent).toBeGreaterThanOrEqual(5_000);expect(frozen.at!-sent).toBeLessThanOrEqual(7_000)
   await waitFor(()=>expect(p.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal)))
  }finally{vi.useRealTimers()}
 })
 for(const [name,loss] of [
  ['another holder',{mine:false,can_write:false,holder:{actor_id:'user:other',display_name:'Jana',kind:'human'},generation:'9007199254740996'}],
  ['write revoked',{can_write:false}],
  ['edit revoked',{can_edit:false}],
  ['another generation',{generation:'9007199254740996'}],
 ] as const)it(`retires write at once on a lease event reporting ${name}, without a server recheck`,async()=>{
  mount();await ready();await acquire()
  const p=ports.at(-1)!,checks=client.lease.mock.calls.length
  act(()=>stream?.('lease',{...ownedLease,...loss}))
  await waitFor(()=>expect(p.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal)),{timeout:500})
  expect(client.lease.mock.calls.slice(checks).length).toBe(0)
 })
})

describe('native agent start gating',()=>{
 const start=()=>screen.getByTestId('agent-start')
 const allowed=(value:boolean)=>waitFor(()=>expect(start()).toHaveAttribute('data-enabled',String(value)))
 afterEach(()=>window.history.replaceState(null,'','/'))
 it('allows a start on a free document for a person with write access',async()=>{mount();await ready();await allowed(true);expect(start()).toHaveAttribute('data-holder','null')})
 it('allows a start while a person holds the document and names the holder kind',async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:otherPerson});mount();await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled());await allowed(true);expect(start()).toHaveAttribute('data-holder','human')})
 it('allows no start on a read-only document',async()=>{client.lease.mockResolvedValue({...emptyLease,can_edit:false});mount();await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled());await screen.findByText('Read-only',{selector:'[role="status"] span'});expect(start()).toHaveAttribute('data-enabled','false')})
 it('withdraws the start once the connection is retired',async()=>{mount();await ready();await allowed(true);await act(async()=>stream?.('session_retired',{}));await allowed(false)})
 it('withdraws the start while a blocked host keeps an uncaptured draft',async()=>{mount();await ready();await acquire();await allowed(true);journal.persist.mockResolvedValueOnce(undefined).mockRejectedValueOnce(Error('disk full'));act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}));fireEvent.click(await screen.findByRole('button',{name:'Hand over to Jana'}));await screen.findByText('The draft cannot be captured safely. The handover did not happen.');await allowed(false)})
 it('withdraws the start until retiring the write has finished',async()=>{
  mount();await ready();await acquire();await allowed(true)
  const capture=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>();ports.at(-1)!.snapshot.mockReturnValue(capture.promise)
  await act(async()=>stream?.('lease',{...ownedLease,mine:false,can_write:false,holder:otherPerson}))
  await allowed(false)
  await act(async()=>capture.resolve({bytes:new TextEncoder().encode('{"body":[1]}'),editorRevision:'snapshot-1',documentId:'doc'}))
  await allowed(true)
 })
 it('follows a started task in the status component and keeps it across a reload',async()=>{
  m.apiFetch.mockImplementation(async()=>new Response(JSON.stringify({work_id:'work-7',state:'queued',agent_id:'agent',run_id:'',application:'wordcraft',uncertain:false,operation_inflight:false,events:[]})))
  mount();await ready();await allowed(true);fireEvent.click(start())
  expect(await screen.findByText('Agent task queued')).toBeInTheDocument()
  expect(new URLSearchParams(window.location.search).get('craft_task_id')).toBe('work-7')
 })
})

// #5381: an editor already open when an administrator turns Word or Sheet off.
describe('application turned off while the editor is open',()=>{
 afterEach(()=>{m.apiFetch.mockReset()})
 const WORD_OFF='An administrator turned Word off on this server. You can read this document; editing is not available.'
 const SHEET_OFF='An administrator turned Sheet off on this server. You can read this document; editing is not available.'
 it('a refused take-over says an administrator turned Word off and offers only reading',async()=>{
  const {NativeAppOffError}=await import('@/lib/mooring/native-app-off')
  mount();await ready()
  client.leaseAction.mockRejectedValueOnce(new NativeAppOffError('app_draining','wordcraft'))
  fireEvent.click(screen.getByRole('button',{name:'Take over editing'}))
  expect(await screen.findByText(WORD_OFF)).toBeInTheDocument()
  expect(screen.queryByText(/The result of the action was not confirmed/)).not.toBeInTheDocument()
  expect(screen.queryByRole('button',{name:'Take over editing'})).not.toBeInTheDocument()
  expect(within(screen.getByRole('toolbar',{name:'Document editing'})).getByText('Read-only')).toBeInTheDocument()
  expect(ports.at(-1)!.setReadOnly).not.toHaveBeenCalledWith(false,expect.anything())
 })
 it('a refused edit request on a Sheet says Sheet is off and stops offering it',async()=>{
  const {NativeAppOffError}=await import('@/lib/mooring/native-app-off')
  client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:otherPerson})
  mount();const ask=await screen.findByRole('button',{name:'Request edit'})
  await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled())
  client.leaseAction.mockRejectedValueOnce(new NativeAppOffError('app_disabled','gridcraft'))
  fireEvent.click(ask)
  expect(await screen.findByText(SHEET_OFF)).toBeInTheDocument()
  expect(screen.queryByRole('button',{name:'Request edit'})).not.toBeInTheDocument()
 })
 it('the holder keeps editing and saving through the drain when a hand-over is refused',async()=>{
  const {NativeAppOffError}=await import('@/lib/mooring/native-app-off')
  mount();await ready();await acquire();await screen.findByText('Ready to edit')
  act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}))
  // The server still renews and saves for this session while draining
  // (TestCraftNativeDrainKeepsOnlyTheHoldingSession); a hand-over is refused.
  m.apiFetch.mockImplementation(async(url:string)=>String(url).includes('/craft/availability')?new Response(JSON.stringify({enabled:true,can_edit:false,application:'wordcraft',edit_refusal:'app_draining',applications:{wordcraft:'draining',gridcraft:'open'}})):undefined)
  client.leaseAction.mockRejectedValueOnce(new NativeAppOffError('app_draining','wordcraft'))
  fireEvent.click(await screen.findByRole('button',{name:'Hand over to Jana'}))
  expect(await screen.findByText('An administrator turned Word off on this server, so editing cannot be handed over. You can keep editing; your changes keep saving until it closes.')).toBeInTheDocument()
  expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false)
  expect(ports.at(-1)!.setReadOnly).not.toHaveBeenCalledWith(true,expect.anything())
  await waitFor(()=>expect(screen.getByText('Ready to edit')).toBeInTheDocument())
  expect(screen.getByRole('button',{name:'Hand back'})).toBeInTheDocument()
  const saves=client.save.mock.calls.length
  ports.at(-1)!.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"body":[3]}'),editorRevision:'drain-edit',documentId:'doc'})
  act(()=>portOptions.at(-1)!.onChanged('drain-edit','doc'))
  act(()=>portOptions.at(-1)!.onSaveRequest({documentId:'doc',editorRevision:'drain-edit'}))
  await waitFor(()=>expect(client.save.mock.calls.length).toBeGreaterThan(saves))
 })
})

describe('an agent saves a new version of the open Sheet',()=>{
 const HASH='sha256:'+'c'.repeat(64),commands=[{command:'cell.set' as const,params:{x:1}}]
 const publish=(version:string,base:string,extra:Record<string,unknown>={})=>act(()=>{publishAgentVersion({pageId:'p',version,contentHash:HASH,baseVersion:base,commands,...extra})})
 const settle=()=>act(async()=>{await new Promise(r=>setTimeout(r,0));await new Promise(r=>setTimeout(r,0))})
 it('a reading editor replays three versions in a row without reopening the document',async()=>{
  mount();await ready();client.read.mockClear()
  publish('2','1');publish('3','2');publish('4','3');await settle()
  await waitFor(()=>expect(ports[0].execute).toHaveBeenCalledTimes(3))
  expect(client.read).not.toHaveBeenCalled();expect(ports).toHaveLength(1)
  expect(ports[0].setReadOnly).toHaveBeenLastCalledWith(true,expect.anything())
  // The same and older versions (a reloaded chat) change nothing.
  publish('4','3');publish('2','1');await settle();expect(ports[0].execute).toHaveBeenCalledTimes(3)
 })
 it('a skipped version opens the whole one once, then later versions are measured against what was loaded',async()=>{
  mount();await ready();client.read.mockClear()
  client.read.mockResolvedValue({...await client.read(),version:'3'});client.read.mockClear()
  publish('3','2');await settle()
  await waitFor(()=>expect(client.read).toHaveBeenCalledOnce());await waitFor(()=>expect(ports).toHaveLength(2))
  expect(ports[0].execute).not.toHaveBeenCalled()
  publish('4','3');await settle()
  await waitFor(()=>expect(ports[1].execute).toHaveBeenCalledOnce());expect(client.read).toHaveBeenCalledOnce()
 })
 it('a rejected execute opens the whole version',async()=>{
  mount();await ready();ports[0].execute.mockRejectedValueOnce(new Error('native_execute_refused'));client.read.mockClear()
  client.read.mockResolvedValue({...await client.read(),version:'2'});client.read.mockClear()
  publish('2','1');await settle()
  await waitFor(()=>expect(client.read).toHaveBeenCalledOnce())
 })
 it('missing commands open the whole version',async()=>{
  mount();await ready()
  client.read.mockResolvedValue({...await client.read(),version:'2'});client.read.mockClear()
  publish('2','1',{commands:undefined,commandsOmitted:true});await settle()
  await waitFor(()=>expect(client.read).toHaveBeenCalledOnce());expect(ports[0].execute).not.toHaveBeenCalled()
 })
 it('taking over after three replays starts from the last version, and the first save lands on top of it',async()=>{
  mount();await ready()
  publish('2','1');publish('3','2');publish('4','3');await waitFor(()=>expect(ports[0].execute).toHaveBeenCalledTimes(3))
  client.read.mockResolvedValue({...await client.read(),version:'4'})
  await acquire();await screen.findByText('Ready to edit')
  const port=ports.at(-1)!
  port.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"body":[7]}'),editorRevision:'mine',documentId:'doc'})
  act(()=>portOptions.at(-1)!.onChanged('mine','doc'));act(()=>portOptions.at(-1)!.onSaveRequest({documentId:'doc',editorRevision:'mine'}))
  await waitFor(()=>expect(client.save).toHaveBeenCalled())
  expect(client.save.mock.calls.at(-1)![0].baseVersion).toBe('4')
 })
 it('a take-over during a replay: the late replay changes nothing and opens nothing',async()=>{
  mount();await ready()
  const running=deferred<{documentId:string;revision:string;applied:number}>();ports[0].execute.mockReturnValueOnce(running.promise)
  publish('2','1');await waitFor(()=>expect(ports[0].execute).toHaveBeenCalled())
  client.read.mockResolvedValue({...await client.read(),version:'2'})
  await acquire();await screen.findByText('Ready to edit')
  client.read.mockClear()
  await act(async()=>running.resolve({documentId:'doc',revision:'doc:2',applied:1}));await settle()
  expect(client.read).not.toHaveBeenCalled();expect(screen.getByText('Ready to edit')).toBeInTheDocument()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
 })
 it('a panel closed during a replay: nothing runs afterwards',async()=>{
  const view=mount();await ready()
  const running=deferred<{documentId:string;revision:string;applied:number}>();ports[0].execute.mockReturnValueOnce(running.promise)
  publish('2','1');await waitFor(()=>expect(ports[0].execute).toHaveBeenCalled())
  view.unmount();client.read.mockClear()
  await act(async()=>running.reject(new Error('native_request_aborted')));await settle()
  expect(client.read).not.toHaveBeenCalled()
  publish('3','2');await settle();expect(ports[0].execute).toHaveBeenCalledTimes(1)
 })
 it('losing the right to read during a replay: the late replay does not reopen anything',async()=>{
  mount();await ready()
  const running=deferred<{documentId:string;revision:string;applied:number}>();ports[0].execute.mockReturnValueOnce(running.promise)
  publish('2','1');await waitFor(()=>expect(ports[0].execute).toHaveBeenCalled())
  act(()=>stream?.('session_retired',{reason:'read_revoked'}));await settle();client.read.mockClear()
  await act(async()=>running.resolve({documentId:'doc',revision:'doc:2',applied:1}));await settle()
  expect(client.read).not.toHaveBeenCalled()
 })
 it('unsaved work of the person is never replaced: the change waits, says so, and is not applied over it',async()=>{
  mount();await ready();await acquire();await screen.findByText('Ready to edit')
  const port=ports.at(-1)!
  port.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"body":[8]}'),editorRevision:'typing',documentId:'doc'})
  client.save.mockImplementation(()=>new Promise(()=>{}))
  act(()=>portOptions.at(-1)!.onChanged('typing','doc'))
  publish('2','1');await settle()
  expect(port.execute).not.toHaveBeenCalled()
  expect(await screen.findByTestId('agent-changed-notice')).toHaveTextContent('Finish your edit')
 })
})

describe('a slow save of a large workbook',()=>{
 const leaveWarns=()=>{const e=new Event('beforeunload',{cancelable:true});window.dispatchEvent(e);return e.defaultPrevented}
 // Each case leaves nothing running: a save still waiting is answered, the editor is unmounted
 // here, and what its teardown started settles before the next case mounts a new one.
 let hanging:(()=>void)|undefined
 afterEach(async()=>{
  hanging?.();hanging=undefined
  await act(async()=>{cleanup();for(let i=0;i<5;i++)await new Promise(r=>setTimeout(r,0))})
 })
 it('keeps editing open while it saves, saves later edits right after with one save at a time, and warns before leaving until it lands',async()=>{
  mount();await ready();await acquire()
  const first=deferred<ReturnType<typeof receipt>>();let running=0,most=0
  hanging=()=>first.resolve(receipt(client.save.mock.calls[0][0]))
  client.save.mockImplementation(async(r:NativeSaveRequest)=>{running++;most=Math.max(most,running);try{return client.save.mock.calls.length===1?await first.promise:receipt(r)}finally{running--}})
  act(()=>portOptions.at(-1)!.onChanged('r2','doc'))
  await waitFor(()=>expect(client.save).toHaveBeenCalledOnce())
  await waitFor(()=>expect(screen.getByRole('status')).toHaveAttribute('data-save-phase','saving'))
  expect(ports.at(-1)!.setReadOnly).not.toHaveBeenLastCalledWith(true,expect.anything())
  expect(leaveWarns()).toBe(true)
  ports.at(-1)!.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"body":[2]}'),editorRevision:'r3',documentId:'doc'})
  act(()=>portOptions.at(-1)!.onChanged('r3','doc'))
  await new Promise(r=>setTimeout(r,20))
  expect(client.save).toHaveBeenCalledOnce()
  await act(async()=>first.resolve(receipt(client.save.mock.calls[0][0])))
  await waitFor(()=>expect(client.save).toHaveBeenCalledTimes(2))
  expect(client.save.mock.calls[1][0].baseVersion).toBe('2')
  expect(most).toBe(1)
  await waitFor(()=>expect(screen.getByRole('status')).toHaveAttribute('data-save-phase','saved'))
  expect(leaveWarns()).toBe(false)
 })
 it('on a conflict keeps the changes, says so, and offers the latest version or a copy, never a retry the server refuses again',async()=>{
  mount();await ready();await acquire()
  client.save.mockRejectedValueOnce(new NativeSaveError('conflict'))
  act(()=>portOptions.at(-1)!.onChanged('r2','doc'))
  const alert=await screen.findByRole('alert')
  // One sentence and two actions; the detail is there, folded.
  expect(alert.querySelector('p')).toHaveTextContent(/^Not saved: this Sheet changed on the server meanwhile\. Your changes are kept here\.$/)
  expect(alert.querySelectorAll('b, strong'),'nothing in bold').toHaveLength(0)
  const detail=alert.querySelector('details')!
  expect(detail).not.toHaveAttribute('open')
  expect(detail).toHaveTextContent('Saving again here would be refused')
  expect(screen.queryByRole('button',{name:/Try again|Save again/})).toBeNull()
  expect(leaveWarns()).toBe(true)
  // The copy is a link click: caught here, so the page never navigates to the blob (that left
  // window.location without an origin, and the reopened editor had no safe address to load).
  const saved:string[]=[],click=Object.getOwnPropertyDescriptor(HTMLAnchorElement.prototype,'click'),objectURL={create:URL.createObjectURL,revoke:URL.revokeObjectURL}
  URL.createObjectURL=()=>'blob:copy';URL.revokeObjectURL=()=>{}
  Object.defineProperty(HTMLAnchorElement.prototype,'click',{configurable:true,writable:true,value:function(this:HTMLAnchorElement){saved.push(this.download)}})
  try{
   fireEvent.click(screen.getByRole('button',{name:'Download my changes'}))
   await waitFor(()=>expect(saved).toHaveLength(1))
  }finally{URL.createObjectURL=objectURL.create;URL.revokeObjectURL=objectURL.revoke;if(click)Object.defineProperty(HTMLAnchorElement.prototype,'click',click);else delete (HTMLAnchorElement.prototype as {click?:unknown}).click}
  const reads=client.read.mock.calls.length
  const opened=ports.length,area=document.querySelector('div[aria-busy][tabindex="-1"]')
  expect(area).not.toBeNull()
  fireEvent.click(screen.getByRole('button',{name:'Load the latest version'}))
  await waitFor(()=>expect(client.read.mock.calls.length).toBeGreaterThan(reads))
  // The latest version is open again before the case ends, so no start is left half done.
  await waitFor(()=>expect(ports.length).toBeGreaterThan(opened))
  await waitFor(()=>expect(ports.at(-1)!.bootstrap).toHaveBeenCalledOnce())
  // The same page opens again in the same place: the editor's area is never torn down for "Connecting…".
  expect(document.querySelector('div[aria-busy][tabindex="-1"]')).toBe(area)
  expect(journal.discardRecovered).not.toHaveBeenCalled()
 })
})

describe('Download .xlsx for a Sheet',()=>{
 const asSheet=async()=>{client.open.mockResolvedValueOnce({sessionId:'private-session',applicationId:'gridcraft'});const base=await client.read(new AbortController().signal);client.read.mockResolvedValue({...base,applicationId:'gridcraft',bytes:new TextEncoder().encode('{"sheets":[]}')})}
 const refusal=(reason:string,retryAfterSeconds?:number)=>Object.assign(new Error(`native_export_${reason}`),{reason,retryAfterSeconds})
 const button=()=>screen.getByRole('button',{name:'Download .xlsx'})
 let clicked:{href:string;download:string}[]
 // Plain replacements put back by hand: no spy here may touch the shared mocks the other cases use.
 const original={create:URL.createObjectURL,revoke:URL.revokeObjectURL,click:Object.getOwnPropertyDescriptor(HTMLAnchorElement.prototype,'click')}
 beforeEach(()=>{
  clicked=[]
  URL.createObjectURL=()=>'blob:xlsx';URL.revokeObjectURL=()=>{}
  Object.defineProperty(HTMLAnchorElement.prototype,'click',{configurable:true,writable:true,value:function(this:HTMLAnchorElement){clicked.push({href:this.href,download:this.download})}})
 })
 afterEach(()=>{
  URL.createObjectURL=original.create;URL.revokeObjectURL=original.revoke
  if(original.click)Object.defineProperty(HTMLAnchorElement.prototype,'click',original.click);else delete (HTMLAnchorElement.prototype as {click?:unknown}).click
 })
 it('is offered for a Sheet the person can read, never for a Word document',async()=>{
  mount();await ready()
  expect(screen.queryByRole('button',{name:'Download .xlsx'})).toBeNull()
  cleanup()
  await asSheet();mount();await ready()
  expect(button()).toBeEnabled()
 })
 it('downloads the saved version once, under the server\'s file name, and cannot be pressed twice meanwhile',async()=>{
  await asSheet();mount();await ready()
  const answer=deferred<{blob:Blob;filename:string;version:string}>()
  client.exportXlsx.mockReturnValueOnce(answer.promise)
  fireEvent.click(button())
  expect(button()).toBeDisabled()
  expect(screen.getByText('Preparing the .xlsx… a large workbook can take a minute.')).toBeInTheDocument()
  fireEvent.click(button())
  expect(client.exportXlsx).toHaveBeenCalledOnce()
  await act(async()=>answer.resolve({blob:new Blob(['PK']),filename:'Quarterly plan.xlsx',version:'3'}))
  expect(clicked).toEqual([{href:'blob:xlsx',download:'Quarterly plan.xlsx'}])
  expect(await screen.findByText('Downloaded version 3 as .xlsx.')).toBeInTheDocument()
  expect(button()).toBeEnabled()
 })
 it('says the download is the last saved version while changes are not saved',async()=>{
  await asSheet();mount();await ready();await acquire()
  client.save.mockRejectedValueOnce(new NativeSaveError('conflict'))
  act(()=>portOptions.at(-1)!.onChanged('r2','doc'))
  expect(await screen.findByText('The download is the last saved version; your unsaved changes are not in it.')).toBeInTheDocument()
 })
 it.each([
  ['busy','The server is preparing other downloads. Try again in a moment.',true],
  ['failed','The workbook could not be turned into an .xlsx file. The Sheet itself did not change.',true],
  ['too_large','This workbook is too large to download as .xlsx.',false],
 ] as const)('a refusal (%s) says why, and offers it again only when it can help',async(reason,text,again)=>{
  await asSheet();mount();await ready()
  client.exportXlsx.mockRejectedValueOnce(refusal(reason,reason==='busy'?30:undefined))
  fireEvent.click(button())
  expect(await screen.findByRole('alert')).toHaveTextContent(text)
  if(again)expect(button()).toBeEnabled();else expect(button()).toBeDisabled()
  expect(clicked).toEqual([])
 })
 it('read access gone: says so and the button goes away',async()=>{
  await asSheet();mount();await ready()
  client.exportXlsx.mockRejectedValueOnce(refusal('forbidden'))
  fireEvent.click(button())
  expect(await screen.findByRole('alert')).toHaveTextContent('You can no longer read this Sheet, so it cannot be downloaded.')
  expect(screen.queryByRole('button',{name:'Download .xlsx'})).toBeNull()
 })
})

// The chat card's Undo is sent by this host, under its session and the lease it holds.
describe('Undo of an agent change from the chat card',()=>{
 const undoAnswer=(status:number,body:unknown)=>new Response(JSON.stringify(body),{status})
 it('while the person holds the lease, sends it with the lease generation and hands back the answer',async()=>{
  const answer=undoAnswer(201,{version:'12',skipped:0});Object.assign(client,{undo:vi.fn(async()=>answer)})
  mount();await ready();await acquire()
  await expect(requestSheetUndo('p','run-1:req-2')).resolves.toBe(answer)
  expect((client as unknown as {undo:ReturnType<typeof vi.fn>}).undo).toHaveBeenCalledWith('run-1:req-2',ownedLease.generation,expect.any(AbortSignal))
 })
 it('without editing sends nothing and says so, so the card asks the person to take over',async()=>{
  const undo=vi.fn();Object.assign(client,{undo})
  mount();await ready()
  await expect(requestSheetUndo('p','run-1:req-2')).resolves.toBe('not_editing')
  expect(undo).not.toHaveBeenCalled()
 })
 it('passes a refusal as moved through unchanged and tells nobody to reload',async()=>{
  const answer=undoAnswer(409,{code:'moved',head:'13'});Object.assign(client,{undo:vi.fn(async()=>answer)})
  mount();await ready();await acquire()
  await expect(requestSheetUndo('p','run-1:req-2')).resolves.toBe(answer)
  expect(screen.queryByText(/Reload the page/)).not.toBeInTheDocument()
 })
 it('after the editor is gone, nothing answers for that Sheet',async()=>{
  Object.assign(client,{undo:vi.fn()})
  const view=mount();await ready();view.unmount()
  await expect(requestSheetUndo('p','run-1:req-2')).resolves.toBe('no_editor')
 })
})

// A save that would drop macros, signatures or customXml is refused and nothing is stored on the server.
describe('a save refused for dropping parts of the workbook',()=>{
 it('says so in words, keeps the work in the editor and the local draft, and is not called a conflict',async()=>{
  mount();await ready();await acquire()
  client.save.mockRejectedValueOnce(new NativeSaveError('fidelity_loss'))
  act(()=>portOptions.at(-1)!.onChanged('edit-with-macros','doc'))
  act(()=>portOptions.at(-1)!.onSaveRequest({documentId:'doc',editorRevision:'edit-with-macros'}))
  expect(await screen.findByText('This save would drop macros or signatures, so it was not saved. Your work is kept.')).toBeInTheDocument()
  expect(journal.persist,'the work is in the local draft').toHaveBeenCalled()
  expect(journal.confirm,'and not retired as saved').not.toHaveBeenCalled()
  expect(screen.queryByText(/fidelity_loss/)).toBeNull()
  expect(screen.queryByText(/Reload the page/)).toBeNull()
 })
})
// A creation custody whose release did not go is released by the editor that opens the
// Sheet, with exactly that session and generation; any other lease of the person is left alone.
describe('a creation custody left behind',()=>{
 it('is released when the Sheet opens, exactly that custody',async()=>{
  const {noteUnreleasedCreation,takeUnreleasedCreation}=await import('@/lib/mooring/creation-custody')
  noteUnreleasedCreation('p',{token:'t'.repeat(64),generation:'5'})
  m.apiFetch.mockResolvedValue(new Response('{}',{status:200}))
  mount()
  await waitFor(()=>expect(m.apiFetch).toHaveBeenCalledWith('/api/v1/pages/%40p/craft/lease/release?workspace_id=w',expect.objectContaining({method:'POST',body:JSON.stringify({generation:'5'}),headers:expect.objectContaining({'X-Craft-Session':'t'.repeat(64)})})))
  expect(takeUnreleasedCreation('p')).toBeUndefined()
 })
 it('releases nothing when none was left behind',async()=>{
  m.apiFetch.mockClear();mount();await ready()
  expect(m.apiFetch.mock.calls.some(([url])=>String(url).includes('/lease/release'))).toBe(false)
 })
})
