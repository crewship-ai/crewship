import {webcrypto} from 'node:crypto'
import {render,screen,waitFor,act} from '@testing-library/react'
import {afterEach,beforeEach,expect,it,vi} from 'vitest'
import {NativeCraftPreview} from '../native-craft-preview'
const m=vi.hoisted(()=>({read:vi.fn(),port:vi.fn(),privacy:vi.fn(),unregister:vi.fn(),handlers:new Map<string,Array<(event:{type:string;payload:Record<string,unknown>})=>void>>()}))
vi.mock('@/hooks/use-realtime',()=>({useRealtimeEventSafe:(type:string,cb:(event:{type:string;payload:Record<string,unknown>})=>void)=>{const React=require('react');React.useEffect(()=>{const list=m.handlers.get(type)??[];list.push(cb);m.handlers.set(type,list);return()=>{m.handlers.set(type,(m.handlers.get(type)??[]).filter(h=>h!==cb))}})}}))
const push=(type:string,payload:Record<string,unknown>)=>act(async()=>{for(const h of [...(m.handlers.get(type)??[])])h({type,payload})})
vi.mock('@/lib/mooring/native-preview-client',()=>({readNativePreview:m.read,NativePreviewError:class extends Error {status=403}}))
// The locked DOM harness disables iframe page loading. Use the same inert
// frame fixture as the native editor host tests; real isolation is browser-tested.
vi.mock('../workbook-editor-host',()=>({editorFrameSupported:()=>true,createEditorFrame:(doc:Document,src:string,onLoad:()=>void)=>{
 const frame=doc.createElement('iframe');frame.setAttribute('sandbox','allow-scripts');frame.setAttribute('credentialless','');frame.setAttribute('referrerpolicy','no-referrer');frame.srcdoc='';frame.src=src;queueMicrotask(onLoad);return frame
}}))
vi.mock('@/lib/mooring/native-editor-port',()=>({createNativeEditorPort:m.port}))
vi.mock('@/lib/document-queue',()=>({registerDocumentDraftWriter:m.privacy}))
vi.mock('../native-craft-editor-host',()=>({nativeCraftFrameURL:()=> 'about:blank'}))
const snapshot=()=>({version:'2',bytes:new Uint8Array([1,2]),applicationId:'wordcraft',enginePin:'a'.repeat(64),editorBundle:'b'.repeat(64),editorURL:'unused'})
let port:{bootstrap:ReturnType<typeof vi.fn>;setReadOnly:ReturnType<typeof vi.fn>;loaded:ReturnType<typeof vi.fn>;handleWindowMessage:ReturnType<typeof vi.fn>;close:ReturnType<typeof vi.fn>}
beforeEach(()=>{m.handlers.clear();vi.stubGlobal('crypto',webcrypto);m.read.mockReset().mockResolvedValue(snapshot());m.privacy.mockReset().mockReturnValue(m.unregister);m.port.mockReset();port={bootstrap:vi.fn().mockResolvedValue({}),setReadOnly:vi.fn().mockResolvedValue(true),loaded:vi.fn(),handleWindowMessage:vi.fn(),close:vi.fn()};m.port.mockImplementation(options=>{queueMicrotask(()=>options.onReady?.('qualified'));return port})})
afterEach(()=>{vi.useRealTimers();vi.unstubAllGlobals()})
it('bootstraps the shared pin readonly without custody or drafts',async()=>{
 render(<NativeCraftPreview humanId="h" workspaceId="w" pageId="p"/>);
 await screen.findByText('Sdílený náhled · verze 2')
 expect(m.port.mock.calls[0][0].requireAccessProtocol).toBe(true)
 expect(port.bootstrap).toHaveBeenCalledWith(new Uint8Array([1,2]),expect.any(AbortSignal))
 expect(port.setReadOnly).toHaveBeenCalledWith(true,expect.any(AbortSignal))
 expect(screen.queryByRole('button')).toBeNull()
})
it('removes the frame and rejects delayed delivery after an identity change',async()=>{
 let resolve!:(value:unknown)=>void;m.read.mockReturnValueOnce(new Promise(done=>{resolve=done}))
 const mounted=render(<NativeCraftPreview humanId="h" workspaceId="w" pageId="p"/>);
 await waitFor(()=>expect(m.read).toHaveBeenCalledOnce())
 m.read.mockRejectedValue(new Error('denied'));mounted.rerender(<NativeCraftPreview humanId="other" workspaceId="w" pageId="p"/>);
 await act(async()=>resolve(snapshot()))
 expect(document.querySelector('iframe')).toBeNull();expect(m.port).not.toHaveBeenCalled()
})
it('closes the frame on privacy retirement without retaining content',async()=>{
 render(<NativeCraftPreview humanId="h" workspaceId="w" pageId="p"/>);await screen.findByText('Sdílený náhled · verze 2')
 await act(async()=>m.privacy.mock.calls[0][1]())
 expect(document.querySelector('iframe')).toBeNull();expect(port.close).toHaveBeenCalled()
 expect(screen.queryByText('Sdílený náhled · verze 2')).toBeNull()
})
it('removes the frame when periodic authority recheck refuses delivery',async()=>{
 vi.useFakeTimers()
 await act(async()=>{render(<NativeCraftPreview humanId="h" workspaceId="w" pageId="p"/>)})
 expect(screen.getByText('Sdílený náhled · verze 2')).toBeInTheDocument()
 m.read.mockRejectedValue(new Error('denied'))
 await act(async()=>vi.advanceTimersByTimeAsync(30000))
 expect(document.querySelector('iframe')).toBeNull();expect(port.close).toHaveBeenCalled()
})
it('does not retire a newer shared pin when an old frame finishes bootstrapping late',async()=>{
 let reject!:(error:Error)=>void
 const old={...port,bootstrap:vi.fn().mockReturnValue(new Promise((_done,fail)=>{reject=fail})),close:vi.fn()}
 m.port.mockImplementationOnce(options=>{queueMicrotask(()=>options.onReady?.('qualified'));return old})
 render(<NativeCraftPreview humanId="h" workspaceId="w" pageId="p"/>);
 await waitFor(()=>expect(old.bootstrap).toHaveBeenCalledOnce())
 m.read.mockResolvedValue({...snapshot(),version:'3'})
 await act(async()=>window.dispatchEvent(new Event('focus')))
 await screen.findByText('Sdílený náhled · verze 3')
 expect(old.close).toHaveBeenCalled()
 await act(async()=>reject(new Error('old port closed')))
 expect(document.querySelector('iframe')).not.toBeNull()
 expect(screen.getByText('Sdílený náhled · verze 3')).toBeInTheDocument()
 expect(port.close).not.toHaveBeenCalled()
})
it('closes readable content when the authority check hangs past its deadline',async()=>{
 vi.useFakeTimers()
 await act(async()=>{render(<NativeCraftPreview humanId="h" workspaceId="w" pageId="p"/>)})
 expect(screen.getByText('Sdílený náhled · verze 2')).toBeInTheDocument()
 m.read.mockReturnValue(new Promise(()=>{}))
 await act(async()=>vi.advanceTimersByTimeAsync(30000))
 expect(document.querySelector('iframe')).not.toBeNull()
 await act(async()=>vi.advanceTimersByTimeAsync(10000))
 expect(document.querySelector('iframe')).toBeNull()
 expect(port.close).toHaveBeenCalled()
})

