import type {NativeDocumentClient} from '@/lib/mooring/native-document-client'
// A Sheet whose stored document is the workbook file itself (mooring protocol 2): the host
// opens the file as it is, says when values are Excel's, asks before the first edit what
// saving here would change, and offers the original file (version 1) for download.
import {webcrypto} from 'node:crypto'
import {act,cleanup,fireEvent,render,screen,within} from '@testing-library/react'
import {afterEach,beforeEach,describe,expect,it,vi} from 'vitest'
import type {GridcraftNativePortOptions} from '@/lib/mooring/gridcraft-native-port'
import type {NativeEditorPort,NativeStructuredPort} from '@/lib/mooring/native-editor-port'
import {SHEET_REALTIME_CAPABILITIES} from '@/lib/mooring/sheet-realtime'
import {publishAgentVersion} from '@/lib/mooring/native-agent-version'
import {suspendOtherDocumentDrafts} from '@/lib/document-queue'
import {PendingSheetDraftCapture} from '@/lib/local-draft-seal'
import {NativeCraftEditorHost,fidelityPartsText,nativeOpenBudget,sheetWarningText,workbookFile} from '../native-craft-editor-host'
// The editor and the server run the same engine build unless a test says otherwise.
const ENGINE_BUILD='0.19.1+59f6b6c14a37 patch '+'d'.repeat(64)
let serverBuild:string|undefined=ENGINE_BUILD

const mock=vi.hoisted(()=>({client:vi.fn(),gridcraft:vi.fn(),legacy:vi.fn(),journal:vi.fn()}))
vi.mock('@/lib/mooring/native-document-client',()=>({createNativeDocumentClient:mock.client}))
vi.mock('@/lib/mooring/native-editor-port',async()=>({...await vi.importActual('@/lib/mooring/native-editor-port'),createNativeEditorPort:mock.legacy}))
vi.mock('@/lib/mooring/gridcraft-native-port',()=>({createGridcraftNativePort:mock.gridcraft}))
vi.mock('../native-craft-agent-start',()=>({NativeCraftAgentStart:()=>null}))
// The host's own requests go to the stubbed fetch, as the network of this test.
vi.mock('@/lib/api-fetch',()=>({apiFetch:(input:RequestInfo|URL,init?:RequestInit)=>fetch(input,init)}))
vi.mock('@/lib/mooring/native-draft',async()=>({...await vi.importActual('@/lib/mooring/native-draft'),openNativeDraftJournal:mock.journal}))
vi.mock('../workbook-editor-host',()=>({editorFrameSupported:()=>true,createEditorFrame:(doc:Document,src:string,onLoad:()=>void)=>{
 const frame=doc.createElement('iframe');frame.srcdoc='';frame.src=src;frame.setAttribute('sandbox','allow-scripts');frame.setAttribute('credentialless','');queueMicrotask(onLoad);return frame
}}))
const {createGridcraftNativePort}=await vi.importActual<typeof import('@/lib/mooring/gridcraft-native-port')>('@/lib/mooring/gridcraft-native-port')

