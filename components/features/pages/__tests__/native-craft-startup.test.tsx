import {webcrypto} from 'node:crypto'
import {act,cleanup,render,screen,fireEvent} from '@testing-library/react'
import {afterEach,beforeEach,describe,expect,it,vi} from 'vitest'
import type {NativeDocumentClient} from '@/lib/mooring/native-document-client'
import type {NativeEditorPort,NativeEditorPortOptions,NativeStructuredPort} from '@/lib/mooring/native-editor-port'
import {NativeCraftEditorHost} from '../native-craft-editor-host'

const mock=vi.hoisted(()=>({client:vi.fn(),port:vi.fn(),journal:vi.fn()}))
vi.mock('@/lib/mooring/native-document-client',()=>({createNativeDocumentClient:mock.client}))
vi.mock('@/lib/mooring/native-editor-port',()=>({createNativeEditorPort:mock.port}))
vi.mock('../native-craft-agent-start',()=>({NativeCraftAgentStart:()=>null}))
vi.mock('@/lib/mooring/native-draft',async()=>({...await vi.importActual('@/lib/mooring/native-draft'),openNativeDraftJournal:mock.journal}))
vi.mock('../workbook-editor-host',()=>({editorFrameSupported:()=>true,createEditorFrame:(doc:Document,src:string,onLoad:()=>void)=>{
 const frame=doc.createElement('iframe');frame.srcdoc='';frame.src=src;frame.setAttribute('sandbox','allow-scripts');frame.setAttribute('credentialless','');queueMicrotask(onLoad);return frame
}}))
const {createNativeEditorPort}=await vi.importActual<typeof import('@/lib/mooring/native-editor-port')>('@/lib/mooring/native-editor-port')
const bytes=new TextEncoder().encode('{"diagnostic":"synthetic document, no user content"}')
let client:ReturnType<typeof makeClient>
let requests:string[],received:string[],engineReady:boolean,delay:number,stallAt:number|undefined
let closed=vi.fn<()=>void>()
let hashes:Promise<ArrayBuffer>[]
let stream:Parameters<NativeDocumentClient['events']>[1]|undefined
let adapters:NativeEditorPort[],optionsSeen:NativeEditorPortOptions[],hangBootstrap:boolean,readyDelay:number,hangDigest:boolean
const vacant={can_edit:true,holder:null,mine:false,generation:null,can_write:false,acquired_at:null,expires_at:null,handoff_requested_by:[]}
const holder={actor_id:'diagnostic-human',display_name:'QA',kind:'human'}
function owned(){return {...vacant,holder,mine:true,generation:'1',can_write:true,expires_at:new Date(Date.now()+15000).toISOString()}}
function journal(){return {persist:vi.fn(),confirm:vi.fn(),discardRecovered:vi.fn(),recovery:vi.fn().mockResolvedValue([]),onRetire:vi.fn(()=>()=>{}),close:vi.fn(),revokeRead:vi.fn(),eraseOwnRow:vi.fn()}}