it('drops the content at once on an access recheck for this page, without waiting for the timer',async()=>{
 vi.useFakeTimers()
 await act(async()=>{render(<NativeCraftPreview humanId="h" workspaceId="w" pageId="p"/>)})
 expect(screen.getByText('Sdílený náhled · verze 2')).toBeInTheDocument()
 m.read.mockClear().mockRejectedValue(new Error('denied'))
 await push('page.access_recheck',{page_id:'p'})
 expect(m.read).toHaveBeenCalledOnce()
 expect(document.querySelector('iframe')).toBeNull();expect(port.close).toHaveBeenCalled()
 expect(screen.getByRole('alert')).toHaveTextContent('Sdílený náhled není dostupný')
})
it('rereads on a generic recheck without a page id (membership or role change)',async()=>{
 await act(async()=>{render(<NativeCraftPreview humanId="h" workspaceId="w" pageId="p"/>)})
 await screen.findByText('Sdílený náhled · verze 2')
 m.read.mockClear().mockRejectedValue(new Error('denied'))
 await push('page.access_recheck',{})
 expect(m.read).toHaveBeenCalledOnce();expect(document.querySelector('iframe')).toBeNull()
})
it('ignores a recheck that names another page and keeps the content',async()=>{
 await act(async()=>{render(<NativeCraftPreview humanId="h" workspaceId="w" pageId="p"/>)})
 await screen.findByText('Sdílený náhled · verze 2')
 m.read.mockClear().mockRejectedValue(new Error('denied'))
 await push('page.access_recheck',{page_id:'other'})
 expect(m.read).not.toHaveBeenCalled();expect(document.querySelector('iframe')).not.toBeNull()
})
it('keeps the content when the recheck confirms access, and rereads once more if a read was in flight',async()=>{
 await act(async()=>{render(<NativeCraftPreview humanId="h" workspaceId="w" pageId="p"/>)})
 await screen.findByText('Sdílený náhled · verze 2')
 let resolve!:(value:unknown)=>void;m.read.mockClear().mockReturnValueOnce(new Promise(done=>{resolve=done}))
 await act(async()=>{window.dispatchEvent(new Event('focus'))})
 await push('page.access_recheck',{page_id:'p'})
 m.read.mockRejectedValue(new Error('denied'))
 await act(async()=>resolve(snapshot()))
 await waitFor(()=>expect(m.read).toHaveBeenCalledTimes(2))
 expect(document.querySelector('iframe')).toBeNull()
})
it('rereads after the realtime connection was re-established',async()=>{
 await act(async()=>{render(<NativeCraftPreview humanId="h" workspaceId="w" pageId="p"/>)})
 await screen.findByText('Sdílený náhled · verze 2')
 m.read.mockClear().mockRejectedValue(new Error('denied'))
 await push('realtime.reconnected',{})
 expect(m.read).toHaveBeenCalledOnce();expect(document.querySelector('iframe')).toBeNull()
})