const workbook=new Uint8Array([0x50,0x4b,3,4,1,2,3])
const vacant={can_edit:true,holder:null,mine:false,generation:null,can_write:false,acquired_at:null,expires_at:null,handoff_requested_by:[]}
let refuseFile=false,refuse:Array<{code:string;count:number}>|null,wires:NativeStructuredPort[],revision:string,sent:Array<Record<string,unknown>>,opened:{calculated:boolean;warnings:Array<{code:string;count:number}>},adapters:NativeEditorPort[],seen:GridcraftNativePortOptions[]
function makeClient(format:'xlsx'|'xlsm'){
 return {
  open:vi.fn().mockResolvedValue({sessionId:'diagnostic-session',applicationId:'gridcraft'}),
  read:vi.fn().mockResolvedValue({version:'3',bytes:workbook,applicationId:'gridcraft',...(serverBuild?{engineBuild:serverBuild}:{}),format,fileName:`Plan.${format}`,size:String(workbook.length),contentHash:'sha256:'+'0'.repeat(64),enginePin:'a'.repeat(64),editorBundle:'b'.repeat(64),editorURL:`https://editor.example/b/${'b'.repeat(64)}/index.html`,quota:{usedBytes:'1',limitBytes:'9999',warning:false,retention:'keep-all'}}),
  lease:vi.fn().mockResolvedValue(vacant),leaseAction:vi.fn<NativeDocumentClient['leaseAction']>(async()=>vacant),
  events:vi.fn(()=>new Promise<void>(()=>{})),reauthorize:vi.fn().mockResolvedValue(true),proposals:vi.fn().mockResolvedValue([]),close:vi.fn(),save:vi.fn(),
  exportXlsx:vi.fn(async(_signal:AbortSignal,version?:string)=>({blob:new Blob(['PK']),filename:`Plan.${format}`,version:version??'3'})),
 }
}
let realtimeCapabilities: boolean
let executed:Record<string,unknown>|null=null
let client:ReturnType<typeof makeClient>
// Fake timers, as in native-craft-startup: the host's deadlines and polls advance with the test, and hashes it started settle in between.
const digest=webcrypto.subtle.digest.bind(webcrypto.subtle)
let hashes:Promise<ArrayBuffer>[],network:ReturnType<typeof vi.fn>
async function settle(ms=500){let remaining=ms;do{const step=Math.min(100,remaining);await act(async()=>{await vi.advanceTimersByTimeAsync(step);await Promise.all(hashes)});remaining-=step}while(remaining>0)}
async function mount(format:'xlsx'|'xlsm'='xlsx',toolbar?:HTMLElement,realtimeChatId?:string, generation?:string){
 client=makeClient(format);mock.client.mockReturnValue(client)
 if(generation){
  client.open.mockResolvedValue({sessionId:'diagnostic-session',applicationId:'gridcraft',realtime:{canWrite:true,generation:'1'}})
  client.lease.mockResolvedValue({realtime_sheet:true,can_edit:true,can_write:true,holder:null,generation})
 }
 const warn=vi.spyOn(console,'warn')
 render(<NativeCraftEditorHost workspaceId="diagnostic-workspace" pageId="diagnostic-page" humanId="diagnostic-human" enabled realtimeChatId={realtimeChatId} {...(toolbar?{toolbar,historyHref:'/artifacts/diagnostic-page'}:{})} />)
 await settle()
 // A start that failed says which step refused, as codes only: show them instead of a missing button.
 expect(warn.mock.calls.flat().map(String).filter(line=>line.startsWith('native_')),'native start codes').toEqual([])
 expect(screen.queryByRole('alert'),'no refusal while opening').toBeNull()
 expect(document.querySelector('iframe'),'the editor frame is mounted').not.toBeNull()
 expect(sent.map(m=>m.type),'what the host sent the editor').toContain('gridcraft:open')
}
beforeEach(()=>{
 vi.useFakeTimers();vi.stubGlobal('crypto',webcrypto);hashes=[]
 vi.spyOn(webcrypto.subtle,'digest').mockImplementation((algorithm,data)=>{const hash=digest(algorithm,data);hashes.push(hash);return hash})
 // No network here: a draft key the journal might ask for is simply not given.
 network=vi.fn(async()=>new Response(null,{status:404}));vi.stubGlobal('fetch',network)
 refuse=null;refuseFile=false;realtimeCapabilities=false;executed=null;wires=[];revision='1';sent=[];adapters=[];seen=[];opened={calculated:false,warnings:[]}
 mock.journal.mockResolvedValue({persist:vi.fn(),confirm:vi.fn(),discardRecovered:vi.fn(),recovery:vi.fn().mockResolvedValue([]),onRetire:vi.fn(()=>()=>{}),close:vi.fn(),revokeRead:vi.fn(),eraseOwnRow:vi.fn()})
 mock.legacy.mockImplementation(()=>{throw Error('a workbook file never opens through the native JSON port')})
 mock.gridcraft.mockImplementation((options:GridcraftNativePortOptions)=>{
  seen.push(options)
  const wire:NativeStructuredPort={onmessage:null,start:vi.fn(),close:vi.fn(),postMessage(raw){
   const m=raw as Record<string,unknown>;sent.push(m)
   setTimeout(()=>{
    if(m.type==='gridcraft:set-access')wire.onmessage?.({data:{type:'gridcraft:access',requestId:m.requestId,readOnly:m.readOnly}})
    else if(m.type==='gridcraft:execute'&&executed)wire.onmessage?.({data:{type:'gridcraft:executed',requestId:m.requestId,...executed}})
    else if(m.type==='gridcraft:save-request'&&refuse)wire.onmessage?.({data:{type:'gridcraft:save',requestId:m.requestId,code:'fidelity_loss',error:"would drop charts in 'Q3'",parts:refuse,...(refuseFile?{bytes:new Uint8Array([0x50,0x4b,9,9]).buffer}:{})}})
    else if(m.type==='gridcraft:save-request')wire.onmessage?.({data:{type:'gridcraft:save',requestId:m.requestId,document:'doc',revision,name:'Plan.xlsx',format:'xlsx',bytes:new Uint8Array([0x50,0x4b,3,4,Number(revision)]).buffer}})
    else if(m.type==='gridcraft:open')wire.onmessage?.({data:{type:'gridcraft:opened',requestId:m.requestId,document:'doc',revision:'1',format:(m.name as string).endsWith('.xlsm')?'xlsm':'xlsx',calculated:opened.calculated,warnings:opened.warnings}})
   },0)
  }}
  wires.push(wire)
  const frame={postMessage(){queueMicrotask(()=>wire.onmessage?.({data:{type:'gridcraft:ready',version:'diagnostic',accessProtocol:1,readOnly:true,build:ENGINE_BUILD,...(realtimeCapabilities?{capabilities:SHEET_REALTIME_CAPABILITIES}:{})}}))}}
  const port=createGridcraftNativePort({...options,frame,createChannel:()=>({port1:wire,port2:{}})})
  adapters.push(port)
  queueMicrotask(()=>port.handleWindowMessage({source:frame,origin:'null',data:{type:'gridcraft:hello',protocol:2,bundle:options.bundle}}))
  return port
 })
})
afterEach(()=>{cleanup();vi.useRealTimers();vi.unstubAllGlobals();vi.restoreAllMocks();vi.clearAllMocks()})

