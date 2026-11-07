import {cleanup,render,waitFor} from '@testing-library/react'
import {afterEach,beforeEach,expect,it,vi} from 'vitest'
import {ArtifactNativeEditor} from '../native-editor'
const m=vi.hoisted(()=>({fetch:vi.fn(),readLoss:vi.fn()}))
vi.mock('@/hooks/use-auth',()=>({useSessionSafe:()=>({status:'authenticated',data:{user:{id:'human'}}})}))
vi.mock('@/lib/api-fetch',()=>({apiFetch:m.fetch}))
vi.mock('@/lib/mooring/native-read-loss',()=>({verifyNativeReadLoss:m.readLoss}))
vi.mock('@/components/features/pages/native-craft-editor-host',()=>({NativeCraftEditorHost:()=><div>host</div>}))
vi.mock('@/components/features/pages/native-craft-preview',()=>({NativeCraftPreview:()=><div>preview</div>}))
vi.mock('@/components/features/pages/native-craft-reader-history',()=>({NativeCraftReaderHistory:()=>null}))
const body=(value:unknown)=>Promise.resolve(new Response(JSON.stringify(value)))
beforeEach(()=>{m.readLoss.mockResolvedValue(false)})
afterEach(()=>{cleanup();vi.clearAllMocks()})
it.each([
 ['preview only',()=>body({enabled:false,can_edit:false,preview_enabled:true})],
 ['read only',()=>body({enabled:true,can_edit:false,preview_enabled:false})],
 ['disabled',()=>body({enabled:false,can_edit:false,preview_enabled:false})],
 ['refused availability',()=>Promise.resolve(new Response(null,{status:403}))],
 ['failed availability',()=>Promise.reject(new TypeError('Failed to fetch'))],
])('asks for proof on %s',async(_,answer)=>{
 m.fetch.mockImplementation(answer)
 render(<ArtifactNativeEditor workspaceId="w" pageId="p" fallback={null}/>)
 await waitFor(()=>expect(m.readLoss).toHaveBeenCalledOnce())
 expect(m.readLoss).toHaveBeenCalledWith({human:'human',workspace:'w',page:'p'})
})
it('does not ask when the editor is admitted',async()=>{
 m.fetch.mockImplementation(()=>body({enabled:true,can_edit:true,preview_enabled:true}))
 const {findByText}=render(<ArtifactNativeEditor workspaceId="w" pageId="p" fallback={null}/>)
 await findByText('host');expect(m.readLoss).not.toHaveBeenCalled()
})
