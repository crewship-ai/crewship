// Concurrent editing in a Sheet stored as its workbook file: the harness of native-craft-sheet-file.test.tsx.
import {webcrypto} from 'node:crypto'
import {act,cleanup,fireEvent,render,screen,within} from '@testing-library/react'
import {afterEach,beforeEach,describe,expect,it,vi} from 'vitest'
import type {GridcraftNativePortOptions} from '@/lib/mooring/gridcraft-native-port'
import type {NativeEditorPort,NativeStructuredPort} from '@/lib/mooring/native-editor-port'
import {publishAgentVersion} from '@/lib/mooring/native-agent-version'
import {NativeSaveError} from '@/lib/mooring/single-editor-save'
import {NativeCraftEditorHost,fidelityPartsText,nativeOpenBudget,sheetWarningText,workbookFile} from '../native-craft-editor-host'

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
  lease:vi.fn().mockResolvedValue(vacant),leaseAction:vi.fn(async()=>vacant),
  events:vi.fn(()=>new Promise<void>(()=>{})),reauthorize:vi.fn().mockResolvedValue(true),proposals:vi.fn().mockResolvedValue([]),close:vi.fn(),save:vi.fn(),
  exportXlsx:vi.fn(async(_signal:AbortSignal,version?:string)=>({blob:new Blob(['PK']),filename:`Plan.${format}`,version:version??'3'})),
 }
}
let executed:Record<string,unknown>|null=null
let client:ReturnType<typeof makeClient>
// Fake timers, as in native-craft-startup: the host's deadlines and polls advance with the test, and hashes it started settle in between.
const digest=webcrypto.subtle.digest.bind(webcrypto.subtle)
let hashes:Promise<ArrayBuffer>[],network:ReturnType<typeof vi.fn>
async function settle(ms=500){let remaining=ms;do{const step=Math.min(100,remaining);await act(async()=>{await vi.advanceTimersByTimeAsync(step);await Promise.all(hashes)});remaining-=step}while(remaining>0)}
async function mount(format:'xlsx'|'xlsm'='xlsx',toolbar?:HTMLElement){
 client=makeClient(format);mock.client.mockReturnValue(client)
 const warn=vi.spyOn(console,'warn')
 render(<NativeCraftEditorHost workspaceId="diagnostic-workspace" pageId="diagnostic-page" humanId="diagnostic-human" enabled {...(toolbar?{toolbar,historyHref:'/artifacts/diagnostic-page'}:{})} />)
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
 refuse=null;refuseFile=false;executed=null;wires=[];revision='1';sent=[];adapters=[];seen=[];opened={calculated:false,warnings:[]}
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
  const frame={postMessage(){queueMicrotask(()=>wire.onmessage?.({data:{type:'gridcraft:ready',version:'diagnostic',accessProtocol:1,readOnly:true,build:ENGINE_BUILD}}))}}
  const port=createGridcraftNativePort({...options,frame,createChannel:()=>({port1:wire,port2:{}})})
  adapters.push(port)
  queueMicrotask(()=>port.handleWindowMessage({source:frame,origin:'null',data:{type:'gridcraft:hello',protocol:2,bundle:options.bundle}}))
  return port
 })
})
afterEach(()=>{cleanup();vi.useRealTimers();vi.unstubAllGlobals();vi.restoreAllMocks();vi.clearAllMocks()})

// Concurrent editing in the host: what the person sees when others saved meanwhile.
// Layout fallback with Keep mine, a save refused as moved, and the 3-try limit.
const owned={...vacant,holder:{actor_id:'user:diagnostic-human',display_name:'QA',kind:'human'},mine:true,generation:'1',can_write:true,acquired_at:'2026-01-01T00:00:00Z',expires_at:'2099-01-01T00:00:00Z'}
const PIN='a'.repeat(64),HEAD=(n:number)=>'sha256:'+String(n).repeat(64).slice(0,64)
const change=async()=>{revision=String(Number(revision)+1);await act(async()=>{wires.at(-1)!.onmessage?.({data:{type:'gridcraft:changed',document:'doc',revision,dirty:true}})})}
async function editing(){
 await mount()
 client.leaseAction.mockImplementation(async(action:string)=>{if(action==='acquire')client.lease.mockResolvedValue(owned);return owned})
 client.save.mockImplementation(async(r:Record<string,unknown>)=>({...r,bytes:undefined,version:(BigInt(r.baseVersion as string)+1n).toString(),candidateHash:r.contentHash,durability:'node',issuedBy:'crewship',phase:'committed'}))
 fireEvent.click(screen.getByRole('button',{name:'Take over editing'}))
 await settle()
}
// The operations log from `from` to from+1: a layout change (not replayable), or one replayable cell.
function serveLog(kind:'layout'|'cell'){
 const calls:string[]=[]
 network.mockImplementation(async(input:RequestInfo|URL)=>{
  const url=String(input)
  if(!url.includes('/craft/document/operations?'))return new Response(null,{status:404})
  const from=new URL(url,'http://x').searchParams.get('from')!;calls.push(from)
  const head=String(Number(from)+1)
  const version={version:head,base_version:from,actor_kind:'agent',actor_name:'Morgan',operation_id:`op-${head}`,engine_pin:PIN,replayable:kind==='cell',...(kind==='cell'?{ops:[{command:'cell.set',params:{cell:"'L'!C1",input:'x'}}]}:{})}
  return new Response(JSON.stringify({from,to:head,head,head_content_hash:HEAD(Number(head)),replayable:kind==='cell',versions:[version]}),{status:200})
 })
 return calls
}
// The conflict line: one sentence, detail folded.
const MOVED_ALERT=/Not saved: this Sheet changed on the server meanwhile\./