describe('a Sheet stored as its workbook file',()=>{
 it('opens the stored file as it is, under its own name, without recalculating, and nothing else',async()=>{
  await mount()
  expect(seen[0]).toMatchObject({fileName:'Plan.xlsx'})
  const open=sent.find(m=>m.type==='gridcraft:open')!
  expect(open).toMatchObject({name:'Plan.xlsx',calculate:false,autoFit:false})
  expect(new Uint8Array(open.bytes as ArrayBuffer)).toEqual(workbook)
  expect(sent.map(m=>m.type)).not.toContain('gridcraft:export-native')
  expect(document.querySelector('iframe')).not.toHaveStyle({opacity:'0'})
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
 })
 it('says the values are the ones saved in the file, and promises no recalculation',async()=>{
  await mount()
  expect(screen.getByText('Values saved in the file (not recalculated)')).toBeInTheDocument()
  // "recalculated" is said only of a real calculation: the open did none.
  expect(document.body.textContent?.replace('(not recalculated)','')).not.toMatch(/recalculat/i)
 })
 // The saved values say in one sentence what they mean, on hover and on keyboard focus.
 it('explains the saved values in one sentence, on keyboard focus',async()=>{
  await mount()
  act(()=>screen.getByText('Values saved in the file (not recalculated)').focus())
  expect(screen.getByRole('tooltip')).toHaveTextContent('Values come from the saved file and are recalculated after your first change.')
 })
 it('does not say so when the editor calculated the workbook',async()=>{
  opened.calculated=true;await mount()
  expect(screen.queryByText(/Values saved in the file/)).not.toBeInTheDocument()
 })
 it('opens a chat workbook without taking custody and keeps the old editor read only',async()=>{
  const toolbar=document.createElement('div')
  document.body.append(toolbar)
  try{
   opened.warnings=[{code:'vba_preserved',count:1}]
   await mount('xlsm',toolbar,'chat-1')
   act(()=>wires[0].onmessage?.({data:{type:'gridcraft:edit-attempt',document:'doc',kind:'key'}}))
   await settle()
   expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
   expect(within(toolbar).queryByText('Take over editing')).not.toBeInTheDocument()
   expect(client.leaseAction).not.toHaveBeenCalled()
   expect(sent.some(message=>message.type==='gridcraft:set-access'&&message.readOnly===false)).toBe(false)
  }finally{toolbar.remove()}
 })
 it.each([
  { generation:'1', capabilities:true, writable:true },
  { generation:'2', capabilities:true, writable:false },
  { generation:'1', capabilities:false, writable:false },
 ])('admits automatic chat editing only with the matching server generation and editor capabilities ($generation, $capabilities)',async({generation,capabilities,writable})=>{
  realtimeCapabilities=capabilities
  await mount('xlsx',undefined,'chat-1',generation)
  expect(mock.client).toHaveBeenCalledWith({workspaceId:'diagnostic-workspace',pageId:'diagnostic-page',chatId:'chat-1'})
  expect(sent.some(message=>message.type==='gridcraft:set-access'&&message.readOnly===false)).toBe(writable)
  expect(client.leaseAction).not.toHaveBeenCalled()
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
 })
 it('asks before the first edit what saving here would change, and takes over only when the person says so',async()=>{
  opened.warnings=[{code:'vba_preserved',count:1},{code:'unsupported_function_held',count:12}];await mount('xlsm')
  fireEvent.click(screen.getByRole('button',{name:'Take over editing'}))
  const dialog=screen.getByRole('alertdialog',{name:'Before you edit this workbook'})
  expect(dialog).toHaveTextContent('Macros (VBA) are kept exactly as they are. They never run here.')
  expect(dialog).toHaveTextContent('12 cells use Excel functions this editor does not calculate. They keep the value Excel calculated.')
  expect(dialog).toHaveTextContent('The file as it was uploaded stays as version 1')
  expect(client.leaseAction).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button',{name:'Keep reading'}))
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();expect(client.leaseAction).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button',{name:'Take over editing'}))
  fireEvent.click(screen.getByRole('button',{name:'Edit anyway'}))
  await settle()
  expect(client.leaseAction.mock.calls.some(call=>call[0]==='acquire')).toBe(true)
 })
 it('takes over at once when the workbook holds nothing the editor would change',async()=>{
  await mount()
  fireEvent.click(screen.getByRole('button',{name:'Take over editing'}))
  await settle()
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(client.leaseAction).toHaveBeenCalled()
 })
 it('lists what the editor keeps as it is while reading',async()=>{
  opened.warnings=[{code:'external_links',count:2}];await mount()
  expect(screen.getByText('What this editor keeps as it is in this workbook')).toBeInTheDocument()
  expect(screen.getByText('Links to other workbooks are kept, but their values are not updated here.')).toBeInTheDocument()
 })
 it('downloads the original file, version 1, and names the format of an .xlsm',async()=>{
  const objectURL={create:URL.createObjectURL,revoke:URL.revokeObjectURL},click=Object.getOwnPropertyDescriptor(HTMLAnchorElement.prototype,'click')
  URL.createObjectURL=()=>'blob:diagnostic';URL.revokeObjectURL=()=>{};Object.defineProperty(HTMLAnchorElement.prototype,'click',{configurable:true,value(){}})
  try{
   await mount('xlsm')
   expect(screen.getByRole('button',{name:'Download .xlsm'})).toBeInTheDocument()
   fireEvent.click(screen.getByRole('button',{name:'Download original'}))
   await settle()
   expect(client.exportXlsx).toHaveBeenCalledWith(expect.any(AbortSignal),'1')
   expect(screen.getByText('Downloaded the original file (version 1) as .xlsm.')).toBeInTheDocument()
  }finally{URL.createObjectURL=objectURL.create;URL.revokeObjectURL=objectURL.revoke;if(click)Object.defineProperty(HTMLAnchorElement.prototype,'click',click);else delete (HTMLAnchorElement.prototype as {click?:unknown}).click}
 })
})

