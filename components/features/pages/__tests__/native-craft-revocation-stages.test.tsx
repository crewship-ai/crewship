// Read revocation on an open native editor as separate stages:
// T0 the server committed it, T1 it reached this client, T2 the client reacted,
// T3 what a reconnect may do. The real document client and draft-key cipher run
// against a scripted server, so no stage can be taken for another.
import {webcrypto} from 'node:crypto'
import {act,cleanup,fireEvent,render,screen} from '@testing-library/react'
import {afterEach,beforeEach,describe,expect,it,vi} from 'vitest'
import type {NativeEditorPortOptions} from '@/lib/mooring/native-editor-port'
import {NATIVE_ADMISSION_DEADLINE_MS,NATIVE_DETECTION_BOUND_MS,NATIVE_REAUTHORIZE_DEADLINE_MS,NATIVE_REAUTHORIZE_INTERVAL_MS} from '@/lib/mooring/native-revocation-bounds'
import {suspendOtherDocumentDrafts} from '@/lib/document-queue'
import {NativeCraftEditorHost} from '../native-craft-editor-host'

type Route='sessions'|'reauthorize'|'lease'|'document'|'save'|'events'|'action'|'draft-key'|'proposals'|'other'
const m=vi.hoisted(()=>({server:undefined as unknown as (input:string,init:RequestInit)=>Promise<Response>,port:vi.fn(),journal:vi.fn(),capture:vi.fn(),purge:vi.fn()}))
vi.mock('@/lib/api-fetch',()=>({apiFetch:(input:string,init:RequestInit)=>m.server(input,init),tryRefresh:async()=>'ok',AUTH_EVENT:'unit-native-auth-retired'}))
vi.mock('@/lib/server-base',async()=>({...await vi.importActual('@/lib/server-base'),getAuthMode:()=>'bearer'}))
vi.mock('@/lib/mooring/native-editor-port',()=>({createNativeEditorPort:m.port}))
vi.mock('@/lib/mooring/native-draft',async()=>({...await vi.importActual('@/lib/mooring/native-draft'),openNativeDraftJournal:m.journal}))
vi.mock('@/lib/artifact-drafts',async()=>({...await vi.importActual('@/lib/artifact-drafts'),captureLocalDraftPage:m.capture,purgeLocalDraftPage:m.purge}))
vi.mock('../native-craft-agent-start',()=>({NativeCraftAgentStart:()=>null}))
vi.mock('../native-craft-agent-task-status',()=>({NativeCraftAgentTaskStatus:()=>null}))
vi.mock('../native-craft-version-history',()=>({NativeCraftVersionHistory:()=>null}))
vi.mock('../workbook-editor-host',()=>({editorFrameSupported:()=>true,createEditorFrame:(doc:Document,src:string,onLoad:()=>void)=>{const f=doc.createElement('iframe');f.srcdoc='';f.src=src;queueMicrotask(onLoad);return f}}))