const digest=webcrypto.subtle.digest.bind(webcrypto.subtle)
function makeClient(applicationId:'wordcraft'|'gridcraft'){
 return {
  open:vi.fn().mockResolvedValue({sessionId:'diagnostic-session',applicationId}),
  read:vi.fn().mockResolvedValue({version:'1',bytes,applicationId,enginePin:'a'.repeat(64),editorBundle:'b'.repeat(64),editorURL:`https://editor.example/b/${'b'.repeat(64)}/index.html`,quota:{usedBytes:'1',limitBytes:'9999',warning:false,retention:'keep-all'}}),
  lease:vi.fn().mockResolvedValue({can_edit:true,holder:null,mine:false,generation:null,can_write:false,acquired_at:null,expires_at:null,handoff_requested_by:[]}),
  events:vi.fn((_signal:AbortSignal,callback:Parameters<NativeDocumentClient['events']>[1])=>{stream=callback;return new Promise<void>(()=>{})}),reauthorize:vi.fn().mockResolvedValue(true),proposals:vi.fn().mockResolvedValue([]),close:vi.fn(),leaseAction:vi.fn(),save:vi.fn(),
 }
}
async function tick(ms:number){let remaining=ms;do{const step=Math.min(100,remaining);await act(async()=>{await vi.advanceTimersByTimeAsync(step);await Promise.all(hashes)});remaining-=step}while(remaining>0)}
async function mount(applicationId:'wordcraft'|'gridcraft'){
 client=makeClient(applicationId);mock.client.mockReturnValue(client)
 const ui=render(<NativeCraftEditorHost workspaceId="diagnostic-workspace" pageId="diagnostic-page" humanId="diagnostic-human" enabled />)
 await tick(0);return ui
}
beforeEach(()=>{
 vi.useFakeTimers();vi.stubGlobal('crypto',webcrypto);hashes=[]
 vi.spyOn(webcrypto.subtle,'digest').mockImplementation((algorithm,data)=>{if(hangDigest)return new Promise<ArrayBuffer>(()=>{});const hash=digest(algorithm,data);hashes.push(hash);return hash})
 requests=[];received=[];closed=vi.fn<()=>void>();engineReady=true;delay=8000;stallAt=undefined;stream=undefined;adapters=[];optionsSeen=[];hangBootstrap=false;readyDelay=0;hangDigest=false;mock.journal.mockResolvedValue(journal())
 mock.port.mockImplementation((options:NativeEditorPortOptions)=>{
  const app=options.applicationId;optionsSeen.push(options)
  let document='welcome'
  const wire:NativeStructuredPort={onmessage:null,start:vi.fn(),close:closed,postMessage(raw){
   const request=raw as Record<string,unknown>;requests.push(request.type as string)
   setTimeout(()=>{
    received.push(request.type as string)
    if(request.type===`${app}:set-access`){wire.onmessage?.({data:{type:`${app}:access`,requestId:request.requestId,readOnly:request.readOnly}})}
    else if(request.type===`${app}:open`){document='imported';wire.onmessage?.({data:{type:`${app}:opened`,requestId:request.requestId,document,revision:'1'}})}
    else wire.onmessage?.({data:{type:app==='wordcraft'?'wordcraft:snapshot-result':'gridcraft:native-exported',requestId:request.requestId,document,revision:'1',bytes:bytes.slice().buffer}})
   },requests.length===stallAt?10001:delay)
  }}
  // Synthetic engine transport; real adapter, RPC deadlines, import barrier,
  // document identity verification and host lifecycle run without modification.
  const frame={postMessage(){if(engineReady){const ready=()=>wire.onmessage?.({data:{type:`${app}:ready`,version:'diagnostic',accessProtocol:1,readOnly:true}});if(readyDelay)setTimeout(ready,readyDelay);else queueMicrotask(ready)}}}
  const port=createNativeEditorPort({...options,frame,createChannel:()=>({port1:wire,port2:{}})})
  adapters.push(port);vi.spyOn(port,'setReadOnly');if(hangBootstrap)vi.spyOn(port,'bootstrap').mockReturnValue(new Promise(()=>{}))
  queueMicrotask(()=>port.handleWindowMessage({source:frame,origin:'null',data:{type:`${app}:hello`,protocol:1,bundle:options.bundle}}))
  return port
 })
})
afterEach(()=>{cleanup();vi.useRealTimers();vi.unstubAllGlobals();vi.restoreAllMocks();vi.clearAllMocks()})

