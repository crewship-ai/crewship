import {webcrypto} from "node:crypto"
import {render,screen,fireEvent,waitFor,act,within} from "@testing-library/react"
import {beforeEach,afterEach,describe,it,expect,vi} from "vitest"
import type {NativeEditorPortOptions} from "@/lib/mooring/native-editor-port"
import {NativeSaveError,type NativeSaveRequest} from "@/lib/mooring/single-editor-save"
import {NativeCraftEditorHost,nativeCraftFrameURL} from "../native-craft-editor-host"

const m=vi.hoisted(()=>({apiFetch:vi.fn(),createClient:vi.fn(),openJournal:vi.fn(),createPort:vi.fn(),registerPrivacy:vi.fn(),purge:vi.fn()}))
vi.mock("@/lib/api-fetch",()=>({apiFetch:m.apiFetch}))
vi.mock("@/lib/mooring/native-document-client",()=>({createNativeDocumentClient:m.createClient}))
vi.mock("@/lib/mooring/native-editor-port",()=>({createNativeEditorPort:m.createPort}))
vi.mock("@/lib/mooring/native-draft",async()=>({...await vi.importActual("@/lib/mooring/native-draft"),openNativeDraftJournal:m.openJournal}))
vi.mock("@/lib/document-queue",async()=>({...await vi.importActual("@/lib/document-queue"),registerDocumentDraftWriter:m.registerPrivacy,purgeDocumentDrafts:m.purge}))
vi.mock("../workbook-editor-host",()=>({editorFrameSupported:()=>true,createEditorFrame:(doc:Document,src:string,onLoad:()=>void)=>{const f=doc.createElement('iframe');f.setAttribute('sandbox','allow-scripts');f.setAttribute('credentialless','');f.setAttribute('referrerpolicy','no-referrer');f.srcdoc='';f.src=src;queueMicrotask(onLoad);return f}}))
const deferred=<T,>()=>{let resolve!:(v:T)=>void;const promise=new Promise<T>(r=>resolve=r);return {promise,resolve}}
const emptyLease={can_edit:true,holder:null,mine:false,generation:null,can_write:false,acquired_at:null,expires_at:null,handoff_requested_by:[]}
const ownedLease={...emptyLease,holder:{actor_id:"user:human",display_name:"Pavel",kind:"human"},mine:true,generation:"9007199254740995",can_write:true,expires_at:'2099-01-01T00:00:00Z'}
const receipt=(r:NativeSaveRequest)=>({...r,bytes:undefined,version:(BigInt(r.baseVersion)+1n).toString(),candidateHash:r.contentHash,durability:"node",issuedBy:"crewship",phase:"committed"})
let authSuspend:(()=>Promise<void>)|undefined
let privacyRetire:(()=>Promise<void>)|undefined
let client:ReturnType<typeof clientRig>,journal:ReturnType<typeof journalRig>,ports:ReturnType<typeof portRig>[],portOptions:NativeEditorPortOptions[],stream:((event:"lease"|"session_retired",value:Record<string,unknown>)=>void)|undefined
function clientRig(){return {share:vi.fn(async(r:{targetVersion:string})=>({version:r.targetVersion,durability:'node',issuedBy:'crewship',phase:'shared'})),open:vi.fn().mockResolvedValue({sessionId:"private-session",applicationId:"wordcraft"}),read:vi.fn().mockResolvedValue({version:"1",bytes:new TextEncoder().encode('{"body":[]}'),applicationId:"wordcraft",enginePin:"a".repeat(64),editorBundle:"b".repeat(64),editorURL:`https://native.example/b/${"b".repeat(64)}/index.html`,quota:{usedBytes:"12",limitBytes:"99999",warning:false,retention:"keep-all"}}),lease:vi.fn().mockResolvedValue(emptyLease),leaseAction:vi.fn(async(action:string):Promise<unknown>=>{if(action==='acquire')client.lease.mockResolvedValue(ownedLease);return ownedLease}),events:vi.fn((_signal:AbortSignal,callback:typeof stream)=>{stream=callback;return new Promise<void>(()=>{})}),reauthorize:vi.fn().mockResolvedValue(true),save:vi.fn(async(r:NativeSaveRequest)=>receipt(r)),reconcile:vi.fn(),close:vi.fn(),proposals:vi.fn().mockResolvedValue([]),proposalBytes:vi.fn(),decideProposal:vi.fn()}}
function journalRig(){return {persist:vi.fn().mockResolvedValue(undefined),confirm:vi.fn().mockResolvedValue(undefined),recovery:vi.fn().mockResolvedValue([]),onRetire:vi.fn(()=>()=>{}),close:vi.fn().mockResolvedValue(undefined),revokeRead:vi.fn().mockResolvedValue(undefined)}}
function portRig(){return {loaded:vi.fn(),handleWindowMessage:vi.fn(),bootstrap:vi.fn().mockResolvedValue({bytes:new TextEncoder().encode('{"body":[]}'),editorRevision:"initial",documentId:"doc"}),snapshot:vi.fn().mockResolvedValue({bytes:new TextEncoder().encode('{"body":[1]}'),editorRevision:"snapshot-1",documentId:"doc"}),validate:vi.fn().mockResolvedValue(undefined),ack:vi.fn().mockResolvedValue(true),saveFailed:vi.fn(),setReadOnly:vi.fn(async(v:boolean)=>v),close:vi.fn()}}
beforeEach(()=>{vi.stubGlobal('crypto',webcrypto);client=clientRig();journal=journalRig();ports=[];portOptions=[];stream=undefined;privacyRetire=undefined;authSuspend=undefined;m.purge.mockResolvedValue(undefined);m.registerPrivacy.mockImplementation((_human:string,retire:()=>Promise<void>,options?:{suspend?:()=>Promise<void>})=>{privacyRetire=retire;authSuspend=options?.suspend;return vi.fn()});m.createClient.mockReturnValue(client);m.openJournal.mockResolvedValue(journal);m.createPort.mockImplementation((o:NativeEditorPortOptions)=>{const p=portRig();ports.push(p);portOptions.push(o);queueMicrotask(()=>o.onReady?.('qualified'));return p})})
afterEach(()=>{vi.unstubAllGlobals();vi.clearAllMocks()})
const mount=()=>render(<NativeCraftEditorHost workspaceId="w" pageId="p" humanId="human" enabled />)
async function ready(){await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalledOnce());await waitFor(()=>expect(screen.getByRole('button',{name:'Převzít úpravy'})).toBeEnabled())}
async function acquire(){fireEvent.click(screen.getByRole('button',{name:'Převzít úpravy'}));await waitFor(()=>expect(ports.at(-1)?.setReadOnly).toHaveBeenCalledWith(false,expect.any(AbortSignal)))}