describe('workbook file helpers',()=>{
 it('recognises only a gridcraft xlsx or xlsm document and keeps its name with the right extension',()=>{
  const base={version:'1',bytes:workbook,enginePin:'a',editorBundle:'b',editorURL:'u',quota:{usedBytes:'1',limitBytes:'2',warning:false,retention:'keep-all'}}
  expect(workbookFile({...base,applicationId:'gridcraft',format:'xlsm',fileName:'Q3 plan.xlsm'} as never)).toEqual({format:'xlsm',fileName:'Q3 plan.xlsm'})
  expect(workbookFile({...base,applicationId:'gridcraft',format:'xlsx',fileName:'Q3 plan'} as never)).toEqual({format:'xlsx',fileName:'Q3 plan.xlsx'})
  expect(workbookFile({...base,applicationId:'gridcraft',format:'xlsx',fileName:'../x/y.xlsx'} as never)).toEqual({format:'xlsx',fileName:'Workbook.xlsx'})
  expect(workbookFile({...base,applicationId:'gridcraft'} as never)).toBeNull()
  expect(workbookFile({...base,applicationId:'wordcraft',format:'xlsx'} as never)).toBeNull()
 })
 it('gives a workbook file one open step by its compressed size',()=>{
  expect(nativeOpenBudget(3_580_000,'file')).toEqual({stepMs:44_000,totalMs:74_000})
  expect(nativeOpenBudget(60*1024*1024,'file').stepMs).toBe(180_000)
 })
 it('names the parts a save would drop in words, an unknown one by its code',()=>{
  expect(fidelityPartsText([{code:'charts',count:2},{code:'pivot_tables',count:1},{code:'vba_project',count:1}])).toBe('2 charts, 1 pivot table, the macros (VBA)')
  expect(fidelityPartsText([{code:'comments',count:1},{code:'slicer_caches',count:3}])).toBe('1 comment, 3 × slicer_caches')
  expect(fidelityPartsText([{code:'pivot_formats',count:4},{code:'pivot_formats',count:1}])).toBe('4 pivot table formats, 1 pivot table format')
  expect(fidelityPartsText([{code:'vba_project',count:1},{code:'vba_signature',count:1}])).toBe('the macros (VBA), the digital signature of the macros')
 })
 it('says an unknown warning by its code only',()=>{
  expect(sheetWarningText('slicer_dropped',1)).toBe('Some parts of this workbook may not be kept exactly when you save (slicer_dropped).')
  expect(sheetWarningText('encrypted_part_dropped',1)).toBe('1 encrypted part cannot be kept. Saving here leaves it out.')
 })
})