const holder={actor_id:'user:human',display_name:'Pavel',kind:'human'}
const lease=()=>({can_edit:true,holder,mine:true,generation:'7',can_write:true,acquired_at:new Date().toISOString(),expires_at:new Date(Date.now()+15_000).toISOString(),handoff_requested_by:[]})
const bundle='b'.repeat(64)
const bytes=btoa('{"body":[]}')
const json=(value:unknown,status=200)=>new Response(JSON.stringify(value),{status})
const encoder=new TextEncoder()
function route(url:string):Route{
 if(url.includes('/draft-key'))return 'draft-key'
 if(url.includes('sessions/reauthorize'))return 'reauthorize'
 if(url.includes('craft/sessions'))return 'sessions'
 if(url.includes('craft/lease/'))return 'action'
 if(url.includes('craft/lease'))return 'lease'
 if(url.includes('craft/events'))return 'events'
 if(url.includes('craft/proposals'))return 'proposals'
 if(url.includes('craft/document/'))return 'save'
 if(url.includes('craft/document'))return 'document'
 return 'other'
}
/** One scripted server. `reachable` is the browser's network, `state` the server's truth. */
function scriptedServer(){
 const s={
  reachable:true,
  state:'readable' as 'readable'|'read-lost'|'comment-only'|'signed-out'|'reader',
  proofDelay:0,
  stall:new Set<Route>(),
  calls:[] as Route[],
  stream:undefined as ReadableStreamDefaultController<Uint8Array>|undefined,
  probes:[] as AbortSignal[],
  retiredPush:0,
  push(event:string,value:unknown){s.stream!.enqueue(encoder.encode(`event: ${event}\ndata: ${JSON.stringify(value)}\n\n`))},
  dropStream(){s.stream?.error(TypeError('network down'));s.stream=undefined},
  count(name:Route){return s.calls.filter(call=>call===name).length},
 }
 m.server=async(input,init)=>{
  const name=route(String(input));s.calls.push(name)
  if(name==='draft-key'&&init.signal)s.probes.push(init.signal)
  if(!s.reachable)throw TypeError('Failed to fetch')
  // The server decides when the request arrives; a delay only holds the answer.
  const state=s.state
  if(s.stall.has(name))return new Promise<Response>((_,reject)=>init.signal?.addEventListener('abort',()=>reject(init.signal!.reason),{once:true}))
  if(name==='draft-key'){
   if(s.proofDelay)await new Promise(resolve=>setTimeout(resolve,s.proofDelay))
   // The real route: draft-key asks canonical Page read, which Comment passes.
   if(state==='comment-only')return json({readable:true})
   if(state==='signed-out')return json({error:'fresh human authentication required'},401)
   if(state==='read-lost')return json({error:'current Page read access is required',code:'draft_read_revoked'},403)
   return json({readable:true})
  }
  if(s.state==='signed-out')return json({error:'authentication required'},401)
  if(s.state==='read-lost'||s.state==='comment-only')return json({error:'native Craft access denied'},403)
  // Read: admitted, and refused the latest content. The shared version is elsewhere.
  if(s.state==='reader'&&name==='document')return json({error:'latest content refused'},403)
  switch(name){
   case 'sessions':return json({session_token:'a'.repeat(64),session_id:'private-session',application_id:'wordcraft',page_id:'p'},201)
   case 'reauthorize':return json({renewed:false})
   case 'lease':return json(lease())
   case 'action':return json(lease())
   case 'save':{const request=JSON.parse(String(init.body));return json({...request,bytes:undefined,version:String(BigInt(request.baseVersion)+1n),candidateHash:request.contentHash,durability:'node',issuedBy:'crewship',phase:'committed'})}
   case 'proposals':return json({proposals:[]})
   case 'document':return json({version:'1',bytes,applicationId:'wordcraft',enginePin:'a'.repeat(64),editorBundle:bundle,editorURL:`https://editor.example/b/${bundle}/index.html`,quota:{usedBytes:'1',limitBytes:'100',warning:false,retention:'all'}})
   case 'events':{
    const body=new ReadableStream<Uint8Array>({start(controller){s.stream=controller}})
    return new Response(body,{headers:{'Content-Type':'text/event-stream'}})
   }
   default:return json({})
  }
 }
 return s
}
let server:ReturnType<typeof scriptedServer>,options:NativeEditorPortOptions[],journal:{persist:ReturnType<typeof vi.fn>,confirm:ReturnType<typeof vi.fn>,recovery:ReturnType<typeof vi.fn>,discardRecovered:ReturnType<typeof vi.fn>,onRetire:ReturnType<typeof vi.fn>,close:ReturnType<typeof vi.fn>,revokeRead:ReturnType<typeof vi.fn>,eraseOwnRow:ReturnType<typeof vi.fn>}
const recovered={draft:{applicationId:'wordcraft',enginePin:'a'.repeat(64),sessionId:'old',generation:'1',baseVersion:'1',editorRevision:'old',bytes:new Uint8Array([99])},actions:['compare','copy'] as const}
const frame=()=>document.querySelector('iframe') as HTMLIFrameElement|null
const contentVisible=()=>{const f=frame();return Boolean(f&&f.style.visibility!=='hidden')}
const offered=()=>screen.queryAllByRole('button',{name:'Kopie konceptu'}).length>0
/** An unconfirmed edit is captured locally while the network is already gone. */
async function editWhileOffline(){
 server.reachable=false
 act(()=>options.at(-1)!.onChanged('typed','doc'))
 await advance(3_000)
 server.dropStream()
}
async function advance(ms:number){let left=ms;while(left>0){const step=Math.min(250,left);await act(async()=>{await vi.advanceTimersByTimeAsync(step)});left-=step}}
// Hashing runs on real crypto threads, so waiting for it takes real time too.
const realTimeout=globalThis.setTimeout
async function settle(ready:()=>boolean){for(let i=0;i<300&&!ready();i++){await advance(20);await new Promise(resolve=>realTimeout(resolve,5))}}
async function opened(){
 render(<NativeCraftEditorHost workspaceId="w" pageId="p" humanId="human" enabled/>)
 await advance(50)
 server.push('lease',lease());await advance(50)
 fireEvent.click(screen.getByRole('button',{name:'Převzít úpravy'}))
 await settle(()=>contentVisible()&&offered())
 expect(contentVisible()).toBe(true)
 expect(offered()).toBe(true)
 server.calls.length=0
}
/** Starts T0 right after a confirmed reauthorization, the worst case for a silent
 * stream. opened() spends a varying part of the first interval on real hashing. */