describe('native Word host authority and persistence',()=>{
 it('keeps the live native frame after failed auth capture and retries local persistence without a save',async()=>{
  mount();await ready();await acquire();const frame=document.querySelector('iframe')
  journal.persist.mockRejectedValueOnce(Error('storage failed'))
  let pending!:Promise<void>
  act(()=>{portOptions.at(-1)!.onChanged('pending-at-expiry','doc');pending=authSuspend!()})
  await expect(pending).rejects.toThrow('native_auth_draft_capture_unconfirmed')
  expect(document.querySelector('iframe')).toBe(frame);expect(journal.close).not.toHaveBeenCalled()
  expect(client.save).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()
  await act(async()=>{await authSuspend!()})
  expect(journal.persist).toHaveBeenCalledTimes(2);expect(journal.close).toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled()
 })
 it('publishes only the final saved and acknowledged version under current custody',async()=>{
  mount();await ready();expect(screen.queryByRole('button',{name:'Publikovat náhled'})).not.toBeInTheDocument();await acquire()
  const ack=deferred<boolean>();ports.at(-1)!.ack.mockReturnValueOnce(ack.promise)
  act(()=>portOptions.at(-1)!.onChanged('last-before-preview','doc'))
  fireEvent.click(screen.getByRole('button',{name:'Publikovat náhled'}))
  await waitFor(()=>expect(ports.at(-1)!.ack).toHaveBeenCalledOnce());expect(client.share).not.toHaveBeenCalled()
  await act(async()=>ack.resolve(true))
  await waitFor(()=>expect(client.share).toHaveBeenCalledOnce())
  expect(client.share).toHaveBeenCalledWith({workspaceId:'w',pageId:'p',applicationId:'wordcraft',enginePin:'a'.repeat(64),sessionId:'private-session',generation:'9007199254740995',baseVersion:'2',targetVersion:'2'},expect.any(AbortSignal))
  await screen.findByText('Sdílený náhled · verze 2')
  expect(client.save.mock.invocationCallOrder[0]).toBeLessThan(client.share.mock.invocationCallOrder[0])
 })
 it('does not publish an unconfirmed final save and keeps the native draft',async()=>{
  mount();await ready();await acquire();client.save.mockRejectedValueOnce(Error('lost ack'))
  fireEvent.click(screen.getByRole('button',{name:'Publikovat náhled'}))
  await screen.findByRole('alert');expect(client.share).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled();expect(document.querySelector('iframe')).not.toBeNull()
 })
 it('never retries an ambiguous preview publication or changes native data',async()=>{
  mount();await ready();await acquire();client.share.mockRejectedValueOnce(Error('lost publication reply'))
  fireEvent.click(screen.getByRole('button',{name:'Publikovat náhled'}));await screen.findByRole('alert')
  expect(client.share).toHaveBeenCalledOnce();expect(client.save).toHaveBeenCalledOnce();expect(journal.revokeRead).not.toHaveBeenCalled();expect(document.querySelector('iframe')).not.toBeNull()
 })

 it.each([false,undefined])('shows a read-only preview with no custody controls unless server confirms edit capability (%s)',async(canEdit)=>{
  client.lease.mockResolvedValue({...emptyLease,can_edit:canEdit})
  mount();await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalledOnce())
  expect(screen.queryByRole('button',{name:'Převzít úpravy'})).not.toBeInTheDocument()
  expect(screen.queryByRole('button',{name:'Požádat o předání'})).not.toBeInTheDocument()
  expect(client.leaseAction).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled();expect(client.share).not.toHaveBeenCalled();expect(screen.queryByRole('button',{name:'Publikovat náhled'})).not.toBeInTheDocument();expect(m.openJournal).not.toHaveBeenCalled()
  act(()=>stream?.('lease',{...ownedLease,mine:false,can_write:false,can_edit:canEdit}))
  await screen.findByText('Upravuje Pavel')
  expect(screen.queryByRole('button',{name:'Požádat o předání'})).not.toBeInTheDocument()
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
  expect(m.openJournal).toHaveBeenCalledWith({human:'human',workspace:'w',page:'p',applicationId:'gridcraft'})
  ports.at(-1)!.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"sheets":[1]}'),editorRevision:'grid:2',documentId:'doc'})
  act(()=>portOptions.at(-1)!.onSaveRequest({documentId:'doc',editorRevision:'grid:2'}))
  await waitFor(()=>expect(client.save).toHaveBeenCalledOnce())
  expect(client.save.mock.calls[0][0].applicationId).toBe('gridcraft')
  await screen.findByText('Uloženo')
 })

 it('stops only the current agent mandate with one H1 request and reloads its fresh version without acquiring',async()=>{
  const agent={...ownedLease,mine:false,can_write:false,can_stop_agent:true,holder:{actor_id:'agent:actual',display_name:'Jamie',kind:'agent'}}
  client.lease.mockResolvedValue(agent)
  const stopped=deferred<Response>();m.apiFetch.mockReturnValueOnce(stopped.promise)
  mount();await screen.findByRole('button',{name:'Zastavit agenta'});await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled())
  fireEvent.click(screen.getByRole('button',{name:'Zastavit agenta'}))
  expect(screen.getByRole('button',{name:'Zastavit agenta'})).toBeDisabled()
  expect(m.apiFetch).toHaveBeenCalledWith('/api/v1/pages/%40p/craft/tasks/cancel?workspace_id=w',expect.objectContaining({method:'POST',body:'{}',skipRefresh:true,redirect:'error',signal:expect.any(AbortSignal)}))
  const fresh={...await client.read(),version:'2',bytes:new TextEncoder().encode('{"body":["agent"]}')}
  client.read.mockResolvedValue(fresh);client.lease.mockResolvedValue(emptyLease)
  await act(async()=>stopped.resolve(new Response(JSON.stringify({stop_pending:true}),{status:202})))
  await waitFor(()=>expect(ports).toHaveLength(2))
  expect(ports[1].bootstrap).toHaveBeenCalledWith(fresh.bytes,expect.any(AbortSignal))
  expect(m.apiFetch).toHaveBeenCalledOnce();expect(client.leaseAction).not.toHaveBeenCalled();expect(client.save).not.toHaveBeenCalled();expect(m.openJournal).not.toHaveBeenCalled()
  expect(screen.queryByText('Agent zastaven')).not.toBeInTheDocument();expect(screen.queryByText('Uloženo')).not.toBeInTheDocument()
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
  expect(screen.getByRole('button',{name:'Převzít úpravy'})).toBeDisabled()
  fireEvent.click(screen.getByRole('button',{name:'Převzít úpravy'}))
  expect(client.leaseAction).not.toHaveBeenCalled()
  await act(async()=>opening.resolve({bytes:newer.bytes,editorRevision:'reader:2',documentId:'doc'}))
  await waitFor(()=>expect(screen.getByRole('button',{name:'Převzít úpravy'})).toBeEnabled())
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
  expect(m.openJournal).toHaveBeenCalledWith({human:'human',workspace:'w',page:'p',applicationId:'gridcraft'})
  ports.at(-1)!.snapshot.mockResolvedValue({bytes:new TextEncoder().encode('{"sheets":[1]}'),editorRevision:'grid:2',documentId:'doc'})
  act(()=>portOptions.at(-1)!.onSaveRequest({documentId:'doc',editorRevision:'grid:2'}))
  await waitFor(()=>expect(client.save).toHaveBeenCalledOnce())
  expect(client.save.mock.calls[0][0].applicationId).toBe('gridcraft')
  await screen.findByText('Uloženo')
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
   expect(client.save).not.toHaveBeenCalled();expect(ports.at(-1)!.ack).not.toHaveBeenCalled();expect(screen.queryByText('Uloženo')).not.toBeInTheDocument()
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
 it('shows the exact named editor to another human without acquiring a write',async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false});mount();await screen.findByText('Upravuje Pavel');expect(screen.getByRole('button',{name:'Požádat o předání'})).toBeEnabled();expect(client.leaseAction).not.toHaveBeenCalled();expect(m.openJournal).not.toHaveBeenCalled();expect(ports.some(p=>p.setReadOnly.mock.calls.some(c=>c[0]===false))).toBe(false)})
 it('cannot unlock from an acquire response without a fresh matching current lease proof',async()=>{mount();await ready();client.leaseAction.mockImplementationOnce(async()=>ownedLease);fireEvent.click(screen.getByRole('button',{name:'Převzít úpravy'}));await waitFor(()=>expect(client.leaseAction).toHaveBeenCalled());await waitFor(()=>expect(screen.getByRole('alert')).toBeInTheDocument());expect(ports.some(p=>p.setReadOnly.mock.calls.some(c=>c[0]===false))).toBe(false);expect(client.save).not.toHaveBeenCalled()})
 it('reports Uloženo only after durable local persist, engine validation, server receipt and editor ACK',async()=>{mount();await ready();await acquire();const p=ports.at(-1)!,ack=deferred<boolean>();p.ack.mockReturnValueOnce(ack.promise);act(()=>portOptions.at(-1)!.onChanged('r2','doc'));await waitFor(()=>expect(p.ack).toHaveBeenCalledOnce());expect(journal.persist.mock.invocationCallOrder[0]).toBeLessThan(p.validate.mock.invocationCallOrder[0]);expect(p.validate.mock.invocationCallOrder[0]).toBeLessThan(client.save.mock.invocationCallOrder[0]);expect(screen.queryByText('Uloženo')).not.toBeInTheDocument();await act(async()=>ack.resolve(true));await screen.findByText('Uloženo')})
 it('retains the frozen engine and tells the truth when native failure precedes idle capture',async()=>{journal.recovery.mockResolvedValueOnce([{draft:{applicationId:'wordcraft',enginePin:'a'.repeat(64),sessionId:'old',generation:'1',baseVersion:'1',editorRevision:'old',bytes:new Uint8Array([99])},actions:['compare','copy']}]);mount();await ready();await acquire();const p=ports.at(-1)!,frame=document.querySelector('iframe');act(()=>{portOptions.at(-1)!.onChanged('not-yet-durable','doc');portOptions.at(-1)!.onError('native_port_closed')});await screen.findByText('Zachycení posledních změn nebylo potvrzeno');expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveAttribute('inert');expect(p.close).toHaveBeenCalled();expect(p.snapshot).not.toHaveBeenCalled();expect(journal.persist).not.toHaveBeenCalled();expect(journal.close).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled();expect(client.close).not.toHaveBeenCalled();expect(screen.getByRole('button',{name:'Kopie konceptu'})).toBeInTheDocument();expect(screen.queryByText('Uloženo')).not.toBeInTheDocument();expect(screen.queryByText(/koncept zachován/i)).not.toBeInTheDocument()})
 it('retains concurrent pending text after editor receipt acknowledgement fails',async()=>{mount();await ready();await acquire();const p=ports.at(-1)!,ack=deferred<boolean>(),frame=document.querySelector('iframe');p.ack.mockReturnValueOnce(ack.promise);act(()=>portOptions.at(-1)!.onChanged('r1','doc'));await waitFor(()=>expect(p.ack).toHaveBeenCalledOnce());act(()=>portOptions.at(-1)!.onChanged('r2-before-idle','doc'));await act(async()=>ack.resolve(false));await screen.findByText('Zachycení posledních změn nebylo potvrzeno');expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveAttribute('inert');expect(journal.close).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled();expect(client.close).not.toHaveBeenCalled();expect(screen.queryByText('Uloženo')).not.toBeInTheDocument()})
 it('explicit read revocation after fatal native failure detaches immediately and purges while journal custody is held',async()=>{mount();await ready();await acquire();const purge=deferred<void>(),p=ports.at(-1)!;journal.revokeRead.mockReturnValueOnce(purge.promise);act(()=>{portOptions.at(-1)!.onChanged('unconfirmed','doc');portOptions.at(-1)!.onError('native_port_closed')});await screen.findByText('Zachycení posledních změn nebylo potvrzeno');expect(journal.close).not.toHaveBeenCalled();act(()=>stream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'read_revoked'}));expect(document.querySelector('iframe')).toBeNull();await waitFor(()=>expect(journal.revokeRead).toHaveBeenCalledOnce());expect(journal.close).not.toHaveBeenCalled();expect(m.purge).not.toHaveBeenCalled();expect(p.snapshot).not.toHaveBeenCalled();expect(screen.queryByRole('button',{name:'Kopie konceptu'})).not.toBeInTheDocument();await act(async()=>purge.resolve())})
 it('the registered privacy retirement removes a fatal frame before awaiting private journal purge',async()=>{mount();await ready();await acquire();const purge=deferred<void>();journal.revokeRead.mockReturnValueOnce(purge.promise);act(()=>portOptions.at(-1)!.onError('native_port_closed'));await screen.findByText('Zachycení posledních změn nebylo potvrzeno');expect(m.registerPrivacy).toHaveBeenCalledWith('human',expect.any(Function),expect.objectContaining({suspend:expect.any(Function)}));let retired!:Promise<void>;act(()=>{retired=privacyRetire!()});expect(document.querySelector('iframe')).toBeNull();expect(journal.revokeRead).toHaveBeenCalledOnce();expect(m.purge).not.toHaveBeenCalled();await act(async()=>{purge.resolve();await retired})})
 it('late lease or ACK callbacks and an unregistered old privacy callback cannot revive or purge a fresh mount',async()=>{const first=mount();await ready();await acquire();const p=ports.at(-1)!,oldPrivacy=privacyRetire!,oldOptions=portOptions.at(-1)!,frame=document.querySelector('iframe');act(()=>oldOptions.onError('native_port_closed'));await screen.findByText('Zachycení posledních změn nebylo potvrzeno');act(()=>{stream?.('lease',ownedLease);oldOptions.onReady?.('late');oldOptions.onChanged('late-revision','doc')});expect(document.querySelector('iframe')).toBe(frame);expect(frame).toHaveAttribute('inert');expect(p.setReadOnly.mock.calls.filter(c=>c[0]===false)).toHaveLength(1);first.unmount();expect(m.registerPrivacy.mock.results[0].value).toHaveBeenCalledOnce();mount();await waitFor(()=>expect(ports.at(-1)!.bootstrap).toHaveBeenCalled());const fresh=document.querySelector('iframe');await act(async()=>oldPrivacy());expect(document.querySelector('iframe')).toBe(fresh);expect(journal.revokeRead).not.toHaveBeenCalled();expect(m.purge).not.toHaveBeenCalled()})
 it('detaches bootstrap failures that never acquired a writable journal',async()=>{const boot=deferred<{bytes:Uint8Array;editorRevision:string;documentId:string}>();m.createPort.mockImplementationOnce((o:NativeEditorPortOptions)=>{const p=portRig();p.bootstrap.mockReturnValueOnce(boot.promise);ports.push(p);portOptions.push(o);queueMicrotask(()=>o.onReady?.('qualified'));return p});mount();await waitFor(()=>expect(ports[0]?.bootstrap).toHaveBeenCalled());act(()=>portOptions[0].onError('bootstrap_failure'));expect(document.querySelector('iframe')).toBeNull();expect(m.openJournal).not.toHaveBeenCalled();expect(client.close).toHaveBeenCalled();expect(screen.queryByText('Zachycení posledních změn nebylo potvrzeno')).not.toBeInTheDocument()})
 it('reuses the same pending privacy barrier until exact journal purge finishes',async()=>{mount();await ready();await acquire();const purge=deferred<void>();journal.revokeRead.mockReturnValueOnce(purge.promise);let first!:Promise<void>,second!:Promise<void>;act(()=>{first=privacyRetire!();second=privacyRetire!()});expect(first).toBe(second);let finished=false;void second.then(()=>{finished=true});await act(async()=>Promise.resolve());expect(finished).toBe(false);expect(journal.revokeRead).toHaveBeenCalledOnce();await act(async()=>{purge.resolve();await second});expect(finished).toBe(true)})
 it('a late old observer privacy event uses the closed custody fence and cannot purge a fresh writer',async()=>{mount();await ready();await acquire();const oldJournal=journal,oldStream=stream,oldPrivacy=privacyRetire!;oldJournal.close.mockImplementation(async()=>{oldJournal.revokeRead.mockRejectedValue(Error('closed_journal_scope_purge_forbidden'))});act(()=>stream?.('lease',{...ownedLease,mine:false,can_write:false}));await waitFor(()=>expect(oldJournal.close).toHaveBeenCalled());const freshJournal=journalRig();journal=freshJournal;client=clientRig();m.createClient.mockReturnValue(client);m.openJournal.mockResolvedValue(freshJournal);const fresh=mount();await waitFor(()=>expect(within(fresh.container).getByRole('button',{name:'Převzít úpravy'})).toBeEnabled());fireEvent.click(within(fresh.container).getByRole('button',{name:'Převzít úpravy'}));await waitFor(()=>expect(freshJournal.recovery).toHaveBeenCalled());await waitFor(()=>expect(ports.at(-1)!.setReadOnly).toHaveBeenCalledWith(false,expect.any(AbortSignal)));const freshFrame=fresh.container.querySelector('iframe');act(()=>oldStream?.('session_retired',{delivery:'stopped',draft:'purge',reason:'read_revoked'}));await waitFor(()=>expect(oldJournal.revokeRead).toHaveBeenCalledOnce());await expect(oldPrivacy()).rejects.toThrow('closed_journal_scope_purge_forbidden');expect(m.purge).not.toHaveBeenCalled();expect(freshJournal.revokeRead).not.toHaveBeenCalled();expect(freshJournal.close).not.toHaveBeenCalled();expect(fresh.container.querySelector('iframe')).toBe(freshFrame);expect(freshFrame).not.toHaveAttribute('inert')})
 it('refuses a mismatched application snapshot before mounting executable content',async()=>{client.open.mockResolvedValueOnce({sessionId:'private-session',applicationId:'gridcraft'});mount();await screen.findByRole('alert');expect(document.querySelector('iframe')).toBeNull();expect(m.openJournal).not.toHaveBeenCalled()})
 it('never unlocks or saves without exclusive durable draft storage',async()=>{m.openJournal.mockRejectedValueOnce(Error('storage unavailable'));mount();await ready();fireEvent.click(screen.getByRole('button',{name:'Převzít úpravy'}));await screen.findByRole('alert');expect(ports.some(p=>p.setReadOnly.mock.calls.some(c=>c[0]===false))).toBe(false);expect(client.save).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()})
 it('retires a recovered draft whose bytes the server already holds instead of offering it',async()=>{const same={applicationId:'wordcraft',enginePin:'a'.repeat(64),sessionId:'old',generation:'1',baseVersion:'0',editorRevision:'lost-ack',bytes:new TextEncoder().encode('{"body":[]}')},other={...same,editorRevision:'later',bytes:new Uint8Array([99])};journal.recovery.mockResolvedValueOnce([{draft:same,actions:['compare','copy']},{draft:other,actions:['compare','copy']}]);mount();await ready();await acquire();await waitFor(()=>expect(journal.confirm).toHaveBeenCalledWith(same));expect(journal.confirm).not.toHaveBeenCalledWith(other);expect(screen.getAllByRole('button',{name:'Porovnat'})).toHaveLength(1);expect(client.save).not.toHaveBeenCalled()})
 it('reconciles the lost save before closing the client when the connection retires',async()=>{mount();await ready();await acquire();client.save.mockImplementationOnce(()=>new Promise(()=>{}));client.reconcile.mockImplementation(async(r:NativeSaveRequest)=>receipt(r));act(()=>portOptions.at(-1)!.onChanged('pending','doc'));await waitFor(()=>expect(client.save).toHaveBeenCalledOnce());act(()=>stream?.('session_retired',{delivery:'stopped',reason:'session_closed'}));await waitFor(()=>expect(client.close).toHaveBeenCalled());expect(client.reconcile).toHaveBeenCalled();expect(client.reconcile.mock.invocationCallOrder[0]).toBeLessThan(client.close.mock.invocationCallOrder[0])})
 it('offers old local recovery only for compare or copy and never publishes it on mount or acquire',async()=>{journal.recovery.mockResolvedValueOnce([{draft:{applicationId:'wordcraft',enginePin:'a'.repeat(64),sessionId:'old',generation:'1',baseVersion:'1',editorRevision:'old',bytes:new Uint8Array([99])},actions:['compare','copy']}]);mount();await ready();expect(m.openJournal).not.toHaveBeenCalled();await acquire();await screen.findByRole('button',{name:'Porovnat'});expect(screen.getByRole('button',{name:'Kopie konceptu'})).toBeInTheDocument();expect(client.save).not.toHaveBeenCalled();expect(journal.persist).not.toHaveBeenCalled();fireEvent.click(screen.getByRole('button',{name:'Porovnat'}));const server=await screen.findByRole('textbox',{name:'Serverová verze'});expect(server).toHaveAttribute('readonly');expect(screen.getByRole('textbox',{name:'Místní koncept'})).toHaveAttribute('readonly');expect(server).toHaveValue('{\"body\":[]}');expect(client.save).not.toHaveBeenCalled()})
 it('does not send a whole version when native engine validation fails',async()=>{mount();await ready();await acquire();ports.at(-1)!.validate.mockRejectedValueOnce(Error('invalid engine state'));act(()=>portOptions.at(-1)!.onChanged('r2','doc'));await waitFor(()=>expect(ports.at(-1)!.validate).toHaveBeenCalledOnce());await screen.findByText('Neuložený koncept');expect(client.save).not.toHaveBeenCalled();expect(screen.queryByText('Uloženo')).not.toBeInTheDocument()})
 it('awaits pending-draft IDB and the applied native read-only ACK before server handoff and journal release',async()=>{mount();await ready();await acquire();const p=ports.at(-1)!,disk=deferred<void>(),guard=deferred<boolean>();journal.persist.mockReturnValueOnce(disk.promise);p.setReadOnly.mockImplementationOnce(()=>guard.promise);client.leaseAction.mockImplementationOnce(async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,generation:'9007199254740996',holder:{actor_id:'user:other',display_name:'Jana',kind:'human'}});return emptyLease});act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}));fireEvent.click(await screen.findByRole('button',{name:'Předat uživateli Jana'}));await waitFor(()=>expect(journal.persist).toHaveBeenCalledOnce());expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false);expect(document.querySelector('iframe')?.style.pointerEvents).toBe('none');await act(async()=>disk.resolve());await waitFor(()=>expect(p.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal)));expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false);await act(async()=>guard.resolve(true));await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(true));expect(journal.revokeRead).not.toHaveBeenCalled();await waitFor(()=>expect(journal.close).toHaveBeenCalled());const handoffIndex=client.leaseAction.mock.calls.findIndex(c=>c[0]==='handoff');expect(client.leaseAction.mock.invocationCallOrder[handoffIndex]).toBeLessThan(journal.close.mock.invocationCallOrder[0]);expect(client.save).toHaveBeenCalledOnce()})
 it('commits and acknowledges the last edit before handing custody to another human before idle autosave',async()=>{mount();await ready();await acquire();const p=ports.at(-1)!,saved=new TextEncoder().encode('{"body":[1]}'),initial=await client.read();client.save.mockImplementationOnce(async(r:NativeSaveRequest)=>{client.read.mockResolvedValue({...initial,version:'2',bytes:r.bytes});return receipt(r)});client.leaseAction.mockImplementationOnce(async()=>{client.lease.mockResolvedValue({...ownedLease,mine:false,can_write:false,holder:{actor_id:'user:other',display_name:'Jana',kind:'human'},generation:'9007199254740996'});return emptyLease});act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}));await screen.findByRole('button',{name:'Předat uživateli Jana'});act(()=>portOptions.at(-1)!.onChanged('last-unflushed-edit','doc'));fireEvent.click(screen.getByRole('button',{name:'Předat uživateli Jana'}));await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(true));expect(client.save).toHaveBeenCalled();expect(p.ack).toHaveBeenCalled();const handoff=client.leaseAction.mock.calls.findIndex(c=>c[0]==='handoff');expect(client.save.mock.invocationCallOrder[0]).toBeLessThan(client.leaseAction.mock.invocationCallOrder[handoff]);expect(p.ack.mock.invocationCallOrder[0]).toBeLessThan(client.leaseAction.mock.invocationCallOrder[handoff]);await waitFor(()=>expect(ports.at(-1)).not.toBe(p));await waitFor(()=>expect(ports.at(-1)!.bootstrap).toHaveBeenCalled());expect(ports.at(-1)!.bootstrap.mock.calls[0][0]).toEqual(saved);expect(ports.at(-1)!.setReadOnly).not.toHaveBeenCalledWith(false,expect.anything());expect(await client.read()).toMatchObject({version:'2',bytes:saved})})
 it('awaits successful editor receipt acknowledgement before transferring custody',async()=>{mount();await ready();await acquire();const acknowledgement=deferred<boolean>(),p=ports.at(-1)!;p.ack.mockReturnValueOnce(acknowledgement.promise);act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}));fireEvent.click(await screen.findByRole('button',{name:'Předat uživateli Jana'}));await waitFor(()=>expect(p.ack).toHaveBeenCalledOnce());expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false);await act(async()=>acknowledgement.resolve(true));await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(true))})
 it('flushes and acknowledges the final whole version before explicit release too',async()=>{mount();await ready();await acquire();const p=ports.at(-1)!;act(()=>portOptions.at(-1)!.onChanged('last-before-release','doc'));fireEvent.click(screen.getByRole('button',{name:'Uvolnit úpravy'}));await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='release')).toBe(true));expect(client.save).toHaveBeenCalled();expect(p.ack).toHaveBeenCalled()})
 it('never hands off an uncertain final save and retains the original private draft',async()=>{mount();await ready();await acquire();client.save.mockRejectedValueOnce(Error('lost ACK'));act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}));fireEvent.click(await screen.findByRole('button',{name:'Předat uživateli Jana'}));await screen.findByRole('alert');expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false);expect(journal.revokeRead).not.toHaveBeenCalled();expect(document.querySelector('iframe')).not.toBeNull();expect(screen.queryByText('Uloženo')).not.toBeInTheDocument()})
 it('a failed durable capture keeps the guarded native frame and never transfers custody',async()=>{mount();await ready();await acquire();journal.persist.mockResolvedValueOnce(undefined).mockRejectedValueOnce(Error('disk full'));act(()=>stream?.('lease',{...ownedLease,handoff_requested_by:[{actor_id:'user:other',request_id:'request',display_name:'Jana',kind:'human'}]}));fireEvent.click(await screen.findByRole('button',{name:'Předat uživateli Jana'}));await screen.findByText('Koncept nelze bezpečně zachytit. Předání neproběhlo.');expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(false);expect(document.querySelector('iframe')).not.toBeNull();expect(ports.at(-1)!.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal));expect(journal.close).not.toHaveBeenCalled();expect(journal.revokeRead).not.toHaveBeenCalled()})
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
  fireEvent.click(await screen.findByRole('button',{name:'Předat uživateli Jana'}))
  await waitFor(()=>expect(client.leaseAction.mock.calls.some(c=>c[0]==='handoff')).toBe(true))
  expect(captures).toBeGreaterThanOrEqual(2)
  expect(client.save).toHaveBeenCalledOnce()
  expect(p.ack).toHaveBeenCalledOnce()
  const handoff=client.leaseAction.mock.calls.findIndex(c=>c[0]==='handoff')
  expect(p.ack.mock.invocationCallOrder[0]).toBeLessThan(client.leaseAction.mock.invocationCallOrder[handoff])
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
   fireEvent.click(await screen.findByRole('button',{name:'Předat uživateli Jana'}))
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
   fireEvent.click(await screen.findByRole('button',{name:'Předat uživateli Jana'}))
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
   expect(screen.getByRole('alert')).toHaveTextContent('Spojení bylo ukončeno')
  }finally{vi.useRealTimers()}
 })
 it('a generic write refusal retains private journal rows and cannot claim Saved',async()=>{mount();await ready();await acquire();client.save.mockRejectedValueOnce(new NativeSaveError('write_revoked'));act(()=>portOptions.at(-1)!.onChanged('r2','doc'));await waitFor(()=>expect(client.save).toHaveBeenCalledOnce());await waitFor(()=>expect(ports.at(-1)!.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal)));expect(journal.revokeRead).not.toHaveBeenCalled();expect(screen.queryByText('Uloženo')).not.toBeInTheDocument()})

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
   expect(alert).toHaveTextContent('Editor je dostupný jen na adrese https://studio.example.test')
   const here=window.location
   expect(within(alert).getByRole('link')).toHaveAttribute('href','https://studio.example.test'+here.pathname+here.search+here.hash)
   expect(screen.queryByText(/Editor se bezpečně nepřipojil/)).toBeNull()
   expect(journal.revokeRead).not.toHaveBeenCalled();expect(m.purge).not.toHaveBeenCalled()
   expect(screen.getByText('Editor odpojen · koncept zachován')).toBeInTheDocument()
  }finally{vi.useRealTimers()}
 })
 it('still explains the mismatch without a link when the policy names no origin',async()=>{
  m.createPort.mockImplementationOnce((o:NativeEditorPortOptions)=>{portOptions.push(o);const p=portRig();p.bootstrap.mockReturnValue(new Promise(()=>{}));ports.push(p);return p})
  mount();await waitFor(()=>expect(document.querySelector('iframe')).not.toBeNull())
  act(()=>{document.dispatchEvent(violation({originalPolicy:"default-src 'self'"}))})
  const alert=await screen.findByRole('alert');expect(alert).toHaveTextContent('nastavené adrese')
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
   await waitFor(()=>expect(screen.getByRole('alert')).toHaveTextContent('Editor se bezpečně nepřipojil'))
   expect(screen.getByRole('alert')).not.toHaveTextContent('Otevřete stránku tam')
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
  expect(screen.queryByText('Uloženo')).toBeNull()
  await act(async()=>rotation.resolve(true))
  expect(document.querySelector('iframe')).toBe(frame);expect(client.save).toHaveBeenCalledOnce();expect(client.leaseAction.mock.calls.filter(call=>call[0]==='acquire')).toHaveLength(1)
  await act(async()=>pendingSave.resolve(receipt(captured)));await screen.findByText('Uloženo')
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
  expect(screen.queryByText('Uloženo')).toBeNull()
  await act(async()=>rotation.resolve(true))
  expect(document.querySelector('iframe')).toBe(frame);expect(client.save).toHaveBeenCalledOnce();expect(client.leaseAction.mock.calls.filter(call=>call[0]==='acquire')).toHaveLength(1)
  await act(async()=>pendingSave.resolve(receipt(captured)));await screen.findByText('Uloženo')
  expect(client.save).toHaveBeenCalledOnce();expect(ownedJournal.revokeRead).not.toHaveBeenCalled()
 }finally{vi.useRealTimers()}
})