describe('saving a workbook file',()=>{
 const owned={...vacant,holder:{actor_id:'user:diagnostic-human',display_name:'QA',kind:'human'},mine:true,generation:'1',can_write:true,acquired_at:'2026-01-01T00:00:00Z',expires_at:'2099-01-01T00:00:00Z'}
 async function editing(){
  await mount()
  client.leaseAction.mockImplementation(async(action:string)=>{if(action==='acquire')client.lease.mockResolvedValue(owned);return owned})
  client.save.mockImplementation(async(r:Record<string,unknown>)=>({...r,bytes:undefined,version:(BigInt(r.baseVersion as string)+1n).toString(),candidateHash:r.contentHash,durability:'node',issuedBy:'crewship',phase:'committed'}))
  fireEvent.click(screen.getByRole('button',{name:'Take over editing'}))
  await settle()
  expect(sent.some(m=>m.type==='gridcraft:set-access'&&m.readOnly===false),'the editor was made writable').toBe(true)
 }
 const change=async()=>{revision=String(Number(revision)+1);await act(async()=>{wires.at(-1)!.onmessage?.({data:{type:'gridcraft:changed',document:'doc',revision,dirty:true}})})}
 it('allows the session-end retry to persist the same frozen workbook locally',async()=>{
  await editing()
  await change()
  const journal=await mock.journal.mock.results.at(-1)!.value as {persist:ReturnType<typeof vi.fn>}
  journal.persist.mockRejectedValue(Error('storage busy'))
  try{
   const first=suspendOtherDocumentDrafts(null).catch(error=>error)
   await settle(2_000)
   expect(await first).toBeInstanceOf(PendingSheetDraftCapture)
   expect(document.querySelector('iframe')).not.toBeNull()
   expect(client.save).not.toHaveBeenCalled()

   journal.persist.mockResolvedValue(undefined)
   const second=suspendOtherDocumentDrafts(null)
   await settle(2_000)
   await second
   expect(journal.persist).toHaveBeenLastCalledWith(expect.objectContaining({format:'xlsx',editorRevision:'doc:2'}),true)
   expect(client.save).not.toHaveBeenCalled()
  }finally{
   journal.persist.mockResolvedValue(undefined)
   await suspendOtherDocumentDrafts('diagnostic-human')
  }
 })
 // Concurrent editing: the agent saves while the person has unsaved work in the open Sheet.
 it('adds the agent version to unsaved work, keeps the person\'s cells, names them, and saves on top of the agent',async()=>{
  await editing()
  await change()
  const HEAD='sha256:'+'c'.repeat(64)
  network.mockImplementation(async(input:RequestInfo|URL)=>String(input).includes('/craft/document/operations?')
   ?new Response(JSON.stringify({from:'3',to:'4',head:'4',head_content_hash:HEAD,replayable:true,versions:[{version:'4',base_version:'3',actor_kind:'agent',actor_name:'Morgan',operation_id:'op-1',engine_pin:'a'.repeat(64),calculate:'saved',replayable:true,ops:[{command:'edit.clearContents',params:{range:"'L'!B8:B577"}}],digest:null}]}),{status:200})
   :new Response(null,{status:404}))
  executed={document:'doc',revision:String(Number(revision)+1),applied:1,conflicts:[{sheet:'L',cell:'B9',agent:'',kept:'LN-0042'}]}
  await act(async()=>{publishAgentVersion({pageId:'diagnostic-page',version:'4'})})
  await settle(1000)
  const replay=sent.find(m=>m.type==='gridcraft:execute')
  expect(replay,'the agent log was replayed in the editor').toMatchObject({keep:'human',label:'Morgan: change',commands:JSON.stringify([{command:'edit.clearContents',params:{range:"'L'!B8:B577"}}])})
  expect(screen.getByTestId('agent-rebase-note')).toHaveTextContent("Morgan's change (version 4) is in your Sheet")
  expect(screen.getByTestId('agent-rebase-note')).toHaveTextContent("'L'!B9: kept “LN-0042”, Morgan wanted “”")
  await settle(12000)
  expect(client.save,'saved on top of the agent version').toHaveBeenCalled()
  expect(client.save.mock.calls.at(-1)![0]).toMatchObject({baseVersion:'4'})
  expect(screen.queryByText(/Reload the page/)).toBeNull()
 })
 it('waits 10 seconds after the last change, then saves the editor file as the workbook format',async()=>{
  await editing()
  await change();await settle(6000)
  await change();await settle(9000)
  expect(client.save,'no save while the person keeps typing, nor within 10 s of the last change').not.toHaveBeenCalled()
  await settle(2000)
  expect(client.save).toHaveBeenCalledTimes(1)
  expect(client.save.mock.calls[0][0]).toMatchObject({format:'xlsx',baseVersion:'3',editorRevision:'doc:3'})
  expect(sent.some(m=>m.type==='gridcraft:save-ack'&&m.version==='4'),'the editor hears which version it became').toBe(true)
 })
 it('says which parts a save here would drop, keeps the changes unsaved, and asks again only for a newer revision',async()=>{
  await editing()
  refuse=[{code:'charts',count:2},{code:'pivot_tables',count:1}]
  await change();await settle(11000)
  const bar=screen.getByRole('alert')
  expect(bar).toHaveTextContent('Saving here would drop parts of this workbook (2 charts, 1 pivot table). Your changes stay in the editor.')
  expect(bar).not.toHaveTextContent('Q3')
  expect(client.save).not.toHaveBeenCalled()
  expect(document.querySelector('[data-save-phase]')).toHaveTextContent('Unsaved draft')
  const asked=()=>sent.filter(m=>m.type==='gridcraft:save-request').length
  const once=asked()
  await settle(30000)
  expect(asked(),'the same revision is not asked again').toBe(once)
  refuse=null
  await change();await settle(11000)
  expect(asked()).toBe(once+1)
  expect(client.save).toHaveBeenCalledTimes(1)
  expect(screen.queryByText(/Saving here would drop parts/)).toBeNull()
 })
 it('after a refusal says truthfully where the changes are, and asks before the page is left or reloaded',async()=>{
  // The editor gave no file, so nothing reached the draft journal: only the open editor holds the change.
  await editing()
  refuse=[{code:'pivot_formats',count:583}]
  await change();await settle(11000)
  const bar=screen.getByRole('alert')
  expect(bar).toHaveTextContent('Saving here would drop parts of this workbook (583 pivot table formats). Your changes stay in the editor.')
  expect(bar).toHaveTextContent(/leaving or reloading this page loses them/)
  expect(bar).not.toHaveTextContent(/kept in this browser/)
  const leave=new Event('beforeunload',{cancelable:true});window.dispatchEvent(leave)
  expect(leave.defaultPrevented,'the browser asks before the page is left').toBe(true)
 })
 it('keeps the file of a refused save in this browser as an incomplete copy to download, never on the server',async()=>{
  // The refusal carries the file as it would be written.
  await editing()
  refuse=[{code:'pivot_formats',count:583}];refuseFile=true
  await change();await settle(11000)
  const journal=await mock.journal.mock.results.at(-1)!.value as {persist:ReturnType<typeof vi.fn>}
  const kept=journal.persist.mock.calls.map(([d])=>d as {lossy?:unknown;format?:string;bytes:Uint8Array}).filter(d=>d.lossy)
  expect(kept).toHaveLength(1)
  expect(kept[0]).toMatchObject({format:'xlsx',lossy:[{code:'pivot_formats',count:583}]})
  expect(Array.from(kept[0].bytes)).toEqual([0x50,0x4b,9,9])
  expect(client.save).not.toHaveBeenCalled()
  const bar=screen.getByRole('alert')
  expect(bar).toHaveTextContent('Nothing was saved on the server. This browser keeps a copy of the file without those parts, to download only.')
  const leave=new Event('beforeunload',{cancelable:true});window.dispatchEvent(leave)
  expect(leave.defaultPrevented,'the editor still holds more than the incomplete copy').toBe(true)
 })
 it('offers an incomplete copy kept from a refused save only for download, and names what it lacks',async()=>{
  const draft={applicationId:'gridcraft',enginePin:'pin',baseVersion:'3',generation:'1',sessionId:'old',editorRevision:'doc:5',format:'xlsx' as const,bytes:new Uint8Array([0x50,0x4b,1]),lossy:[{code:'pivot_formats',count:583},{code:'charts',count:2}]}
  mock.journal.mockResolvedValue({persist:vi.fn(),confirm:vi.fn(),discardRecovered:vi.fn(),recovery:vi.fn().mockResolvedValue([{draft,actions:['download']}]),onRetire:vi.fn(()=>()=>{}),close:vi.fn(),revokeRead:vi.fn(),eraseOwnRow:vi.fn()})
  // The journal, and so its recoveries, opens when this tab takes over editing.
  await editing()
  const row=screen.getByRole('group',{name:'Unsaved changes, incomplete file'})
  expect(row).toHaveTextContent('Unsaved changes on version 3, not saved: this copy lacks 583 pivot table formats, 2 charts')
  expect(row).toHaveTextContent('The stored version keeps every part.')
  expect(within(row).getAllByRole('button').map(b=>b.textContent)).toEqual(['Download incomplete copy'])
 })
 it('saves nothing when the workbook was taken over and left unedited',async()=>{
  await editing()
  await settle(15000)
  expect(client.save).not.toHaveBeenCalled()
 })
})