describe('native startup budgets with the real capability adapter',()=>{
 it.each(['wordcraft','gridcraft'] as const)('allows each progressing %s import RPC its own bounded deadline',async app=>{
  await mount(app)
  expect(requests).toHaveLength(1)
  await tick(8000);expect(requests).toHaveLength(2)
  await tick(8000);expect(requests).toHaveLength(3)
  // Each RPC responds in 8s (<10s), but their serial total is 24s (>20s).
  await tick(8000)
  expect(received).toHaveLength(3)
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(screen.getByRole('button',{name:'Převzít úpravy'})).toBeEnabled()
  expect(client.events).toHaveBeenCalledOnce()
  expect(client.leaseAction).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled()
  expect(closed).not.toHaveBeenCalled()
 })
 it('still stops a frame that never becomes ready after 20s',async()=>{
  engineReady=false;await mount('wordcraft');await tick(20000)
  expect(screen.getByRole('alert')).toHaveTextContent('Editor se bezpečně nepřipojil')
  expect(screen.getByRole('alert')).toHaveTextContent('Pro nový pokus obnovte stránku')
  expect(screen.getByText('Editor odpojen')).toBeInTheDocument()
  expect(screen.queryByText(/koncept zachován/i)).not.toBeInTheDocument()
  expect(document.querySelector('iframe')).toBeNull();expect(client.save).not.toHaveBeenCalled()
 })
 it.each([['wordcraft',1],['wordcraft',2],['wordcraft',3],['gridcraft',1],['gridcraft',2],['gridcraft',3]] as const)('still stops a ready %s engine whose RPC %s exceeds 10s',async(app,stage)=>{
  stallAt=stage;await mount(app)
  for(let n=1;n<stage;n++)await tick(8000)
  await tick(10000)
  expect(screen.getByRole('alert')).toHaveTextContent('Dokument nelze bezpečně otevřít')
  expect(document.querySelector('iframe')).toBeNull();expect(client.save).not.toHaveBeenCalled()
 })
 it('closes monitoring and never grants custody after unmount during import',async()=>{
  await mount('wordcraft');await tick(8000);expect(requests).toHaveLength(2)
  cleanup();await tick(16000)
  expect(client.close).toHaveBeenCalled();expect(client.leaseAction).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled();expect(closed).toHaveBeenCalledOnce()
 })
})


describe('authority and cancellation throughout slow initialization',()=>{
 it.each(['read','write'] as const)('retires %s authority during import before any document is exposed',async right=>{
  await mount('wordcraft');await tick(8000)
  expect(client.events).toHaveBeenCalledOnce()
  const frame=document.querySelector('iframe')!
  expect(frame).toHaveStyle({opacity:'0'});expect(frame).toHaveAttribute('inert')
  act(()=>stream?.('session_retired',right==='read'?{draft:'purge',reason:'authority_changed'}:{draft:'retain',reason:'write_revoked'}))
  await tick(16000)
  expect(document.querySelector('iframe')).toBeNull();expect(client.open).toHaveBeenCalledOnce()
  expect(client.leaseAction).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled();expect(mock.journal).not.toHaveBeenCalled()
  expect(screen.queryByText(/^Uloženo/)).not.toBeInTheDocument()
 })
 it('checks current authority after import before revealing the hidden frame',async()=>{
  await mount('wordcraft');await tick(16000)
  client.lease.mockRejectedValue(Error('admission retired while importing'))
  await tick(8000)
  expect(document.querySelector('iframe')).toBeNull()
  expect(screen.getByRole('alert')).toHaveTextContent('Dokument nelze bezpečně otevřít')
  expect(client.save).not.toHaveBeenCalled();expect(mock.journal).not.toHaveBeenCalled()
 })
 it('bounds the whole frame startup even when the bootstrap promise ignores cancellation',async()=>{
  hangBootstrap=true;await mount('wordcraft');await tick(50000)
  expect(document.querySelector('iframe')).toBeNull();expect(screen.getByRole('alert')).toHaveTextContent('Dokument nelze bezpečně otevřít')
  expect(client.close).toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled()
 })
 it('bounds initial admission even when the transport never answers',async()=>{
  client=makeClient('wordcraft');client.open.mockReturnValue(new Promise(()=>{}));mock.client.mockReturnValue(client)
  render(<NativeCraftEditorHost workspaceId="diagnostic-workspace" pageId="diagnostic-page" humanId="diagnostic-human" enabled />)
  await tick(60000)
  expect(screen.getByRole('alert')).toHaveTextContent('Dokument nelze bezpečně otevřít')
  expect(client.close).toHaveBeenCalled();expect(client.read).not.toHaveBeenCalled();expect(document.querySelector('iframe')).toBeNull()
 })
 it.each(['human','page','workspace'] as const)('ignores a late import from a replaced %s scope',async changed=>{
  client=makeClient('wordcraft');const old=client;mock.client.mockReturnValue(old)
  const ui=render(<NativeCraftEditorHost workspaceId="diagnostic-workspace" pageId="diagnostic-page" humanId="diagnostic-human" enabled />)
  await tick(8000);const oldFrame=document.querySelector('iframe'),oldOptions=optionsSeen[0]
  const fresh=makeClient('wordcraft');mock.client.mockReturnValue(fresh)
  ui.rerender(<NativeCraftEditorHost workspaceId={changed==='workspace'?'next-workspace':'diagnostic-workspace'} pageId={changed==='page'?'next-page':'diagnostic-page'} humanId={changed==='human'?'next-human':'diagnostic-human'} enabled />)
  await tick(0);const newFrame=document.querySelector('iframe')
  expect(newFrame).not.toBe(oldFrame)
  act(()=>{oldOptions.onReady?.('late');oldOptions.onChanged('late','old-document')})
  await tick(16000)
  expect(document.querySelector('iframe')).toBe(newFrame);expect(newFrame).toHaveStyle({opacity:'0'})
  await tick(8000)
  expect(document.querySelector('iframe')).toBe(newFrame);expect(newFrame).not.toHaveStyle({opacity:'0'})
  expect(old.close).toHaveBeenCalledOnce();expect(old.save).not.toHaveBeenCalled();expect(fresh.save).not.toHaveBeenCalled()
 })
 it.each(['expired','handoff'] as const)('never unlocks a writer whose lease %s during slow import',async loss=>{
  await mount('wordcraft');await tick(24000)
  client.leaseAction.mockImplementation(async()=>{const proof=owned();client.lease.mockResolvedValue(proof);return proof})
  fireEvent.click(screen.getByRole('button',{name:'Převzít úpravy'}));await tick(8000)
  if(loss==='handoff'){
   const next={...vacant,holder:{...holder,actor_id:'other-person'},generation:'2',can_write:false,expires_at:new Date(Date.now()+15000).toISOString()}
   client.lease.mockResolvedValue(next);act(()=>stream?.('lease',next))
  }
  await tick(16000)
  // Fake time alone does not finish the dynamic imports on a loaded runner. The
  // writer stays locked in every step, however long the refusal takes to show.
  for(let step=0;step<30&&!screen.queryByRole('alert');step++)await tick(1000)
  expect(adapters.flatMap(port=>vi.mocked(port.setReadOnly).mock.calls).some(([readOnly])=>readOnly===false)).toBe(false)
  expect(client.lease).toHaveBeenCalled();expect(screen.getByRole('alert')).toBeInTheDocument()
  expect(client.save).not.toHaveBeenCalled();expect(screen.queryByText(/^Uloženo/)).not.toBeInTheDocument()
 })
 it('ignores save/change signals until import and current authority both finish',async()=>{
  await mount('wordcraft');await tick(16000)
  act(()=>{optionsSeen[0].onChanged('premature','imported');optionsSeen[0].onSaveRequest({documentId:'imported',editorRevision:'premature'})})
  expect(client.save).not.toHaveBeenCalled();expect(mock.journal).not.toHaveBeenCalled();expect(screen.queryByText(/^Uloženo/)).not.toBeInTheDocument()
  await tick(8000);expect(client.save).not.toHaveBeenCalled()
 })
})