async function justConfirmed(){
 for(let i=0;i<1300&&!server.count('reauthorize');i++)await advance(25)
 expect(server.count('reauthorize')).toBe(1)
 server.calls.length=0
}
beforeEach(()=>{
 vi.useFakeTimers({shouldAdvanceTime:false})
 vi.stubGlobal('crypto',webcrypto)
 server=scriptedServer();options=[]
 journal={persist:vi.fn().mockResolvedValue(undefined),confirm:vi.fn().mockResolvedValue(undefined),recovery:vi.fn().mockResolvedValue([recovered]),discardRecovered:vi.fn().mockResolvedValue(undefined),onRetire:vi.fn(()=>()=>{}),close:vi.fn().mockResolvedValue(undefined),revokeRead:vi.fn().mockResolvedValue(undefined),eraseOwnRow:vi.fn().mockResolvedValue(undefined)}
 m.journal.mockResolvedValue(journal);m.capture.mockResolvedValue({local:[],artifacts:[],documents:[{key:['row']}]});m.purge.mockResolvedValue(undefined)
 m.port.mockImplementation((o:NativeEditorPortOptions)=>{options.push(o);queueMicrotask(()=>o.onReady?.('qualified'));return {loaded:vi.fn(),handleWindowMessage:vi.fn(),bootstrap:vi.fn().mockResolvedValue({bytes:encoder.encode('{"body":[1]}'),editorRevision:'initial',documentId:'doc'}),snapshot:vi.fn().mockResolvedValue({bytes:encoder.encode('{"body":[1]}'),editorRevision:'initial',documentId:'doc'}),validate:vi.fn().mockResolvedValue(undefined),ack:vi.fn().mockResolvedValue(true),saveFailed:vi.fn(),setReadOnly:vi.fn(async(v:boolean)=>v),close:vi.fn()}})
})
afterEach(()=>{cleanup();vi.useRealTimers();vi.unstubAllGlobals();vi.clearAllMocks()})

