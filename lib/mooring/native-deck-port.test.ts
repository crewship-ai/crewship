import {webcrypto} from 'node:crypto'
import {afterEach,beforeEach,expect,it,vi} from 'vitest'
import {createNativeEditorPort,type NativeEditorPortOptions,type NativeStructuredPort} from './native-editor-port'
beforeEach(()=>vi.stubGlobal('crypto',webcrypto))
afterEach(()=>vi.unstubAllGlobals())
const bytes=new Uint8Array([80,75,3,4,0,255,128]) // Wire fixture only; original engine validates real ZIP files.
const signal=()=>new AbortController().signal
function rig(){
 const wire={postMessage:vi.fn(),onmessage:null,start:vi.fn(),close:vi.fn()} satisfies NativeStructuredPort
 const frame={postMessage:vi.fn()},onError=vi.fn(),onChanged=vi.fn(),onSaveRequest=vi.fn()
 const options:NativeEditorPortOptions={applicationId:'deckcraft',enginePin:'pinned',bundle:'a'.repeat(64),frame,scope:{workspaceId:'w',pageId:'p',baseVersion:'1',generation:'7',sessionId:'session'},createChannel:()=>({port1:wire,port2:{}}),onError,onChanged,onSaveRequest,requireAccessProtocol:true}
 const port=createNativeEditorPort(options),receive=(data:unknown)=>(wire as NativeStructuredPort).onmessage?.({data}),last=()=>wire.postMessage.mock.calls.at(-1)![0] as Record<string,unknown>
 port.loaded();expect(port.handleWindowMessage({source:frame,origin:'null',data:{type:'deckcraft:hello',protocol:1,bundle:options.bundle}})).toBe(true)
 receive({type:'deckcraft:ready',version:'qualified',accessProtocol:1,readOnly:true});receive({type:'deckcraft:closed'})
 return {port,wire,frame,onError,onChanged,onSaveRequest,receive,last}
}
async function opened(r:ReturnType<typeof rig>){
 const boot=r.port.bootstrap(bytes,signal());void boot.catch(()=>{})
 expect(r.last()).toMatchObject({type:'deckcraft:open',name:'document.deckcraft'})
 r.receive({type:'deckcraft:opened',requestId:r.last().requestId,document:'17',revision:'1'})
 await vi.waitFor(()=>expect(r.last().type).toBe('deckcraft:export-native'))
 r.receive({type:'deckcraft:native-exported',requestId:r.last().requestId,document:'17',revision:'1',bytes:bytes.slice().buffer})
 await boot
}
it('binds opaque Deck ZIP only after original import and validates before exact receipt ACK',async()=>{
 const r=rig();await opened(r)
 expect(JSON.stringify(r.frame.postMessage.mock.calls)).not.toContain('session')
 const snapshot=r.port.snapshot(signal());const id=r.last().requestId
 r.receive({type:'deckcraft:native-exported',requestId:id,document:'17',revision:'2',bytes:bytes.slice().buffer});const snap=await snapshot
 expect(snap.bytes).toEqual(bytes)
 const draft={...snap,applicationId:'deckcraft',enginePin:'pinned',baseVersion:'1',generation:'7',sessionId:'session'}
 const validate=r.port.validate(draft,signal());expect(r.last().type).toBe('deckcraft:validate-native')
 r.receive({type:'deckcraft:native-validated',requestId:r.last().requestId,valid:true});await validate
 const hash='sha256:'+Buffer.from(await webcrypto.subtle.digest('SHA-256',bytes)).toString('hex')
 const receipt={workspaceId:'w',pageId:'p',...draft,bytes:undefined,contentHash:hash,candidateHash:hash,version:'2',issuedBy:'crewship' as const,durability:'node' as const,phase:'committed' as const}
 expect(await r.port.ack({...receipt,contentHash:'sha256:'+'0'.repeat(64)})).toBe(false)
 expect(await r.port.ack(receipt)).toBe(true)
 expect(r.last()).toEqual({type:'deckcraft:save-ack',requestId:id,document:'17',revision:'2'})
 expect(await r.port.ack(receipt)).toBe(false);r.port.close()
})
it('rejects Deck validation failure and foreign document changes without forwarding a save',async()=>{
 const r=rig();await opened(r)
 const draft={applicationId:'deckcraft',enginePin:'pinned',baseVersion:'1',generation:'7',sessionId:'session',editorRevision:'snapshot',bytes}
 const validation=r.port.validate(draft,signal());void validation.catch(()=>{})
 r.receive({type:'deckcraft:native-validated',requestId:r.last().requestId,valid:false,error:'invalid_native'})
 await expect(validation).rejects.toThrow('invalid_native')
 r.receive({type:'deckcraft:changed',document:'18',revision:'3',dirty:true})
 expect(r.onError).toHaveBeenCalledWith('native_document_changed');expect(r.onChanged).not.toHaveBeenCalled();expect(r.onSaveRequest).not.toHaveBeenCalled()
})

it.each([['0','1'],['01','1'],['17','0'],['17','01'],['18446744073709551616','1'],['17','18446744073709551616']])('rejects noncanonical Deck document/revision %s/%s before binding',async(document,revision)=>{
 const r=rig(),boot=r.port.bootstrap(bytes,signal());void boot.catch(()=>{})
 r.receive({type:'deckcraft:opened',requestId:r.last().requestId,document,revision})
 await expect(boot).rejects.toThrow();expect(r.onError).toHaveBeenCalledWith('native_port_protocol');expect(r.onChanged).not.toHaveBeenCalled();expect(r.onSaveRequest).not.toHaveBeenCalled()
})
it('keeps the same wire revision byte-stable for Deck and closes on changed native bytes',async()=>{
 const r=rig();await opened(r)
 const snapshot=r.port.snapshot(signal());void snapshot.catch(()=>{})
 r.receive({type:'deckcraft:native-exported',requestId:r.last().requestId,document:'17',revision:'1',bytes:new Uint8Array([80,75,1,4]).buffer})
 await expect(snapshot).rejects.toThrow('unstable_editor_revision');expect(r.onError).toHaveBeenCalledWith('unstable_editor_revision')
})

it('refuses writable Deck readiness before any original document import',()=>{
 const wire={postMessage:vi.fn(),onmessage:null,start:vi.fn(),close:vi.fn()} satisfies NativeStructuredPort
 const frame={postMessage:vi.fn()},onError=vi.fn()
 const port=createNativeEditorPort({applicationId:'deckcraft',enginePin:'pinned',bundle:'a'.repeat(64),frame,scope:{workspaceId:'w',pageId:'p',baseVersion:'1',generation:'7',sessionId:'session'},createChannel:()=>({port1:wire,port2:{}}),onError,onChanged:vi.fn(),onSaveRequest:vi.fn(),requireAccessProtocol:true})
 port.loaded();port.handleWindowMessage({source:frame,origin:'null',data:{type:'deckcraft:hello',protocol:1,bundle:'a'.repeat(64)}})
 ;(wire as NativeStructuredPort).onmessage?.({data:{type:'deckcraft:ready',version:'qualified',accessProtocol:1,readOnly:false}})
 expect(onError).toHaveBeenCalledWith('native_access_boot_writable');expect(wire.postMessage).not.toHaveBeenCalled();port.close()
})