describe('startup limits and fail-closed admission',()=>{
 it('accepts a verified ready at 19.9s and three 9.9s replies within the 50s frame cap',async()=>{
  readyDelay=19900;delay=9900;await mount('wordcraft');await tick(49600)
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();expect(document.querySelector('iframe')).not.toHaveStyle({opacity:'0'})
  expect(client.lease).toHaveBeenCalledTimes(2);expect(client.events).toHaveBeenCalledOnce();expect(client.save).not.toHaveBeenCalled()
 })
 it('keeps a hung snapshot hash inside the existing 10s RPC deadline',async()=>{
  hangDigest=true;await mount('wordcraft');await tick(10000)
  expect(screen.getByRole('alert')).toHaveTextContent('Dokument nelze bezpečně otevřít');expect(document.querySelector('iframe')).toBeNull()
  expect(requests).toEqual(['wordcraft:snapshot']);expect(client.save).not.toHaveBeenCalled()
 })
 it('bounds final authority checking even when its response never arrives',async()=>{
  await mount('wordcraft');await tick(16000);client.lease.mockReturnValue(new Promise(()=>{}))
  await tick(34000)
  expect(screen.getByRole('alert')).toHaveTextContent('Dokument nelze bezpečně otevřít');expect(document.querySelector('iframe')).toBeNull()
  expect(client.save).not.toHaveBeenCalled();expect(mock.journal).not.toHaveBeenCalled()
 })
 it('retires a generic admission during import without reviving it on late completion',async()=>{
  await mount('wordcraft');await tick(8000)
  act(()=>stream?.('session_retired',{draft:'retain',reason:'authority_changed'}));await tick(16000)
  expect(document.querySelector('iframe')).toBeNull();expect(client.close).toHaveBeenCalled()
  expect(screen.queryByText(/koncept zachován/i)).not.toBeInTheDocument();expect(client.save).not.toHaveBeenCalled()
 })
 it('retires a refused metadata stream before importing or exposing a document',async()=>{
  client=makeClient('wordcraft');client.events.mockRejectedValue(Error('stream refused'));mock.client.mockReturnValue(client)
  render(<NativeCraftEditorHost workspaceId="diagnostic-workspace" pageId="diagnostic-page" humanId="diagnostic-human" enabled />)
  await tick(24000)
  expect(document.querySelector('iframe')).toBeNull();expect(client.close).toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled()
  expect(screen.queryByText(/koncept zachován/i)).not.toBeInTheDocument()
 })
 it('retires failed current-identity reauthorization while a slow import is pending',async()=>{
  readyDelay=19900;delay=9900;await mount('wordcraft');client.reauthorize.mockRejectedValue(Error('identity retired'))
  await tick(31000)
  expect(client.reauthorize).toHaveBeenCalledOnce();expect(document.querySelector('iframe')).toBeNull();expect(client.save).not.toHaveBeenCalled()
  expect(screen.queryByText(/koncept zachován/i)).not.toBeInTheDocument()
 })
 it('never claims a captured draft for a viewer disconnected after bootstrap',async()=>{
  await mount('wordcraft');await tick(24000)
  act(()=>stream?.('session_retired',{draft:'retain',reason:'authority_changed'}));await tick(0)
  expect(screen.getByText('Editor odpojen')).toBeInTheDocument();expect(screen.queryByText(/koncept zachován/i)).not.toBeInTheDocument()
  expect(mock.journal).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled()
 })
})


