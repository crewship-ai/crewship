import {webcrypto} from 'node:crypto'
import {act,cleanup,render,screen} from '@testing-library/react'
import {afterEach,beforeEach,describe,expect,it,vi} from 'vitest'
import type {NativeEditorPortOptions,NativeStructuredPort} from '@/lib/mooring/native-editor-port'
import {NativeCraftEditorHost} from '../native-craft-editor-host'

const mock=vi.hoisted(()=>({client:vi.fn(),port:vi.fn()}))
vi.mock('@/lib/mooring/native-document-client',()=>({createNativeDocumentClient:mock.client}))
vi.mock('@/lib/mooring/native-editor-port',()=>({createNativeEditorPort:mock.port}))
vi.mock('../workbook-editor-host',()=>({editorFrameSupported:()=>true,createEditorFrame:(doc:Document,src:string,onLoad:()=>void)=>{
 const frame=doc.createElement('iframe');frame.srcdoc='';frame.src=src;frame.setAttribute('sandbox','allow-scripts');frame.setAttribute('credentialless','');queueMicrotask(onLoad);return frame
}}))
const {createNativeEditorPort}=await vi.importActual<typeof import('@/lib/mooring/native-editor-port')>('@/lib/mooring/native-editor-port')
const bytes=new TextEncoder().encode('{"diagnostic":"synthetic document, no user content"}')
let client:ReturnType<typeof makeClient>
let requests:string[],received:string[],engineReady:boolean,delay:number,stallAt:number|undefined
let closed=vi.fn<()=>void>()
let hashes:Promise<ArrayBuffer>[]
const digest=webcrypto.subtle.digest.bind(webcrypto.subtle)
function makeClient(applicationId:'wordcraft'|'gridcraft'){
 return {
  open:vi.fn().mockResolvedValue({sessionId:'diagnostic-session',applicationId}),
  read:vi.fn().mockResolvedValue({version:'1',bytes,applicationId,enginePin:'a'.repeat(64),editorBundle:'b'.repeat(64),editorURL:`https://editor.example/b/${'b'.repeat(64)}/index.html`,quota:{usedBytes:'1',limitBytes:'9999',warning:false,retention:'keep-all'}}),
  lease:vi.fn().mockResolvedValue({can_edit:true,holder:null,mine:false,generation:null,can_write:false,acquired_at:null,expires_at:null,handoff_requested_by:[]}),
  events:vi.fn(()=>new Promise<void>(()=>{})),reauthorize:vi.fn().mockResolvedValue(true),proposals:vi.fn().mockResolvedValue([]),close:vi.fn(),leaseAction:vi.fn(),save:vi.fn(),
 }
}
async function tick(ms:number){await act(async()=>{await vi.advanceTimersByTimeAsync(ms);await Promise.all(hashes)})}
async function mount(applicationId:'wordcraft'|'gridcraft'){
 client=makeClient(applicationId);mock.client.mockReturnValue(client)
 render(<NativeCraftEditorHost workspaceId="diagnostic-workspace" pageId="diagnostic-page" humanId="diagnostic-human" enabled />)
 await tick(0)
}
beforeEach(()=>{
 vi.useFakeTimers();vi.stubGlobal('crypto',webcrypto);hashes=[]
 vi.spyOn(webcrypto.subtle,'digest').mockImplementation((algorithm,data)=>{const hash=digest(algorithm,data);hashes.push(hash);return hash})
 requests=[];received=[];closed=vi.fn<()=>void>();engineReady=true;delay=8000;stallAt=undefined
 mock.port.mockImplementation((options:NativeEditorPortOptions)=>{
  const app=options.applicationId
  let document='welcome'
  const wire:NativeStructuredPort={onmessage:null,start:vi.fn(),close:closed,postMessage(raw){
   const request=raw as Record<string,unknown>;requests.push(request.type as string)
   setTimeout(()=>{
    received.push(request.type as string)
    if(request.type===`${app}:open`){document='imported';wire.onmessage?.({data:{type:`${app}:opened`,requestId:request.requestId,document,revision:'1'}})}
    else wire.onmessage?.({data:{type:app==='wordcraft'?'wordcraft:snapshot-result':'gridcraft:native-exported',requestId:request.requestId,document,revision:'1',bytes:bytes.slice().buffer}})
   },requests.length===stallAt?10001:delay)
  }}
  // Synthetic engine transport; real adapter, RPC deadlines, import barrier,
  // document identity verification and host lifecycle run without modification.
  const frame={postMessage(){if(engineReady)queueMicrotask(()=>wire.onmessage?.({data:{type:`${app}:ready`,version:'diagnostic',accessProtocol:1,readOnly:true}}))}}
  const port=createNativeEditorPort({...options,frame,createChannel:()=>({port1:wire,port2:{}})})
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
  expect(document.querySelector('iframe')).toBeNull();expect(client.events).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled()
 })
 it.each([1,2,3])('still stops a ready engine whose RPC %s exceeds 10s',async stage=>{
  stallAt=stage;await mount('wordcraft')
  for(let n=1;n<stage;n++)await tick(8000)
  await tick(10000)
  expect(screen.getByRole('alert')).toHaveTextContent('Dokument nelze bezpečně otevřít')
  expect(document.querySelector('iframe')).toBeNull();expect(client.events).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled()
 })
 it('never opens monitoring or custody after unmount during import',async()=>{
  await mount('wordcraft');await tick(8000);expect(requests).toHaveLength(2)
  cleanup();await tick(16000)
  expect(client.events).not.toHaveBeenCalled();expect(client.leaseAction).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled();expect(closed).toHaveBeenCalledOnce()
 })
})
