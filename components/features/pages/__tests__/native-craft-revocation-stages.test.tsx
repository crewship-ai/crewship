// Read revocation on an open native editor as separate stages:
// T0 the server committed it, T1 it reached this client, T2 the client reacted,
// T3 what a reconnect may do. The real document client and draft-key cipher run
// against a scripted server, so no stage can be taken for another.
import {webcrypto} from 'node:crypto'
import {act,cleanup,fireEvent,render,screen} from '@testing-library/react'
import {afterEach,beforeEach,describe,expect,it,vi} from 'vitest'
import type {NativeEditorPortOptions} from '@/lib/mooring/native-editor-port'
import {NATIVE_ADMISSION_DEADLINE_MS,NATIVE_DETECTION_BOUND_MS,NATIVE_REAUTHORIZE_DEADLINE_MS,NATIVE_REAUTHORIZE_INTERVAL_MS} from '@/lib/mooring/native-revocation-bounds'
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
  state:'readable' as 'readable'|'read-lost'|'signed-out',
  stall:new Set<Route>(),
  calls:[] as Route[],
  stream:undefined as ReadableStreamDefaultController<Uint8Array>|undefined,
  retiredPush:0,
  push(event:string,value:unknown){s.stream!.enqueue(encoder.encode(`event: ${event}\ndata: ${JSON.stringify(value)}\n\n`))},
  dropStream(){s.stream?.error(TypeError('network down'));s.stream=undefined},
  count(name:Route){return s.calls.filter(call=>call===name).length},
 }
 m.server=async(input,init)=>{
  const name=route(String(input));s.calls.push(name)
  if(!s.reachable)throw TypeError('Failed to fetch')
  if(s.stall.has(name))return new Promise<Response>((_,reject)=>init.signal?.addEventListener('abort',()=>reject(init.signal!.reason),{once:true}))
  if(name==='draft-key'){
   if(s.state==='signed-out')return json({error:'fresh human authentication required'},401)
   if(s.state==='read-lost')return json({error:'current Page read access is required',code:'draft_read_revoked'},403)
   return json({envelope:'v1:unit'})
  }
  if(s.state==='signed-out')return json({error:'authentication required'},401)
  if(s.state==='read-lost')return json({error:'native Craft access denied'},403)
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
async function opened(){
 render(<NativeCraftEditorHost workspaceId="w" pageId="p" humanId="human" enabled/>)
 await advance(50)
 server.push('lease',lease());await advance(50)
 fireEvent.click(screen.getByRole('button',{name:'Převzít úpravy'}))
 for(let i=0;i<40&&!(contentVisible()&&offered());i++)await advance(50)
 expect(contentVisible()).toBe(true)
 expect(offered()).toBe(true)
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
  // T3: refused admission, then the positive proof, and only then the erase.
  expect(server.count('sessions')).toBeGreaterThan(0);expect(server.count('document')).toBe(0);expect(offered()).toBe(false)
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
describe('lease handoff',()=>{
 it('a transfer POST that never answers ends at the admission deadline, which starts once the writer retired',async()=>{
  await opened()
  server.push('lease',{...lease(),handoff_requested_by:[{actor_id:'user:other',display_name:'Eva',kind:'human',request_id:'r1'}]});await advance(50)
  server.stall=new Set<Route>(['action'])
  act(()=>options.at(-1)!.onChanged('final','doc'))
  fireEvent.click(screen.getByRole('button',{name:/Předat/}))
  // The final save hashes on real crypto threads; the deadline starts after it.
  for(let i=0;i<100&&!server.count('action');i++)await advance(20)
  expect(server.count('action')).toBe(1)
  await advance(NATIVE_ADMISSION_DEADLINE_MS-2_000)
  expect(screen.queryByRole('alert')).toBeNull()
  await advance(2_000)
  expect(screen.getByRole('alert')).toHaveTextContent('Vypršel čas připojení')
  expect(journal.revokeRead).not.toHaveBeenCalled()
 })
})