it('ignores a delayed final authority reply after the page admission is replaced',async()=>{
 const ui=await mount('wordcraft'),previous=client
 await tick(16000)
 let resolve!:(value:typeof vacant)=>void
 previous.lease.mockReturnValue(new Promise<typeof vacant>(done=>{resolve=done}))
 await tick(8000);expect(previous.lease).toHaveBeenCalledTimes(2)
 const next=makeClient('wordcraft');mock.client.mockReturnValue(next)
 ui.rerender(<NativeCraftEditorHost workspaceId="diagnostic-workspace" pageId="next-page" humanId="diagnostic-human" enabled />)
 await tick(0);const frame=document.querySelector('iframe')
 await act(async()=>resolve(vacant));await tick(24000)
 expect(document.querySelector('iframe')).toBe(frame);expect(frame).not.toHaveStyle({opacity:'0'})
 expect(next.close).not.toHaveBeenCalled();expect(previous.save).not.toHaveBeenCalled();expect(next.save).not.toHaveBeenCalled()
 await tick(30000);expect(document.querySelector('iframe')).toBe(frame);expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})
it('stops before document delivery if the initial stream immediately reports read loss',async()=>{
 client=makeClient('wordcraft');client.events.mockImplementation((_signal,callback)=>{callback('session_retired',{draft:'purge',reason:'authority_changed'});return new Promise<void>(()=>{})});mock.client.mockReturnValue(client)
 render(<NativeCraftEditorHost workspaceId="diagnostic-workspace" pageId="diagnostic-page" humanId="diagnostic-human" enabled />)
 await tick(24000)
 expect(document.querySelector('iframe')).toBeNull();expect(client.read).not.toHaveBeenCalled();expect(requests).toHaveLength(0)
 expect(screen.getByRole('status')).toHaveTextContent('Přístup ke čtení byl odebrán')
})


it('bounds an acquisition that never answers without reviving the prior read-only frame',async()=>{
 await mount('wordcraft');await tick(24000)
 client.leaseAction.mockReturnValue(new Promise(()=>{}))
 fireEvent.click(screen.getByRole('button',{name:'Převzít úpravy'}));await tick(60000)
 expect(document.querySelector('iframe')).toBeNull();expect(client.close).toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled();expect(mock.journal).not.toHaveBeenCalled()
 expect(screen.getByRole('alert')).toHaveTextContent('Dokument nelze bezpečně otevřít')
})