// In the panel beside the chat everything but the grid is one compact bar.
describe('a Sheet beside the chat, with one bar above the grid',()=>{
 let bar:HTMLElement
 beforeEach(()=>{bar=document.createElement('div');bar.setAttribute('data-testid','panel-bar');document.body.append(bar)})
 afterEach(()=>bar.remove())
 // findBy* polls on real timers, and these tests run on fake ones: open, then let the clock run.
 const openMenu=async()=>{fireEvent.pointerDown(within(bar).getByRole('button',{name:'More for this Sheet'}),{button:0,ctrlKey:false});await settle(100)}
 const NOTE='Values saved in the file (not recalculated)'
 const SAVED_NOTE='The values are the ones saved in the file. They update once the Sheet is recalculated.'
 const owned={...vacant,holder:{actor_id:'user:diagnostic-human',display_name:'QA',kind:'human'},mine:true,generation:'1',can_write:true,acquired_at:'2026-01-01T00:00:00Z',expires_at:'2099-01-01T00:00:00Z'}
 const change=async()=>{revision=String(Number(revision)+1);await act(async()=>{wires.at(-1)!.onmessage?.({data:{type:'gridcraft:changed',document:'doc',revision,dirty:true}})})}
 // The badge and "Free" crowded the bar until it covered Expand; the note lives in the menu.
 it('keeps who edits and a menu in the bar, the saved-values note in the menu, and no explaining sentences beside the grid',async()=>{
  await mount('xlsx',bar)
  const toolbar=within(bar).getByRole('toolbar',{name:'Document editing'})
  expect(toolbar,'nobody editing needs no words').not.toHaveTextContent('Free')
  expect(within(bar).queryByText('Saved values'),'no badge in the bar').toBeNull()
  const section=screen.getByRole('region',{name:'GridCraft workbook'})
  for(const name of [/^Download/,'Publish preview','Take over editing'])expect(within(section).queryByRole('button',{name}),String(name)).toBeNull()
  expect(within(section).queryByText(NOTE)).toBeNull()
  await openMenu()
  const items=screen.getAllByRole('menuitem').map(i=>i.textContent)
  expect(items).toEqual(['Download .xlsx','Download original (version 1)','Version history'])
  expect(screen.getByRole('menu')).toHaveTextContent(SAVED_NOTE)
  expect(screen.getByRole('menuitem',{name:'Version history'})).toHaveAttribute('href','/artifacts/diagnostic-page')
 })
 it('explains Take over editing in the bar in one sentence, on keyboard focus',async()=>{
  await mount('xlsx',bar)
  act(()=>within(bar).getByRole('button',{name:'Take over editing'}).focus())
  expect(screen.getByRole('tooltip')).toHaveTextContent('You start editing the workbook; the agent does not write to it meanwhile.')
 })
 it('explains the saved-values note in the menu in one sentence, on hover',async()=>{
  await mount('xlsx',bar)
  await openMenu()
  const note=within(screen.getByRole('menu')).getByText(SAVED_NOTE)
  fireEvent.pointerMove(note,{pointerType:'mouse'})
  await settle(400)
  expect(screen.getByRole('tooltip')).toHaveTextContent('Values come from the saved file and are recalculated after your first change.')
 })
 it('takes over from the bar, and says in the menu that a download leaves out unsaved changes',async()=>{
  await mount('xlsx',bar)
  client.leaseAction.mockImplementation(async(action:string)=>{if(action==='acquire')client.lease.mockResolvedValue(owned);return owned})
  fireEvent.click(within(bar).getByRole('button',{name:'Take over editing'}))
  await settle()
  expect(within(bar).getByRole('button',{name:'Hand back'})).toBeInTheDocument()
  await change();await settle(200)
  await openMenu()
  expect(screen.getByRole('menuitem',{name:/^Download \.xlsx/})).toHaveTextContent('Last saved version, without unsaved changes')
  expect(screen.queryByText('The download is the last saved version; your unsaved changes are not in it.')).toBeNull()
 })
 it('says nothing about saved values once the editor calculated the workbook',async()=>{
  opened.calculated=true;await mount('xlsx',bar)
  await openMenu()
  expect(screen.getByRole('menu')).not.toHaveTextContent(SAVED_NOTE)
 })
 it('drops the saved-values note once the editor has calculated the whole workbook',async()=>{
  await mount('xlsx',bar)
  await act(async()=>{wires.at(-1)!.onmessage?.({data:{type:'gridcraft:changed',document:'doc',revision:'1',dirty:false,calculated:true}})})
  await settle(100)
  await openMenu()
  expect(screen.getByRole('menu')).not.toHaveTextContent(SAVED_NOTE)
 })
 // The agent changes the Sheet open beside the chat, and the person sees it there.
 it('while the agent edits, says so in the bar and opens each version the agent saves, without asking',async()=>{
  const agent={...vacant,holder:{actor_id:'agent:morgan',display_name:'Morgan',kind:'agent'},generation:'2',acquired_at:'2026-01-01T00:00:00Z',expires_at:'2099-01-01T00:00:00Z'}
  client=makeClient('xlsx');mock.client.mockReturnValue(client);client.lease.mockResolvedValue(agent)
  render(<NativeCraftEditorHost workspaceId="diagnostic-workspace" pageId="diagnostic-page" humanId="diagnostic-human" enabled toolbar={bar} historyHref="/a" />)
  await settle()
  expect(within(bar).getByRole('toolbar',{name:'Document editing'})).toHaveTextContent('Morgan is editing')
  // A name cut to "Fr…" said nothing; the bar shows who as an icon, named for every reader.
  expect(within(bar).getByTitle('Morgan is editing'),'the full name on hover').toBeInTheDocument()
  expect(within(bar).queryByRole('button',{name:'Take over editing'}),'nothing the person may not do').toBeNull()
  const opens=()=>sent.filter(m=>m.type==='gridcraft:open').length
  const before=opens()
  const read=await client.read.mock.results[0].value as Record<string,unknown>
  client.read.mockResolvedValue({...read,version:'4',bytes:new Uint8Array([0x50,0x4b,4,4]),contentHash:'sha256:'+'4'.repeat(64)})
  await act(async()=>{publishAgentVersion({pageId:'diagnostic-page',version:'4'})})
  await settle(1000)
  expect(opens(),'the agent version was opened').toBe(before+1)
  expect(sent.filter(m=>m.type==='gridcraft:open').at(-1)?.bytes).toBeDefined()
  expect(screen.queryByRole('alertdialog')).toBeNull()
  expect(screen.queryByText(/Reload the page/)).toBeNull()
 })
 it('offers Publish preview only when the server lets this person share',async()=>{
  await mount('xlsx',bar)
  await openMenu()
  expect(screen.getAllByRole('menuitem').length,'the menu opened').toBeGreaterThan(0)
  expect(screen.queryByRole('menuitem',{name:'Publish preview'})).toBeNull()
 })
})