describe('agent proposals while a person holds the editor (#4480)',()=>{
 const proposal={id:'ncp_0123456789abcdef01234567',authorName:'Jamie',baseVersion:'1',summary:'Nadpis',createdAt:'2026-10-29T17:00:00Z',state:'open' as const}
 it('accepts only as the current holder after saving its own edits, then reopens from the server',async()=>{
  client.proposals.mockResolvedValue([proposal])
  mount();await ready()
  await screen.findByText('Jamie připravil návrh nad verzí 1')
  expect(screen.queryByRole('button',{name:'Přijmout'})).not.toBeInTheDocument()
  await acquire()
  client.decideProposal.mockResolvedValue({...proposal,state:'accepted',resultVersion:'2'})
  act(()=>portOptions.at(-1)!.onChanged('human-edit','doc'))
  fireEvent.click(await screen.findByRole('button',{name:'Přijmout'}))
  await waitFor(()=>expect(client.decideProposal).toHaveBeenCalledWith(proposal.id,'accept','9007199254740995',expect.any(AbortSignal)))
  expect(client.save.mock.invocationCallOrder[0]).toBeLessThan(client.decideProposal.mock.invocationCallOrder[0])
  await waitFor(()=>expect(client.read.mock.calls.length).toBeGreaterThanOrEqual(3))
 })
 it('reports a stale proposal without changing the document',async()=>{
  client.proposals.mockResolvedValue([proposal])
  mount();await ready();await acquire()
  client.decideProposal.mockResolvedValue({...proposal,state:'stale'})
  fireEvent.click(await screen.findByRole('button',{name:'Přijmout'}))
  await screen.findByText(/Návrh nebyl použit/)
  expect(document.querySelector('iframe')).not.toBeNull()
 })
 it('rejects without a lease and compares the candidate read-only',async()=>{
  client.proposals.mockResolvedValue([proposal])
  client.proposalBytes.mockResolvedValue(new TextEncoder().encode('{"body":["agent"]}'))
  client.decideProposal.mockResolvedValue({...proposal,state:'rejected'})
  mount();await ready()
  fireEvent.click(await screen.findByRole('button',{name:'Porovnat'}))
  expect(await screen.findByLabelText('Návrh Jamie nad verzí 1')).toHaveValue('{"body":["agent"]}')
  fireEvent.click(screen.getByRole('button',{name:'Odmítnout'}))
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
  expect(await screen.findByText(/Kapacita úložiště neumožňuje další uložení/)).toBeInTheDocument()
  const click=vi.fn();const create=vi.spyOn(document,'createElement')
  vi.stubGlobal('URL',Object.assign(URL,{createObjectURL:vi.fn(()=>'blob:x'),revokeObjectURL:vi.fn()}))
  create.mockImplementationOnce(()=>Object.assign(document.createElementNS('http://www.w3.org/1999/xhtml','a') as HTMLAnchorElement,{click}))
  fireEvent.click(screen.getByRole('button',{name:'Stáhnout koncept'}))
  await waitFor(()=>expect(click).toHaveBeenCalledOnce())
  expect(journal.revokeRead).not.toHaveBeenCalled();expect(document.querySelector('iframe')).not.toBeNull()
 })
})