describe('bounds',()=>{
 it('derives the detection bound from the 30 s reauthorization and nothing else',()=>{
  expect(NATIVE_REAUTHORIZE_INTERVAL_MS).toBe(30_000)
  expect(NATIVE_REAUTHORIZE_DEADLINE_MS).toBe(NATIVE_REAUTHORIZE_INTERVAL_MS)
  expect(NATIVE_DETECTION_BOUND_MS).toBe(60_000)
 })
})
describe('healthy stream',()=>{
 it('T0 changes nothing here, T1 is the event, T2 hides and erases at once, T3 asks the server for nothing',async()=>{
  await opened()
  server.state='read-lost' // T0: committed on the server, not delivered.
  await advance(4_000)
  expect(contentVisible()).toBe(true);expect(offered()).toBe(true);expect(journal.revokeRead).not.toHaveBeenCalled()
  // T1 and T2 share one callback: the frame leaves in the event's own turn.
  server.push('session_retired',{delivery:'stopped',reason:'authority_changed',draft:'purge'})
  await advance(1)
  expect(frame()).toBeNull();expect(offered()).toBe(false);expect(journal.revokeRead).toHaveBeenCalledOnce()
  expect(screen.getByRole('status')).toHaveTextContent('Přístup ke čtení byl odebrán')
  server.calls.length=0
  await advance(5*60_000)
  expect(server.count('document')).toBe(0);expect(server.count('sessions')).toBe(0);expect(offered()).toBe(false)
 })
 it('a retained retirement (sign-in ended) hides content and keeps the draft',async()=>{
  await opened()
  server.state='signed-out'
  server.push('session_retired',{delivery:'stopped',reason:'authority_changed',draft:'retain'})
  await advance(1)
  expect(contentVisible()).toBe(false);expect(offered()).toBe(false)
  await advance(5*60_000)
  expect(journal.revokeRead).not.toHaveBeenCalled();expect(m.purge).not.toHaveBeenCalled();expect(offered()).toBe(false)
 })
})
describe('silent stream',()=>{
 it('reauthorization delivers a refusal within one interval; content hides then, and only the draft-key proof erases',async()=>{
  await opened()
  await justConfirmed()
  server.state='read-lost' // T0, right after the previous confirmation.
  await advance(NATIVE_REAUTHORIZE_INTERVAL_MS-300)
  expect(server.count('reauthorize')).toBe(0);expect(contentVisible()).toBe(true);expect(offered()).toBe(true)
  await advance(600)
  // T1: the refusal arrived. T2: hidden and withdrawn before any proof of read loss.
  expect(server.count('reauthorize')).toBe(1);expect(contentVisible()).toBe(false);expect(offered()).toBe(false)
  await advance(60_000)
  expect(server.count('document')).toBe(0)
  expect(server.count('draft-key')).toBeGreaterThan(0);expect(m.purge).toHaveBeenCalledWith({human:'human',workspace:'w',page:'p'},expect.anything())
  expect(screen.getByRole('status')).toHaveTextContent('Přístup ke čtení byl odebrán')
 })
 it('both monitors stalled: content and draft leave at the detection bound, not later',async()=>{
  await opened()
  await justConfirmed()
  server.stall=new Set<Route>(['reauthorize','draft-key','sessions'])
  server.state='read-lost'
  await advance(NATIVE_DETECTION_BOUND_MS-500)
  expect(contentVisible()).toBe(true);expect(offered()).toBe(true)
  await advance(1_000)
  expect(contentVisible()).toBe(false);expect(offered()).toBe(false)
  expect(journal.revokeRead).not.toHaveBeenCalled();expect(m.purge).not.toHaveBeenCalled()
 })
})
describe('offline',()=>{
 it('cannot learn revocation: keeps the draft unread and unoffered, and on reconnect reads nothing before verified admission',async()=>{
  await opened()
  await editWhileOffline()
  server.state='read-lost' // T0 while offline: this browser cannot know.
  await advance(10*60_000)
  expect(contentVisible()).toBe(false);expect(offered()).toBe(false)
  expect(journal.revokeRead).not.toHaveBeenCalled();expect(m.purge).not.toHaveBeenCalled()
  server.calls.length=0;server.reachable=true
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  // T3: nothing is read, and only the positive proof erases. Whichever of the kept
  // draft's watch and the admission retry reaches the server first may ask for it.
  expect(server.count('draft-key')).toBeGreaterThan(0);expect(server.count('document')).toBe(0);expect(offered()).toBe(false)
  expect(m.purge).toHaveBeenCalledOnce()
 })
 it('reconnecting with read intact offers the draft only after a fresh admission and read',async()=>{
  await opened()
  await editWhileOffline()
  await advance(2*60_000)
  expect(offered()).toBe(false)
  server.calls.length=0;server.reachable=true
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  expect(server.calls.indexOf('sessions')).toBeGreaterThanOrEqual(0)
  expect(server.calls.indexOf('sessions')).toBeLessThan(server.calls.indexOf('document'))
  expect(offered()).toBe(true);expect(m.purge).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
 })
 it('a sign-in that ended while offline keeps the draft: 401 is no proof of read loss',async()=>{
  await opened()
  await editWhileOffline();server.state='signed-out'
  await advance(2*60_000)
  server.reachable=true
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  expect(offered()).toBe(false);expect(m.purge).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
 })
})
describe('proof',()=>{
 it('an Edit to Comment downgrade missed while offline keeps the draft hidden: admission refuses but read of the Page is not lost',async()=>{
  await opened()
  await editWhileOffline()
  server.state='comment-only'
  await advance(60_000)
  server.reachable=true
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  expect(server.count('document')).toBe(0);expect(offered()).toBe(false)
  expect(m.purge).not.toHaveBeenCalled()
 })
 it('a positive proof arriving after the reauthorization deadline still erases the captured revisions',async()=>{
  await opened()
  await justConfirmed()
  server.state='read-lost';server.proofDelay=NATIVE_REAUTHORIZE_DEADLINE_MS+1_000
  await advance(NATIVE_REAUTHORIZE_INTERVAL_MS+300)
  expect(contentVisible()).toBe(false)
  await advance(NATIVE_REAUTHORIZE_DEADLINE_MS+2_000)
  expect(m.purge).toHaveBeenCalledWith({human:'human',workspace:'w',page:'p'},expect.anything())
 })
})
describe('a draft kept after write loss',()=>{
 it('Edit to Comment stops admission, and removing Comment is still found without a reload',async()=>{
  await opened()
  act(()=>options.at(-1)!.onChanged('typed','doc'))
  await advance(3_000)
  server.state='comment-only'
  server.push('session_retired',{delivery:'stopped',reason:'write_revoked',draft:'retain'})
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  // Comment keeps the Page: admission refuses, the proof says readable, the draft stays.
  expect(server.count('draft-key')).toBeGreaterThan(0);expect(m.purge).not.toHaveBeenCalled()
  server.state='read-lost'
  await advance(NATIVE_DETECTION_BOUND_MS)
  expect(m.purge).toHaveBeenCalledWith({human:'human',workspace:'w',page:'p'},expect.anything())
  expect(screen.getByRole('status')).toHaveTextContent('Přístup ke čtení byl odebrán')
  server.calls.length=0
  await advance(5*60_000)
  expect(server.count('draft-key')).toBe(0)
 })
 it('a replacement admission that stalls past its deadline does not end the watch',async()=>{
  await opened()
  act(()=>options.at(-1)!.onChanged('typed','doc'))
  await advance(3_000)
  server.state='comment-only';server.stall.add('sessions')
  server.push('session_retired',{delivery:'stopped',reason:'write_revoked',draft:'retain'})
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  expect(m.purge).not.toHaveBeenCalled()
  // Comment goes while nothing else is left to notice; recovery brings the proof.
  server.state='read-lost';server.reachable=false
  await advance(NATIVE_DETECTION_BOUND_MS)
  server.reachable=true
  await advance(NATIVE_DETECTION_BOUND_MS)
  expect(m.purge).toHaveBeenCalledWith({human:'human',workspace:'w',page:'p'},expect.anything())
 })
 it('asks one question at a time, and a late proof still erases',async()=>{
  await opened()
  act(()=>options.at(-1)!.onChanged('typed','doc'))
  await advance(3_000)
  server.state='comment-only'
  server.push('session_retired',{delivery:'stopped',reason:'write_revoked',draft:'retain'})
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  server.state='read-lost';server.proofDelay=5*60_000
  await settle(()=>server.count('draft-key')>0&&server.probes.some(signal=>!signal.aborted))
  await advance(NATIVE_REAUTHORIZE_INTERVAL_MS)
  server.calls.length=0
  await advance(4*60_000)
  expect(server.count('draft-key')).toBe(0);expect(m.purge).not.toHaveBeenCalled()
  await advance(60_000+NATIVE_REAUTHORIZE_INTERVAL_MS)
  expect(m.purge).toHaveBeenCalled()
 })
 it.each([['unmounting',()=>cleanup()],['the end of the sign-in',()=>{void suspendOtherDocumentDrafts(null).catch(()=>{})}]])('%s cancels the question on the wire',async(_,end)=>{
  await opened()
  act(()=>options.at(-1)!.onChanged('typed','doc'))
  await advance(3_000)
  server.state='comment-only'
  server.push('session_retired',{delivery:'stopped',reason:'write_revoked',draft:'retain'})
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  server.stall.add('draft-key');server.probes.length=0
  await advance(NATIVE_REAUTHORIZE_INTERVAL_MS+1_000)
  expect(server.probes).toHaveLength(1);expect(server.probes[0].aborted).toBe(false)
  end();await advance(50)
  expect(server.probes[0].aborted).toBe(true)
  server.calls.length=0
  await advance(5*60_000)
  expect(server.count('draft-key')).toBe(0)
  // The journal identity is per module: the next test signs in again.
  await suspendOtherDocumentDrafts('human')
 })
 it('marks the capture of a retiring editor as final, and an edit as not',async()=>{
  await opened()
  // The capture hashes with real crypto, which fake time does not move on a busy host.
  const settled=async(final:boolean)=>{for(let i=0;i<200&&journal.persist.mock.calls.at(-1)?.[1]!==final;i++){await advance(50)}}
  act(()=>options.at(-1)!.onChanged('typed','doc'))
  await settled(false)
  expect(journal.persist.mock.calls.at(-1)?.[1]).toBe(false)
  server.state='comment-only'
  server.push('session_retired',{delivery:'stopped',reason:'write_revoked',draft:'retain'})
  await settled(true)
  expect(journal.persist.mock.calls.at(-1)?.[1]).toBe(true)
 })
 it('the watch ends with the editor',async()=>{
  await opened()
  act(()=>options.at(-1)!.onChanged('typed','doc'))
  await advance(3_000)
  server.state='comment-only'
  server.push('session_retired',{delivery:'stopped',reason:'write_revoked',draft:'retain'})
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  cleanup();server.calls.length=0
  await advance(5*60_000)
  expect(server.count('draft-key')).toBe(0)
 })
})
describe('keeping a draft is not offering it',()=>{
 const rows=()=>screen.queryAllByText(/^Místní koncept/).length
 const recoveredOffer=()=>offered()||rows()>0
 const setVisibility=(state:'hidden'|'visible')=>Object.defineProperty(document,'visibilityState',{configurable:true,get:()=>state})
 afterEach(()=>{Reflect.deleteProperty(document,'visibilityState')})
 it('Read on a shared version keeps the draft and offers nothing',async()=>{
  await opened()
  act(()=>options.at(-1)!.onChanged('typed','doc'))
  await advance(3_000)
  server.state='reader'
  server.push('session_retired',{delivery:'stopped',reason:'write_revoked',draft:'retain'})
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  expect(recoveredOffer()).toBe(false);expect(contentVisible()).toBe(false)
  expect(m.purge).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
 })
 it('Read of the latest content keeps the draft and offers nothing either, in the same tab',async()=>{
  await opened()
  act(()=>options.at(-1)!.onChanged('typed','doc'))
  await advance(3_000)
  const answer=m.server
  m.server=async(input,init)=>{const response=await answer(input,init);return route(String(input))==='lease'||route(String(input))==='action'?json({...lease(),can_edit:false,mine:false,can_write:false,holder:undefined}):response}
  server.push('session_retired',{delivery:'stopped',reason:'write_revoked',draft:'retain'})
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  expect(screen.queryAllByRole('button',{name:'Kopie konceptu'})).toHaveLength(0)
  expect(rows()).toBe(0)
  expect(m.purge).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
 })
 it('Comment on a shared version keeps the draft and offers nothing',async()=>{
  await opened()
  act(()=>options.at(-1)!.onChanged('typed','doc'))
  await advance(3_000)
  server.state='comment-only'
  server.push('session_retired',{delivery:'stopped',reason:'write_revoked',draft:'retain'})
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  expect(recoveredOffer()).toBe(false);expect(m.purge).not.toHaveBeenCalled()
 })
 it('on reconnect the offer leaves first and returns only with the answer',async()=>{
  await opened()
  server.stall.add('document');server.calls.length=0
  await act(async()=>{window.dispatchEvent(new Event('online'))})
  expect(offered()).toBe(false)
  await advance(1_000)
  expect(server.count('document')).toBe(1);expect(offered()).toBe(false)
  server.stall.delete('document')
  await act(async()=>{window.dispatchEvent(new Event('online'))})
  await settle(()=>offered())
  expect(offered()).toBe(true)
 })
 it('on reconnect a refused answer never shows the offer again',async()=>{
  await opened()
  server.state='reader'
  await act(async()=>{window.dispatchEvent(new Event('online'))})
  expect(offered()).toBe(false)
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  expect(offered()).toBe(false);expect(m.purge).not.toHaveBeenCalled()
 })
 it.each([['visibilitychange',()=>{setVisibility('visible');document.dispatchEvent(new Event('visibilitychange'))}],['pageshow',()=>{window.dispatchEvent(new Event('pageshow'))}]])('an emulated return by %s asks at once, with no timer',async(_,back)=>{
  await opened()
  act(()=>options.at(-1)!.onChanged('typed','doc'))
  await advance(3_000)
  server.calls.length=0
  await act(async()=>{back()})
  await act(async()=>{await Promise.resolve()})
  expect(server.count('reauthorize')).toBe(1);expect(server.calls).toContain('draft-key');expect(server.count('document')).toBe(1)
 })
 describe('a live editor on return',()=>{
  const opacity=()=>frame()?.style.opacity
  const returns=[['visibilitychange',()=>{setVisibility('visible');document.dispatchEvent(new Event('visibilitychange'))}],['pageshow',()=>{window.dispatchEvent(new Event('pageshow'))}],['online',()=>{window.dispatchEvent(new Event('online'))}]] as const
  async function dirty(){await opened();act(()=>options.at(-1)!.onChanged('typed','doc'));await advance(3_000);server.calls.length=0}
  it.each(returns)('%s leaves the screen before any answer, without taking focus',async(_,back)=>{
   await dirty()
   server.stall.add('document')
   await act(async()=>{back()})
   expect(opacity()).toBe('0')
   expect(frame()!.isConnected).toBe(true)
  })
  it.each(returns)('%s hides it from pointer and accessibility, keeping focus and the engine as it is, until the read',async(_,back)=>{
   await dirty()
   frame()!.focus()
   const readOnly=m.port.mock.results.at(-1)!.value.setReadOnly as ReturnType<typeof vi.fn>
   readOnly.mockClear()
   server.stall.add('document')
   await act(async()=>{back()})
   await advance(1_000)
   expect(frame()!.style.pointerEvents).toBe('none')
   expect(frame()!.getAttribute('aria-hidden')).toBe('true')
   expect(frame()!.tabIndex).toBe(-1)
   expect(document.activeElement).toBe(frame())
   server.stall.delete('document')
   await act(async()=>{back()})
   await settle(()=>opacity()==='')
   expect(readOnly).not.toHaveBeenCalled()
   expect(frame()!.style.pointerEvents).toBe('')
   expect(frame()!.hasAttribute('aria-hidden')).toBe(false)
   expect(document.activeElement).toBe(frame())
  })
  it('a tab going hidden stores the draft at once and sends nothing for it',async()=>{
   await opened();journal.persist.mockClear();server.calls.length=0
   act(()=>options.at(-1)!.onChanged('typed','doc'))
   setVisibility('hidden')
   await act(async()=>{document.dispatchEvent(new Event('visibilitychange'))})
   await advance(100)
   setVisibility('visible')
   expect(journal.persist).toHaveBeenCalled()
   expect(server.count('save')).toBe(0)
  })
  it('input during a check stays out of the journal, and is saved once the read confirms',async()=>{
   await dirty()
   journal.persist.mockClear()
   server.stall.add('document')
   await act(async()=>{window.dispatchEvent(new Event('pageshow'))})
   act(()=>options.at(-1)!.onChanged('blind','doc'))
   await advance(3_000)
   expect(journal.persist).not.toHaveBeenCalled()
   server.stall.delete('document')
   await act(async()=>{window.dispatchEvent(new Event('pageshow'))})
   await settle(()=>opacity()==='')
   await advance(3_000)
   expect(journal.persist).toHaveBeenCalled()
  })
  it('a loss of Edit during a check captures nothing over the row from before it',async()=>{
   await dirty()
   journal.persist.mockClear()
   server.stall.add('document')
   await act(async()=>{window.dispatchEvent(new Event('pageshow'))})
   act(()=>options.at(-1)!.onChanged('blind','doc'))
   server.push('session_retired',{delivery:'stopped',reason:'write_revoked',draft:'retain'})
   await advance(3_000)
   expect(journal.persist).not.toHaveBeenCalled()
   expect(journal.revokeRead).not.toHaveBeenCalled()
  })
  it.each(returns)('%s comes back only with a read started after it',async(_,back)=>{
   await dirty()
   server.stall.add('document')
   await act(async()=>{back()})
   await advance(1_000)
   expect(opacity()).toBe('0')
   server.stall.delete('document')
   await act(async()=>{back()})
   await settle(()=>opacity()==='')
   expect(opacity()).toBe('')
  })
  it('stays off screen when read is refused, and the draft stays',async()=>{
   await dirty()
   server.state='reader'
   await act(async()=>{window.dispatchEvent(new Event('pageshow'))})
   await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
   expect(contentVisible()&&opacity()==='').toBe(false)
   expect(m.purge).not.toHaveBeenCalled()
  })
  it('an unclear answer keeps it off screen and asks again without waiting for the interval',async()=>{
   await dirty()
   const answer=m.server
   let refused=true
   m.server=async(input,init)=>{if(refused&&route(String(input))==='document')return json({error:'unavailable'},503);return answer(input,init)}
   await act(async()=>{window.dispatchEvent(new Event('pageshow'))})
   await advance(1_000)
   expect(opacity()).toBe('0')
   refused=false
   await advance(5_000)
   await settle(()=>opacity()==='')
   expect(opacity()).toBe('')
  })
  it('says why it is empty when the answer takes long, and not on a quick one',async()=>{
   await dirty()
   server.stall.add('document')
   await act(async()=>{window.dispatchEvent(new Event('pageshow'))})
   expect(screen.queryByText('Ověřuji přístup…')).toBeNull()
   await advance(1_500)
   expect(screen.getByText('Ověřuji přístup…')).toBeTruthy()
  })
  // An answer to something asked before the return says nothing about after it.
  function held(){
   const answer=m.server,h={holdLease:true,holdRead:false,lease:undefined as (()=>void)|undefined,reads:[] as {go:()=>void,signal?:AbortSignal|null}[],version:'1'}
   m.server=async(input,init)=>{
    const name=route(String(input))
    if(name==='lease'&&h.holdLease&&frame())await new Promise<void>(go=>{h.lease=go})
    if(name==='document'&&h.holdRead)await new Promise<void>(go=>{h.reads.push({go,signal:init.signal})})
    const response=await answer(input,init)
    return name==='document'&&h.version!=='1'?json({...await response.json(),version:h.version}):response
   }
   return h
  }
  const backNow=()=>act(async()=>{window.dispatchEvent(new Event('pageshow'))})
  async function finishAdmission(h:ReturnType<typeof held>){h.holdLease=false;h.lease!();await advance(500)}
  async function answerRead(h:ReturnType<typeof held>){h.holdRead=false;h.reads.at(-1)!.go();await settle(()=>opacity()==='');expect(opacity()).toBe('')}
  it('a first admission finishing after a return waits for a read asked after it',async()=>{
   const h=held()
   render(<NativeCraftEditorHost workspaceId="w" pageId="p" humanId="human" enabled/>)
   await advance(50)
   expect(h.lease).toBeTruthy()
   h.holdRead=true
   await backNow()
   await finishAdmission(h)
   expect(opacity()).toBe('0')
   expect(h.reads).toHaveLength(1)
   await answerRead(h)
  })
  it('a replacement frame finishing after a return neither shows nor cancels the read asked after it',async()=>{
   const h=held();h.holdLease=false
   render(<NativeCraftEditorHost workspaceId="w" pageId="p" humanId="human" enabled/>)
   await advance(50)
   server.push('lease',lease());await advance(50)
   const first=frame()
   h.holdLease=true;h.version='2'
   server.push('lease',lease())
   await settle(()=>frame()!==first&&Boolean(h.lease))
   expect(frame()).not.toBe(first)
   h.holdRead=true
   await backNow()
   expect(h.reads).toHaveLength(1)
   await finishAdmission(h)
   expect(opacity()).toBe('0')
   expect(h.reads[0].signal?.aborted).toBe(false)
   await answerRead(h)
  })
  it('a read that never answers is given up, asked again, and says so, with the editor off screen',async()=>{
   await dirty()
   const answer=m.server
   let silent=true,asked=0
   m.server=async(input,init)=>{if(route(String(input))==='document'){asked++;if(silent)return new Promise<Response>(()=>{})}return answer(input,init)}
   await backNow()
   await advance(11_000+5_000)
   expect(asked).toBeGreaterThan(1)
   expect(opacity()).toBe('0')
   expect(screen.getByText('Ověřuji přístup…')).toBeTruthy()
   silent=false
   await advance(20_000)
   await settle(()=>opacity()==='')
   expect(opacity()).toBe('')
   expect(screen.queryByText('Ověřuji přístup…')).toBeNull()
  })
 })
 it('going hidden asks nothing',async()=>{
  await opened()
  server.calls.length=0;setVisibility('hidden')
  await act(async()=>{document.dispatchEvent(new Event('visibilitychange'))})
  expect(server.calls).toEqual([])
 })
})
// Full Chromium stops rendering a visibility:hidden frame (#5060), and an engine that
// answers on an animation frame then never gives up its last changes.
describe('a retiring editor keeps rendering until it is captured',()=>{
 async function watched(){
  await opened()
  const seen:string[]=[],port=m.port.mock.results.at(-1)!.value as {snapshot:ReturnType<typeof vi.fn>}
  const answer=port.snapshot.getMockImplementation() as (...args:unknown[])=>unknown
  port.snapshot.mockImplementation((...args:unknown[])=>{seen.push(frame()?.style.visibility??'gone');return answer(...args)})
  act(()=>options.at(-1)!.onChanged('typed','doc'))
  return seen
 }
 it.each([
  ['Edit lost',()=>{server.state='reader';server.push('session_retired',{delivery:'stopped',reason:'write_revoked',draft:'retain'})}],
  ['a retired connection',()=>{server.push('session_retired',{delivery:'stopped',reason:'session_ended',draft:'retain'})}],
 ] as [string,()=>void][])('%s',async(_,retire)=>{
  const seen=await watched()
  await act(async()=>{retire()})
  await advance(2_000)
  expect(seen.length).toBeGreaterThan(0)
  expect(seen).not.toContain('hidden')
  expect(frame()===null||!contentVisible()||frame()!.style.opacity==='0').toBe(true)
 })
 it('an auth suspension',async()=>{
  const seen=await watched()
  await act(async()=>{await suspendOtherDocumentDrafts('human').catch(()=>{})})
  await advance(2_000)
  expect(seen.length).toBeGreaterThan(0)
  expect(seen).not.toContain('hidden')
 })
})
describe('a stale proof',()=>{
 it('is superseded by a later verified read and never reaches the successor journal',async()=>{
  await opened()
  await editWhileOffline()
  server.state='read-lost';server.proofDelay=5*60_000
  server.reachable=true
  // Proof A is decided now and its answer stays on the wire.
  await settle(()=>server.count('draft-key')>0)
  server.state='readable';server.proofDelay=0
  await advance(NATIVE_ADMISSION_DEADLINE_MS+1_000)
  expect(contentVisible()).toBe(true);expect(offered()).toBe(true)
  // Read fails again before A lands, so a boolean "readable" guard would pass.
  server.reachable=false;server.dropStream()
  await advance(30_000)
  expect(contentVisible()).toBe(false)
  await advance(5*60_000)
  expect(journal.revokeRead).not.toHaveBeenCalled();expect(journal.eraseOwnRow).not.toHaveBeenCalled();expect(m.purge).not.toHaveBeenCalled()
 })
})
describe('lease handoff',()=>{
 it('a transfer POST that never answers ends at the admission deadline, which starts once the writer retired',async()=>{
  await opened()
  server.push('lease',{...lease(),handoff_requested_by:[{actor_id:'user:other',display_name:'Eva',kind:'human',request_id:'r1'}]});await advance(50)
  server.stall=new Set<Route>(['action'])
  act(()=>options.at(-1)!.onChanged('final','doc'))
  fireEvent.click(screen.getByRole('button',{name:/Předat/}))
  await settle(()=>server.count('action')>0)
  expect(server.count('action')).toBe(1)
  await advance(NATIVE_ADMISSION_DEADLINE_MS-2_000)
  expect(screen.queryByRole('alert')).toBeNull()
  await advance(2_000)
  expect(screen.getByRole('alert')).toHaveTextContent('Vypršel čas připojení')
  expect(journal.revokeRead).not.toHaveBeenCalled()
 })
})