describe('concurrent editing in the open Sheet',()=>{
 it('offers Keep mine over a layout change and saves the person\'s work on top of it as it is',async()=>{
  await editing();await change()
  serveLog('layout')
  await act(async()=>{publishAgentVersion({pageId:'diagnostic-page',version:'4'})})
  await settle(1000)
  const note=screen.getByTestId('agent-rebase-note')
  expect(note).toHaveTextContent("Morgan saved version 4, which changes the sheet's layout, so it was not added to your unsaved changes. Your changes are kept.")
  // Before the choice, the card says what Keep mine costs.
  expect(note).toHaveTextContent("If you keep yours, Morgan's change in version 4 will not be in the Sheet; it stays in the history.")
  expect(sent.some(m=>m.type==='gridcraft:execute'),'a layout change is never replayed').toBe(false)
  fireEvent.click(within(note).getByRole('button',{name:'Keep mine'}))
  await settle(1000)
  expect(screen.getByTestId('agent-rebase-note')).toHaveTextContent("Your changes are saved on top. Morgan's change from version 4 is not in the Sheet; it stays in version 4 in the history.")
  expect(client.save).toHaveBeenCalled()
  expect(client.save.mock.calls.at(-1)![0]).toMatchObject({baseVersion:'4',rebase:'keep-local'})
 })

 // In a 410 px panel the sentence shared one flex row with the buttons, got no width and wrapped one word per line.
 it('puts the layout sentence on its own row and the actions below it, so the text never shrinks to a column',async()=>{
  await editing();await change()
  serveLog('layout')
  await act(async()=>{publishAgentVersion({pageId:'diagnostic-page',version:'4'})})
  await settle(1000)
  const note=screen.getByTestId('agent-rebase-note')
  const sentence=within(note).getByText(/changes the sheet's layout/)
  const keep=within(note).getByRole('button',{name:'Keep mine'})
  expect(sentence.parentElement,'the sentence is not a flex sibling of the buttons').not.toBe(keep.parentElement)
  expect(sentence.compareDocumentPosition(keep)&Node.DOCUMENT_POSITION_FOLLOWING,'the actions come after the sentence').toBeTruthy()
  expect(within(note).getByRole('button',{name:'Dismiss'}).parentElement).toBe(keep.parentElement)
 })

 // RED: RebaseNote keeps its own "kept" state across notes, so a second layout change is announced as already kept.
 it('asks again for a second layout change after Keep mine, instead of saying it was kept',async()=>{
  await editing();await change()
  serveLog('layout')
  await act(async()=>{publishAgentVersion({pageId:'diagnostic-page',version:'4'})})
  await settle(1000)
  fireEvent.click(within(screen.getByTestId('agent-rebase-note')).getByRole('button',{name:'Keep mine'}))
  await settle(1000)
  expect(client.save.mock.calls.at(-1)![0]).toMatchObject({baseVersion:'4'})
  // The person types again; the agent saves another layout change on top of the person's version 5.
  await change()
  await act(async()=>{publishAgentVersion({pageId:'diagnostic-page',version:'6'})})
  await settle(1000)
  const note=screen.getByTestId('agent-rebase-note')
  expect(note).toHaveTextContent("Morgan saved version 6, which changes the sheet's layout")
  expect(note).not.toHaveTextContent('Your changes are saved on top')
  expect(within(note).queryByRole('button',{name:'Keep mine'})).not.toBeNull()
 })

 it('answers a save refused as moved by replaying what was saved meanwhile and saving on top',async()=>{
  await editing();const saved=revision;await change()
  const calls=serveLog('cell')
  executed={document:'doc',revision:String(Number(revision)+1),applied:1}
  let first=true
  const committed=client.save.getMockImplementation()!
  client.save.mockImplementation(async(r:Record<string,unknown>)=>{if(first){first=false;throw new NativeSaveError('moved')}return committed(r)})
  await settle(12000)
  expect(calls,'the log after the refused base was read').toEqual(['3'])
  expect(sent.filter(m=>m.type==='gridcraft:execute')).toHaveLength(1)
  // Keep the person's cells, one undo step with a name, on the revision of the SAVED version.
  const replay=sent.find(m=>m.type==='gridcraft:execute')!
  expect(replay).toMatchObject({keep:'human',baseRevision:saved})
  expect(String(replay.label).length).toBeGreaterThan(0);expect(String(replay.label).length).toBeLessThanOrEqual(80)
  await settle(12000)
  expect(client.save.mock.calls.at(-1)![0]).toMatchObject({baseVersion:'4'})
  expect(screen.queryByText(MOVED_ALERT),'nothing is left unsaved').toBeNull()
  expect(document.querySelector('[data-save-phase]')).toHaveTextContent('Saved')
 })

 it('stops replaying after 3 refusals in a row and shows the conflict',async()=>{
  await editing();await change()
  const calls=serveLog('cell')
  executed={document:'doc',revision:'99',applied:1}
  client.save.mockImplementation(async()=>{throw new NativeSaveError('moved')})
  for(let i=0;i<8;i++)await settle(12000)
  expect(calls.length,'at most 3 rebases after moved').toBe(3)
  expect(screen.getByText(MOVED_ALERT)).toBeInTheDocument()
 })

 // RED: after a moved save over a layout change the page shows two answers at once.
 it('does not tell the person that saving again is refused while it offers Keep mine for the same change',async()=>{
  await editing();await change()
  serveLog('layout')
  let first=true
  const committed=client.save.getMockImplementation()!
  client.save.mockImplementation(async(r:Record<string,unknown>)=>{if(first){first=false;throw new NativeSaveError('moved')}return committed(r)})
  await settle(12000)
  const note=screen.getByTestId('agent-rebase-note')
  expect(within(note).getByRole('button',{name:'Keep mine'})).toBeInTheDocument()
  expect(screen.queryByText(/Saving again here would be refused/),'Keep mine and "saving again is refused" contradict each other').toBeNull()
 })
})

// "Open the agent's version" opens exactly that version in Artifacts.
import {agentVersionHref} from '../native-craft-editor-host'
// The editor and the server run the same engine build unless a test says otherwise.
const ENGINE_BUILD='0.19.1+59f6b6c14a37 patch '+'d'.repeat(64)
let serverBuild:string|undefined=ENGINE_BUILD
it('links the agent\'s version itself, not only the history',()=>{
 expect(agentVersionHref('/artifacts/p?workspace_id=w','4')).toBe('/artifacts/p?workspace_id=w&version=4')
 expect(agentVersionHref('/artifacts/p','12')).toBe('/artifacts/p?version=12')
})

// After a replay over unsaved work the saved version is the agent's, named apart. While the
// person's work is still unsaved (their save refused again as moved), the next replay is based on that saved version.
it('bases the next replay on the saved version the last replay reported',async()=>{
 await editing();await change()
 serveLog('cell')
 executed={document:'doc',revision:String(Number(revision)+1),savedRevision:'saved-1',applied:1}
 let refusals=2
 const committed=client.save.getMockImplementation()!
 client.save.mockImplementation(async(r:Record<string,unknown>)=>{if(refusals>0){refusals--;throw new NativeSaveError('moved')}return committed(r)})
 await settle(12000);await settle(12000)
 const replays=sent.filter(m=>m.type==='gridcraft:execute')
 expect(replays).toHaveLength(2)
 expect(replays[1]).toMatchObject({keep:'human',baseRevision:'saved-1'})
 expect(client.save.mock.calls.at(-1)![0]).toMatchObject({baseVersion:'5'})
})

// Another engine build on the server: nothing is replayed, the person gets the choice.
it('replays nothing when the server runs another engine build, and offers the choice',async()=>{
 serverBuild='0.19.1+59f6b6c14a37 patch '+'e'.repeat(64)
 try{
  await editing();await change()
  serveLog('cell')
  await act(async()=>{publishAgentVersion({pageId:'diagnostic-page',version:'4'})})
  await settle(1000)
  expect(sent.some(m=>m.type==='gridcraft:execute')).toBe(false)
  expect(screen.getByTestId('agent-rebase-note')).toHaveTextContent('Keep mine')
 }finally{serverBuild=ENGINE_BUILD}
})
